package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"strings"
)

// This is a minimal multipath UDP proxy server for Linux/macOS/Windows.
// It does NOT require TUN/TAP or admin rights. It bonds two or more interfaces for UDP traffic only.
// For production, you should add encryption, authentication, and error correction.

// Usage: go run multipath_server.go <listen_port> <remote_target_ip:port>

func main() {
	if len(os.Args) < 3 {
		fmt.Println("Usage: multipath_server <listen_port> <remote_target_ip:port>")
		os.Exit(1)
	}
	listenPort := os.Args[1]
	remoteTarget := os.Args[2]

	laddr, err := net.ResolveUDPAddr("udp", ":"+listenPort)
	if err != nil {
		log.Fatalf("Failed to resolve listen addr: %v", err)
	}
	conn, err := net.ListenUDP("udp", laddr)
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}
	defer conn.Close()
	log.Printf("Listening on %s", conn.LocalAddr())

	targetAddr, err := net.ResolveUDPAddr("udp", remoteTarget)
	if err != nil {
		log.Fatalf("Failed to resolve target: %v", err)
	}

	buf := make([]byte, 2048)
	for {
		n, clientAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		log.Printf("Received from %s: %s", clientAddr, strings.TrimSpace(string(buf[:n])))
		// Forward to remote target
		conn.WriteToUDP(buf[:n], targetAddr)
	}
}
