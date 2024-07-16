/*
Copyright The Kubernetes Authors.

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

package perf_test

import (
	"fmt"

	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/karpenter/pkg/test"
)

func MakeFixedResourceTopologySpreadPodOptions(key string, cpu int, memory int, deployment_label string) test.PodOptions {
	deploy_labels := map[string]string{
		"my-label": deployment_label,
	}
	return test.PodOptions{
		ObjectMeta: metav1.ObjectMeta{Labels: lo.Assign(deploy_labels, map[string]string{test.DiscoveryLabel: "owned"})},
		TopologySpreadConstraints: []v1.TopologySpreadConstraint{
			{
				MaxSkew:           1,
				TopologyKey:       key,
				WhenUnsatisfiable: v1.DoNotSchedule,
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: deploy_labels,
				},
			},
		},
		ResourceRequirements: v1.ResourceRequirements{
			Requests: v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%dm", cpu)),
				v1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", memory)),
			},
		}}
}

func MakeFixedResourceNoConstraintsPodOptions(cpu int, memory int, deployment_label string) test.PodOptions {
	deploy_labels := map[string]string{
		"my-label": deployment_label,
	}
	return test.PodOptions{
		ObjectMeta: metav1.ObjectMeta{Labels: lo.Assign(deploy_labels, map[string]string{test.DiscoveryLabel: "owned"})},
		ResourceRequirements: v1.ResourceRequirements{
			Requests: v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%dm", cpu)),
				v1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", memory)),
			},
		}}
}
func simpleStdScenarioInstanceSpreadPodOptions(cpu int, memory int) []test.PodOptions {
	var pods []test.PodOptions
	pods = append(pods, MakeFixedResourceTopologySpreadPodOptions(v1.LabelHostname, cpu, memory, "A"))
	pods = append(pods, MakeFixedResourceTopologySpreadPodOptions(v1.LabelHostname, cpu, memory, "B"))
	pods = append(pods, MakeFixedResourceNoConstraintsPodOptions(cpu, memory, "C"))
	return pods
}
