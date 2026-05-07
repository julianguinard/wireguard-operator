# Client-Agent Quick Start Guide

This guide walks you through setting up a client machine to join your WireGuard mesh network using the client-agent.

## Prerequisites

1. A running Kubernetes cluster with wireguard-operator installed
2. A client machine (VM, bare metal, or laptop) that needs to join the mesh
3. WireGuard installed on the client machine
4. Network connectivity from the client to the Kubernetes API server

## Step 1: Install WireGuard on Client Machine

### Ubuntu/Debian
```bash
sudo apt update
sudo apt install wireguard wireguard-tools
```

### RHEL/CentOS/Fedora
```bash
sudo dnf install wireguard-tools
```

### macOS
```bash
brew install wireguard-tools
```

## Step 2: Create WireguardPeer Resource

On your Kubernetes cluster, create a peer for the client machine:

```bash
# Create the Wireguard instance if not already created
kubectl apply -f - <<EOF
apiVersion: vpn.wireguard-operator.io/v1alpha1
kind: Wireguard
metadata:
  name: vpn
spec:
  mtu: "1380"
EOF

# Create the peer for your client machine
kubectl apply -f - <<EOF
apiVersion: vpn.wireguard-operator.io/v1alpha1
kind: WireguardPeer
metadata:
  name: client-machine-1
spec:
  wireguardRef: "vpn"
EOF
```

## Step 3: Prepare Kubernetes Access

The client-agent needs to read from the Kubernetes API. Create a ServiceAccount with minimal permissions:

```bash
# Create namespace if needed
kubectl create namespace wireguard-clients

# Create ServiceAccount
kubectl apply -f - <<EOF
apiVersion: v1
kind: ServiceAccount
metadata:
  name: client-agent
  namespace: wireguard-clients
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: client-agent
  namespace: default
rules:
- apiGroups: ["vpn.wireguard-operator.io"]
  resources: ["wireguardpeers", "wireguards"]
  verbs: ["get", "list", "watch"]
- apiGroups: [""]
  resources: ["secrets"]
  verbs: ["get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: client-agent
  namespace: default
subjects:
- kind: ServiceAccount
  name: client-agent
  namespace: wireguard-clients
roleRef:
  kind: Role
  name: client-agent
  apiGroup: rbac.authorization.k8s.io
EOF

# Get the token and CA certificate
TOKEN=$(kubectl -n wireguard-clients create token client-agent)
CA_CERT=$(kubectl config view --raw --minify -o jsonpath='{.clusters[0].cluster.certificate-authority-data}')
API_SERVER=$(kubectl config view --raw --minify -o jsonpath='{.clusters[0].cluster.server}')

# Create kubeconfig on client machine
cat > /etc/wireguard/kubeconfig <<EOF
apiVersion: v1
kind: Config
clusters:
- name: kubernetes
  cluster:
    certificate-authority-data: ${CA_CERT}
    server: ${API_SERVER}
contexts:
- name: client-agent
  context:
    cluster: kubernetes
    user: client-agent
current-context: client-agent
users:
- name: client-agent
  user:
    token: ${TOKEN}
EOF

chmod 600 /etc/wireguard/kubeconfig
```

## Step 4: Install Client-Agent

### Option A: Using Systemd (Recommended)

```bash
# Download the binary (replace with actual release URL)
curl -L https://github.com/nccloud/wireguard-operator/releases/latest/download/client-agent \
  -o /usr/local/bin/client-agent
chmod +x /usr/local/bin/client-agent

# Create systemd service
cat > /etc/systemd/system/client-agent.service <<EOF
[Unit]
Description=WireGuard Operator Client Agent
After=network.target wireguard@wg0.service
Wants=wireguard@wg0.service

[Service]
Type=simple
ExecStart=/usr/local/bin/client-agent \\
  --peer-name=client-machine-1 \\
  --wireguard-ref=vpn \\
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

# Start the service
sudo systemctl daemon-reload
sudo systemctl enable client-agent
sudo systemctl start client-agent
sudo systemctl status client-agent
```

