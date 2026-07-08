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
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakecr "sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/controllers/provisioning/scheduling"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	"sigs.k8s.io/karpenter/pkg/operator/injection"
	"sigs.k8s.io/karpenter/pkg/operator/options"
	"sigs.k8s.io/karpenter/pkg/test"
	"sigs.k8s.io/karpenter/pkg/test/bench"
)

// BenchmarkTopologyAffinitiesParallel measures lock contention on
// Cluster.ForPodsWithAntiAffinity when multiple goroutines concurrently
// construct topology objects (as happens during parallel scheduling).
// Sensitivity: #2114-class regressions degrade the parallel/single ratio
// by 4-10x while single-threaded performance stays flat.
func BenchmarkTopologyAffinitiesParallel(b *testing.B) {
	for _, parallelism := range []int{1, 5, 10} {
		b.Run(fmt.Sprintf("P%d", parallelism), func(b *testing.B) {
			benchmarkTopologyParallel(b, parallelism)
		})
	}
}

func benchmarkTopologyParallel(b *testing.B, parallelism int) {
	const (
		nodeCount           = 100
		podsPerNode         = 10
		antiAffinityPercent = 10 // 1 in 10 pods has anti-affinity
	)

	ctx := options.ToContext(injection.WithControllerName(context.Background(), "provisioner"), test.Options())
	fakeClient := fakecr.NewClientBuilder().WithIndex(
		&corev1.Pod{}, "spec.nodeName",
		func(o client.Object) []string { return []string{o.(*corev1.Pod).Spec.NodeName} },
	).Build()
	fakeCloudProvider, instanceTypes := bench.NewCloudProvider(20)
	clk := &clock.RealClock{}
	clusterState := state.NewCluster(clk, fakeClient, fakeCloudProvider)

	nodePool := bench.NodePoolWithLimits()

	// Populate cluster state with nodes and pods (some with anti-affinity).
	for i := 0; i < nodeCount; i++ {
		nodeName := fmt.Sprintf("node-%04d", i)
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: nodeName,
				Labels: map[string]string{
					corev1.LabelTopologyZone:       fmt.Sprintf("zone-%d", i%3),
					corev1.LabelHostname:           nodeName,
					corev1.LabelInstanceTypeStable: instanceTypes[i%len(instanceTypes)].Name,
				},
			},
			Spec: corev1.NodeSpec{
				ProviderID: fmt.Sprintf("fake://%s", nodeName),
			},
		}
		if err := clusterState.UpdateNode(ctx, node); err != nil {
			b.Fatalf("UpdateNode: %v", err)
		}

		for j := 0; j < podsPerNode; j++ {
			podOpts := test.PodOptions{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("pod-%04d-%02d", i, j),
					Namespace: "default",
					UID:       uuid.NewUUID(),
				},
				NodeName: nodeName,
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("128Mi"),
					},
				},
			}
			if j%antiAffinityPercent == 0 {
				podOpts.PodAntiRequirements = []corev1.PodAffinityTerm{
					{
						LabelSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"app": "bench"},
						},
						TopologyKey: corev1.LabelTopologyZone,
					},
				}
			}
			pod := test.Pod(podOpts)
			pod.Spec.NodeName = nodeName
			if err := clusterState.UpdatePod(ctx, pod); err != nil {
				b.Fatalf("UpdatePod: %v", err)
			}
		}
	}

	// Pods to schedule (without anti-affinity themselves, but they must
	// respect the inverse anti-affinity from existing pods).
	pendingPods := make([]*corev1.Pod, 10)
	for i := range pendingPods {
		pendingPods[i] = test.Pod(test.PodOptions{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("pending-%02d", i),
				Namespace: "default",
				UID:       uuid.NewUUID(),
			},
			ResourceRequirements: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("128Mi"),
				},
			},
		})
	}

	itMap := map[string][]*cloudprovider.InstanceType{
		nodePool.Name: instanceTypes,
	}

	b.SetParallelism(parallelism)
	b.ResetTimer()

	var ops atomic.Int64
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := scheduling.NewTopology(
				ctx,
				fakeClient,
				clusterState,
				nil,
				[]*v1.NodePool{nodePool},
				itMap,
				pendingPods,
			)
			if err != nil {
				b.Errorf("NewTopology: %v", err)
				return
			}
			ops.Add(1)
		}
	})

	b.ReportMetric(float64(ops.Load())/b.Elapsed().Seconds(), "topologies/sec")
	b.ReportMetric(float64(nodeCount*podsPerNode), "cluster-pods")
	b.ReportMetric(float64(nodeCount*podsPerNode/antiAffinityPercent), "anti-affinity-pods")
}

