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

package resources

import (
	"fmt"

	"github.com/nccloud/wireguard-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	// HTTPPort is the port for HTTP health checks.
	HTTPPort = 8080
	// DefaultWstunnelImage is the default container image for the wstunnel sidecar.
	DefaultWstunnelImage = "ghcr.io/erebe/wstunnel:latest"
)

// PodBuilder provides common pod building functionality for both Deployment and DaemonSet.
type PodBuilder struct {
	agentImage           string
	agentImagePullPolicy corev1.PullPolicy
}

// NewPodBuilder creates a new PodBuilder.
func NewPodBuilder(agentImage string, agentImagePullPolicy corev1.PullPolicy) *PodBuilder {
	return &PodBuilder{
		agentImage:           agentImage,
		agentImagePullPolicy: agentImagePullPolicy,
	}
}

// BuildPodSpec creates the common pod spec used by both Deployment and DaemonSet.
func (b *PodBuilder) BuildPodSpec(wg *v1alpha1.Wireguard) corev1.PodSpec {
	httpPort := HTTPPort
	if wg.Spec.AgentHTTPPort != 0 {
		httpPort = int(wg.Spec.AgentHTTPPort)
	}

	// Security context settings
	readOnlyRootFilesystem := true
	allowPrivilegeEscalation := false
	automountServiceAccountToken := false

	podSpec := corev1.PodSpec{
		HostNetwork:      wg.Spec.HostNetwork,
		ImagePullSecrets: wg.Spec.ImagePullSecrets,
		NodeSelector:     wg.Spec.NodeSelector,
		Tolerations:      wg.Spec.Tolerations,
		SecurityContext: &corev1.PodSecurityContext{
			SeccompProfile: &corev1.SeccompProfile{
				Type: corev1.SeccompProfileType("RuntimeDefault"),
			},
		},
		AutomountServiceAccountToken: &automountServiceAccountToken,
		Volumes: []corev1.Volume{
			{
				Name: "socket",
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			},
			{
				Name: "config",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: SanitizeName(wg.Name, ""),
					},
				},
			},
		},
		InitContainers: []corev1.Container{},
		Containers: []corev1.Container{
			b.buildAgentContainer(wg, readOnlyRootFilesystem, allowPrivilegeEscalation, httpPort),
		},
	}

	// Add IP forwarding init container if requested
	if wg.Spec.EnableIpForwardOnPodInit {
		podSpec.InitContainers = append(podSpec.InitContainers, b.buildIPForwardInitContainer())
	}

	// Add wstunnel sidecar if tunnel is enabled
	if wg.Spec.Tunnel.Enabled {
		podSpec.Containers = append(
			podSpec.Containers,
			b.buildWstunnelContainer(wg, readOnlyRootFilesystem, allowPrivilegeEscalation),
		)
	}

	// Add userspace implementation flag if requested
	if wg.Spec.UseWgUserspaceImplementation {
		for i, c := range podSpec.Containers {
			if c.Name == "agent" {
				podSpec.Containers[i].Command = append(podSpec.Containers[i].Command, "--wg-use-userspace-implementation")
			}
		}
	}

	return podSpec
}

// BuildPodTemplateSpec creates the complete PodTemplateSpec used by both Deployment and DaemonSet.
func (b *PodBuilder) BuildPodTemplateSpec(wg *v1alpha1.Wireguard, labels map[string]string) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: labels,
			Annotations: map[string]string{
				"cluster-autoscaler.kubernetes.io/safe-to-evict": "true",
			},
		},
		Spec: b.BuildPodSpec(wg),
	}
}

