package clientagent

import (
	"net"
	"net/http"
	"strconv"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.zx2c4.com/wireguard/wgctrl"
)

// wireguardClientCollector exports WireGuard client metrics
type wireguardClientCollector struct {
	iface        string
	peerName     string
	wireguardRef string
}

func newWireguardClientCollector(iface, peerName, wireguardRef string) *wireguardClientCollector {
	return &wireguardClientCollector{
		iface:        iface,
		peerName:     peerName,
		wireguardRef: wireguardRef,
	}
}

func (c *wireguardClientCollector) Describe(ch chan<- *prometheus.Desc) {
	prometheus.DescribeByCollect(c, ch)
}

func (c *wireguardClientCollector) Collect(ch chan<- prometheus.Metric) {
	client, err := wgctrl.New()
	if err != nil {
		return
	}
	defer func() { _ = client.Close() }()

	dev, err := client.Device(c.iface)
	if err != nil || dev == nil {
		return
	}

	// Only collect metrics for the configured peer
	for _, p := range dev.Peers {
		labelNames := []string{"interface", "peer_name", "wireguard_ref", "public_key"}
		labelValues := []string{dev.Name, c.peerName, c.wireguardRef, p.PublicKey.String()}

		// sent bytes
		sentDesc := prometheus.NewDesc(
			"wireguard_client_sent_bytes_total",
			"Bytes sent to the WireGuard server",
			labelNames, nil,
		)
		ch <- prometheus.MustNewConstMetric(sentDesc, prometheus.CounterValue, float64(p.TransmitBytes), labelValues...)

		// received bytes
		recvDesc := prometheus.NewDesc(
			"wireguard_client_received_bytes_total",
			"Bytes received from the WireGuard server",
			labelNames, nil,
		)
		ch <- prometheus.MustNewConstMetric(recvDesc, prometheus.CounterValue, float64(p.ReceiveBytes), labelValues...)

		// latest handshake seconds
		var ts float64
		if !p.LastHandshakeTime.IsZero() {
			ts = float64(p.LastHandshakeTime.Unix())
		} else {
			ts = 0
		}
		hsDesc := prometheus.NewDesc(
			"wireguard_client_latest_handshake_seconds",
			"Unix timestamp of the last handshake with the server",
			labelNames, nil,
		)
		ch <- prometheus.MustNewConstMetric(hsDesc, prometheus.GaugeValue, ts, labelValues...)

		// connection state (1 = connected, 0 = disconnected)
		connected := 0
		if !p.LastHandshakeTime.IsZero() {
			connected = 1
		}
		connDesc := prometheus.NewDesc(
			"wireguard_client_connected",
			"Whether the client is connected to the server (1 = connected, 0 = disconnected)",
			labelNames, nil,
		)
		ch <- prometheus.MustNewConstMetric(connDesc, prometheus.GaugeValue, float64(connected), labelValues...)
	}
}

// StartMetricsServer starts an HTTP server exposing Prometheus metrics
func StartMetricsServer(bindAddress string, log logr.Logger) error {
	// Register collector with default interface name
	collector := newWireguardClientCollector("wg0", "", "")
	prometheus.MustRegister(collector)

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	addr := bindAddress
	if _, _, err := net.SplitHostPort(bindAddress); err != nil {
		if _, convErr := strconv.Atoi(bindAddress); convErr == nil {
			addr = ":" + bindAddress
		}
	}
	log.Info("starting metrics endpoint", "addr", addr)
	return http.ListenAndServe(addr, mux)
}
