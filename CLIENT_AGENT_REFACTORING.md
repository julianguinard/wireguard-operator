# Client-Agent Refactoring Summary

## Changes Based on Review Feedback

### 1. Removed Unused Variable
**Issue:** `serverPrivateKey` variable was declared but never used (line 110)

**Fix:** Removed the variable from the `ClientState` struct and all related code.

**Impact:** Cleaner code, no functional change.

---

### 2. Support for Multiple WireguardPeer Resources

**Previous Approach:**
- Client-agent watched a single `WireguardPeer` resource
- Specified via `--peer-name` and `--wireguard-ref` flags
- Only one connection per client-agent instance

**New Approach:**
- Client-agent discovers **all** peer configurations for a given public key
- Automatically finds configs across multiple WireGuard instances
- Merges configurations into a single WireGuard interface
- Single client-agent can manage multiple simultaneous connections

**Implementation:**
```go
// Find all peer-config secrets matching the public key
func (r *Reconciler) findPeerConfigs(ctx context.Context) ([]PeerConfigEntry, error) {
    // List all secrets in namespace
    // Filter for *-peer-configs secrets
    // Extract keys matching <publicKey>-peer-config
    // Return all matching configs
}
```

**Benefits:**
- ✅ Simpler deployment (one instance per client)
- ✅ Automatic discovery of new connections
- ✅ Support for complex multi-environment setups
- ✅ Better alignment with controller's secret generation

---

### 3. Leverage Existing `-peer-configs` Secrets

**Previous Approach:**
- Reconstruct WireGuard config from individual CR fields
- Fetch `WireguardPeer`, `Wireguard`, and `Secret` resources
- Manually build configuration

**New Approach:**
- Read pre-generated configs from `<wireguard-name>-peer-configs` secrets
- Each secret contains complete `.conf` file content
- Key naming: `<publicKey>-peer-config`
- Parse and merge existing configs

**Secret Structure:**
```yaml
apiVersion: v1
kind: Secret
metadata:
  name: vpn-prod-peer-configs
data:
  xYz123ABC...-peer-config: |
    [Interface]
    PrivateKey = ...
    Address = 10.8.0.3
    ...
    
    [Peer]
    PublicKey = ...
    Endpoint = prod.example.com:51820
    ...
  
  other-peer-config: |
    ...
```

**Benefits:**
- ✅ No duplication of config generation logic
- ✅ Uses controller's authoritative config
- ✅ Simpler client-agent code
- ✅ Consistent with how in-cluster peers work

---

### 4. Configuration Merging by Interface

**New Feature:** Smart merging of multiple configs

**Algorithm:**
1. Parse each config into Interface and Peer sections
2. Group configs by Interface section hash
3. Merge Peer sections within each group
4. Generate final merged config

**Example:**
```ini
# Config 1 (Production)
[Interface]
PrivateKey = abc123...
Address = 10.8.0.3

[Peer]
PublicKey = prod-server-key
Endpoint = prod.example.com:51820
AllowedIPs = 10.0.0.0/8

# Config 2 (Development)
[Interface]
PrivateKey = abc123...  # Same key
Address = 10.9.0.3

[Peer]
PublicKey = dev-server-key
Endpoint = dev.example.com:51820
AllowedIPs = 172.16.0.0/12

# Merged Result
[Interface]
PrivateKey = abc123...
Address = 10.8.0.3, 10.9.0.3

[Peer]
PublicKey = prod-server-key
Endpoint = prod.example.com:51820
AllowedIPs = 10.0.0.0/8

[Peer]
PublicKey = dev-server-key
Endpoint = dev.example.com:51820
AllowedIPs = 172.16.0.0/12
```

**Benefits:**
- ✅ Single interface for all connections
- ✅ Clean routing through one wg device
- ✅ Easier management and monitoring

---

### 5. Safe Updates with syncconf

**Previous Approach:**
- Use `wg-quick up` to apply config
- Restarts entire interface
- Drops all existing connections
- Disruptive for multi-peer setups

**New Approach:**
- Primary: Use `syncconf` for safe synchronization
- Fallback: `wg-quick` if syncconf unavailable
- Only applies necessary changes
- Preserves existing connections

**Implementation:**
```go
func (r *Reconciler) applyConfigWithSyncconf(configPath string) error {
    // Try syncconf first
    syncconfPath, err := exec.LookPath("syncconf")
    if err == nil {
        cmd := exec.Command(syncconfPath, "-c", configPath, r.InterfaceName)
        return cmd.Run()
    }
    
    // Fallback to wg-quick
    r.Logger.Info("syncconf not found, falling back to wg-quick")
    // ... wg-quick logic ...
}
```

**What is syncconf?**
- Tool for safe WireGuard config synchronization
- Compares current vs. desired state
- Only applies minimal changes
- Preserves active connections
- Install: `apt install syncconf` or `go install github.com/JoshuaDoes/syncconf`

**Benefits:**
- ✅ Zero-downtime configuration updates
- ✅ Critical for multi-peer setups
- ✅ New peers added without disruption
- ✅ Removed peers cleanly disconnected
- ✅ Modified peers updated in-place

---

## Updated Command-Line Interface

### New Flags

```bash
# Recommended: Public key-based discovery
client-agent --public-key=xYz123ABC... \
  --namespace=default \
  --kubeconfig=/etc/wireguard/kubeconfig

# Alternative: Specific peer resource
client-agent --peer-name=client-1 \
  --wireguard-ref=vpn \
  --namespace=default
```

