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

package provisioning

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	"pgregory.net/rapid"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	"sigs.k8s.io/karpenter/pkg/operator/options"
	"sigs.k8s.io/karpenter/pkg/test"
)

// Feature: in-place-pod-vertical-scaling, Property 9: Provisioning patience defers then falls back
// Validates: Requirements 6.1, 6.2, 6.3
func TestProperty9_ProvisioningPatienceDefersAndFallsBack(t *testing.T) {
	t.Run("defers_when_patience_has_not_elapsed_and_pod_fits_at_steady_state", func(t *testing.T) {
		rapid.Check(t, func(rt *rapid.T) {
			// Generate random patience duration between 10s and 300s
			patienceSec := rapid.IntRange(10, 300).Draw(rt, "patienceSec")
			patienceDuration := time.Duration(patienceSec) * time.Second

			// Generate a pod age strictly less than patience duration
			podAgeSec := rapid.IntRange(0, patienceSec-1).Draw(rt, "podAgeSec")
			podAge := time.Duration(podAgeSec) * time.Second

			// Generate random steady-state values strictly less than spec requests
			specCPUMilli := rapid.IntRange(500, 8000).Draw(rt, "specCPUMilli")
			steadyStateCPUMilli := rapid.IntRange(100, specCPUMilli-1).Draw(rt, "steadyStateCPUMilli")
			specMemMi := rapid.IntRange(512, 16384).Draw(rt, "specMemMi")
			steadyStateMemMi := rapid.IntRange(64, specMemMi-1).Draw(rt, "steadyStateMemMi")

			// Node allocatable must be >= steady-state so pod fits at steady-state
			// Add some headroom to ensure fit
			nodeAllocCPUMilli := steadyStateCPUMilli + rapid.IntRange(100, 2000).Draw(rt, "nodeHeadroomCPU")
			nodeAllocMemMi := steadyStateMemMi + rapid.IntRange(64, 2000).Draw(rt, "nodeHeadroomMem")

			baseTime := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
			fakeClock := clocktesting.NewFakeClock(baseTime)

			// Create a state.Cluster with a node that has enough capacity for steady-state
			cluster, _ := buildClusterWithNode(fakeClock, nodeAllocCPUMilli, nodeAllocMemMi)

			podUID := types.UID(fmt.Sprintf("uid-prop9-defer-%d-%d", patienceSec, podAgeSec))
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "patience-pod",
					Namespace: "default",
					UID:       podUID,
					Annotations: map[string]string{
						karpv1.SteadyStateCPUAnnotationKey:    fmt.Sprintf("%dm", steadyStateCPUMilli),
						karpv1.SteadyStateMemoryAnnotationKey: fmt.Sprintf("%dMi", steadyStateMemMi),
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "main",
							Image: "test:latest",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%dm", specCPUMilli)),
									corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", specMemMi)),
								},
							},
						},
					},
				},
			}

			p := &Provisioner{
				clock:        fakeClock,
				podFirstSeen: make(map[types.UID]time.Time),
				cluster:      cluster,
			}

			// Pod was first seen (podAge) ago — patience hasn't expired
			p.podFirstSeen[podUID] = baseTime.Add(-podAge)

			ctx := options.ToContext(context.Background(), test.Options(test.OptionsFields{
				FeatureGates: test.FeatureGates{
					InPlacePodVerticalScaling: ptrBool(true),
				},
				IPVSPatienceDuration: &patienceDuration,
			}))

			ready, deferred := p.filterPatiencePods(ctx, []*corev1.Pod{pod})

			if len(deferred) != 1 {
				rt.Fatalf("expected 1 deferred pod, got %d (patienceDuration=%s, podAge=%s, "+
					"steadyStateCPU=%dm, steadyStateMem=%dMi, nodeAllocCPU=%dm, nodeAllocMem=%dMi)",
					len(deferred), patienceDuration, podAge,
					steadyStateCPUMilli, steadyStateMemMi, nodeAllocCPUMilli, nodeAllocMemMi)
			}
			if len(ready) != 0 {
				rt.Fatalf("expected 0 ready pods, got %d", len(ready))
			}
		})
	})

	t.Run("provisions_with_current_spec_requests_when_patience_expires", func(t *testing.T) {
		rapid.Check(t, func(rt *rapid.T) {
			// Generate random patience duration between 10s and 300s
			patienceSec := rapid.IntRange(10, 300).Draw(rt, "patienceSec")
			patienceDuration := time.Duration(patienceSec) * time.Second

			// Generate a pod age >= patience duration
			podAgeSec := rapid.IntRange(patienceSec, patienceSec+600).Draw(rt, "podAgeSec")
			podAge := time.Duration(podAgeSec) * time.Second

			// Generate random spec and steady-state values
			specCPUMilli := rapid.IntRange(500, 8000).Draw(rt, "specCPUMilli")
			steadyStateCPUMilli := rapid.IntRange(100, specCPUMilli-1).Draw(rt, "steadyStateCPUMilli")
			specMemMi := rapid.IntRange(512, 16384).Draw(rt, "specMemMi")
			steadyStateMemMi := rapid.IntRange(64, specMemMi-1).Draw(rt, "steadyStateMemMi")

			baseTime := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
			fakeClock := clocktesting.NewFakeClock(baseTime)

			// Use a cluster with a node (pod would fit at steady-state, but patience expired)
			cluster, _ := buildClusterWithNode(fakeClock, 16000, 32768)

			podUID := types.UID(fmt.Sprintf("uid-prop9-expire-%d-%d", patienceSec, podAgeSec))
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "patience-pod-expired",
					Namespace: "default",
					UID:       podUID,
					Annotations: map[string]string{
						karpv1.SteadyStateCPUAnnotationKey:    fmt.Sprintf("%dm", steadyStateCPUMilli),
						karpv1.SteadyStateMemoryAnnotationKey: fmt.Sprintf("%dMi", steadyStateMemMi),
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "main",
							Image: "test:latest",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%dm", specCPUMilli)),
									corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", specMemMi)),
								},
							},
						},
					},
				},
			}

			p := &Provisioner{
				clock:        fakeClock,
				podFirstSeen: make(map[types.UID]time.Time),
				cluster:      cluster,
			}

			// Pod was first seen (podAge) ago — patience has expired
			p.podFirstSeen[podUID] = baseTime.Add(-podAge)

			ctx := options.ToContext(context.Background(), test.Options(test.OptionsFields{
				FeatureGates: test.FeatureGates{
					InPlacePodVerticalScaling: ptrBool(true),
				},
				IPVSPatienceDuration: &patienceDuration,
			}))

			ready, deferred := p.filterPatiencePods(ctx, []*corev1.Pod{pod})

			if len(ready) != 1 {
				rt.Fatalf("expected 1 ready pod after patience expired, got %d (patienceDuration=%s, podAge=%s)",
					len(ready), patienceDuration, podAge)
			}
			if len(deferred) != 0 {
				rt.Fatalf("expected 0 deferred pods after patience expired, got %d", len(deferred))
			}
		})
	})

	t.Run("pods_without_steady_state_annotations_are_never_deferred", func(t *testing.T) {
		rapid.Check(t, func(rt *rapid.T) {
			// Generate random patience duration
			patienceSec := rapid.IntRange(10, 300).Draw(rt, "patienceSec")
			patienceDuration := time.Duration(patienceSec) * time.Second

			// Generate random spec requests
			specCPUMilli := rapid.IntRange(100, 8000).Draw(rt, "specCPUMilli")
			specMemMi := rapid.IntRange(64, 16384).Draw(rt, "specMemMi")

			baseTime := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
			fakeClock := clocktesting.NewFakeClock(baseTime)

			// Use a cluster with a large node (plenty of capacity)
			cluster, _ := buildClusterWithNode(fakeClock, 16000, 32768)

			podUID := types.UID(fmt.Sprintf("uid-prop9-noss-%d", patienceSec))
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "no-steadystate-pod",
					Namespace: "default",
					UID:       podUID,
					// No steady-state annotations
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "main",
							Image: "test:latest",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%dm", specCPUMilli)),
									corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", specMemMi)),
								},
							},
						},
					},
				},
			}

			p := &Provisioner{
				clock:        fakeClock,
				podFirstSeen: make(map[types.UID]time.Time),
				cluster:      cluster,
			}

			ctx := options.ToContext(context.Background(), test.Options(test.OptionsFields{
				FeatureGates: test.FeatureGates{
					InPlacePodVerticalScaling: ptrBool(true),
				},
				IPVSPatienceDuration: &patienceDuration,
			}))

			ready, deferred := p.filterPatiencePods(ctx, []*corev1.Pod{pod})

			if len(ready) != 1 {
				rt.Fatalf("expected 1 ready pod (no steady-state annotations), got %d", len(ready))
			}
			if len(deferred) != 0 {
				rt.Fatalf("expected 0 deferred pods (no steady-state annotations), got %d", len(deferred))
			}
		})
	})
}

