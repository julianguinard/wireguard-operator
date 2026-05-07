package clientagent

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Reconciler handles reconciliation of WireGuard client configuration
type Reconciler struct {
	Client        client.Client
	Logger        logr.Logger
	Namespace     string
	WgConfigPath  string
	InterfaceName string
	PublicIP      string   // Public IP of this client machine
	InterfaceIPs  []string // List of interface IPs to match against (used when PublicIP is empty)

	mu               sync.Mutex
	lastConfigHashes map[string]string // Map of interface hash -> config hash
}

// WireGuardConfig represents a WireGuard configuration file
type WireGuardConfig struct {
	Interface InterfaceConfig `toml:"interface"`
	Peer      []PeerConfig    `toml:"peer"`
}

// InterfaceConfig represents the [Interface] section
type InterfaceConfig struct {
	PrivateKey              string `toml:"PrivateKey"`
	Address                 string `toml:"Address"`
	DNS                     string `toml:"DNS"`
	MTU                     string `toml:"MTU"`
	PreUp                   string `toml:"PreUp"`
	PostUp                  string `toml:"PostUp"`
	PreDown                 string `toml:"PreDown"`
	PostDown                string `toml:"PostDown"`
	SaveConfig              *bool  `toml:"SaveConfig"`
	Table                   string `toml:"Table"`
	FwMark                  string `toml:"FwMark"`
	ListenPort              *int   `toml:"ListenPort"`
	ReplaceDefaultAddresses *bool  `toml:"ReplaceDefaultAddresses"`
}

// PeerConfig represents a [Peer] section
type PeerConfig struct {
	PublicKey           string `toml:"PublicKey"`
	PreSharedKey        string `toml:"PreSharedKey"`
	AllowedIPs          string `toml:"AllowedIPs"`
	Endpoint            string `toml:"Endpoint"`
	PersistentKeepalive *int   `toml:"PersistentKeepalive"`
}

// PeerConfigEntry represents a peer configuration entry
type PeerConfigEntry struct {
	WireguardRef string
	ConfigData   string
}

// InterfaceGroup represents a grouped WireGuard configuration
type InterfaceGroup struct {
	Interface     string
	PeerSections  []string
	WireguardRefs []string
}

// Reconcile fetches the current state from Kubernetes and updates WireGuard configuration
func (r *Reconciler) Reconcile(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.Logger.Info("starting reconciliation", "publicIP", r.PublicIP, "namespace", r.Namespace)

	// Find all peer-config secrets for this client
	peerConfigs, err := r.findPeerConfigs(ctx)
	if err != nil {
		return fmt.Errorf("failed to find peer configs: %w", err)
	}

	if len(peerConfigs) == 0 {
		r.Logger.Info("no peer configurations found for this client")
		return nil
	}

	r.Logger.Info("found peer configurations", "count", len(peerConfigs))

	// Parse all configs and group by Interface
	interfaceGroups, err := r.groupByInterface(peerConfigs)
	if err != nil {
		return fmt.Errorf("failed to group configs by interface: %w", err)
	}

	r.Logger.Info("grouped configs by interface", "groups", len(interfaceGroups))

	// Generate and apply config for each interface group
	configHashes := make(map[string]string)

	interfaceIndex := 0
	for interfaceHash, group := range interfaceGroups {
		// Determine config file path
		var configPath string
		if interfaceIndex == 0 {
			// use the configured path + interface name for first interface
			configPath = r.WgConfigPath + "/" + r.InterfaceName + ".conf"
		} else {
			// Multiple interfaces: create separate files for other interfaces
			configPath = fmt.Sprintf("%s/%s-%s.conf", r.WgConfigPath, r.InterfaceName, interfaceHash[:8])
		}
		interfaceIndex++

		// Generate merged configuration
		mergedConfig, err := r.mergeInterfaceGroup(group)
		if err != nil {
			r.Logger.Error(err, "failed to merge interface group", "hash", interfaceHash)
			continue
		}

		// Check if configuration has changed
		configHashes[configPath] = r.hashConfig(mergedConfig)
		if configHashes[configPath] == r.lastConfigHashes[configPath] {
			r.Logger.V(2).Info("configuration unchanged, skipping update", "path", configPath)
			continue
		}

		r.Logger.Info("configuration changed, updating WireGuard", "path", configPath)

		// Write configuration to temporary file
		tmpConfigPath := configPath + ".tmp"
		if err := r.writeConfig(tmpConfigPath, mergedConfig); err != nil {
			r.Logger.Error(err, "failed to write temp config", "path", tmpConfigPath)
			continue
		}

		// Determine interface name for this config
		interfaceName := r.InterfaceName
		if len(interfaceGroups) > 1 {
			// Use hash-based interface name for multiple configs
			interfaceName = fmt.Sprintf("%s_%s", r.InterfaceName, interfaceHash[:6])
		}

		// Apply configuration using wgctrl
		if err := r.applyConfigWithWgCtrl(tmpConfigPath, interfaceName); err != nil {
			// Clean up temp file on error
			_ = os.Remove(tmpConfigPath)
			r.Logger.Error(err, "failed to apply config", "path", configPath, "interface", interfaceName)
			continue
		}

		// Move temp config to final location
		if err := os.Rename(tmpConfigPath, configPath); err != nil {
			r.Logger.Error(err, "failed to move config to final location", "path", configPath)
			continue
		}

		r.Logger.Info("successfully applied configuration", "path", configPath, "interface", interfaceName)
	}

	// Update last config hashes
	r.lastConfigHashes = configHashes

	r.Logger.Info("reconciliation completed successfully")

	return nil
}

