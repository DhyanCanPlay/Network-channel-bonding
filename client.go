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
	"sync/atomic"
	"time"

	"github.com/songgao/water"
)

// NOTE: A portion of this code (constants, header, TUN/crypto helpers) is duplicated
// in server.go. This is to adhere to the requested file structure. In a real-world
// project, this would be in a shared 'common' package.

// Constants
const (
	MTU         = 1400 // Lower MTU to accommodate encryption and protocol overhead
	HEADER_SIZE = 9    // 8 bytes for sequence number, 1 byte for type
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

// InterfaceConn represents a network interface with its connection and stats
type InterfaceConn struct {
	Conn    *net.UDPConn
	Addr    *net.UDPAddr
	Latency time.Duration
	mu      sync.RWMutex
}

func (ic *InterfaceConn) SetLatency(l time.Duration) {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	ic.Latency = l
}

func (ic *InterfaceConn) GetLatency() time.Duration {
	ic.mu.RLock()
	defer ic.mu.RUnlock()
	return ic.Latency
}

// Scheduler handles traffic distribution across multiple interfaces
type Scheduler struct {
	interfaces []*InterfaceConn
	mode       string
	mu         sync.RWMutex
	nextIndex  int
}

// NewScheduler creates a new scheduler
func NewScheduler(mode string) *Scheduler {
	return &Scheduler{
		interfaces: make([]*InterfaceConn, 0),
		mode:       mode,
	}
}

// AddInterface adds a new interface to the scheduler
func (s *Scheduler) AddInterface(iface *InterfaceConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.interfaces = append(s.interfaces, iface)
}

// GetInterfaces returns a copy of the interfaces slice
func (s *Scheduler) GetInterfaces() []*InterfaceConn {
	s.mu.RLock()
	defer s.mu.RUnlock()
	// Return a copy to prevent race conditions on the slice itself
	ifaces := make([]*InterfaceConn, len(s.interfaces))
	copy(ifaces, s.interfaces)
	return ifaces
}

// SelectInterface decides which interface to use based on the mode
func (s *Scheduler) SelectInterface() *InterfaceConn {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.interfaces) == 0 {
		return nil
	}

	switch s.mode {
	case "speed":
		// Find the interface with the lowest latency
		bestIface := s.interfaces[0]
		minLatency := bestIface.GetLatency()
		for i := 1; i < len(s.interfaces); i++ {
			if s.interfaces[i].GetLatency() < minLatency {
				minLatency = s.interfaces[i].GetLatency()
				bestIface = s.interfaces[i]
			}
		}
		return bestIface
	case "redundant":
		// In redundant mode, we send on all, so this selection is round-robin for simplicity
		// The actual sending logic will iterate over all interfaces
		iface := s.interfaces[s.nextIndex]
		s.nextIndex = (s.nextIndex + 1) % len(s.interfaces)
		return iface
	default: // Default to round-robin
		iface := s.interfaces[s.nextIndex]
		s.nextIndex = (s.nextIndex + 1) % len(s.interfaces)
		return iface
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

// discoverInterfaces finds all non-loopback network interfaces and creates UDP sockets for them
func discoverInterfaces(serverAddr *net.UDPAddr) []*InterfaceConn {
	var conns []*InterfaceConn
	ifaces, err := net.Interfaces()
	if err != nil {
		log.Fatalf("Failed to get system interfaces: %v", err)
	}

	for _, i := range ifaces {
		// Skip loopback, down, and non-multicast interfaces
		if (i.Flags&net.FlagUp == 0) || (i.Flags&net.FlagLoopback != 0) {
			continue
		}
		addrs, err := i.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}

			// Consider only IPv4 for this implementation
			if ip == nil || ip.IsLoopback() || ip.To4() == nil {
				continue
			}

			localUDPAddr := &net.UDPAddr{IP: ip, Port: 0} // Port 0 lets OS choose
			conn, err := net.DialUDP("udp", localUDPAddr, serverAddr)
			if err != nil {
				log.Printf("Could not dial from interface %s (%s): %v", i.Name, ip, err)
				continue
			}

			log.Printf("Successfully bound to interface %s with address %s", i.Name, conn.LocalAddr())
			conns = append(conns, &InterfaceConn{
				Conn:    conn,
				Addr:    serverAddr,
				Latency: 999 * time.Second, // Initialize with high latency
			})
			break // Move to the next interface after finding one valid IP
		}
	}
	return conns
}

// healthChecker sends periodic health checks on all interfaces to measure latency
func healthChecker(s *Scheduler, key []byte) {
	var healthCheckSeq uint64 = 0
	pendingChecks := make(map[uint64]time.Time)
	var mu sync.Mutex

	go func() {
		for {
			time.Sleep(2 * time.Second)
			interfaces := s.GetInterfaces()
			if len(interfaces) == 0 {
				continue
			}

			mu.Lock()
			currentSeq := healthCheckSeq
			healthCheckSeq++
			mu.Unlock()

			header := PacketHeader{SequenceNumber: currentSeq, Type: PacketHealthCheck}
			payload, err := encrypt(header.Serialize(), key)
			if err != nil {
				log.Printf("Health Check: Failed to encrypt: %v", err)
				continue
			}

			mu.Lock()
			pendingChecks[currentSeq] = time.Now()
			mu.Unlock()

			for _, iface := range interfaces {
				if _, err := iface.Conn.Write(payload); err != nil {
					// Don't log error here, as it can be noisy if an interface goes down
				}
			}
		}
	}()

	// This function is called from the main read loop when a health check response is received
	handleHealthCheckResponse := func(header PacketHeader) {
		mu.Lock()
		defer mu.Unlock()

		if startTime, ok := pendingChecks[header.SequenceNumber]; ok {
			latency := time.Since(startTime)
			delete(pendingChecks, header.SequenceNumber)
			// This is a bit of a hack: we don't know which interface delivered the response.
			// We'll update the latency for ALL interfaces. The 'speed' scheduler will
			// still pick the one that consistently delivers first.
			interfaces := s.GetInterfaces()
			for _, iface := range interfaces {
				iface.SetLatency(latency)
			}
			log.Printf("Health check RTT: %v (updated %d interfaces)", latency, len(interfaces))
		}
	}

	// Expose the handler
	_ = handleHealthCheckResponse // This is just to show where it would be used
}

