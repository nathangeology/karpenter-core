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

package scheduling_test

import (
	"context"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakecr "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/cloudprovider/fake"
	"sigs.k8s.io/karpenter/pkg/controllers/provisioning/scheduling"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	"sigs.k8s.io/karpenter/pkg/events"
	"sigs.k8s.io/karpenter/pkg/operator/injection"
	"sigs.k8s.io/karpenter/pkg/operator/logging"
	"sigs.k8s.io/karpenter/pkg/operator/options"
	"sigs.k8s.io/karpenter/pkg/test"
)

func init() {
	log.SetLogger(logging.NopLogger)
}

// BenchmarkTopologyAffinitiesSerial measures NewTopology cost with anti-affinity
// pods in cluster state, single-threaded. This is the baseline for the parallel
// variant; the ratio serial/parallel exposes lock contention in ForPodsWithAntiAffinity.
func BenchmarkTopologyAffinitiesSerial(b *testing.B) {
	benchmarkTopologyAffinities(b, 0)
}

func BenchmarkTopologyAffinitiesParallel5(b *testing.B) {
	benchmarkTopologyAffinities(b, 5)
}

func BenchmarkTopologyAffinitiesParallel10(b *testing.B) {
	benchmarkTopologyAffinities(b, 10)
}

func benchmarkTopologyAffinities(b *testing.B, parallelism int) {
	ctx := options.ToContext(injection.WithControllerName(context.Background(), "provisioner"), test.Options())

	cp := fake.NewCloudProvider()
	instanceTypes := fake.InstanceTypes(100)
	cp.InstanceTypes = instanceTypes

	kubeClient := fakecr.NewClientBuilder().WithIndex(
		&corev1.Pod{}, "spec.nodeName",
		func(o client.Object) []string {
			return []string{o.(*corev1.Pod).Spec.NodeName}
		},
	).Build()
	clk := &clock.RealClock{}
	cl := state.NewCluster(clk, kubeClient, cp)

	nodePool := test.NodePool(v1.NodePool{
		Spec: v1.NodePoolSpec{
			Limits: v1.Limits{
				corev1.ResourceCPU:    resource.MustParse("1000000"),
				corev1.ResourceMemory: resource.MustParse("1000000Gi"),
			},
		},
	})

	// Populate cluster state: 100 nodes with 1000 pods, 100 of which have anti-affinity (10%).
	// Higher anti-affinity fraction amplifies RLock contention in ForPodsWithAntiAffinity.
	const nodeCount = 100
	const podCount = 1000
	const antiAffinityFraction = 0.10

	for i := 0; i < nodeCount; i++ {
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("node-%d", i),
				Labels: map[string]string{
					corev1.LabelTopologyZone:       fmt.Sprintf("zone-%d", i%3),
					corev1.LabelHostname:           fmt.Sprintf("node-%d", i),
					corev1.LabelInstanceTypeStable: "m5.xlarge",
					v1.NodePoolLabelKey:            nodePool.Name,
					v1.NodeInitializedLabelKey:     "true",
				},
			},
			Spec: corev1.NodeSpec{
				ProviderID: fmt.Sprintf("provider://node-%d", i),
			},
			Status: corev1.NodeStatus{
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("16"),
					corev1.ResourceMemory: resource.MustParse("64Gi"),
					corev1.ResourcePods:   resource.MustParse("110"),
				},
			},
		}
		if err := cl.UpdateNode(ctx, node); err != nil {
			b.Fatalf("updating node: %v", err)
		}
	}

	antiAffinityLabels := map[string]string{"app": "critical"}
	antiAffinityCount := int(float64(podCount) * antiAffinityFraction)

	for i := 0; i < podCount; i++ {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("pod-%d", i),
				Namespace: "default",
				UID:       types.UID(uuid.NewUUID()),
			},
			Spec: corev1.PodSpec{
				NodeName: fmt.Sprintf("node-%d", i%nodeCount),
				Containers: []corev1.Container{{
					Name:  "main",
					Image: "pause",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("128Mi"),
						},
					},
				}},
			},
		}
		if i < antiAffinityCount {
			pod.Labels = antiAffinityLabels
			pod.Spec.Affinity = &corev1.Affinity{
				PodAntiAffinity: &corev1.PodAntiAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
						{
							LabelSelector: &metav1.LabelSelector{MatchLabels: antiAffinityLabels},
							TopologyKey:   corev1.LabelHostname,
						},
					},
				},
			}
		}
		if err := cl.UpdatePod(ctx, pod); err != nil {
			b.Fatalf("updating pod: %v", err)
		}
	}

	// Pods to schedule: each has anti-affinity, forcing newForAffinities + updateInverseAffinities
	pendingPods := makeAntiAffinityPods(100, antiAffinityLabels)

	instanceTypeMap := map[string][]*cloudprovider.InstanceType{
		nodePool.Name: instanceTypes,
	}
	nodePools := []*v1.NodePool{nodePool}
	recorder := events.NewRecorder(&record.FakeRecorder{})

	if parallelism == 0 {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			topology, err := scheduling.NewTopology(ctx, kubeClient, cl, nil, nodePools, instanceTypeMap, pendingPods)
			if err != nil {
				b.Fatalf("creating topology: %v", err)
			}
			_ = scheduling.NewScheduler(ctx, kubeClient, nodePools, cl, nil, topology, instanceTypeMap, nil, recorder, clk, nil)
		}
	} else {
		b.SetParallelism(parallelism)
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				topology, err := scheduling.NewTopology(ctx, kubeClient, cl, nil, nodePools, instanceTypeMap, pendingPods)
				if err != nil {
					b.Fatalf("creating topology: %v", err)
				}
				_ = scheduling.NewScheduler(ctx, kubeClient, nodePools, cl, nil, topology, instanceTypeMap, nil, recorder, clk, nil)
			}
		})
	}
}

func makeAntiAffinityPods(count int, labels map[string]string) []*corev1.Pod {
	pods := make([]*corev1.Pod, count)
	for i := 0; i < count; i++ {
		pods[i] = test.Pod(test.PodOptions{
			ObjectMeta: metav1.ObjectMeta{
				Labels: labels,
				UID:    uuid.NewUUID(),
			},
			PodAntiRequirements: []corev1.PodAffinityTerm{
				{
					LabelSelector: &metav1.LabelSelector{MatchLabels: labels},
					TopologyKey:   corev1.LabelHostname,
				},
			},
			ResourceRequirements: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("128Mi"),
				},
			},
		})
	}
	return pods
}