// findPeerConfigs finds all peer-config secrets for this client
func (r *Reconciler) findPeerConfigs(ctx context.Context) ([]PeerConfigEntry, error) {
	var peerConfigs []PeerConfigEntry

	// List all secrets in the namespace
	secretList := &corev1.SecretList{}
	if err := r.Client.List(ctx, secretList, &client.ListOptions{Namespace: r.Namespace}); err != nil {
		return nil, fmt.Errorf("failed to list secrets: %w", err)
	}

	// Determine which IPs to match against
	var matchIPs []string
	if r.PublicIP != "" {
		// Use explicit public IP if provided
		matchIPs = []string{r.PublicIP}
		r.Logger.V(2).Info("using explicit public IP for matching", "publicIP", r.PublicIP)
	} else if len(r.InterfaceIPs) > 0 {
		// Use interface IPs if public IP is not provided
		matchIPs = r.InterfaceIPs
		r.Logger.V(2).Info("using interface IPs for matching", "interfaceIPs", r.InterfaceIPs)
	} else {
		r.Logger.Info("no public IP or interface IPs configured, skipping peer config search")
		return peerConfigs, nil
	}

	// Find secrets matching the pattern: <wireguard-name>-peer-configs
	// that contain a key matching: <publicIP>-peer-config or <interfaceIP>-peer-config
	for _, secret := range secretList.Items {
		// Check if secret name ends with "-peer-configs"
		if !strings.HasSuffix(secret.Name, "-peer-configs") {
			continue
		}

		// Extract wireguard name from secret name
		wgName := strings.TrimSuffix(secret.Name, "-peer-configs")

		// Look for a key matching any of the IPs
		var matchingKeyName *string
		var matchingConfigData []byte
		for keyName, configData := range secret.Data {
			if IPMatchesPrefix(keyName, matchIPs) {
				matchingKeyName = &keyName
				matchingConfigData = configData
				break
			}
		}
		if matchingKeyName == nil {
			continue
		}

		r.Logger.V(2).Info("found peer config", "wireguard", wgName, "key", *matchingKeyName)

		peerConfigs = append(peerConfigs, PeerConfigEntry{
			WireguardRef: wgName,
			ConfigData:   string(matchingConfigData),
		})
	}

	return peerConfigs, nil
}

// groupByInterface parses configs and groups them by Interface section
func (r *Reconciler) groupByInterface(peerConfigs []PeerConfigEntry) (map[string]*InterfaceGroup, error) {
	interfaceGroups := make(map[string]*InterfaceGroup)

	for _, pc := range peerConfigs {
		// Parse the config by splitting on sections
		interfaceSection, peerSections := r.parseWireGuardConfig(pc.ConfigData)

		if interfaceSection == "" {
			r.Logger.Info("no interface section found in config", "wireguard", pc.WireguardRef)
			continue
		}

		// Create hash of interface section for grouping
		interfaceHash := r.hashString(interfaceSection)

		if group, exists := interfaceGroups[interfaceHash]; exists {
			// Add peer sections to existing group
			group.PeerSections = append(group.PeerSections, peerSections...)
			group.WireguardRefs = append(group.WireguardRefs, pc.WireguardRef)
		} else {
			// Create new group
			interfaceGroups[interfaceHash] = &InterfaceGroup{
				Interface:     interfaceSection,
				PeerSections:  peerSections,
				WireguardRefs: []string{pc.WireguardRef},
			}
		}
	}

	return interfaceGroups, nil
}