// BenchmarkTopologyAffinitiesContended measures topology construction
// performance when concurrent writers (UpdatePod) compete with readers
// (ForPodsWithAntiAffinity). This catches Lock vs RLock contention that
// the pure-parallel read benchmark above cannot surface.
func BenchmarkTopologyAffinitiesContended(b *testing.B) {
	const (
		nodeCount           = 100
		podsPerNode         = 10
		antiAffinityPercent = 10
	)

	ctx := options.ToContext(injection.WithControllerName(context.Background(), "provisioner"), test.Options())
	fakeClient := fakecr.NewClientBuilder().WithIndex(
		&corev1.Pod{}, "spec.nodeName",
		func(o client.Object) []string { return []string{o.(*corev1.Pod).Spec.NodeName} },
	).Build()
	fakeCloudProvider, instanceTypes := bench.NewCloudProvider(20)
	clk := &clock.RealClock{}
	clusterState := state.NewCluster(clk, fakeClient, fakeCloudProvider)

	nodePool := bench.NodePoolWithLimits()

	var allPods []*corev1.Pod
	for i := 0; i < nodeCount; i++ {
		nodeName := fmt.Sprintf("node-%04d", i)
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: nodeName,
				Labels: map[string]string{
					corev1.LabelTopologyZone:       fmt.Sprintf("zone-%d", i%3),
					corev1.LabelHostname:           nodeName,
					corev1.LabelInstanceTypeStable: instanceTypes[i%len(instanceTypes)].Name,
				},
			},
			Spec: corev1.NodeSpec{
				ProviderID: fmt.Sprintf("fake://%s", nodeName),
			},
		}
		if err := clusterState.UpdateNode(ctx, node); err != nil {
			b.Fatalf("UpdateNode: %v", err)
		}

		for j := 0; j < podsPerNode; j++ {
			podOpts := test.PodOptions{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("pod-%04d-%02d", i, j),
					Namespace: "default",
					UID:       uuid.NewUUID(),
				},
				NodeName: nodeName,
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("128Mi"),
					},
				},
			}
			if j%antiAffinityPercent == 0 {
				podOpts.PodAntiRequirements = []corev1.PodAffinityTerm{
					{
						LabelSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"app": "bench"},
						},
						TopologyKey: corev1.LabelTopologyZone,
					},
				}
			}
			pod := test.Pod(podOpts)
			pod.Spec.NodeName = nodeName
			if err := clusterState.UpdatePod(ctx, pod); err != nil {
				b.Fatalf("UpdatePod: %v", err)
			}
			allPods = append(allPods, pod)
		}
	}

	pendingPods := make([]*corev1.Pod, 10)
	for i := range pendingPods {
		pendingPods[i] = test.Pod(test.PodOptions{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("pending-%02d", i),
				Namespace: "default",
				UID:       uuid.NewUUID(),
			},
			ResourceRequirements: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("128Mi"),
				},
			},
		})
	}

	itMap := map[string][]*cloudprovider.InstanceType{
		nodePool.Name: instanceTypes,
	}

	// Writer goroutine: continuously updates pods to create Lock contention
	// against the RLock in ForPodsWithAntiAffinity.
	done := make(chan struct{})
	go func() {
		idx := 0
		for {
			select {
			case <-done:
				return
			default:
				pod := allPods[idx%len(allPods)]
				_ = clusterState.UpdatePod(ctx, pod)
				idx++
			}
		}
	}()

	b.SetParallelism(5)
	b.ResetTimer()

	var ops atomic.Int64
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := scheduling.NewTopology(
				ctx,
				fakeClient,
				clusterState,
				nil,
				[]*v1.NodePool{nodePool},
				itMap,
				pendingPods,
			)
			if err != nil {
				b.Errorf("NewTopology: %v", err)
				return
			}
			ops.Add(1)
		}
	})

	b.StopTimer()
	close(done)

	b.ReportMetric(float64(ops.Load())/b.Elapsed().Seconds(), "topologies/sec")
}
