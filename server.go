package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/songgao/water"
)

// NOTE: A portion of this code (constants, header, TUN/crypto helpers) is duplicated
// in client.go. This is to adhere to the requested file structure. In a real-world
// project, this would be in a shared 'common' package.

// Constants
const (
	MTU                = 1400 // Lower MTU to accommodate encryption and protocol overhead
	HEADER_SIZE        = 9    // 8 bytes for sequence number, 1 byte for type
	REASSEMBLY_TIMEOUT = 2 * time.Second
	CLEANUP_INTERVAL   = 1 * time.Second
)

// PacketType defines the type of packet
type PacketType byte

const (
	PacketData        PacketType = 0x01
	PacketHealthCheck PacketType = 0x02
)

// PacketHeader defines the custom protocol header
type PacketHeader struct {
	SequenceNumber uint64
	Type           PacketType
}

// Serialize converts the header to a byte slice
func (h *PacketHeader) Serialize() []byte {
	buf := make([]byte, HEADER_SIZE)
	binary.BigEndian.PutUint64(buf[0:8], h.SequenceNumber)
	buf[8] = byte(h.Type)
	return buf
}

// Deserialize populates the header from a byte slice
func (h *PacketHeader) Deserialize(data []byte) error {
	if len(data) < HEADER_SIZE {
		return fmt.Errorf("data too short for header, got %d bytes, want %d", len(data), HEADER_SIZE)
	}
	h.SequenceNumber = binary.BigEndian.Uint64(data[0:8])
	h.Type = PacketType(data[8])
	return nil
}

// ReassemblyBuffer holds out-of-order packets for reordering
type ReassemblyBuffer struct {
	buffer       map[uint64][]byte
	timestamps   map[uint64]time.Time
	mu           sync.Mutex
	nextSequence uint64
	tun          *water.Interface
	cond         *sync.Cond
}

// NewReassemblyBuffer creates a new reassembly buffer
func NewReassemblyBuffer(tun *water.Interface) *ReassemblyBuffer {
	rb := &ReassemblyBuffer{
		buffer:       make(map[uint64][]byte),
		timestamps:   make(map[uint64]time.Time),
		nextSequence: 0,
		tun:          tun,
	}
	rb.cond = sync.NewCond(&rb.mu)
	return rb
}

// AddPacket adds a packet to the buffer and signals the reader goroutine
func (rb *ReassemblyBuffer) AddPacket(seq uint64, packet []byte) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	// Don't buffer old packets that have already been processed
	if seq < rb.nextSequence {
		return
	}

	rb.buffer[seq] = packet
	rb.timestamps[seq] = time.Now()
	rb.cond.Broadcast() // Wake up the reader goroutine
}

// StartReading continuously reads in-order packets from the buffer and writes them to the TUN interface
func (rb *ReassemblyBuffer) StartReading() {
	for {
		rb.mu.Lock()
		packet, ok := rb.buffer[rb.nextSequence]
		if !ok {
			// Wait for a new packet to be added
			rb.cond.Wait()
			rb.mu.Unlock()
			continue
		}

		// Packet found, write it to TUN
		delete(rb.buffer, rb.nextSequence)
		delete(rb.timestamps, rb.nextSequence)

		_, err := rb.tun.Write(packet)
		if err != nil {
			log.Printf("Error writing to TUN: %v", err)
		}

		rb.nextSequence++
		rb.mu.Unlock()
	}
}

// StartCleaner periodically removes old packets from the buffer to prevent memory leaks
func (rb *ReassemblyBuffer) StartCleaner() {
	ticker := time.NewTicker(CLEANUP_INTERVAL)
	defer ticker.Stop()

	for range ticker.C {
		rb.mu.Lock()
		for seq, ts := range rb.timestamps {
			if time.Since(ts) > REASSEMBLY_TIMEOUT {
				delete(rb.buffer, seq)
				delete(rb.timestamps, seq)
				log.Printf("Dropped old packet %d from reassembly buffer", seq)
			}
		}
		rb.mu.Unlock()
	}
}

// createTun creates and configures a TUN interface for the given OS
func createTun(name, ipCIDR string) (*water.Interface, error) {
	config := water.Config{DeviceType: water.TUN}
	config.Name = name

	ifce, err := water.New(config)
	if err != nil {
		return nil, err
	}

	log.Printf("TUN interface created: %s", ifce.Name())

	switch runtime.GOOS {
	case "linux":
		if err := exec.Command("ip", "addr", "add", ipCIDR, "dev", ifce.Name()).Run(); err != nil {
			return nil, fmt.Errorf("failed to set IP address: %v", err)
		}
		if err := exec.Command("ip", "link", "set", "dev", ifce.Name(), "up").Run(); err != nil {
			return nil, fmt.Errorf("failed to bring up interface: %v", err)
		}
	case "darwin":
		parts := strings.Split(ipCIDR, "/")
		addr := parts[0]
		if err := exec.Command("ifconfig", ifce.Name(), "inet", addr, addr, "up").Run(); err != nil {
			return nil, fmt.Errorf("failed to set IP address and bring up interface: %v", err)
		}
	default:
		return nil, fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}

	return ifce, nil
}