// parseWireGuardConfig splits a WireGuard config into interface and peer sections
func (r *Reconciler) parseWireGuardConfig(config string) (interfaceSection string, peerSections []string) {
	lines := strings.Split(config, "\n")

	var currentContent strings.Builder
	inInterface := false
	inPeer := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Check for section headers
		if strings.HasPrefix(trimmed, "[") {
			// Save previous section if any
			if inInterface {
				interfaceSection = strings.TrimSpace(currentContent.String())
				currentContent.Reset()
				inInterface = false
			} else if inPeer {
				peerSections = append(peerSections, strings.TrimSpace(currentContent.String()))
				currentContent.Reset()
				inPeer = false
			}

			// Determine new section type
			if strings.EqualFold(trimmed, "[Interface]") {
				inInterface = true
			} else if strings.EqualFold(trimmed, "[Peer]") {
				inPeer = true
			}
			continue
		}

		// Add line to current section if we're in one
		if inInterface || inPeer {
			currentContent.WriteString(line)
			currentContent.WriteString("\n")
		}
	}

	// Don't forget the last section
	if inInterface {
		interfaceSection = strings.TrimSpace(currentContent.String())
	} else if inPeer {
		peerSections = append(peerSections, strings.TrimSpace(currentContent.String()))
	}

	return interfaceSection, peerSections
}

// hashInterfaceConfig creates a hash of the interface configuration for grouping
func (r *Reconciler) hashInterfaceConfig(iface string) string {
	return r.hashString(iface)
}

// hashString creates a hash of a string
func (r *Reconciler) hashString(s string) string {
	hash := md5.Sum([]byte(s))
	return hex.EncodeToString(hash[:])
}

// mergeInterfaceGroup merges all peer sections for a given interface group
func (r *Reconciler) mergeInterfaceGroup(group *InterfaceGroup) (string, error) {
	var sb strings.Builder

	sb.WriteString("# WireGuard configuration generated by client-agent\n")
	sb.WriteString(fmt.Sprintf("# WireGuard instances: %s\n", strings.Join(group.WireguardRefs, ", ")))
	sb.WriteString(fmt.Sprintf("# Generated for public IP: %s\n", r.PublicIP))
	sb.WriteString("\n")

	// Write Interface section
	sb.WriteString("[Interface]\n")
	sb.WriteString(group.Interface)
	sb.WriteString("\n")

	// Write all Peer sections
	for _, peer := range group.PeerSections {
		sb.WriteString("[Peer]\n")
		sb.WriteString(peer)
		sb.WriteString("\n")
	}

	return sb.String(), nil
}

// hashConfig generates a hash of the configuration
func (r *Reconciler) hashConfig(config string) string {
	return r.hashString(config)
}

// writeConfig writes the configuration to the specified path
func (r *Reconciler) writeConfig(path string, config string) error {
	// Ensure directory exists
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}

	// Write with secure permissions
	return os.WriteFile(path, []byte(config), 0600)
}

// applyConfigWithWgCtrl applies the WireGuard configuration using wgctrl
func (r *Reconciler) applyConfigWithWgCtrl(configPath string, interfaceName string) error {
	r.Logger.Info("using wgctrl to apply configuration", "interface", interfaceName)

	// Parse the configuration file
	config, err := r.parseWireGuardConfigFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to parse config file: %w", err)
	}

	// Create wgctrl client
	client, err := wgctrl.New()
	if err != nil {
		return fmt.Errorf("failed to create wgctrl client: %w", err)
	}
	defer client.Close()

	// Build wgctrl configuration
	cfg := wgtypes.Config{}

	// Configure interface settings if private key is present
	if config.Interface.PrivateKey != "" {
		key, err := wgtypes.ParseKey(config.Interface.PrivateKey)
		if err != nil {
			return fmt.Errorf("failed to parse private key: %w", err)
		}
		cfg.PrivateKey = &key
	}

	// Configure listen port if specified
	if config.Interface.ListenPort != nil {
		cfg.ListenPort = config.Interface.ListenPort
	}

	// Configure peers
	var peerConfigs []wgtypes.PeerConfig
	for _, peer := range config.Peer {
		publicKey, err := wgtypes.ParseKey(peer.PublicKey)
		if err != nil {
			return fmt.Errorf("failed to parse peer public key %s: %w", peer.PublicKey, err)
		}

		peerConfig := wgtypes.PeerConfig{
			PublicKey: publicKey,
		}

		// Parse allowed IPs
		if peer.AllowedIPs != "" {
			allowedIPs, err := r.parseAllowedIPs(peer.AllowedIPs)
			if err != nil {
				return fmt.Errorf("failed to parse allowed IPs for peer %s: %w", peer.PublicKey, err)
			}
			peerConfig.AllowedIPs = allowedIPs
			peerConfig.ReplaceAllowedIPs = true
		}

		// Parse endpoint if specified
		if peer.Endpoint != "" {
			endpoint, err := net.ResolveUDPAddr("udp", peer.Endpoint)
			if err != nil {
				return fmt.Errorf("failed to parse endpoint %s for peer %s: %w", peer.Endpoint, peer.PublicKey, err)
			}
			peerConfig.Endpoint = endpoint
		}

		// Parse pre-shared key if specified
		if peer.PreSharedKey != "" {
			psk, err := wgtypes.ParseKey(peer.PreSharedKey)
			if err != nil {
				return fmt.Errorf("failed to parse pre-shared key for peer %s: %w", peer.PublicKey, err)
			}
			peerConfig.PresharedKey = &psk
		}

		// Parse persistent keepalive if specified
		if peer.PersistentKeepalive != nil && *peer.PersistentKeepalive > 0 {
			duration := time.Duration(*peer.PersistentKeepalive) * time.Second
			peerConfig.PersistentKeepaliveInterval = &duration
		}

		peerConfigs = append(peerConfigs, peerConfig)
	}

	cfg.Peers = peerConfigs
	// make sure we do not interrupt existing sessions
	cfg.ReplacePeers = false

	// Apply configuration
	if err := client.ConfigureDevice(interfaceName, cfg); err != nil {
		return fmt.Errorf("failed to configure device %s: %w", interfaceName, err)
	}

	r.Logger.Info("successfully applied configuration using wgctrl", "interface", interfaceName, "peers", len(peerConfigs))

	return nil
}

