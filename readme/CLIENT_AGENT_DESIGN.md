# Client-Agent Design Document

## Overview

The client-agent enables external machines to join a WireGuard mesh network managed by the wireguard-operator. It watches Kubernetes for `WireguardPeer` resources matching the client's public key and automatically configures the local WireGuard interface.

## Key Design Decisions

### 1. Public Key-Based Matching

**Decision:** Client-agent identifies itself using its WireGuard public key, not a peer name.

**Rationale:**
- A single machine may have multiple `WireguardPeer` resources (connecting to different WireGuard instances)
- Public key is the natural identifier in WireGuard
- Eliminates need to configure peer name on the client
- More secure - matches on cryptographic identity

**Implementation:**
```bash
client-agent --public-key=$(cat /etc/wireguard/publickey)
```

### 2. Leveraging Existing Peer Configuration Secrets

**Decision:** Read configurations from `<wireguard-name>-peer-configs` secrets created by the controller.

**Rationale:**
- Controller already generates complete, validated WireGuard configurations
- Avoids duplicating configuration generation logic
- Ensures consistency between server and client views
- Supports all controller features (MTU, DNS, routing, etc.)

**Implementation:**
```go
// Fetch secret: vpn-peer-configs
secret := &corev1.Secret{}
client.Get(ctx, "vpn-peer-configs", secret)

// Extract config for this peer: secret.Data["client-machine-1"]
configData := secret.Data[peer.Name]
```

### 3. Supporting Multiple WireGuard Instances

**Decision:** Aggregate configurations from multiple `WireguardPeer` resources.

**Rationale:**
- A client may connect to multiple WireGuard meshes simultaneously
- Each mesh may have different interface configurations
- Need to merge configurations intelligently

**Implementation:**
```go
// List all peers matching this public key
for _, peer := range peerList.Items {
    if peer.Spec.PublicKey == clientPublicKey {
        // Fetch and process config
    }
}
```

### 4. Grouping by Interface Configuration

**Decision:** Group peer configurations by identical `[Interface]` sections.

**Rationale:**
- Peers connecting to the same WireGuard instance share the same interface config
- Different WireGuard instances may have different settings (MTU, DNS, addresses)
- Grouping allows merging multiple peers under one interface

**Implementation:**
```go
// Extract [Interface] section as grouping key
interfaceKey := extractInterfaceSection(configData)
groups[interfaceKey] = append(groups[interfaceKey], peerConfig)
```

### 5. Using syncconf for Safe Updates

**Decision:** Prefer `syncconf` to apply configuration changes.

**Rationale:**
- `syncconf` updates WireGuard configuration without disrupting existing connections
- Critical for production systems where connectivity must be maintained
- Falls back to `wg-quick` if `syncconf` unavailable
- Last resort: `wgctl` for runtime-only updates

