"# Multi-WireGuard Support in Client-Agent

## Overview

The client-agent supports connecting a single client machine to **multiple WireGuard servers** simultaneously. This is achieved through intelligent discovery and merging of peer configurations from multiple WireGuard instances.

## How It Works

### Architecture

```
Client Machine (Public Key: xYz123...)
┌────────────────────────────────────────────────┐
│  Client-Agent                                  │
│  1. Scans *-peer-configs secrets               │
│  2. Finds keys: xYz123...-peer-config          │
│  3. Merges configs by Interface                │
│  4. Applies with syncconf                      │
└────────────────┬───────────────────────────────┘
                 │
                 │ Single wg0 interface
                 │
    ┌────────────┼────────────┬────────────┐
    │            │            │            │
    ▼            ▼            ▼            ▼
┌────────┐  ┌────────┐  ┌────────┐  ┌────────┐
│ Prod   │  │ Dev    │  │ Staging│  │ Test   │
│ VPN    │  │ VPN    │  │ VPN    │  │ VPN    │
│10.8.0.1│  │10.9.0.1│  │10.10.0.1│ │10.11.0.1│
└────────┘  └────────┘  └────────┘  └────────┘
```

### Configuration Flow

1. **Controller creates secrets:**
   ```
   vpn-prod-peer-configs
   ├── xYz123...-peer-config  (complete .conf for prod)
   └── other-peer-config
   
   vpn-dev-peer-configs
   ├── xYz123...-peer-config  (complete .conf for dev)
   └── other-peer-config
   
   vpn-staging-peer-configs
   ├── xYz123...-peer-config  (complete .conf for staging)
   └── other-peer-config
   ```

2. **Client-agent discovers and merges:**
   ```bash
   # Client-agent scans all *-peer-configs secrets
   # Finds all keys matching: xYz123...-peer-config
   # Merges into single config:
   
   [Interface]
   PrivateKey = <client-private-key>
   Address = 10.8.0.3, 10.9.0.3, 10.10.0.3
   DNS = 8.8.8.8, 10.0.0.10, 10.1.0.10
   
   [Peer]  # Production
   PublicKey = <prod-server-key>
   Endpoint = prod.example.com:51820
   AllowedIPs = 10.0.0.0/8
   
   [Peer]  # Development
   PublicKey = <dev-server-key>
   Endpoint = dev.example.com:51820
   AllowedIPs = 172.16.0.0/12
   
   [Peer]  # Staging
   PublicKey = <staging-server-key>
   Endpoint = staging.example.com:51820
   AllowedIPs = 192.168.0.0/16
   ```

3. **Safe application with syncconf:**
   - Only changes that differ are applied
   - Existing connections remain stable
   - New peers are added without interruption
   - Removed peers are cleanly disconnected

## Use Cases

### 1. Multi-Environment Access

Developers needing access to multiple environments:
```yaml
# Production
apiVersion: vpn.wireguard-operator.io/v1alpha1
kind: WireguardPeer
metadata:
  name: dev-machine-prod
spec:
  wireguardRef: vpn-prod
  allowedIPs: "10.0.0.0/8"

# Development
apiVersion: vpn.wireguard-operator.io/v1alpha1
kind: WireguardPeer
metadata:
  name: dev-machine-dev
spec:
  wireguardRef: vpn-dev
  allowedIPs: "172.16.0.0/12"

# Staging
apiVersion: vpn.wireguard-operator.io/v1alpha1
kind: WireguardPeer
metadata:
  name: dev-machine-staging
spec:
  wireguardRef: vpn-staging
  allowedIPs: "192.168.0.0/16"
```

### 2. Hub-and-Spoke Topology

Central office connecting to multiple branches:
```
                  ┌─────────────┐
                  │   Central   │
                  │   Office    │
                  └──────┬──────┘
                         │
         ┌───────────────┼───────────────┐
         │               │               │
    ┌────┴────┐     ┌────┴────┐     ┌────┴────┐
    │ Branch 1│     │ Branch 2│     │ Branch 3│
    │  VPN    │     │  VPN    │     │  VPN    │
    └─────────┘     └─────────┘     └─────────┘
```

### 3. Multi-Cloud Connectivity

Connecting to multiple cloud providers:
```yaml
# AWS VPC
apiVersion: vpn.wireguard-operator.io/v1alpha1
kind: WireguardPeer
metadata:
  name: gateway-aws
spec:
  wireguardRef: vpn-aws
  allowedIPs: "10.0.0.0/8"

# GCP VPC
apiVersion: vpn.wireguard-operator.io/v1alpha1
kind: WireguardPeer
metadata:
  name: gateway-gcp
spec:
  wireguardRef: vpn-gcp
  allowedIPs: "172.16.0.0/12"

# Azure VNet
apiVersion: vpn.wireguard-operator.io/v1alpha1
kind: WireguardPeer
metadata:
  name: gateway-azure
spec:
  wireguardRef: vpn-azure
  allowedIPs: "192.168.0.0/16"
```

## Deployment

### Step 1: Generate Client Key Pair

```bash
# On client machine
wg genkey | tee /etc/wireguard/privatekey | wg pubkey > /etc/wireguard/publickey
PUBLIC_KEY=$(cat /etc/wireguard/publickey)
echo "Client public key: $PUBLIC_KEY"
```

### Step 2: Create Peer Resources