// parseWireGuardConfigFile parses a WireGuard configuration file into a WireGuardConfig struct
func (r *Reconciler) parseWireGuardConfigFile(configPath string) (*WireGuardConfig, error) {
	content, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	return r.parseWireGuardConfigContent(string(content))
}

// parseWireGuardConfigContent parses WireGuard config content into a WireGuardConfig struct
func (r *Reconciler) parseWireGuardConfigContent(content string) (*WireGuardConfig, error) {
	config := &WireGuardConfig{}

	lines := strings.Split(content, "\n")
	var currentSection string
	var currentPeer PeerConfig

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Skip comments and empty lines
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Check for section headers
		if strings.HasPrefix(trimmed, "[") {
			// Save previous peer if exists
			if currentSection == "peer" && currentPeer.PublicKey != "" {
				config.Peer = append(config.Peer, currentPeer)
				currentPeer = PeerConfig{}
			}

			if strings.EqualFold(trimmed, "[Interface]") {
				currentSection = "interface"
			} else if strings.EqualFold(trimmed, "[Peer]") {
				currentSection = "peer"
			}
			continue
		}

		// Parse key=value pairs
		parts := strings.SplitN(trimmed, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		switch currentSection {
		case "interface":
			switch key {
			case "PrivateKey":
				config.Interface.PrivateKey = value
			case "Address":
				config.Interface.Address = value
			case "DNS":
				config.Interface.DNS = value
			case "MTU":
				config.Interface.MTU = value
			case "ListenPort":
				if port, err := parseInt(value); err == nil {
					config.Interface.ListenPort = &port
				}
			case "Table":
				config.Interface.Table = value
			case "FwMark":
				config.Interface.FwMark = value
			case "SaveConfig":
				if val, err := parseBool(value); err == nil {
					config.Interface.SaveConfig = &val
				}
			}

		case "peer":
			switch key {
			case "PublicKey":
				currentPeer.PublicKey = value
			case "PreSharedKey":
				currentPeer.PreSharedKey = value
			case "AllowedIPs":
				currentPeer.AllowedIPs = value
			case "Endpoint":
				currentPeer.Endpoint = value
			case "PersistentKeepalive":
				if keepalive, err := parseInt(value); err == nil {
					currentPeer.PersistentKeepalive = &keepalive
				}
			}
		}
	}

	// Save last peer if exists
	if currentSection == "peer" && currentPeer.PublicKey != "" {
		config.Peer = append(config.Peer, currentPeer)
	}

	return config, nil
}

// parseAllowedIPs parses a comma-separated list of allowed IPs
func (r *Reconciler) parseAllowedIPs(allowedIPs string) ([]net.IPNet, error) {
	var result []net.IPNet

	ipList := strings.Split(allowedIPs, ",")
	for _, ip := range ipList {
		ip = strings.TrimSpace(ip)
		if ip == "" {
			continue
		}

		_, ipnet, err := net.ParseCIDR(ip)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", ip, err)
		}
		result = append(result, *ipnet)
	}

	return result, nil
}

// parseInt parses a string to int
func parseInt(s string) (int, error) {
	var result int
	_, err := fmt.Sscanf(s, "%d", &result)
	return result, err
}

// parseBool parses a string to bool
func parseBool(s string) (bool, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "true" || s == "yes" || s == "1", nil
}
