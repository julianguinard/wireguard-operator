# Client Agent

The `client-agent` is a component of the WireGuard Operator that runs on **client machines** (outside the Kubernetes cluster) to enable them to join the WireGuard mesh network. It watches for `WireguardPeer` resources and automatically updates the local WireGuard configuration when peers are added, updated, or deleted.

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│  Kubernetes Cluster                                          │
│                                                              │
│  ┌──────────────┐      ┌──────────────┐                     │
│  │  Controller  │──────│   Agent      │                     │
│  │  (Manager)   │      │  (Server)    │                     │
│  └──────────────┘      └──────────────┘                     │
│         │                    │                               │
│         │ watches            │ serves as                     │
│         ▼                    ▼                               │
│  ┌──────────────┐      ┌──────────────┐                     │
│  │ Wireguard    │      │ Wireguard    │                     │
│  │ WireguardPeer│      │ Config Secret│                     │
│  └──────────────┘      └──────────────┘                     │
└─────────────────────────────────────────────────────────────┘
                              │
                              │ Kubernetes API
                              ▼
┌─────────────────────────────────────────────────────────────┐
│  Client Machine (External)                                   │
│                                                              │
│  ┌──────────────┐      ┌──────────────┐                     │
│  │ Client-Agent │──────│ WireGuard    │                     │
│  │              │      │ Interface    │                     │
│  └──────────────┘      └──────────────┘                     │
│         │                    │                               │
│         │ watches            │ configures                    │
│         ▼                    ▼                               │
│  ┌──────────────┐      ┌──────────────┐                     │
│  │ WireguardPeer│      │ wg0.conf     │                     │
│  │ (client ref) │      │              │                     │
│  └──────────────┘      └──────────────┘                     │
└─────────────────────────────────────────────────────────────┘
```

## When to Use Client-Agent

The client-agent is designed for scenarios where:

1. **External machines** need to join the WireGuard mesh
2. WireGuard is configured at the **host level** (not in containers)
3. You want **automatic configuration updates** when peers change
4. The client machine has **network access to the Kubernetes API**

## Prerequisites

- WireGuard installed on the client machine (`wg-quick` or `wireguard-tools`)
- Network access to the Kubernetes API server
- A `WireguardPeer` resource created for the client machine
- kubeconfig file with permissions to read `WireguardPeer` and `Secret` resources

## Installation

### Option 1: Systemd Service (Recommended for bare metal)

1. **Install the binary:**

```bash
sudo cp client-agent /usr/local/bin/
sudo chmod +x /usr/local/bin/client-agent
```

2. **Create the systemd service:**

```bash
sudo cp examples/client-agent-systemd.service /etc/systemd/system/
sudo systemctl daemon-reload
```

3. **Configure the service:**

Edit `/etc/systemd/system/client-agent-systemd.service` and update:
- `--peer-name`: Name of the WireguardPeer resource
- `--wireguard-ref`: Name of the Wireguard resource
- `--namespace`: Kubernetes namespace
- `--kubeconfig`: Path to kubeconfig file

4. **Start the service:**

```bash
sudo systemctl enable client-agent
sudo systemctl start client-agent
sudo systemctl status client-agent
```

### Option 2: Docker/Podman Container

```bash
docker run -d \
  --name client-agent \
  --network host \
  --cap-add NET_ADMIN \
  --cap-add SYS_MODULE \
  -v /etc/wireguard:/etc/wireguard \
  -v /lib/modules:/lib/modules:ro \
  -v ~/.kube/config:/kubeconfig \
  wireguard-operator/client-agent:latest \
  --peer-name=client-machine-1 \
  --wireguard-ref=vpn \
  --namespace=default \
  --kubeconfig=/kubeconfig \
  --wg-config-path=/etc/wireguard/wg0.conf
