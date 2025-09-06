# Go Multipath "Speedify" Clone

This project is a command-line application that replicates the core functionality of a multipath bonding VPN like Speedify. It aggregates the bandwidth of multiple internet connections (e.g., Wi-Fi and Ethernet) to provide a faster and more reliable connection.

The project consists of two Go applications:
- `server.go`: The server component, intended to run on a VPS with a public IP.
- `client.go`: The client component, intended to run on a local machine.

## Core Features

- **Multipath Traffic Bonding**: Utilizes all available network interfaces on the client to send traffic to the server.
- **Two Modes of Operation**:
    - `speed`: Distributes traffic based on the lowest latency interface, dynamically measured via health checks.
    - `redundant`: Sends every packet over all available interfaces to maximize reliability.
- **AES-256-GCM Encryption**: All traffic between the client and server is encrypted using a pre-shared key.
- **Packet Reassembly**: The server reassembles out-of-order packets to ensure a smooth data stream.
- **Cross-Platform TUN/TAP**: Uses the `water` library to create virtual network interfaces on Linux and macOS.

## Prerequisites

1.  **Go**: Ensure you have Go (1.18+) installed.
2.  **Water Library**: Install the `water` library dependency.
    ```sh
    go get github.com/songgao/water
    ```
3.  **Root/Admin Privileges**: The applications require elevated privileges to create and manage network interfaces and routes.

---

## Setup & Usage

### 1. Compile the Applications

Clone the repository or save `server.go` and `client.go` into a directory. Then, compile them:

```sh
# Compile the server
go build server.go

# Compile the client
go build client.go
```

### 2. Server Configuration (on your VPS)

Let's assume your VPS public IP is `YOUR_VPS_IP`.

#### a. Configure Firewall and Kernel Parameters

Run these commands as root on your server. This enables IP forwarding and sets up NAT to route traffic from the VPN to the public internet.

**IMPORTANT**: Replace `eth0` with your server's primary public network interface (e.g., `ens3`, `enp0s3`). You can find it using `ip addr`.

```sh
# --- Kernel Parameters ---
# Enable IP forwarding
sudo sysctl -w net.ipv4.ip_forward=1

# Make the change persistent across reboots
echo "net.ipv4.ip_forward=1" | sudo tee /etc/sysctl.d/99-ip_forward.conf

# --- IPTables Firewall Rules ---
# Allow traffic from the VPN subnet (10.200.0.0/24)
sudo iptables -A FORWARD -i speedify-tun -o eth0 -j ACCEPT
sudo iptables -A FORWARD -i eth0 -o speedify-tun -m state --state RELATED,ESTABLISHED -j ACCEPT

# Set up NAT to masquerade traffic from the VPN client
sudo iptables -t nat -A POSTROUTING -s 10.200.0.0/24 -o eth0 -j MASQUERADE

# Allow UDP traffic on your chosen port (e.g., 8080)
sudo iptables -A INPUT -p udp --dport 8080 -j ACCEPT

# It's recommended to save iptables rules. For Debian/Ubuntu:
# sudo apt-get update && sudo apt-get install iptables-persistent -y
# sudo netfilter-persistent save
```

#### b. Run the Server

Choose a strong, 32-character pre-shared key.

```sh
# Example key: "this-is-a-very-secure-32-byte-key"
sudo ./server -port 8080 -key "this-is-a-very-secure-32-byte-key"
```
The server is now running and waiting for a client to connect.

### 3. Client Configuration (on your local machine)

#### a. Configure Routing Table

This command tells your OS to route all internet traffic through the virtual `speedify-tun` interface instead of your default gateway.

**IMPORTANT**: This will redirect your *entire* internet connection through the VPN.

```sh
# macOS and Linux
# Add two specific routes that override the default route.
# This is more robust than deleting and re-adding the default route.
sudo ip route add 0.0.0.0/1 dev speedify-tun
sudo ip route add 128.0.0.0/1 dev speedify-tun

# You must also add a specific route for your VPS so the client
# can connect to it directly, not via the tunnel.
# Find your current gateway IP with: ip route | grep default
# Example: default via 192.168.1.1 dev eno1
sudo ip route add YOUR_VPS_IP via YOUR_GATEWAY_IP
```
*Example*: If your VPS IP is `203.0.113.10` and your home router is `192.168.1.1`, the command would be:
`sudo ip route add 203.0.113.10 via 192.168.1.1`

#### b. Run the Client

Use the same pre-shared key as the server.

**For Speed Mode (Best Performance):**
```sh
sudo ./client -server YOUR_VPS_IP -port 8080 -key "this-is-a-very-secure-32-byte-key" -mode speed
```

**For Redundant Mode (Best Reliability):**
```sh
sudo ./client -server YOUR_VPS_IP -port 8080 -key "this-is-a-very-secure-32-byte-key" -mode redundant
```

Once the client is running, all your traffic will be securely tunneled through your VPS.

### 4. How to Stop and Clean Up

1.  **Stop the Client/Server**: Press `Ctrl+C` in the terminal where the application is running.
2.  **Restore Client Routing**: The routes added on the client are not persistent and will be removed on reboot. To remove them manually:
    ```sh
    sudo ip route del 0.0.0.0/1
    sudo ip route del 128.0.0.0/1
    sudo ip route del YOUR_VPS_IP
    ```
3.  **Server Firewall**: The `iptables` rules can be removed by replacing `-A` (Append) with `-D` (Delete) in the original commands. If you used `iptables-persistent`, you can edit the rules file directly.