func main() {
	serverIP := flag.String("server", "", "Server IP address")
	serverPort := flag.Int("port", 8080, "Server port")
	keyStr := flag.String("key", "", "32-byte pre-shared key for encryption")
	mode := flag.String("mode", "speed", "Scheduler mode: speed or redundant")
	flag.Parse()

	if *serverIP == "" || *keyStr == "" {
		log.Fatalf("Server IP (-server) and key (-key) are required")
	}
	if *mode != "speed" && *mode != "redundant" {
		log.Fatalf("Invalid mode: %s. Must be 'speed' or 'redundant'", *mode)
	}
	key := []byte(*keyStr)
	if len(key) != 32 {
		log.Fatalf("Key must be 32 bytes long")
	}

	// Create TUN interface
	tun, err := createTun("speedify-tun", "10.200.0.2/24")
	if err != nil {
		log.Fatalf("Failed to create TUN interface: %v", err)
	}
	defer tun.Close()

	// Resolve server address
	serverAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", *serverIP, *serverPort))
	if err != nil {
		log.Fatalf("Failed to resolve server address: %v", err)
	}

	// Discover interfaces and create scheduler
	interfaces := discoverInterfaces(serverAddr)
	if len(interfaces) == 0 {
		log.Fatalf("No usable network interfaces found. Please check network connectivity.")
	}
	scheduler := NewScheduler(*mode)
	for _, iface := range interfaces {
		scheduler.AddInterface(iface)
	}

	// Start health checker
	pendingChecks := &sync.Map{}
	go func() {
		var hcSeq uint64
		for {
			time.Sleep(2 * time.Second)
			interfaces := scheduler.GetInterfaces()
			if len(interfaces) == 0 {
				continue
			}

			currentSeq := atomic.AddUint64(&hcSeq, 1)
			header := PacketHeader{SequenceNumber: currentSeq, Type: PacketHealthCheck}
			payload, err := encrypt(header.Serialize(), key)
			if err != nil {
				continue
			}

			pendingChecks.Store(currentSeq, time.Now())
			for _, iface := range interfaces {
				iface.Conn.Write(payload)
			}
		}
	}()

	// Goroutine to listen on all interfaces for server responses
	var wg sync.WaitGroup
	for _, iface := range scheduler.GetInterfaces() {
		wg.Add(1)
		go func(conn *net.UDPConn) {
			defer wg.Done()
			buf := make([]byte, MTU*2)
			for {
				n, err := conn.Read(buf)
				if err != nil {
					// This can happen if the connection is closed.
					return
				}

				decrypted, err := decrypt(buf[:n], key)
				if err != nil {
					continue
				}

				var header PacketHeader
				if err := header.Deserialize(decrypted); err != nil {
					continue
				}

				if header.Type == PacketHealthCheck {
					if startTime, ok := pendingChecks.Load(header.SequenceNumber); ok {
						latency := time.Since(startTime.(time.Time))
						// We don't know which interface got the reply first, so we update the one
						// that received the packet. This is a simple but effective approach.
						connIface := scheduler.interfaces[0] // Find the correct one
						for _, i := range scheduler.interfaces {
							if i.Conn == conn {
								connIface = i
								break
							}
						}
						connIface.SetLatency(latency)
						log.Printf("Health check RTT on %s: %v", conn.LocalAddr(), latency)
						pendingChecks.Delete(header.SequenceNumber)
					}
					continue
				}

				if header.Type == PacketData {
					_, err := tun.Write(decrypted[HEADER_SIZE:])
					if err != nil {
						log.Printf("Error writing to TUN: %v", err)
					}
				}
			}
		}(iface.Conn)
	}

	// Main loop: Read from TUN -> Encrypt -> Send to Server
	var clientSeq uint64 = 0
	packet := make([]byte, MTU)
	for {
		n, err := tun.Read(packet)
		if err != nil {
			log.Printf("Error reading from TUN: %v", err)
			break
		}

		header := PacketHeader{SequenceNumber: clientSeq, Type: PacketData}
		clientSeq++

		payload := append(header.Serialize(), packet[:n]...)
		encryptedPayload, err := encrypt(payload, key)
		if err != nil {
			log.Printf("Failed to encrypt packet: %v", err)
			continue
		}

		if *mode == "redundant" {
			for _, iface := range scheduler.GetInterfaces() {
				_, err := iface.Conn.Write(encryptedPayload)
				if err != nil {
					// Don't log, can be noisy
				}
			}
		} else {
			iface := scheduler.SelectInterface()
			if iface == nil {
				log.Println("No available interface to send packet")
				continue
			}
			_, err := iface.Conn.Write(encryptedPayload)
			if err != nil {
				// Don't log, can be noisy
			}
		}
	}

	wg.Wait() // Wait for listener goroutines to finish
}