```

### Option 3: Kubernetes DaemonSet (for worker nodes)

For client machines that are also Kubernetes nodes, you can deploy as a DaemonSet:

```bash
kubectl apply -f examples/client-agent.yaml
```

## Configuration

### Command Line Flags

| Flag | Required | Default | Description |
|------|----------|---------|-------------|
| `--peer-name` | Yes | - | Name of the WireguardPeer resource |
| `--wireguard-ref` | Yes | - | Name of the Wireguard resource |
| `--namespace` | No | `default` | Kubernetes namespace |
| `--kubeconfig` | No | In-cluster | Path to kubeconfig file |
| `--wg-config-path` | No | `/etc/wireguard/wg0.conf` | WireGuard config file path |
| `--wg-iface` | No | `wg0` | WireGuard interface name |
| `--v` | No | `1` | Verbosity level |
| `--metrics-bind-address` | No | `:9587` | Metrics endpoint address |
| `--health-port` | No | `8082` | Health check port |

### Kubernetes RBAC

The client-agent needs permissions to read:
- `WireguardPeer` resources
- `Wireguard` resources  
- `Secret` resources (for keys)

Create a ServiceAccount and Role:

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: client-agent
  namespace: default
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
  namespace: default
roleRef:
  kind: Role
  name: client-agent
  apiGroup: rbac.authorization.k8s.io
```

## How It Works

1. **Initial Setup:**
   - Create a `WireguardPeer` resource for the client machine
   - The controller generates keys and stores them in a Secret
   - The server agent picks up the configuration

2. **Client-Agent Operation:**
   - Polls Kubernetes API every 30 seconds (configurable)
   - Fetches the `WireguardPeer` and `Wireguard` resources
   - Retrieves private key from the peer secret
   - Retrieves server configuration from the Wireguard secret
   - Generates WireGuard configuration file
   - Applies configuration using `wg-quick` or `wgctl`

3. **Updates:**
   - When `WireguardPeer` is updated (e.g., new AllowedIPs)
   - When `Wireguard` server configuration changes
   - When additional peers are added to the mesh
   - Client-agent detects changes and updates configuration automatically

## Metrics

The client-agent exposes Prometheus metrics on port `9587` (configurable):

- `wireguard_client_sent_bytes_total` - Bytes sent to the server
- `wireguard_client_received_bytes_total` - Bytes received from the server
- `wireguard_client_latest_handshake_seconds` - Last handshake timestamp
- `wireguard_client_connected` - Connection state (1 = connected, 0 = disconnected)

## Health Checks

Health endpoint available on port `8082` (configurable):

```bash
curl http://localhost:8082/health
```

Returns:
- `200 OK` - Client is healthy and configured
- `503 Service Unavailable` - Configuration missing or interface not up

## Troubleshooting

### Check logs

```bash
# Systemd
journalctl -u client-agent -f

# Docker
docker logs -f client-agent
```

### Verify configuration

```bash
# Check generated config
cat /etc/wireguard/wg0.conf

# Check interface status
wg show wg0

# Test connectivity
ping <server-internal-ip>
```

### Common issues

1. **Permission denied:**
   - Ensure the service has `NET_ADMIN` and `SYS_MODULE` capabilities
   - Run as root or with appropriate sudo privileges

2. **Cannot connect to Kubernetes API:**
   - Verify kubeconfig is valid and has correct permissions
   - Check network connectivity to API server

3. **WireGuard interface not created:**
   - Ensure WireGuard kernel module is loaded: `lsmod | grep wireguard`
   - Install wireguard-tools: `apt install wireguard` or `yum install wireguard-tools`

## Example Workflow

```bash
# 1. Create Wireguard instance (on cluster)
kubectl apply -f examples/server.yaml

# 2. Create peer for client machine (on cluster)
cat <<EOF | kubectl apply -f -
apiVersion: vpn.wireguard-operator.io/v1alpha1
kind: WireguardPeer
metadata:
  name: client-machine-1
spec:
  wireguardRef: "vpn"
EOF

# 3. Install client-agent (on client machine)
sudo systemctl start client-agent

# 4. Verify connection
wg show

# 5. Check connectivity
ping 10.8.0.1  # Server IP
```

## Security Considerations

- Private keys are stored in Kubernetes Secrets and read by client-agent
- Configuration file is written with `0600` permissions
- Client-agent requires access to Kubernetes API - secure your kubeconfig
- Consider using mTLS or network policies to restrict API access
- Run client-agent with minimal required privileges

## Comparison: Agent vs Client-Agent

| Feature | Agent (Server) | Client-Agent |
|---------|----------------|--------------|
| Deployment | Kubernetes Pod | Systemd/Container |
| Location | Inside cluster | Outside cluster |
| WireGuard Role | Server | Client |
| Configuration | JSON state file | wg-quick config |
| Watch | File system | Kubernetes API |
| Network | Host network (optional) | Host network (required) |
