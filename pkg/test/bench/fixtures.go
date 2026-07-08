//go:build test_performance

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

package bench

import (
	"fmt"
	"math/rand"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/cloudprovider/fake"
	"sigs.k8s.io/karpenter/pkg/test"
)

// Rand is a deterministically-seeded random source for benchmark fixtures.
// All benchmark helpers use this by default so results are reproducible.
//
//nolint:gosec
var Rand = rand.New(rand.NewSource(42))

// RandomCPU returns a random CPU resource from a discrete set of common request sizes.
func RandomCPU() resource.Quantity {
	cpu := []int{100, 250, 500, 1000, 1500}
	return resource.MustParse(fmt.Sprintf("%dm", cpu[Rand.Intn(len(cpu))]))
}

// RandomMemory returns a random memory resource from a discrete set of common request sizes.
func RandomMemory() resource.Quantity {
	mem := []int{100, 256, 512, 1024, 2048, 4096}
	return resource.MustParse(fmt.Sprintf("%dMi", mem[Rand.Intn(len(mem))]))
}

// RandomLabels returns a label map with a single key "my-label" set to a random value.
func RandomLabels() map[string]string {
	return map[string]string{"my-label": randomLabelValue()}
}

// RandomAffinityLabels returns a label map with a single key "my-affininity"
// set to a random value, for use in pod-affinity fixtures.
func RandomAffinityLabels() map[string]string {
	return map[string]string{"my-affininity": randomLabelValue()}
}

func randomLabelValue() string {
	values := []string{"a", "b", "c", "d", "e", "f", "g"}
	return values[Rand.Intn(len(values))]
}

// MakeGenericPods creates pods with random resource requests and labels.
func MakeGenericPods(count int) []*corev1.Pod {
	pods := make([]*corev1.Pod, 0, count)
	for i := 0; i < count; i++ {
		pods = append(pods, test.Pod(test.PodOptions{
			ObjectMeta: metav1.ObjectMeta{
				Labels: RandomLabels(),
				UID:    uuid.NewUUID(),
			},
			ResourceRequirements: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    RandomCPU(),
					corev1.ResourceMemory: RandomMemory(),
				},
			},
		}))
	}
	return pods
}

// MakeTopologySpreadPods creates pods with a TopologySpreadConstraint on the given key.
func MakeTopologySpreadPods(count int, key string) []*corev1.Pod {
	pods := make([]*corev1.Pod, 0, count)
	for i := 0; i < count; i++ {
		pods = append(pods, test.Pod(test.PodOptions{
			ObjectMeta: metav1.ObjectMeta{
				Labels: RandomLabels(),
				UID:    uuid.NewUUID(),
			},
			TopologySpreadConstraints: []corev1.TopologySpreadConstraint{
				{
					MaxSkew:           1,
					TopologyKey:       key,
					WhenUnsatisfiable: corev1.DoNotSchedule,
					LabelSelector:     &metav1.LabelSelector{MatchLabels: RandomLabels()},
				},
			},
			ResourceRequirements: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    RandomCPU(),
					corev1.ResourceMemory: RandomMemory(),
				},
			},
		}))
	}
	return pods
}

// MakePodAffinityPods creates pods with self-affinity on the given topology key.
func MakePodAffinityPods(count int, key string) []*corev1.Pod {
	pods := make([]*corev1.Pod, 0, count)
	for i := 0; i < count; i++ {
		labels := RandomAffinityLabels()
		pods = append(pods, test.Pod(test.PodOptions{
			ObjectMeta: metav1.ObjectMeta{
				Labels: labels,
				UID:    uuid.NewUUID(),
			},
			PodRequirements: []corev1.PodAffinityTerm{
				{
					LabelSelector: &metav1.LabelSelector{MatchLabels: labels},
					TopologyKey:   key,
				},
			},
			ResourceRequirements: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    RandomCPU(),
					corev1.ResourceMemory: RandomMemory(),
				},
			},
		}))
	}
	return pods
}

// MakePodAntiAffinityPods creates pods with anti-affinity on the given topology key.
func MakePodAntiAffinityPods(count int, key string) []*corev1.Pod {
	pods := make([]*corev1.Pod, 0, count)
	labels := map[string]string{"app": "nginx"}
	for i := 0; i < count; i++ {
		pods = append(pods, test.Pod(test.PodOptions{
			ObjectMeta: metav1.ObjectMeta{
				Labels: labels,
				UID:    uuid.NewUUID(),
			},
			PodAntiRequirements: []corev1.PodAffinityTerm{
				{
					LabelSelector: &metav1.LabelSelector{MatchLabels: labels},
					TopologyKey:   key,
				},
			},
			ResourceRequirements: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    RandomCPU(),
					corev1.ResourceMemory: RandomMemory(),
				},
			},
		}))
	}
	return pods
}

// MakeDiversePods creates a mix of generic, topology-spread, affinity, and anti-affinity pods.
func MakeDiversePods(count int) []*corev1.Pod {
	numTypes := 5
	var pods []*corev1.Pod
	pods = append(pods, MakeGenericPods(count/numTypes)...)
	pods = append(pods, MakeTopologySpreadPods(count/numTypes, corev1.LabelTopologyZone)...)
	pods = append(pods, MakeTopologySpreadPods(count/numTypes, corev1.LabelHostname)...)
	pods = append(pods, MakePodAffinityPods(count/numTypes, corev1.LabelTopologyZone)...)
	pods = append(pods, MakePodAntiAffinityPods(count/numTypes, corev1.LabelHostname)...)
	// fill remainder with generic pods
	pods = append(pods, MakeGenericPods(count-len(pods))...)
	return pods
}

// NodePoolWithLimits creates a NodePool with effectively-unlimited resource limits for benchmarks.
func NodePoolWithLimits(opts ...v1.NodePool) *v1.NodePool {
	base := v1.NodePool{
		Spec: v1.NodePoolSpec{
			Limits: v1.Limits{
				corev1.ResourceCPU:    resource.MustParse("10000000"),
				corev1.ResourceMemory: resource.MustParse("10000000Gi"),
			},
		},
	}
	return test.NodePool(append([]v1.NodePool{base}, opts...)...)
}

// NewCloudProvider creates a fake CloudProvider with the given number of instance types.
func NewCloudProvider(instanceTypeCount int) (*fake.CloudProvider, []*cloudprovider.InstanceType) {
	cp := fake.NewCloudProvider()
	its := fake.InstanceTypes(instanceTypeCount)
	cp.InstanceTypes = its
	return cp, its
}

// NodePoolsWithInstances creates n NodePools with effectively-unlimited limits and
// returns both the NodePools and a map of nodePoolName -> instanceTypes for use in
// scheduler setup.
func NodePoolsWithInstances(n, instanceTypeCount int) ([]*v1.NodePool, map[string][]*cloudprovider.InstanceType) {
	_, its := NewCloudProvider(instanceTypeCount)
	nodePools := make([]*v1.NodePool, n)
	itMap := make(map[string][]*cloudprovider.InstanceType, n)
	for i := range nodePools {
		np := NodePoolWithLimits()
		nodePools[i] = np
		itMap[np.Name] = its
	}
	return nodePools, itMap
}