// encrypt encrypts data using AES-256-GCM, prepending a random nonce
func encrypt(plaintext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return ciphertext, nil
}

// decrypt decrypts data using AES-256-GCM, assuming a prepended nonce
func decrypt(ciphertext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, err
	}
	return plaintext, nil
}

func main() {
	port := flag.Int("port", 8080, "Port to listen on")
	keyStr := flag.String("key", "", "32-byte pre-shared key for encryption")
	flag.Parse()

	if *keyStr == "" {
		log.Fatalf("Pre-shared key is required via -key flag")
	}
	key := []byte(*keyStr)
	if len(key) != 32 {
		log.Fatalf("Key must be 32 bytes long")
	}

	// Create TUN interface
	tun, err := createTun("speedify-tun", "10.200.0.1/24")
	if err != nil {
		log.Fatalf("Failed to create TUN interface: %v", err)
	}
	defer tun.Close()

	// Listen for UDP packets
	addr := fmt.Sprintf(":%d", *port)
	pconn, err := net.ListenPacket("udp", addr)
	if err != nil {
		log.Fatalf("Failed to listen on UDP port %d: %v", *port, err)
	}
	defer pconn.Close()
	log.Printf("Listening for connections on %s", pconn.LocalAddr().String())

	// Initialize and start the reassembly buffer
	reassemblyBuffer := NewReassemblyBuffer(tun)
	go reassemblyBuffer.StartReading()
	go reassemblyBuffer.StartCleaner()

	var clientAddr net.Addr
	var clientAddrMu sync.RWMutex
	var serverSeq uint64 = 0
	var serverSeqMu sync.Mutex

	// Goroutine: Read from TUN -> Encrypt -> Send to Client
	go func() {
		packet := make([]byte, MTU)
		for {
			n, err := tun.Read(packet)
			if err != nil {
				log.Printf("Error reading from TUN: %v", err)
				continue
			}

			clientAddrMu.RLock()
			if clientAddr == nil {
				clientAddrMu.RUnlock()
				continue
			}
			targetAddr := clientAddr
			clientAddrMu.RUnlock()

			serverSeqMu.Lock()
			currentSeq := serverSeq
			serverSeq++
			serverSeqMu.Unlock()

			header := PacketHeader{SequenceNumber: currentSeq, Type: PacketData}
			payload := append(header.Serialize(), packet[:n]...)

			encryptedPayload, err := encrypt(payload, key)
			if err != nil {
				log.Printf("Failed to encrypt packet for client: %v", err)
				continue
			}

			_, err = pconn.WriteTo(encryptedPayload, targetAddr)
			if err != nil {
				log.Printf("Failed to send packet to client %s: %v", targetAddr, err)
			}
		}
	}()

	// Main loop: Read from UDP -> Decrypt -> Add to Reassembly Buffer
	buf := make([]byte, MTU*2) // Allow larger buffer for potential jumbo frames
	for {
		n, addr, err := pconn.ReadFrom(buf)
		if err != nil {
			log.Printf("Error reading from UDP: %v", err)
			continue
		}

		clientAddrMu.Lock()
		if clientAddr == nil || clientAddr.String() != addr.String() {
			log.Printf("Client connection established from %s", addr.String())
			clientAddr = addr
		}
		clientAddrMu.Unlock()

		decrypted, err := decrypt(buf[:n], key)
		if err != nil {
			// This can happen often with malformed/invalid packets, so don't flood logs
			continue
		}

		var header PacketHeader
		if err := header.Deserialize(decrypted); err != nil {
			log.Printf("Failed to parse header from %s: %v", addr, err)
			continue
		}

		// Handle health checks by echoing them back for RTT measurement
		if header.Type == PacketHealthCheck {
			encryptedResponse, err := encrypt(decrypted, key)
			if err != nil {
				log.Printf("Failed to re-encrypt health check response: %v", err)
				continue
			}
			pconn.WriteTo(encryptedResponse, addr)
			continue
		}

		// Add data packet to reassembly buffer
		ipPacket := decrypted[HEADER_SIZE:]
		reassemblyBuffer.AddPacket(header.SequenceNumber, ipPacket)
	}
}
