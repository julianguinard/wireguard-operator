package clientagent

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/go-logr/logr"
	"github.com/nccloud/wireguard-operator/internal/wireguard"
	"golang.org/x/sys/unix"
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
		if err := r.applyConfigWithWgSyncconf(tmpConfigPath, interfaceName); err != nil {
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

// applyConfigWithWgSyncconf applies the WireGuard configuration using 'wg syncconf'
func (r *Reconciler) applyConfigWithWgSyncconf(configPath string, interfaceName string) error {
	// Read the original config file
	originalConfig, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	// Parse the config to extract only PrivateKey from [Interface] section
	syncconfConfig, err := r.createSyncconfConfig(string(originalConfig))
	if err != nil {
		return fmt.Errorf("failed to create syncconf config: %w", err)
	}

	// Write syncconf-valid config to a temporary file
	tmpSyncconfPath := configPath + ".syncconf"
	if err := os.WriteFile(tmpSyncconfPath, []byte(syncconfConfig), 0600); err != nil {
		return fmt.Errorf("failed to write syncconf temp file: %w", err)
	}
	defer os.Remove(tmpSyncconfPath) // Clean up after use

	// Use 'wg syncconf' command
	wgPath, err := exec.LookPath("wg")
	if err != nil {
		return fmt.Errorf("wg command not found: %w", err)
	}

	r.Logger.Info("using 'wg syncconf' to apply configuration", "interface", interfaceName)

	// Command: wg syncconf <interface> <config-file>
	cmd := exec.Command(wgPath, "syncconf", interfaceName, tmpSyncconfPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("wg syncconf failed: %w", err)
	}

	// After applying config, sync routes for all AllowedIPs (like wg quick would do)
	if err := r.syncAllowedIPRoutes(interfaceName, string(originalConfig)); err != nil {
		r.Logger.Error(err, "failed to sync routes for AllowedIPs", "interface", interfaceName)
		// Note: We don't fail here as the config was applied successfully, just routes failed
	}

	return nil
}

// createSyncconfConfig creates a syncconf-valid config with only PrivateKey in [Interface] section
func (r *Reconciler) createSyncconfConfig(config string) (string, error) {
	lines := strings.Split(config, "\n")
	var result strings.Builder
	inInterface := false
	interfaceWritten := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Check for section headers
		if strings.HasPrefix(trimmed, "[") {
			if strings.EqualFold(trimmed, "[Interface]") {
				inInterface = true
				interfaceWritten = false
				result.WriteString("[Interface]\n")
				continue
			} else {
				inInterface = false
			}
		}

		// In [Interface] section, only keep PrivateKey
		if inInterface {
			if strings.HasPrefix(trimmed, "PrivateKey") {
				result.WriteString(line)
				result.WriteString("\n")
				interfaceWritten = true
			}
			continue
		}

		// Keep all other sections (Peer, etc.) as-is
		result.WriteString(line)
		result.WriteString("\n")
	}

	if !interfaceWritten {
		return "", fmt.Errorf("no PrivateKey found in [Interface] section")
	}

	return result.String(), nil
}

// syncAllowedIPRoutes adds routes for all AllowedIPs in the configuration
func (r *Reconciler) syncAllowedIPRoutes(interfaceName string, config string) error {
	// Parse peer sections to extract AllowedIPs
	allowedIPs := r.extractAllowedIPs(config)

	if len(allowedIPs) == 0 {
		r.Logger.V(2).Info("no AllowedIPs found to sync routes for", "interface", interfaceName)
		return nil
	}

	r.Logger.Info("syncing routes for AllowedIPs", "interface", interfaceName, "count", len(allowedIPs))

	// Call wireguard.SyncRoute for each AllowedIP
	for _, allowedIP := range allowedIPs {
		// Determine IP family
		family := unix.AF_INET
		if strings.Contains(allowedIP, ":") {
			family = unix.AF_INET6
		}

		// Parse the CIDR to get the gateway IP (similar to how syncV4CIDR/syncV6CIDR work)
		prefix, err := netip.ParsePrefix(allowedIP)
		if err != nil {
			r.Logger.Error(err, "failed to parse AllowedIP prefix", "allowedIP", allowedIP)
			continue
		}

		// Get gateway IP from prefix (next IP in the prefix)
		gw := prefix.Addr().Next()
		var gwIP net.IP
		if gw.Is4() {
			b := gw.As4()
			gwIP = net.IPv4(b[0], b[1], b[2], b[3])
		} else if gw.Is6() {
			b := gw.As16()
			gwIP = net.IP(b[:])
		} else {
			r.Logger.Error(fmt.Errorf("unsupported address family"), "unsupported address family for AllowedIP", "allowedIP", allowedIP)
			continue
		}

		// Call wireguard.SyncRoute to add the route
		if err := wireguard.SyncRoute(interfaceName, allowedIP, gwIP, family); err != nil {
			r.Logger.Error(err, "failed to sync route for AllowedIP", "allowedIP", allowedIP, "interface", interfaceName)
			// Continue with other routes even if one fails
		} else {
			r.Logger.V(2).Info("successfully synced route for AllowedIP", "allowedIP", allowedIP, "interface", interfaceName)
		}
	}

	return nil
}

// extractAllowedIPs parses the config and extracts all AllowedIPs from peer sections
func (r *Reconciler) extractAllowedIPs(config string) []string {
	var allowedIPs []string
	lines := strings.Split(config, "\n")

	inPeer := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Check for section headers
		if strings.HasPrefix(trimmed, "[") {
			inPeer = strings.EqualFold(trimmed, "[Peer]")
			continue
		}

		// Extract AllowedIPs from peer sections
		if inPeer && strings.HasPrefix(trimmed, "AllowedIPs") {
			// Parse the AllowedIPs value (format: AllowedIPs = 10.0.0.1/32, 192.168.1.0/24)
			parts := strings.SplitN(trimmed, "=", 2)
			if len(parts) == 2 {
				ipsStr := strings.TrimSpace(parts[1])
				// Split by comma to handle multiple IPs
				ips := strings.Split(ipsStr, ",")
				for _, ip := range ips {
					ip = strings.TrimSpace(ip)
					if ip != "" {
						allowedIPs = append(allowedIPs, ip)
					}
				}
			}
		}
	}

	return allowedIPs
}
