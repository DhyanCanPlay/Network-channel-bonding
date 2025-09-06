# Go Multipath "Speedify" Clone

This project is a command-line application that replicates the core functionality of a multipath bonding VPN like Speedify. It aggregates the bandwidth of multiple internet connections (e.g., Wi-Fi and Ethernet) to provide a faster and more reliable connection.

The project consists of two Go applications:
- `server.go`: The server component, intended to run on a VPS with a public IP.
- `client.go`: The client component, intended to run on a local machine (Linux, macOS, and Windows).

## Core Features

- **Multipath Traffic Bonding**: Utilizes all or a selection of network interfaces on the client to send traffic to the server.
- **Two Modes of Operation**:
    - `speed`: Distributes traffic based on the lowest latency interface, dynamically measured via health checks.
    - `redundant`: Sends every packet over all available interfaces to maximize reliability.
- **Interface Selection**: The client can list available network interfaces and be configured to use only specific ones.
- **AES-256-GCM Encryption**: All traffic between the client and server is encrypted using a pre-shared key.
- **Packet Reassembly**: The server reassembles out-of-order packets to ensure a smooth data stream.
- **Cross-Platform**: Supports Linux, macOS, and Windows.

## Prerequisites

1.  **Go**: Ensure you have Go (1.18+) installed on both client and server machines.
2.  **Root/Admin Privileges**: The applications require elevated privileges to create and manage network interfaces and routes.

This project uses Go modules, so the required `water` library will be downloaded automatically when you build the applications.

---

## 1. Compile the Applications

Clone the repository or save `server.go` and `client.go` into a directory.

**On the Linux Server:**
```sh
# Compile the server
go build server.go
```

**On the Client Machine (e.g., Windows):**
If you are cross-compiling for Windows from a Linux/macOS machine, use:
```sh
GOOS=windows GOARCH=amd64 go build client.go
```
If you are on the Windows machine directly, simply use:
```sh
go build client.go
```

---

## 2. Server Configuration (on your Ubuntu 22.04 VPS)

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

---

## 3. Windows Client Setup

#### a. Install TAP Driver

The client requires a TAP driver to create the virtual network interface on Windows.
1.  Download the official OpenVPN community installer, which includes the TAP driver: [OpenVPN Community Downloads](https://openvpn.net/community-downloads/)
2.  Run the installer.
3.  During installation, ensure that **only the "TAP Virtual Ethernet Adapter"** component is selected. You do not need to install the full OpenVPN client.

#### b. Find and Select Network Interfaces

First, run the client with the `--list-interfaces` flag in `cmd` or `PowerShell` (as Administrator) to see which interfaces you can use.

```sh
.\client.exe --list-interfaces
```
This will output something like:
```
Available network interfaces:
- Interface: Ethernet
  - IPv4 Address: 192.168.1.100
- Interface: Wi-Fi
  - IPv4 Address: 192.168.1.101
```

#### c. Configure Routing and Run the Client

You must run these commands in a command prompt (`cmd`) or PowerShell with **Administrator privileges**.

1.  **Add a route for the VPN server**: This ensures you can connect to the server *before* the tunnel is established. Replace `YOUR_VPS_IP` and `YOUR_GATEWAY_IP` (your router's IP, e.g., `192.168.1.1`). Find your gateway by running `ipconfig`.

    ```sh
    route add YOUR_VPS_IP mask 255.255.255.255 YOUR_GATEWAY_IP
    ```

2.  **Run the client**: Use the `--interfaces` flag to provide a comma-separated list of the IPv4 addresses you chose from the list above.

    ```sh
    # Example using Ethernet and Wi-Fi from the example above
    .\client.exe -server YOUR_VPS_IP -port 8080 -key "this-is-a-very-secure-32-byte-key" -interfaces "192.168.1.100,192.168.1.101"
    ```
    Once the client connects, it will say "Successfully configured TUN interface...". The name might be "Ethernet 2" or similar.

3.  **Change the default route**: Now, tell Windows to send all internet traffic through the VPN. The TUN interface IP is hard-coded to `10.200.0.2`, and its gateway is the server's side of the tunnel, `10.200.0.1`.

    ```sh
    # This command deletes your current default route and adds a new one through the VPN
    route add 0.0.0.0 mask 0.0.0.0 10.200.0.1 metric 5
    ```

Your internet traffic is now being routed through the VPN.

#### d. How to Stop and Clean Up

1.  Stop the client by pressing `Ctrl+C`.
2.  Delete the routes you added:
    ```sh
    route delete 0.0.0.0
    route delete YOUR_VPS_IP
    ```
    Your original default route will be restored automatically by Windows.

---

## 4. Linux & macOS Client Setup

#### a. Configure Routing Table

Run these commands as root.

```sh
# Add a specific route for your VPS so the client can connect to it directly.
# Find your gateway IP with: ip route | grep default
# Example: default via 192.168.1.1 dev eno1
sudo ip route add YOUR_VPS_IP via YOUR_GATEWAY_IP

# Add two specific routes that override the default route for all other traffic.
sudo ip route add 0.0.0.0/1 dev speedify-tun
sudo ip route add 128.0.0.0/1 dev speedify-tun
```

#### b. Run the Client

You can list interfaces first (`sudo ./client --list-interfaces`) and then select them with the `--interfaces` flag, or omit the flag to use all available interfaces.

```sh
# Example with specific interfaces
sudo ./client -server YOUR_VPS_IP -port 8080 -key "this-is-a-very-secure-32-byte-key" -interfaces "192.168.1.100,10.0.0.10"

# Example using all available interfaces
sudo ./client -server YOUR_VPS_IP -port 8080 -key "this-is-a-very-secure-32-byte-key" -mode speed
```

#### c. How to Stop and Clean Up
1.  Stop the client with `Ctrl+C`.
2.  The routes are not persistent. To remove them manually:
    ```sh
    sudo ip route del YOUR_VPS_IP
    sudo ip route del 0.0.0.0/1
    sudo ip route del 128.0.0.0/1
    ```
