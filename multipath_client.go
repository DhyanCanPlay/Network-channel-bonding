package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

// This is a minimal multipath UDP proxy client for Windows/Linux/macOS.
// It does NOT require TUN/TAP or admin rights. It bonds two or more interfaces for UDP traffic only.
// For production, you should add encryption, authentication, and error correction.

// Usage: go run multipath_client.go <server_ip:port> <remote_target_ip:port> <local_interface1_ip> <local_interface2_ip> ...

func main() {
	if len(os.Args) < 4 {
		fmt.Println("Usage: multipath_client <server_ip:port> <remote_target_ip:port> <local_interface1_ip> <local_interface2_ip> ...")
		os.Exit(1)
	}
	serverAddr := os.Args[1]
	remoteTarget := os.Args[2]
	localIPs := os.Args[3:]

	// Open UDP sockets on each interface
	var conns []*net.UDPConn
	for _, ip := range localIPs {
		laddr := &net.UDPAddr{IP: net.ParseIP(ip), Port: 0}
		conn, err := net.ListenUDP("udp", laddr)
		if err != nil {
			log.Fatalf("Failed to bind to %s: %v", ip, err)
		}
		conns = append(conns, conn)
		log.Printf("Bound to %s", conn.LocalAddr())
	}

	// Start goroutine to receive from server and print
	var wg sync.WaitGroup
	for _, conn := range conns {
		wg.Add(1)
		go func(c *net.UDPConn) {
			defer wg.Done()
			buf := make([]byte, 2048)
			for {
				n, addr, err := c.ReadFromUDP(buf)
				if err != nil {
					return
				}
				fmt.Printf("Received from %s: %s\n", addr, strings.TrimSpace(string(buf[:n])))
			}
		}(conn)
	}

	// Read from stdin and send to server via all interfaces (round-robin)
	serverUDP, err := net.ResolveUDPAddr("udp", serverAddr)
	if err != nil {
		log.Fatalf("Failed to resolve server: %v", err)
	}
	input := make([]byte, 2048)
	idx := 0
	for {
		n, err := os.Stdin.Read(input)
		if err != nil {
			break
		}
		// Send to server via one interface (round-robin)
		conn := conns[idx%len(conns)]
		conn.WriteToUDP(input[:n], serverUDP)
		idx++
	}
	wg.Wait()
}