**Implementation:**
```bash
# Preferred: syncconf (no disruption)
syncconf -v wg0 /etc/wireguard/wg0.conf

# Fallback: wg-quick (may restart interface)
wg-quick up wg0

# Last resort: wgctl (runtime only, no persistence)
wgctl set wg0 ...
```

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│  Client Machine                                          │
│                                                          │
│  ┌──────────────────────────────────────────────────┐  │
│  │  Client-Agent                                     │  │
│  │  - Public Key: abc123...                         │  │
│  │  - Polls every 30s                               │  │
│  └──────────────────┬───────────────────────────────┘  │
│                     │                                   │
│                     │ List WireguardPeers               │
│                     │ Filter by publicKey=abc123...     │
│                     ▼                                   │
│  ┌──────────────────────────────────────────────────┐  │
│  │  Kubernetes API                                   │  │
│  │  - WireguardPeer(client-machine-1)               │  │
│  │  - WireguardPeer(client-machine-2)               │  │
│  │  (both have same publicKey)                      │  │
│  └──────────────────┬───────────────────────────────┘  │
│                     │                                   │
│                     │ Fetch secrets:                    │
│                     │ - vpn-peer-configs                │
│                     │ - mesh-peer-configs               │
│                     ▼                                   │
│  ┌──────────────────────────────────────────────────┐  │
│  │  Peer Configuration Secrets                       │  │
│  │  vpn-peer-configs:                                │  │
│  │    client-machine-1: [Interface]...[Peer]...     │  │
│  │  mesh-peer-configs:                               │  │
│  │    client-machine-2: [Interface]...[Peer]...     │  │
│  └──────────────────┬───────────────────────────────┘  │
│                     │                                   │
│                     │ Group by [Interface]              │
│                     │ Merge [Peer] sections             │
│                     ▼                                   │
│  ┌──────────────────────────────────────────────────┐  │
│  │  Generated wg0.conf                               │  │
│  │  [Interface] (from vpn)                           │  │
│  │    PrivateKey = ...                               │  │
│  │    Address = 10.8.0.3                             │  │
│  │  [Peer] (vpn server)                              │  │
│  │    PublicKey = ...                                │  │
│  │    Endpoint = vpn.example.com:51820               │  │
│  │  [Peer] (optional: other peers)                   │  │
│  │                                                   │  │
│  │  [Interface] (from mesh)                          │  │
│  │    PrivateKey = ...                               │  │
│  │    Address = 10.9.0.5                             │  │
│  │  [Peer] (mesh server)                             │  │
│  │    PublicKey = ...                                │  │
│  │    Endpoint = mesh.example.com:51820              │  │
│  └──────────────────┬───────────────────────────────┘  │
│                     │                                   │
│                     │ Apply with syncconf               │
│                     ▼                                   │
│  ┌──────────────────────────────────────────────────┐  │
│  │  WireGuard Interface (wg0)                        │  │
│  │  - Multiple peers configured                      │  │
│  │  - No disruption to existing connections          │  │
│  └───────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────┘
```

## Workflow

### Initial Setup

1. **Generate Keys on Client:**
   ```bash
   wg genkey | tee privatekey | wg pubkey > publickey
   PUBLIC_KEY=$(cat publickey)
   ```

2. **Create WireguardPeer Resource:**
   ```yaml
   apiVersion: vpn.wireguard-operator.io/v1alpha1
   kind: WireguardPeer
   metadata:
     name: client-machine-1
   spec:
     wireguardRef: "vpn"
     publicKey: "$PUBLIC_KEY"  # Match client's public key
   ```

3. **Controller Actions:**
   - Validates peer configuration
   - Allocates IP address
   - Generates complete WireGuard config
   - Stores in `<wireguard-name>-peer-configs` secret

4. **Client-Agent Starts:**
   ```bash
   client-agent --public-key=$PUBLIC_KEY --namespace=default
   ```

5. **Reconciliation Loop:**
   - Lists all `WireguardPeer` resources
   - Filters by matching `spec.publicKey`
   - For each match, fetches `<wireguard-ref>-peer-configs` secret
   - Extracts config from `secret.Data[peer.Name]`
   - Groups by `[Interface]` section
   - Merges `[Peer]` sections
   - Applies with `syncconf`

### Handling Updates

When a `WireguardPeer` is updated:

1. Controller updates the peer configuration secret
2. Client-agent detects change on next poll (30s)
3. Regenerates merged configuration
4. Uses `syncconf` to apply changes without disruption
5. Existing connections remain active

### Handling Multiple WireGuard Instances

If a client connects to multiple meshes:

```bash
# Peer 1: Connects to "vpn" instance
kubectl apply -f - <<EOF
apiVersion: vpn.wireguard-operator.io/v1alpha1
kind: WireguardPeer
metadata:
  name: client-vpn
spec:
  wireguardRef: "vpn"
  publicKey: "$PUBLIC_KEY"
EOF

