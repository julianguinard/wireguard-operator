# Kubernetes Resource Naming Examples

This document demonstrates how the WireGuard Operator handles Kubernetes resource naming, especially for long resource names.

## Kubernetes Name Constraints

Kubernetes resource names must:
- Be no more than 63 characters
- Contain only lowercase alphanumeric characters, '-', or '.'
- Start and end with an alphanumeric character

## Automatic Name Sanitization

The operator automatically sanitizes resource names to comply with these constraints. When a name would exceed 63 characters, it's truncated and a hash is appended to ensure uniqueness.

### Naming Format

```
<truncated-base-name>-<8-char-hash><suffix>
```

Where:
- `truncated-base-name`: The original name truncated to fit
- `8-char-hash`: SHA256 hash of the original full name (first 8 characters)
- `suffix`: Resource type suffix (e.g., `-svc`, `-dep`, `-config`)

## Examples

### Example 1: Short Name (No Sanitization Needed)

**WireGuard Resource Name:** `my-vpn`

| Resource Type | Suffix | Generated Name | Length |
|--------------|--------|----------------|--------|
| Service | `-svc` | `my-vpn-svc` | 11 |
| Metrics Service | `-metrics-svc` | `my-vpn-metrics-svc` | 21 |
| Deployment | `-dep` | `my-vpn-dep` | 11 |
| ConfigMap | `-config` | `my-vpn-config` | 13 |
| Secret (server) | `` | `my-vpn` | 6 |
| Secret (peer configs) | `-peer-configs` | `my-vpn-peer-configs` | 21 |

### Example 2: Long UUID Name (Sanitization Applied)

**WireGuard Resource Name:** `poc1-ovh-control-plane-878bff92-96a6-4012-bc14-f44432ae977a` (62 characters)

| Resource Type | Suffix | Generated Name | Length |
|--------------|--------|----------------|--------|
| Service | `-svc` | `poc1-ovh-control-plane-878b-<hash>-svc` | 63 |
| Metrics Service | `-metrics-svc` | `poc1-ovh-control-plane-8-<hash>-metrics-svc` | 63 |
| Deployment | `-dep` | `poc1-ovh-control-plane-878b-<hash>-dep` | 63 |
| ConfigMap | `-config` | `poc1-ovh-control-plane-878b-<hash>-config` | 63 |
| Secret (server) | `` | `poc1-ovh-control-plane-878b-<hash>` | 63 |
| Secret (peer configs) | `-peer-configs` | `poc1-ovh-control-<hash>-peer-configs` | 63 |

*Note: `<hash>` represents an 8-character SHA256 hash (e.g., `a1b2c3d4`)*

### Example 3: Extremely Long Name

**WireGuard Resource Name:** `this-is-a-very-long-name-that-exceeds-the-kubernetes-limit-by-a-lot` (69 characters)

| Resource Type | Suffix | Generated Name | Length |
|--------------|--------|----------------|--------|
| Service | `-svc` | `this-is-a-very-long-name-that-excee-<hash>-svc` | 63 |
| Metrics Service | `-metrics-svc` | `this-is-a-very-long-name-that-<hash>-metrics-svc` | 63 |

## How the Hash is Calculated

The hash is calculated from the **original full name** (base + suffix) using SHA256, then truncated to 8 characters:

```go
import (
    "crypto/sha256"
    "encoding/hex"
)

func generateHash(input string) string {
    hash := sha256.Sum256([]byte(input))
    return hex.EncodeToString(hash[:])[:8]
}

// Example:
fullName := "poc1-ovh-control-plane-878bff92-96a6-4012-bc14-f44432ae977a-metrics-svc"
hash := generateHash(fullName) // e.g., "a1b2c3d4"
```

This ensures:
1. **Consistency**: Same input always produces the same output
2. **Uniqueness**: Different inputs produce different hashes
3. **Predictability**: You can calculate the name in advance

## Retrieving Resources

When working with long-named resources, you can:

### 1. Use Label Selectors

All resources are labeled with `instance=<original-name>`:

```bash
# Find all resources for a WireGuard instance
kubectl get all -l instance=poc1-ovh-control-plane-878bff92-96a6-4012-bc14-f44432ae977a -n wireguard
```

### 2. List and Filter

```bash
# List services and filter by label
kubectl get svc -n wireguard -o jsonpath='{.items[*].metadata.name}' | \
  tr ' ' '\n' | grep poc1-ovh
```

### 3. Calculate the Name

If you need the exact name, you can calculate it:

```bash
# Using OpenSSL to calculate SHA256 hash
fullName="poc1-ovh-control-plane-878bff92-96a6-4012-bc14-f44432ae977a-metrics-svc"
hash=$(echo -n "$fullName" | openssl dgst -sha256 | awk '{print $2}' | cut -c1-8)
echo "Generated name: poc1-ovh-control-plane-8-${hash}-metrics-svc"
```

## Client-Agent Behavior

The client-agent discovers peer configurations by:
1. Scanning all secrets matching `*-peer-configs` pattern
2. Looking for keys matching `<publicIP>-peer-config`
3. The secret name's wireguard portion is used only for logging

This means the client-agent works correctly regardless of name sanitization.

## Best Practices

1. **Use Short, Descriptive Names**: Keep WireGuard resource names under 40 characters when possible
   - ✅ Good: `prod-vpn`, `staging-wireguard`
   - ❌ Avoid: `production-kubernetes-cluster-control-plane-node-uuid-12345`

2. **Use Labels for Identification**: Add custom labels to identify resources
   ```yaml
   apiVersion: vpn.wireguard-operator.io/v1alpha1
   kind: Wireguard
   metadata:
     name: my-vpn
     labels:
       environment: production
       team: platform
   ```

3. **Reference by Label in Scripts**: Instead of hardcoding names
   ```bash
   # Instead of:
   kubectl get secret my-very-long-name-svc
   
   # Use:
   kubectl get secret -l app=wireguard,instance=my-very-long-name
   ```

## Troubleshooting

### Resource Not Found

If you can't find a resource by name:

```bash
# Check if the name was sanitized
kubectl get svc -n wireguard -o custom-columns=NAME:.metadata.name | grep <partial-name>

# Or list all and check labels
kubectl get svc -n wireguard --show-labels | grep <instance-label>
```

### Name Collision

The hash ensures uniqueness, but if you somehow get a collision (extremely unlikely):
- The operator will detect the conflict during reconciliation
- Manual intervention may be required to resolve

## Migration from Old Versions

If upgrading from a version without name sanitization:

1. **Short names**: No action needed
2. **Long names**: Resources may fail to create
   ```bash
   # Delete old resources
   kubectl delete wireguard <name> -n <namespace>
   
   # Recreate (will use sanitized names)
   kubectl apply -f wireguard.yaml
   ```

See `NAMING_FIX_SUMMARY.md` for detailed migration instructions.
