package clientagent

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"

	"github.com/go-logr/logr"
)

// StartHealthServer starts an HTTP health check server
func StartHealthServer(port string, log logr.Logger) error {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		// Check if config file exists and is readable
		configPath := os.Getenv("WG_CONFIG_PATH")
		if configPath == "" {
			configPath = "/etc/wireguard/wg0.conf"
		}

		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			log.Error(err, "config file does not exist")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("config file not found"))
			return
		}

		// Check if WireGuard interface exists and is up
		if err := checkInterface(); err != nil {
			log.Error(err, "interface check failed")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(fmt.Sprintf("interface check failed: %v", err)))
			return
		}

		log.V(2).Info("health check passed")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	addr := ":" + port
	log.Info("starting health endpoint", "addr", addr)
	return http.ListenAndServe(addr, mux)
}

// checkInterface verifies that the WireGuard interface is up
func checkInterface() error {
	// Try to use wg command to check interface status
	wgPath, err := exec.LookPath("wg")
	if err == nil {
		cmd := exec.Command(wgPath, "show")
		output, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("wg show failed: %w", err)
		}
		// Check if any interface is shown in output
		if len(output) == 0 {
			return fmt.Errorf("no WireGuard interfaces found")
		}
		return nil
	}

	// Fallback: check if interface exists in /sys/class/net
	// This is less reliable but works without wg-tools
	if _, err := os.Stat("/sys/class/net/wg0"); os.IsNotExist(err) {
		return fmt.Errorf("interface wg0 does not exist in /sys/class/net")
	}
	return nil
}
