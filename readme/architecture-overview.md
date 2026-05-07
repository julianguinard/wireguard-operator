# WireGuard Operator Architecture Overview

## Components

The WireGuard Operator consists of three main components:

### 1. Controller (Manager)

**Location:** Kubernetes cluster  
**Purpose:** Orchestrates the entire WireGuard mesh network

**Responsibilities:**
- Watches `Wireguard` and `WireguardPeer` Custom Resources
- Generates cryptographic keys for servers and peers
- Allocates IP addresses from configured CIDR ranges
- Creates Kubernetes Secrets containing configuration
- Manages the lifecycle of Agent deployments
- Handles reconciliation of desired state vs actual state

**Deployment:**
```yaml
kubectl apply -f https://github.com/nccloud/wireguard-operator/releases/latest/download/release.yaml
```

### 2. Agent (Server)

**Location:** Kubernetes cluster (one per Wireguard instance)  
**Purpose:** Runs the WireGuard server that peers connect to

**Responsibilities:**
- Watches a JSON state file mounted from a Secret
- Configures the WireGuard interface (`wg0`)
- Manages peer connections (add/remove/update)
- Applies network policies and iptables rules
- Exposes metrics and health endpoints
- Can run in userspace mode if kernel module unavailable

**Deployment:**
Automatically deployed by the Controller when a `Wireguard` CR is created.

**Key Features:**
- Automatic fallback to `wireguard-go` if kernel module missing
- Supports traffic obfuscation via wstunnel
- Prometheus metrics export
- Health check endpoint

### 3. Client-Agent (NEW)

**Location:** External client machines (VMs, bare metal, laptops)  
**Purpose:** Enables external machines to join the WireGuard mesh as clients

**Responsibilities:**
- Watches `WireguardPeer` resources via Kubernetes API
- Fetches server configuration from Secrets
- Generates and applies WireGuard client configuration
- Automatically updates configuration when peers change
- Exposes metrics and health endpoints

**Deployment:**
- Systemd service (recommended for bare metal)
- Docker/Podman container
- Kubernetes DaemonSet (for worker nodes)

**Key Features:**
- Automatic configuration updates
- Supports both IPv4 and IPv6
- Minimal dependencies (just WireGuard tools)
- Secure key handling

## Architecture Diagram

```
┌─────────────────────────────────────────────────────────────────┐
│                    Kubernetes Cluster                            │
│                                                                  │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │                   Controller (Manager)                    │  │
│  │  - Watches Wireguard CRs                                  │  │
│  │  - Watches WireguardPeer CRs                              │  │
│  │  - Generates keys & allocates IPs                         │  │
│  │  - Creates configuration Secrets                          │  │
│  └──────────────────────────────────────────────────────────┘  │
│                              │                                   │
│                              │ creates/manages                   │
│                              ▼                                   │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │              Agent (Server) - Pod per Wireguard           │  │
│  │  - Reads state from Secret (JSON)                         │  │
│  │  - Configures wg0 interface                               │  │
│  │  - Manages peer connections                               │  │
│  │  - Exposes :51820 (or tunnel port)                        │  │
│  │  - Metrics: :9586                                         │  │
│  │  - Health: :8080                                          │  │
│  └──────────────────────────────────────────────────────────┘  │
│                              ▲                                   │
│                              │ WireGuard UDP                     │
│                              │ (or WebSocket/TLS)                │
└──────────────────────────────│───────────────────────────────────┘
                               │
                               │ Internet / Network
                               │
┌──────────────────────────────│───────────────────────────────────┐
│                    Client Machines                               │
│                                                                  │
│  ┌──────────────────────────┐   ┌──────────────────────────┐   │
│  │   Client-Agent (VM 1)    │   │   Client-Agent (VM 2)    │   │
│  │  - Watches K8s API       │   │  - Watches K8s API       │   │
│  │  - Generates wg0.conf    │   │  - Generates wg0.conf    │   │
│  │  - Applies config        │   │  - Applies config        │   │
│  │  - Metrics: :9587        │   │  - Metrics: :9587        │   │
│  │  - Health: :8082         │   │  - Health: :8082         │   │
│  └──────────────────────────┘   └──────────────────────────┘   │
│            │                                  │                 │
│            │ WireGuard                        │ WireGuard       │
│            ▼                                  ▼                 │
│  ┌──────────────────────────┐   ┌──────────────────────────┐   │
│  │   WireGuard Interface    │   │   WireGuard Interface    │   │
│  │   (wg0 - Client Mode)    │   │   (wg0 - Client Mode)    │   │
│  └──────────────────────────┘   └──────────────────────────┘   │
└─────────────────────────────────────────────────────────────────┘
```

## Data Flow

### Server Setup Flow

1. **User creates Wireguard CR:**
   ```yaml
   apiVersion: vpn.wireguard-operator.io/v1alpha1
   kind: Wireguard
   metadata:
     name: vpn
   spec:
     mtu: "1380"
   ```

2. **Controller:**
   - Generates server keypair
   - Creates Agent Deployment
   - Creates Secret with server keys

3. **Agent:**
   - Reads state from mounted Secret
   - Configures WireGuard interface
   - Starts listening on port 51820

### Peer Setup Flow (In-Cluster)

1. **User creates WireguardPeer CR:**
   ```yaml
   apiVersion: vpn.wireguard-operator.io/v1alpha1
   kind: WireguardPeer
   metadata:
     name: peer1
   spec:
     wireguardRef: "vpn"
   ```

2. **Controller:**
   - Generates peer keypair
   - Allocates IP address
   - Updates peer Secret
   - Updates server state Secret

3. **Agent:**
   - Detects state file change
   - Adds peer to WireGuard interface
   - Peer can now connect

