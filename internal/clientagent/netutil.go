package clientagent

import (
	"fmt"
	"net"
	"strings"
)

// GetInterfaceIPs returns a list of IP addresses from all network interfaces
func GetInterfaceIPs() ([]string, error) {
	var ips []string

	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("failed to list network interfaces: %w", err)
	}

	for _, iface := range interfaces {
		// Skip loopback and down interfaces
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue // Skip interfaces that can't be queried
		}

		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}

			if ip == nil {
				continue
			}

			// Only include IPv4 addresses
			if ip.To4() == nil {
				continue
			}

			ips = append(ips, ip.String())
		}
	}

	return ips, nil
}

// IPMatchesPrefix checks if an IP address starts with any of the given prefixes
func IPMatchesPrefix(ip string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(ip, prefix) {
			return true
		}
	}
	return false
}
