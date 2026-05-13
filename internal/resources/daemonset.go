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
	"github.com/nccloud/wireguard-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// DaemonSetBuilder builds daemonsets for wireguard resources.
type DaemonSetBuilder struct {
	scheme     *runtime.Scheme
	podBuilder *PodBuilder
}

// NewDaemonSetBuilder creates a new DaemonSetBuilder.
func NewDaemonSetBuilder(scheme *runtime.Scheme, agentImage string, agentImagePullPolicy corev1.PullPolicy) *DaemonSetBuilder {
	return &DaemonSetBuilder{
		scheme:     scheme,
		podBuilder: NewPodBuilder(agentImage, agentImagePullPolicy),
	}
}

// ForWireguard creates a daemonset for a Wireguard server.
func (b *DaemonSetBuilder) ForWireguard(wg *v1alpha1.Wireguard) (*appsv1.DaemonSet, error) {
	ls := LabelsForWireguard(wg.Name)

	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SanitizeName(wg.Name, "-ds"),
			Namespace: wg.Namespace,
			Labels:    ls,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: ls,
			},
			Template: b.podBuilder.BuildPodTemplateSpec(wg, ls),
		},
	}

	if err := SetOwnerReference(wg, ds, b.scheme); err != nil {
		return nil, err
	}

	return ds, nil
}
