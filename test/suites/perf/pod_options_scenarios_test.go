package perf_test

import (
	"fmt"
	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/karpenter/pkg/test"
)

func MakeFixedResourceTopologySpreadPodOptions(key string, cpu int, memory int) test.PodOptions {
	return test.PodOptions{
		ObjectMeta: metav1.ObjectMeta{Labels: lo.Assign(test.RandomLabels(), map[string]string{test.DiscoveryLabel: "owned"})},
		TopologySpreadConstraints: []v1.TopologySpreadConstraint{
			{
				MaxSkew:           1,
				TopologyKey:       key,
				WhenUnsatisfiable: v1.DoNotSchedule,
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: test.RandomLabels(),
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

func MakeFixedResourceNoConstraintsPodOptions(cpu int, memory int) test.PodOptions {
	return test.PodOptions{
		ObjectMeta: metav1.ObjectMeta{Labels: lo.Assign(test.RandomLabels(), map[string]string{test.DiscoveryLabel: "owned"})},
		ResourceRequirements: v1.ResourceRequirements{
			Requests: v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%dm", cpu)),
				v1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", memory)),
			},
		}}
}
func simpleStdScenarioInstanceSpreadPodOptions(cpu int, memory int) []test.PodOptions {
	var pods []test.PodOptions
	pods = append(pods, MakeFixedResourceTopologySpreadPodOptions(v1.LabelHostname, cpu, memory))
	pods = append(pods, MakeFixedResourceTopologySpreadPodOptions(v1.LabelHostname, cpu, memory))
	pods = append(pods, MakeFixedResourceNoConstraintsPodOptions(cpu, memory))
	return []test.PodOptions{}
}