### Option B: Using Docker

```bash
docker run -d \
  --name client-agent \
  --network host \
  --cap-add NET_ADMIN \
  --cap-add SYS_MODULE \
  -v /etc/wireguard:/etc/wireguard \
  -v /lib/modules:/lib/modules:ro \
  -v /etc/wireguard/kubeconfig:/kubeconfig:ro \
  ghcr.io/nccloud/wireguard-operator/client-agent:latest \
  --peer-name=client-machine-1 \
  --wireguard-ref=vpn \
  --namespace=default \
  --kubeconfig=/kubeconfig \
  --wg-config-path=/etc/wireguard/wg0.conf
```

## Step 5: Verify Connection

```bash
# Check client-agent logs
journalctl -u client-agent -f

# Check WireGuard interface
wg show wg0

# Expected output should show:
# - interface: wg0
# - public key: (your peer public key)
# - private key: (hidden)
# - listening port: 51820
# 
# peer: (server public key)
#   endpoint: <server-ip>:51820
#   allowed ips: 0.0.0.0/0
#   latest handshake: <timestamp>
#   transfer: X B received, Y B sent

# Test connectivity to server
ping 10.8.0.1  # or your server's internal IP

# Test connectivity through the tunnel
kubectl get pods -n default  # if you have access to cluster resources
```

## Step 6: Monitor and Maintain

### Check Metrics

```bash
curl http://localhost:9587/metrics | grep wireguard_client
```

### Check Health

```bash
curl http://localhost:8082/health
```

### Update Configuration

When you need to update the peer configuration (e.g., add AllowedIPs):

```bash
# Update the WireguardPeer resource
kubectl patch wireguardpeer client-machine-1 --type=merge -p '{
  "spec": {
    "allowedIPs": "10.0.0.0/8"
  }
}'

# Client-agent will automatically detect and apply changes within 30 seconds
```

### Rotate Keys

To rotate keys, delete and recreate the peer:

```bash
# Delete the peer
kubectl delete wireguardpeer client-machine-1

# Recreate
kubectl apply -f - <<EOF
apiVersion: vpn.wireguard-operator.io/v1alpha1
kind: WireguardPeer
metadata:
  name: client-machine-1
spec:
  wireguardRef: "vpn"
EOF

# Client-agent will automatically pick up the new configuration
```

## Troubleshooting

### Client-agent not starting

```bash
# Check systemd logs
journalctl -u client-agent -n 50

# Common issues:
# - Missing kubeconfig: verify /etc/wireguard/kubeconfig exists
# - Permission denied: ensure NET_ADMIN capability
# - WireGuard module not loaded: sudo modprobe wireguard
```

### Cannot connect to Kubernetes API

```bash
# Test API connectivity
curl --cacert <ca-cert> -H "Authorization: Bearer <token>" <api-server>/healthz

# Verify kubeconfig
kubectl --kubeconfig=/etc/wireguard/kubeconfig get wireguardpeers
```

### WireGuard interface not created

```bash
# Check if module is loaded
lsmod | grep wireguard

# Load module if needed
sudo modprobe wireguard

# Check interface
ip link show wg0

# Manual bring up for testing
sudo wg-quick up /etc/wireguard/wg0.conf
```

### No traffic flowing

```bash
# Check routing
ip route show

# Check firewall rules
sudo iptables -L -n -v

# Check server-side configuration
kubectl get secret vpn -o jsonpath='{.data}' | jq 'keys'

# Test from server side
kubectl exec -it <agent-pod> -- ping <client-ip>
```

## Next Steps

- Configure additional peers for other client machines
- Set up monitoring with Prometheus/Grafana
- Configure firewall rules for the WireGuard interface
- Set up automatic key rotation
- Review security best practices in the main documentation

For more detailed information, see [client-agent.md](client-agent.md).