// buildAgentContainer creates the agent container.
func (b *PodBuilder) buildAgentContainer(wg *v1alpha1.Wireguard, readOnlyRootFilesystem, allowPrivilegeEscalation bool, httpPort int) corev1.Container {
	return corev1.Container{
		SecurityContext: &corev1.SecurityContext{
			ReadOnlyRootFilesystem:   &readOnlyRootFilesystem,
			AllowPrivilegeEscalation: &allowPrivilegeEscalation,
			Capabilities:             &corev1.Capabilities{Add: []corev1.Capability{"NET_ADMIN"}},
		},
		Image:           b.agentImage,
		ImagePullPolicy: b.agentImagePullPolicy,
		Name:            "agent",
		Command: []string{
			"agent",
			"--v", "11",
			"--wg-iface", "wg0",
			"--wg-listen-port", fmt.Sprintf("%d", WireguardPort),
			"--state", "/tmp/wireguard/state.json",
			"--wg-userspace-implementation-fallback", "wireguard-go",
			"--http-port", fmt.Sprintf("%d", httpPort),
		},
		Ports: []corev1.ContainerPort{
			{
				ContainerPort: WireguardPort,
				Name:          "wireguard",
				Protocol:      corev1.ProtocolUDP,
			},
			{
				ContainerPort: int32(httpPort),
				Name:          "http",
				Protocol:      corev1.ProtocolTCP,
			},
			{
				ContainerPort: MetricsPort,
				Name:          "metrics",
				Protocol:      corev1.ProtocolTCP,
			},
		},
		EnvFrom: []corev1.EnvFromSource{
			{
				ConfigMapRef: &corev1.ConfigMapEnvSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: SanitizeName(wg.Name, "-config")},
				},
			},
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Port: intstr.FromInt(httpPort),
					Path: "/health",
				},
			},
		},
		LivenessProbe: &corev1.Probe{
			PeriodSeconds: 5,
			ProbeHandler: corev1.ProbeHandler{
				TCPSocket: &corev1.TCPSocketAction{
					Port: intstr.FromInt(httpPort),
				},
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "socket",
				MountPath: "/var/run/wireguard/",
			},
			{
				Name:      "config",
				MountPath: "/tmp/wireguard/",
			},
		},
		Resources: wg.Spec.Agent.Resources,
	}
}

// buildWstunnelContainer creates the wstunnel sidecar container for traffic obfuscation.
func (b *PodBuilder) buildWstunnelContainer(wg *v1alpha1.Wireguard, readOnlyRootFilesystem, allowPrivilegeEscalation bool) corev1.Container {
	image := wg.Spec.Tunnel.Image
	if image == "" {
		image = DefaultWstunnelImage
	}
	tunnelPort := wg.Spec.Tunnel.Port
	if tunnelPort == 0 {
		tunnelPort = DefaultTunnelPort
	}
	return corev1.Container{
		SecurityContext: &corev1.SecurityContext{
			ReadOnlyRootFilesystem:   &readOnlyRootFilesystem,
			AllowPrivilegeEscalation: &allowPrivilegeEscalation,
		},
		Image: image,
		Name:  "wstunnel",
		Command: []string{
			"/usr/bin/dumb-init", "--",
			"/home/app/wstunnel", "server",
			"--restrict-to", fmt.Sprintf("127.0.0.1:%d", WireguardPort),
			fmt.Sprintf("wss://0.0.0.0:%d", tunnelPort),
		},
		Ports: []corev1.ContainerPort{
			{
				ContainerPort: tunnelPort,
				Name:          "tunnel",
				Protocol:      corev1.ProtocolTCP,
			},
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				TCPSocket: &corev1.TCPSocketAction{
					Port: intstr.FromInt(int(tunnelPort)),
				},
			},
		},
		Resources: wg.Spec.Tunnel.Resources,
	}
}

// buildIPForwardInitContainer creates an init container to enable IP forwarding.
func (b *PodBuilder) buildIPForwardInitContainer() corev1.Container {
	privileged := true
	return corev1.Container{
		SecurityContext: &corev1.SecurityContext{
			Privileged: &privileged,
		},
		Image:           b.agentImage,
		ImagePullPolicy: b.agentImagePullPolicy,
		Name:            "sysctl",
		Command:         []string{"/bin/sh"},
		Args:            []string{"-c", "echo 1 > /proc/sys/net/ipv4/ip_forward"},
	}
}
