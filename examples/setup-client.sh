#!/bin/bash
# Helper script to set up a client machine for wireguard-operator
# This script generates keys, creates the WireguardPeer resource, and prepares the client-agent

set -e

# Configuration
NAMESPACE="${NAMESPACE:-default}"
WIREGUARD_REF="${WIREGUARD_REF:-vpn}"
PEER_NAME="${PEER_NAME:-$(hostname)}"
WG_CONFIG_DIR="/etc/wireguard"
KUBECONFIG_PATH="${KUBECONFIG:-$HOME/.kube/config}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${GREEN}=== WireGuard Operator Client Setup ===${NC}"
echo ""

# Step 1: Check prerequisites
echo -e "${YELLOW}Step 1: Checking prerequisites...${NC}"

if ! command -v wg &> /dev/null; then
    echo -e "${RED}Error: wg command not found. Please install wireguard-tools.${NC}"
    exit 1
fi

if ! command -v kubectl &> /dev/null; then
    echo -e "${RED}Error: kubectl command not found. Please install kubectl.${NC}"
    exit 1
fi

if [ ! -f "$KUBECONFIG_PATH" ]; then
    echo -e "${RED}Error: Kubeconfig not found at $KUBECONFIG_PATH${NC}"
    exit 1
fi

echo -e "${GREEN}✓ Prerequisites check passed${NC}"
echo ""

# Step 2: Generate WireGuard keys
echo -e "${YELLOW}Step 2: Generating WireGuard keys...${NC}"

mkdir -p "$WG_CONFIG_DIR"
cd "$WG_CONFIG_DIR"

if [ -f "privatekey" ] && [ -f "publickey" ]; then
    echo -e "${YELLOW}Keys already exist. Reusing existing keys.${NC}"
else
    echo "Generating new keypair..."
    wg genkey | tee privatekey | wg pubkey > publickey
    chmod 600 privatekey
    chmod 644 publickey
    echo -e "${GREEN}✓ New keypair generated${NC}"
fi

PUBLIC_KEY=$(cat publickey)
echo "Public Key: $PUBLIC_KEY"
echo ""

# Step 3: Create WireguardPeer resource
echo -e "${YELLOW}Step 3: Creating WireguardPeer resource...${NC}"

cat <<EOF | kubectl apply -f -
apiVersion: vpn.wireguard-operator.io/v1alpha1
kind: WireguardPeer
metadata:
  name: $PEER_NAME
  namespace: $NAMESPACE
spec:
  wireguardRef: "$WIREGUARD_REF"
  publicKey: "$PUBLIC_KEY"
EOF

echo -e "${GREEN}✓ WireguardPeer resource created${NC}"
echo ""

# Step 4: Wait for peer to be ready
echo -e "${YELLOW}Step 4: Waiting for peer to be ready...${NC}"

for i in {1..30}; do
    STATUS=$(kubectl get wireguardpeer $PEER_NAME -n $NAMESPACE -o jsonpath='{.status.status}' 2>/dev/null || echo "")
    if [ "$STATUS" = "ready" ]; then
        echo -e "${GREEN}✓ Peer is ready${NC}"
        break
    fi
    echo "Waiting for peer to be ready... (attempt $i/30, status: $STATUS)"
    sleep 2
done

if [ "$STATUS" != "ready" ]; then
    echo -e "${RED}Warning: Peer is not ready yet. Continuing anyway...${NC}"
fi
echo ""

# Step 5: Create ServiceAccount and RBAC (if needed)
echo -e "${YELLOW}Step 5: Setting up RBAC...${NC}"

kubectl create namespace wireguard-clients --dry-run=client -o yaml | kubectl apply -f -

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
  namespace: $NAMESPACE
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
  namespace: $NAMESPACE
subjects:
- kind: ServiceAccount
  name: client-agent
  namespace: wireguard-clients
roleRef:
  kind: Role
  name: client-agent
  apiGroup: rbac.authorization.k8s.io
EOF

echo -e "${GREEN}✓ RBAC configured${NC}"
echo ""

# Step 6: Generate kubeconfig for client-agent
echo -e "${YELLOW}Step 6: Generating kubeconfig for client-agent...${NC}"

TOKEN=$(kubectl -n wireguard-clients create token client-agent --duration=8760h)
CA_CERT=$(kubectl config view --raw --minify -o jsonpath='{.clusters[0].cluster.certificate-authority-data}')
API_SERVER=$(kubectl config view --raw --minify -o jsonpath='{.clusters[0].cluster.server}')

cat > "$WG_CONFIG_DIR/kubeconfig" <<EOF
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

chmod 600 "$WG_CONFIG_DIR/kubeconfig"
echo -e "${GREEN}✓ Kubeconfig generated at $WG_CONFIG_DIR/kubeconfig${NC}"
echo ""

# Step 7: Install client-agent
echo -e "${YELLOW}Step 7: Installing client-agent...${NC}"

# Check if client-agent binary exists
if ! command -v client-agent &> /dev/null; then
    echo "client-agent binary not found. Please download it first:"
    echo "  curl -L https://github.com/nccloud/wireguard-operator/releases/latest/download/client-agent -o /usr/local/bin/client-agent"
    echo "  chmod +x /usr/local/bin/client-agent"
    echo ""
    echo -e "${YELLOW}Skipping client-agent installation. You can install it manually.${NC}"
else
    # Create systemd service
    cat > /etc/systemd/system/client-agent.service <<EOF
[Unit]
Description=WireGuard Operator Client Agent
After=network.target wireguard@wg0.service
Wants=wireguard@wg0.service

[Service]
Type=simple
ExecStart=/usr/local/bin/client-agent \\
  --public-key=$PUBLIC_KEY \\
  --namespace=$NAMESPACE \\
  --kubeconfig=$WG_CONFIG_DIR/kubeconfig \\
  --wg-config-path=$WG_CONFIG_DIR/wg0.conf \\
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
    
    echo -e "${GREEN}✓ client-agent installed and started${NC}"
    echo ""
    
    # Show status
    echo -e "${YELLOW}Client-agent status:${NC}"
    systemctl status client-agent --no-pager -l
    echo ""
fi

# Step 8: Summary
echo -e "${GREEN}=== Setup Complete ===${NC}"
echo ""
echo "Summary:"
echo "  Peer Name: $PEER_NAME"
echo "  Public Key: $PUBLIC_KEY"
echo "  Namespace: $NAMESPACE"
echo "  WireGuard Ref: $WIREGUARD_REF"
echo "  Config Path: $WG_CONFIG_DIR/wg0.conf"
echo ""
echo "Next steps:"
echo "  1. Check client-agent logs: journalctl -u client-agent -f"
echo "  2. Verify WireGuard interface: wg show wg0"
echo "  3. Test connectivity: ping <server-ip>"
echo ""
echo -e "${GREEN}Done!${NC}"