// buildClusterWithNode creates a state.Cluster with a single unmanaged node
// that has the specified allocatable CPU (millicores) and memory (MiB).
// Unmanaged nodes (no NodeClaim) are always considered Initialized and not
// marked for deletion, making them eligible for podFitsAtSteadyState checks.
func buildClusterWithNode(clk *clocktesting.FakeClock, cpuMilli int, memMi int) (*state.Cluster, ctrlclient.Client) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = storagev1.AddToScheme(scheme)

	kubeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithIndex(&corev1.Pod{}, "spec.nodeName", func(o ctrlclient.Object) []string {
			pod := o.(*corev1.Pod)
			return []string{pod.Spec.NodeName}
		}).
		Build()

	cluster := state.NewCluster(clk, kubeClient, nil)

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-node",
			// No NodePoolLabelKey → unmanaged node → always Initialized
		},
		Spec: corev1.NodeSpec{
			ProviderID: "test-node", // Required for UpdateNode
		},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%dm", cpuMilli)),
				corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", memMi)),
				corev1.ResourcePods:   resource.MustParse("100"),
			},
		},
	}

	ctx := context.Background()
	_ = cluster.UpdateNode(ctx, node)

	return cluster, kubeClient
}

func ptrBool(b bool) *bool {
	return &b
}