### Flag Requirements

- **Either:** `--public-key` (recommended)
- **Or:** Both `--peer-name` and `--wireguard-ref`

---

## Updated Deployment Examples

### Systemd Service (Recommended)

```ini
[Unit]
Description=WireGuard Operator Client Agent
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/client-agent \
  --public-key=${PUBLIC_KEY} \
  --namespace=default \
  --kubeconfig=/etc/wireguard/kubeconfig \
  --wg-config-path=/etc/wireguard/wg0.conf \
  --wg-iface=wg0 \
  --v=2

Restart=always
```

### Multi-WireGuard Scenario

Single client connecting to 3 environments:

```bash
# One client-agent instance discovers all:
# - vpn-prod-peer-configs/xYz123...-peer-config
# - vpn-dev-peer-configs/xYz123...-peer-config
# - vpn-staging-peer-configs/xYz123...-peer-config

# Merges into single config with 3 [Peer] sections
# Applies to wg0 interface
# Client can reach all 3 networks simultaneously
```

---

## Testing Recommendations

### Unit Tests
- [ ] Config parsing logic
- [ ] Interface grouping algorithm
- [ ] Peer section merging
- [ ] Hash comparison for change detection

### Integration Tests
- [ ] Single WireGuard connection
- [ ] Multiple WireGuard connections
- [ ] Config update propagation
- [ ] syncconf vs. wg-quick fallback
- [ ] Secret discovery and filtering

### Manual Testing
```bash
# 1. Create multiple WireGuard instances
kubectl apply -f examples/multi-wireguard-client.yaml

# 2. Generate client keypair
wg genkey | tee privatekey | wg pubkey > publickey

# 3. Start client-agent
client-agent --public-key=$(cat publickey) --namespace=default

# 4. Verify merged config
cat /etc/wireguard/wg0.conf

# 5. Check connections
wg show wg0

# 6. Test connectivity to all networks
ping 10.0.0.1  # Production
ping 172.16.0.1  # Development
ping 192.168.0.1  # Staging
```

---

## Migration Guide

### From Old to New Approach

**Old Deployment:**
```bash
client-agent \
  --peer-name=client-1 \
  --wireguard-ref=vpn \
  --namespace=default
```

**New Deployment (Recommended):**
```bash
# Get your public key
PUBLIC_KEY=$(cat /etc/wireguard/publickey)

client-agent \
  --public-key=${PUBLIC_KEY} \
  --namespace=default
```

**Benefits of Migration:**
- Automatic discovery of new connections
- Support for multiple WireGuard instances
- No need to update config when adding new peers
- Aligns with controller's secret generation

---

## Performance Considerations

### Secret Scanning
- Lists all secrets in namespace once per reconciliation (30s)
- Filters client-side for `*-peer-configs` pattern
- Efficient for namespaces with <1000 secrets
- For larger scales, consider namespace isolation

### Config Parsing
- Parses each matching config once
- Groups by interface hash (O(n) complexity)
- Merges peer sections linearly
- Total complexity: O(n) where n = number of peer configs

### syncconf Overhead
- Minimal overhead for small changes
- Compares configs before applying
- Only modifies what changed
- Faster than full interface restart

---

## Security Considerations

### Secret Access
- Client-agent needs read access to secrets
- RBAC should limit to specific namespace
- Token-based authentication
- Secrets contain private keys (handle carefully)

### Config File Permissions
- Written with 0600 permissions
- Only readable by root/wireguard user
- Temporary files cleaned up immediately

### Network Access
- Requires connectivity to Kubernetes API
- Consider using dedicated ServiceAccount
- Token rotation for long-running deployments

---

## Documentation Updates

### New Documents
- `readme/multi-wireguard.md` - Multi-WireGuard support guide
- `examples/multi-wireguard-client.yaml` - Complete example

### Updated Documents
- `readme/client-agent.md` - Added multi-WireGuard section
- `readme/client-agent-quickstart.md` - Updated deployment examples
- `CLIENT_AGENT_IMPLEMENTATION.md` - Refactored implementation details

---

## Summary of Improvements

| Aspect | Before | After |
|--------|--------|-------|
| **Discovery** | Manual (peer-name + wireguard-ref) | Automatic (public-key based) |
| **Connections** | Single WireGuard server | Multiple servers simultaneously |
| **Config Source** | Reconstructed from CRs | Pre-generated in secrets |
| **Updates** | wg-quick (disruptive) | syncconf (safe, minimal changes) |
| **Complexity** | Higher (manual config building) | Lower (parse and merge) |
| **Scalability** | One instance per connection | One instance per client |
| **Alignment** | Different from controller | Uses controller's output |

---

## Next Steps

1. **Code Review:** Review the refactored implementation
2. **Testing:** Test multi-WireGuard scenarios
3. **Documentation:** Update main README with new approach
4. **Release Notes:** Highlight breaking changes (if any)
5. **Migration Guide:** Help existing users migrate

---

## Backward Compatibility

The new approach is **backward compatible**:
- Old `--peer-name` and `--wireguard-ref` flags still work
- New `--public-key` flag is optional but recommended
- Existing deployments continue to function
- Gradual migration path available

**Recommendation:** Encourage new deployments to use `--public-key` approach.