Create `WireguardPeer` resources for each WireGuard instance the client should connect to. The controller will automatically create entries in the `-peer-configs` secrets.

### Step 3: Deploy Client-Agent

```bash
# Systemd service
cat > /etc/systemd/system/client-agent.service <<EOF
[Unit]
Description=WireGuard Operator Client Agent
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/client-agent \\
  --public-key=${PUBLIC_KEY} \\
  --namespace=default \\
  --kubeconfig=/etc/wireguard/kubeconfig \\
  --wg-config-path=/etc/wireguard/wg0.conf \\
  --wg-iface=wg0 \\
  --v=2

Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable client-agent
systemctl start client-agent
```

## Configuration Merging Logic

### Grouping by Interface

Configs are grouped by their `[Interface]` section content:

```go
// Same private key = same group
if hash(interface1) == hash(interface2) {
    // Merge peer sections
    merged.Peers = append(interface1.Peers, interface2.Peers...)
}
```

### Merging Rules

1. **Interface Section:**
   - First occurrence is used as the base
   - Addresses from all configs are combined
   - DNS servers are combined
   - MTU uses the first non-empty value

2. **Peer Sections:**
   - All peer sections are preserved
   - Each represents a different WireGuard server
   - AllowedIPs determine routing

3. **Conflict Resolution:**
   - Duplicate peers (same public key) are deduplicated
   - Last occurrence wins for conflicting settings
   - Warnings are logged for conflicts

## Safe Updates with syncconf

### What is syncconf?

`syncconf` is a tool that safely synchronizes WireGuard configurations by:
- Comparing current and desired states
- Only applying necessary changes
- Preserving active connections
- Avoiding interface restarts

### Installation

```bash
# Ubuntu/Debian
apt install syncconf

# RHEL/CentOS
dnf install syncconf

# From source
go install github.com/JoshuaDoes/syncconf@latest
```

### Fallback Behavior

If `syncconf` is not available:
1. Client-agent falls back to `wg-quick`
2. Interface is restarted (brief interruption)
3. All connections are re-established

**Recommendation:** Always install `syncconf` for production use.

## Monitoring

### Metrics

Each peer connection is tracked separately:

```prometheus
# Bytes sent to each server
wireguard_client_sent_bytes_total{peer_name="prod"}
wireguard_client_sent_bytes_total{peer_name="dev"}
wireguard_client_sent_bytes_total{peer_name="staging"}

# Connection state for each server
wireguard_client_connected{peer_name="prod"}  # 1 = connected
wireguard_client_connected{peer_name="dev"}   # 1 = connected
wireguard_client_connected{peer_name="staging"}  # 0 = disconnected
```

### Health Checks

Health endpoint verifies:
- Configuration file exists
- At least one peer is configured
- WireGuard interface is up
- At least one peer has recent handshake

```bash
curl http://localhost:8082/health
```

## Troubleshooting

### Check discovered configs

```bash
# Enable verbose logging
journalctl -u client-agent -f --no-pager | grep "found peer config"

# Should show:
# found peer config wireguard=vpn-prod key=xYz123...-peer-config
# found peer config wireguard=vpn-dev key=xYz123...-peer-config
```

### Verify merged configuration

```bash
# View generated config
cat /etc/wireguard/wg0.conf

# Should show multiple [Peer] sections
```

### Check individual connections

```bash
# Show all peers and their status
wg show wg0

# Look for multiple peers with different endpoints
# Each should show "latest handshake" timestamp
```

### Test connectivity to each network

```bash
# Test production network
ping 10.0.0.1

# Test development network
ping 172.16.0.1

# Test staging network
ping 192.168.0.1
```

### Common Issues

**Issue: Only one peer connected**
- Check that all `WireguardPeer` resources are in `Ready` status
- Verify secrets contain the correct `<publicKey>-peer-config` keys
- Check client-agent logs for parsing errors

**Issue: Connection drops during updates**
- Install `syncconf` for safe updates
- Without syncconf, `wg-quick` restarts the interface

**Issue: Routing conflicts**
- Ensure `AllowedIPs` don't overlap between peers
- Or use more specific routes with policy routing

## Best Practices

1. **Use descriptive peer names:**
   ```yaml
   metadata:
     name: developer-laptop-prod  # Clear purpose
   ```

2. **Restrict AllowedIPs:**
   ```yaml
   spec:
     allowedIPs: "10.0.0.0/8"  # Only necessary networks
   ```

3. **Set persistent keepalive for NAT:**
   ```yaml
   spec:
     persistentKeepalive: 25
   ```

4. **Monitor all connections:**
   ```prometheus
   # Alert if any peer is disconnected
   wireguard_client_connected == 0
   ```

5. **Use syncconf in production:**
   - Prevents connection drops during updates
   - Essential for multi-peer setups

## Advanced: Custom Interface Grouping

If you need separate interfaces for different WireGuard networks:

```bash
# Run multiple client-agent instances with different public keys
# Instance 1: --public-key=PROD_KEY --wg-iface=wg-prod
# Instance 2: --public-key=DEV_KEY --wg-iface=wg-dev
```

This creates separate `wg0`, `wg1`, etc. interfaces for isolation.

## See Also

- [Client-Agent Quick Start](client-agent-quickstart.md)
- [Client-Agent Documentation](client-agent.md)
- [Architecture Overview](architecture-overview.md)
- [Multi-WireGuard Example](../examples/multi-wireguard-client.yaml)
"