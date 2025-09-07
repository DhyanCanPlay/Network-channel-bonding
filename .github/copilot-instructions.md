# Copilot Instructions for AI Agents

## Project Overview
This project implements a multipath VPN (Speedify-like) in Go, with two main entrypoints:
- `client.go`: The client, designed for Windows, Linux, and macOS, bonds multiple local interfaces and creates a TUN device.
- `server.go`: The server, designed for Linux VPS, aggregates and decrypts traffic, reassembles packets, and routes to the internet.

## Architecture & Data Flow
- **Multipath UDP**: Client discovers and binds to multiple local interfaces, sending encrypted UDP packets to the server.
- **Custom Protocol**: Each packet has a custom header (sequence, type) and is encrypted with AES-256-GCM using a pre-shared key.
- **Modes**: 'speed' (lowest-latency interface) and 'redundant' (send on all interfaces).
- **TUN Device**: Both client and server use a TUN device for IP-level routing. On Windows, the client requires the OpenVPN TAP driver.
- **Health Checks**: Client sends health-check packets to measure interface latency and select the best path.

## Build & Run Workflows
- **Go 1.18+ required**. Uses Go modules and the `github.com/songgao/water` package for TUN support.
- **Build (Windows):**
  ```sh
  go build client.go
  ```
- **Build (Linux/macOS):**
  ```sh
  go build client.go
  go build server.go
  ```
- **Cross-compile for Windows:**
  ```sh
  GOOS=windows GOARCH=amd64 go build client.go
  ```
- **Run as Administrator/root**: Required for TUN/TAP device creation and routing changes.

## Windows-Specific Setup
- **TAP Driver**: User must install the OpenVPN TAP driver (see README for link/instructions).
- **Routing**: Client adds/removes routes for VPN and default gateway via `route` commands.
- **Interface Selection**: Use `--list-interfaces` to enumerate usable interfaces and `--interfaces` to select by IP.

## Key Conventions & Patterns
- **No shared package**: Protocol constants and helpers are duplicated in both `client.go` and `server.go` for simplicity.
- **MTU**: Set to 1400 to accommodate protocol overhead.
- **Encryption**: Always AES-256-GCM with a 32-byte pre-shared key (user-supplied at runtime).
- **TUN Naming**: Client TUN is `speedify-tun` with IP `10.200.0.2/24`; server TUN is `speedify-tun` with IP `10.200.0.1/24`.
- **No persistent config files**: All configuration is via command-line flags.

## Integration Points
- **External**: Relies on the `water` Go package for TUN/TAP, and the OpenVPN TAP driver on Windows.
- **System**: Uses `ip`/`ifconfig`/`netsh` for interface setup, and `route` for routing changes.

## Examples
- List interfaces (Windows):
  ```sh
  .\client.exe --list-interfaces
  ```
- Run client (Windows):
  ```sh
  .\client.exe -server 1.2.3.4 -port 8080 -key "32-byte-key" -interfaces "192.168.1.100,192.168.1.101"
  ```
- Run server (Linux):
  ```sh
  sudo ./server -port 8080 -key "32-byte-key"
  ```

## See Also
- `README.md` for full setup, routing, and troubleshooting instructions.
- `client.go` and `server.go` for protocol and workflow details.