# Peer 2: Connects to "mesh" instance
kubectl apply -f - <<EOF
apiVersion: vpn.wireguard-operator.io/v1alpha1
kind: WireguardPeer
metadata:
  name: client-mesh
spec:
  wireguardRef: "mesh"
  publicKey: "$PUBLIC_KEY"
EOF
```

Client-agent will:
- Fetch both `vpn-peer-configs` and `mesh-peer-configs` secrets
- Extract configs for `client-vpn` and `client-mesh`
- If interfaces differ, create multiple interface sections
- If interfaces match, merge peer sections
- Apply unified configuration

## Configuration Format

### Input (from Secret)

Each peer configuration in the secret is a complete WireGuard config:

```
[Interface]
PrivateKey = <server-private-key>
Address = 10.8.0.3/24
DNS = 8.8.8.8
MTU = 1380

[Peer]
PublicKey = <server-public-key>
Endpoint = vpn.example.com:51820
AllowedIPs = 0.0.0.0/0
```

### Output (Merged)

Client-agent generates a merged config:

```
# WireGuard configuration generated by client-agent
# Public Key: abc123...

[Interface]
PrivateKey = <server-private-key>
Address = 10.8.0.3/24
DNS = 8.8.8.8
MTU = 1380

[Peer]
PublicKey = <server-public-key>
Endpoint = vpn.example.com:51820
AllowedIPs = 0.0.0.0/0

[Peer]
PublicKey = <another-peer-public-key>
Endpoint = peer2.example.com:51820
AllowedIPs = 10.8.0.4/32
```

## Security Considerations

### Key Management
- Private keys never leave the client machine
- Public keys used for identification (not secret)
- Secrets contain server configurations, not client private keys

### RBAC Permissions
Minimal read-only access:
```yaml
rules:
- apiGroups: ["vpn.wireguard-operator.io"]
  resources: ["wireguardpeers", "wireguards"]
  verbs: ["get", "list", "watch"]
- apiGroups: [""]
  resources: ["secrets"]
  verbs: ["get"]
```

### Configuration File Security
- Written with `0600` permissions
- Owned by root
- Stored in `/etc/wireguard/` (secure directory)

## Advantages Over Previous Design

### Previous Design (Peer Name-Based)
- ❌ Required configuring peer name on client
- ❌ One peer per client-agent instance
- ❌ Duplicated configuration generation logic
- ❌ Risk of inconsistency with server
- ❌ Used `wg-quick` (disruptive updates)

### New Design (Public Key-Based)
- ✅ Automatic identification via public key
- ✅ Multiple peers per client-agent instance
- ✅ Reuses controller's configuration generation
- ✅ Guaranteed consistency with server view
- ✅ Uses `syncconf` (non-disruptive updates)

## Future Enhancements

1. **Event-Driven Updates:**
   - Replace polling with Kubernetes watch API
   - Faster reaction to changes
   - Reduced API server load

2. **Mesh Mode Support:**
   - Configure peer-to-peer connections
   - Direct client-to-client tunnels
   - Reduced latency

3. **Configuration Validation:**
   - Pre-apply validation
   - Rollback on failure
   - Health checks before switching

4. **Multiple Interfaces:**
   - Support for wg0, wg1, wg2, etc.
   - Separate interfaces per WireGuard instance
   - Better isolation

## Testing Strategy

### Unit Tests
- Interface section extraction
- Peer section extraction
- Configuration grouping logic
- Hash-based change detection

### Integration Tests
- Single peer configuration
- Multiple peers (same interface)
- Multiple peers (different interfaces)
- Configuration updates
- Key rotation scenario

### End-to-End Tests
- Full setup workflow
- Connectivity verification
- Metrics collection
- Health check validation
- Disruption-free updates

## Conclusion

The client-agent design leverages existing controller capabilities while adding intelligent configuration merging and safe update mechanisms. By using public key-based identification and `syncconf` for updates, it provides a robust solution for external clients to join WireGuard meshes managed by the operator.
