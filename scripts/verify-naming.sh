#!/bin/bash

# This script demonstrates the name sanitization logic
# It requires OpenSSL for SHA256 calculation

echo "=== Kubernetes Resource Name Sanitization Demo ==="
echo ""

# Function to sanitize a name (mimics the Go logic)
sanitize_name() {
    local base_name="$1"
    local suffix="$2"
    local full_name="${base_name}${suffix}"
    local max_len=63
    local hash_len=8
    
    if [ ${#full_name} -le $max_len ]; then
        echo "$full_name"
        return
    fi
    
    # Calculate hash using sha256sum or openssl
    local hash
    if command -v sha256sum &> /dev/null; then
        hash=$(echo -n "$full_name" | sha256sum | cut -c1-${hash_len})
    elif command -v openssl &> /dev/null; then
        hash=$(echo -n "$full_name" | openssl dgst -sha256 | sed 's/.*= //' | cut -c1-${hash_len})
    else
        hash="00000000"
    fi
    
    # Calculate max base length
    # Format: <base>-<hash><suffix>
    local suffix_len=${#suffix}
    local max_base_len=$((max_len - 1 - hash_len - suffix_len))

    if [ $max_base_len -lt 1 ]; then
        max_base_len=1
    fi

    # Truncate base
    local truncated_base="${base_name:0:$max_base_len}"
    
    echo "${truncated_base}-${hash}${suffix}"
}

# Test cases
echo "Test Case 1: Short name (no sanitization needed)"
echo "  Input: my-vpn + -svc"
result=$(sanitize_name "my-vpn" "-svc")
echo "  Output: $result"
echo "  Length: ${#result}"
echo ""

echo "Test Case 2: Long UUID name (sanitization applied)"
base="poc1-ovh-control-plane-878bff92-96a6-4012-bc14-f44432ae977a"
echo "  Input: $base + -metrics-svc"
result=$(sanitize_name "$base" "-metrics-svc")
echo "  Output: $result"
echo "  Length: ${#result}"
echo ""

echo "Test Case 3: Very long name"
base=$(printf 'a%.0s' {1..70})
echo "  Input: ${base:0:20}... (${#base} chars) + -dep"
result=$(sanitize_name "$base" "-dep")
echo "  Output: $result"
echo "  Length: ${#result}"
echo ""

echo "Test Case 4: Peer configs secret"
base="poc1-ovh-control-plane-878bff92-96a6-4012-bc14-f44432ae977a"
echo "  Input: $base + -peer-configs"
result=$(sanitize_name "$base" "-peer-configs")
echo "  Output: $result"
echo "  Length: ${#result}"
echo ""

echo "=== Verification ==="
echo "All generated names should be <= 63 characters"
echo "Names with hash should follow pattern: <truncated-base>-<8-char-hash><suffix>"
echo ""