### Client Setup Flow (External)

1. **User creates WireguardPeer CR** (same as above)

2. **User deploys Client-Agent** on external machine:
   - Configures with peer name and Wireguard ref
   - Provides kubeconfig for API access

3. **Client-Agent:**
   - Polls Kubernetes API
   - Fetches peer configuration
   - Generates `wg0.conf`
   - Applies configuration using `wg-quick`

4. **Client-Agent (ongoing):**
   - Continuously watches for changes
   - Updates configuration automatically
   - Reports metrics and health

## Configuration Storage

### Server Configuration

Stored in Kubernetes Secrets:
- `<wireguard-name>` - Server private/public keys
- `<wireguard-name>-peer-configs` - Peer configurations (one key per peer)
- Agent reads these as a merged JSON state file

### Client Configuration

Generated by Client-Agent:
- Written to `/etc/wireguard/wg0.conf` (configurable)
- Standard WireGuard configuration format
- Includes interface and peer sections
- Automatically updated on changes

## Network Topology

```
                    ┌─────────────┐
                    │   Server    │
                    │  (Agent)    │
                    │  10.8.0.1   │
                    └──────┬──────┘
                           │
          ┌────────────────┼────────────────┐
          │                │                │
    ┌─────┴─────┐    ┌─────┴─────┐    ┌─────┴─────┐
    │  Peer 1   │    │  Peer 2   │    │  Peer 3   │
    │ (In-Cluster)│   │ (Client)  │    │ (Client)  │
    │  10.8.0.2  │    │  10.8.0.3 │    │  10.8.0.4 │
    └───────────┘    └───────────┘    └───────────┘
```

All peers can communicate with each other through the server (hub-and-spoke topology).

## Security Model

### Key Management

- **Server Keys:** Generated by Controller, stored in Secrets
- **Peer Keys:** Generated by Controller (in-cluster) or Client-Agent (external)
- **Key Rotation:** Delete and recreate Peer resource
- **Access Control:** Kubernetes RBAC for Secret access

### Network Security

- **Encryption:** WireGuard provides modern cryptography (Noise IK protocol)
- **Authentication:** Public key-based mutual authentication
- **Access Control:** EgressNetworkPolicies can restrict peer traffic
- **Isolation:** Each Wireguard instance is isolated from others

### Client-Agent Security

- **Minimal Permissions:** Read-only access to specific resources
- **Secure Config:** Configuration files written with 0600 permissions
- **Token-Based Auth:** Uses Kubernetes ServiceAccount tokens
- **No Privilege Escalation:** Runs with minimal capabilities

## Monitoring and Observability

### Metrics

**Agent (Server):**
- `wireguard_sent_bytes_total`
- `wireguard_received_bytes_total`
- `wireguard_latest_handshake_seconds`
- Labels: interface, public_key, peer_name, allowed_ips

**Client-Agent:**
- `wireguard_client_sent_bytes_total`
- `wireguard_client_received_bytes_total`
- `wireguard_client_latest_handshake_seconds`
- `wireguard_client_connected`
- Labels: interface, peer_name, wireguard_ref, public_key

### Health Checks

**Agent:**
- Endpoint: `:8080/health`
- Checks: State validity, WireGuard sync

**Client-Agent:**
- Endpoint: `:8082/health`
- Checks: Config file exists, interface up

### Logging

Both components use structured logging with configurable verbosity (`-v` flag).

## Deployment Scenarios

### Scenario 1: In-Cluster Mesh

All peers are Kubernetes workloads:
- Use only Controller + Agent
- Peers configured via WireguardPeer CRs
- No Client-Agent needed

### Scenario 2: Hybrid Mesh

Mix of in-cluster and external peers:
- Controller + Agent for server
- In-cluster peers via CRs
- External peers via Client-Agent

### Scenario 3: Multi-Cluster

Multiple clusters connected via WireGuard:
- Deploy Agent in each cluster
- Use Client-Agent on gateway nodes
- Create WireguardPeer for each gateway

### Scenario 4: Remote Access

Individual users connecting to cluster:
- Single Agent deployment
- Client-Agent on user laptops
- Each user has their own Peer resource

## Comparison Table

| Feature | Controller | Agent (Server) | Client-Agent |
|---------|-----------|----------------|--------------|
| **Location** | K8s cluster | K8s cluster | External |
| **Deployment** | Deployment | Deployment/Pod | Systemd/Container |
| **Watches** | CRs | State file | K8s API |
| **Configures** | Secrets/Deployments | wg0 (server) | wg0 (client) |
| **Keys** | Generates all | Uses server key | Uses peer key |
| **Port** | N/A | 51820 (UDP) | Ephemeral |
| **Metrics** | Standard K8s | :9586 | :9587 |
| **Health** | :8081 | :8080 | :8082 |
| **Network** | Cluster IP | Host network | Host network |

## Future Enhancements

Potential areas for expansion:

1. **Client-Agent Improvements:**
   - Push-based updates via Kubernetes events
   - Support for multiple WireGuard interfaces
   - Automatic MTU discovery
   - Connection quality metrics

2. **Security Enhancements:**
   - Automatic key rotation
   - Integration with external PKI
   - Audit logging for configuration changes

3. **Advanced Topologies:**
   - Mesh mode (peer-to-peer direct connections)
   - Multi-hop routing
   - Load balancing across multiple servers

4. **Integration:**
   - Service mesh integration
   - Network policy enforcement
   - CNI plugin for Kubernetes

For implementation details, see:
- [Controller Documentation](../internal/controller/)
- [Agent Documentation](../internal/agent/)
- [Client-Agent Documentation](client-agent.md)
