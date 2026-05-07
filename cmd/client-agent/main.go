/*
Copyright 2021.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/go-logr/stdr"
	vpnv1alpha1 "github.com/nccloud/wireguard-operator/api/v1alpha1"
	"github.com/nccloud/wireguard-operator/internal/clientagent"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	_ = corev1.AddToScheme(scheme)
	_ = vpnv1alpha1.AddToScheme(scheme)
}

func main() {
	var kubeconfig string
	var namespace string
	var peerName string
	var wgConfigPath string
	var iface string
	var verbosity int
	var metricsBindAddress string
	var healthPort int
	var publicIP string

	// Use a custom flag set to avoid conflicts with controller-runtime flags
	fs := flag.NewFlagSet("client-agent", flag.ExitOnError)
	fs.StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig file. Required when running outside cluster.")
	fs.StringVar(&namespace, "namespace", "default", "Namespace to watch for WireguardPeer resources.")
	fs.StringVar(&wgConfigPath, "wg-config-path", "/etc/wireguard", "Directory where WireGuard stores configuration files.")
	fs.StringVar(&iface, "wg-iface", "wg0", "The WireGuard interface name. Default is wg0.")
	fs.IntVar(&verbosity, "v", 1, "The verbosity level.")
	fs.StringVar(&metricsBindAddress, "metrics-bind-address", ":9587", "The address the Prometheus metrics endpoint binds to.")
	fs.IntVar(&healthPort, "health-port", 8082, "The health check server port.")
	fs.StringVar(&publicIP, "public-ip", "", "Public IP of this client machine. Used to find matching peer configs in secrets. If omitted, the agent will automatically detect interface IPs.")

	fs.Parse(os.Args[1:])

	stdr.SetVerbosity(verbosity)
	log := stdr.NewWithOptions(log.New(os.Stderr, "", log.LstdFlags), stdr.Options{LogCaller: stdr.All})
	log = log.WithName("client-agent")

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{
		Development: true,
	})))

	// Create kubernetes client
	var config *rest.Config
	var err error

	if kubeconfig != "" {
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	} else {
		config, err = rest.InClusterConfig()
	}

	if err != nil {
		setupLog.Error(err, "unable to create kubernetes config")
		os.Exit(1)
	}

	k8sClient, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		setupLog.Error(err, "unable to create kubernetes client")
		os.Exit(1)
	}

	// Determine interface IPs if public IP is not provided
	var interfaceIPs []string
	if publicIP == "" {
		interfaceIPs, err = clientagent.GetInterfaceIPs()
		if err != nil {
			setupLog.Error(err, "unable to detect interface IPs")
			os.Exit(1)
		}
		if len(interfaceIPs) == 0 {
			setupLog.Error(fmt.Errorf("no interface IPs detected"), "no network interfaces found")
			os.Exit(1)
		}
		log.Info("auto-detected interface IPs", "ips", interfaceIPs)
	} else {
		log.Info("using explicit public IP", "publicIP", publicIP)
	}

	// Create reconciler
	reconciler := &clientagent.Reconciler{
		Client:        k8sClient,
		Logger:        log.WithName("reconciler"),
		Namespace:     namespace,
		WgConfigPath:  wgConfigPath,
		InterfaceName: iface,
		PublicIP:      publicIP,
		InterfaceIPs:  interfaceIPs,
	}

	// Start the watcher
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	log.Info("starting client-agent",
		"peer-name", peerName,
		"namespace", namespace,
		"wg-config-path", wgConfigPath,
		"interface", iface,
		"public-ip", publicIP,
		"interface-ips", interfaceIPs)

	// Start health server
	go func() {
		if err := clientagent.StartHealthServer(strconv.Itoa(healthPort), log.WithName("health")); err != nil {
			log.Error(err, "health server failed")
		}
	}()

	// Start metrics server
	go func() {
		if err := clientagent.StartMetricsServer(metricsBindAddress, log.WithName("metrics")); err != nil {
			log.Error(err, "metrics server failed")
		}
	}()

	// Start the reconciliation loop
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		// Initial reconciliation
		if err := reconciler.Reconcile(ctx); err != nil {
			log.Error(err, "initial reconciliation failed")
		}

		for {
			select {
			case <-ctx.Done():
				log.Info("shutting down client-agent")
				return
			case <-ticker.C:
				if err := reconciler.Reconcile(ctx); err != nil {
					log.Error(err, "reconciliation failed")
				}
			}
		}
	}()

	// Start manager for event watching (optional, can be used for watching events)
	// For now, we use polling, but this could be enhanced with informers
	<-ctx.Done()

	log.Info("client-agent stopped")
}
