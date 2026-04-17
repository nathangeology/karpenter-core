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

package provisioning_test

import (
	"time"

	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/operator/options"
	"sigs.k8s.io/karpenter/pkg/test"
	. "sigs.k8s.io/karpenter/pkg/test/expectations"
)

// Provisioner-level IPVS awareness tests
// These verify the end-to-end wiring from provisioner through scheduler to NodeClaim resource requests.
// The provisioner delegates to the scheduler's updateCachedPodData which uses IPVSAwareRequestsForPod
// when the IPVS gate is enabled. These tests confirm that wiring at the provisioner level.
// Validates: Requirements 4.1, 4.2, 4.3

var _ = Describe("IPVS Provisioning", func() {
	Context("Peak Envelope for Instance Type Selection", func() {
		It("should use peak annotation values for NodeClaim resource requests when IPVS gate is enabled", func() {
			// Requirements 4.1: Use peak envelope for instance type selection
			ctx = options.ToContext(ctx, test.Options(test.OptionsFields{
				FeatureGates: test.FeatureGates{
					InPlacePodVerticalScaling: lo.ToPtr(true),
				},
			}))
			ExpectApplied(ctx, env.Client, test.NodePool())
			pod := test.UnschedulablePod(test.PodOptions{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						v1.PeakCPUAnnotationKey:    "2",
						v1.PeakMemoryAnnotationKey: "4Gi",
					},
				},
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
				},
			})
			ExpectProvisioned(ctx, env.Client, cluster, cloudProvider, prov, pod)
			Expect(cloudProvider.CreateCalls).To(HaveLen(1))
			// NodeClaim should use peak values (2 CPU, 4Gi memory), not spec values (500m, 1Gi)
			ExpectNodeClaimRequests(cloudProvider.CreateCalls[0], corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("4Gi"),
			})
		})

		It("should sum peak envelopes when multiple annotated pods are on the same NodeClaim", func() {
			// Requirements 4.3: Sum peak envelopes for multiple pods on same NodeClaim
			ctx = options.ToContext(ctx, test.Options(test.OptionsFields{
				FeatureGates: test.FeatureGates{
					InPlacePodVerticalScaling: lo.ToPtr(true),
				},
			}))
			ExpectApplied(ctx, env.Client, test.NodePool())
			podA := test.UnschedulablePod(test.PodOptions{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						v1.PeakCPUAnnotationKey:    "2",
						v1.PeakMemoryAnnotationKey: "2Gi",
					},
				},
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("512Mi"),
					},
				},
			})
			podB := test.UnschedulablePod(test.PodOptions{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						v1.PeakCPUAnnotationKey:    "1",
						v1.PeakMemoryAnnotationKey: "1Gi",
					},
				},
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("250m"),
						corev1.ResourceMemory: resource.MustParse("256Mi"),
					},
				},
			})
			ExpectProvisioned(ctx, env.Client, cluster, cloudProvider, prov, podA, podB)
			Expect(cloudProvider.CreateCalls).To(HaveLen(1))
			// NodeClaim should sum peak envelopes: 2+1=3 CPU, 2Gi+1Gi=3Gi memory
			ExpectNodeClaimRequests(cloudProvider.CreateCalls[0], corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("3"),
				corev1.ResourceMemory: resource.MustParse("3Gi"),
			})
		})

		It("should use spec requests for unannotated pods when IPVS gate is enabled", func() {
			// Requirements 4.2: Unannotated pods use spec requests
			ctx = options.ToContext(ctx, test.Options(test.OptionsFields{
				FeatureGates: test.FeatureGates{
					InPlacePodVerticalScaling: lo.ToPtr(true),
				},
			}))
			ExpectApplied(ctx, env.Client, test.NodePool())
			pod := test.UnschedulablePod(test.PodOptions{
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
			})
			ExpectProvisioned(ctx, env.Client, cluster, cloudProvider, prov, pod)
			Expect(cloudProvider.CreateCalls).To(HaveLen(1))
			// No peak annotations, so spec requests should be used
			ExpectNodeClaimRequests(cloudProvider.CreateCalls[0], corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("2Gi"),
			})
		})

		It("should use spec requests when IPVS gate is disabled even with peak annotations", func() {
			// Requirements 4.2, 5.2: Gate disabled preserves current behavior
			ctx = options.ToContext(ctx, test.Options(test.OptionsFields{
				FeatureGates: test.FeatureGates{
					InPlacePodVerticalScaling: lo.ToPtr(false),
				},
			}))
			ExpectApplied(ctx, env.Client, test.NodePool())
			pod := test.UnschedulablePod(test.PodOptions{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						v1.PeakCPUAnnotationKey:    "4",
						v1.PeakMemoryAnnotationKey: "8Gi",
					},
				},
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
				},
			})
			ExpectProvisioned(ctx, env.Client, cluster, cloudProvider, prov, pod)
			Expect(cloudProvider.CreateCalls).To(HaveLen(1))
			// Gate disabled: should use spec requests (500m, 1Gi), ignoring peak annotations
			ExpectNodeClaimRequests(cloudProvider.CreateCalls[0], corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			})
		})
	})
	// Steady-State Scheduling Evaluation Tests
	// These verify that the provisioner evaluates existing node capacity using steady-state
	// values during the patience window, and provisions immediately if the pod doesn't fit
	// at steady-state on any existing node.
	// Validates: Requirements 6.1, 6.2, 6.3
	Context("Steady-State Scheduling Evaluation", func() {
		It("should defer provisioning when pod fits at steady-state on existing node", func() {
			// Requirements 6.1, 6.2: Pod with steady-state annotations that fits on existing node is deferred
			ctx = options.ToContext(ctx, test.Options(test.OptionsFields{
				FeatureGates: test.FeatureGates{
					InPlacePodVerticalScaling: lo.ToPtr(true),
				},
			}))
			nodePool := test.NodePool()
			// Create an existing node with 4 CPU and 8Gi memory available
			node := test.Node(test.NodeOptions{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						v1.NodePoolLabelKey:            nodePool.Name,
						corev1.LabelInstanceTypeStable: "default-instance-type",
					},
					Finalizers: []string{v1.TerminationFinalizer},
				},
				ProviderID: test.RandomProviderID(),
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("4"),
					corev1.ResourceMemory: resource.MustParse("8Gi"),
					corev1.ResourcePods:   resource.MustParse("100"),
				},
			})
			ExpectApplied(ctx, env.Client, nodePool, node)
			ExpectReconcileSucceeded(ctx, nodeController, client.ObjectKeyFromObject(node))

			// Pod requests 2 CPU but steady-state is 500m — fits on existing node at steady-state
			pod := test.UnschedulablePod(test.PodOptions{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						v1.SteadyStateCPUAnnotationKey:    "500m",
						v1.SteadyStateMemoryAnnotationKey: "512Mi",
					},
				},
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
			})
			// Pod should be deferred (fits at steady-state), so no new NodeClaim is created
			ExpectApplied(ctx, env.Client, pod)
			results, err := prov.Schedule(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(results.NewNodeClaims).To(HaveLen(0))
		})

		It("should provision immediately when pod does not fit at steady-state on any existing node", func() {
			// Requirements 6.1, 6.2: Pod that doesn't fit even at steady-state is provisioned immediately
			ctx = options.ToContext(ctx, test.Options(test.OptionsFields{
				FeatureGates: test.FeatureGates{
					InPlacePodVerticalScaling: lo.ToPtr(true),
				},
			}))
			nodePool := test.NodePool()
			// Create an existing node with very little available capacity
			node := test.Node(test.NodeOptions{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						v1.NodePoolLabelKey:            nodePool.Name,
						corev1.LabelInstanceTypeStable: "default-instance-type",
					},
					Finalizers: []string{v1.TerminationFinalizer},
				},
				ProviderID: test.RandomProviderID(),
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("128Mi"),
					corev1.ResourcePods:   resource.MustParse("100"),
				},
			})
			ExpectApplied(ctx, env.Client, nodePool, node)
			ExpectReconcileSucceeded(ctx, nodeController, client.ObjectKeyFromObject(node))

			// Pod requests 2 CPU, steady-state is 1 CPU — still doesn't fit on the tiny node
			pod := test.UnschedulablePod(test.PodOptions{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						v1.SteadyStateCPUAnnotationKey:    "1",
						v1.SteadyStateMemoryAnnotationKey: "512Mi",
					},
				},
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
			})
			// Pod doesn't fit at steady-state, so it should be provisioned immediately
			ExpectProvisioned(ctx, env.Client, cluster, cloudProvider, prov, pod)
			Expect(cloudProvider.CreateCalls).To(HaveLen(1))
		})

		It("should provision with current spec requests after patience expires", func() {
			// Requirements 6.3: After patience expires, provision with current (higher) spec requests
			ctx = options.ToContext(ctx, test.Options(test.OptionsFields{
				FeatureGates: test.FeatureGates{
					InPlacePodVerticalScaling: lo.ToPtr(true),
				},
				IPVSPatienceDuration: lo.ToPtr(30 * time.Second),
			}))
			nodePool := test.NodePool()
			// Create an existing node with enough capacity for steady-state but not for spec requests
			node := test.Node(test.NodeOptions{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						v1.NodePoolLabelKey:            nodePool.Name,
						corev1.LabelInstanceTypeStable: "default-instance-type",
					},
					Finalizers: []string{v1.TerminationFinalizer},
				},
				ProviderID: test.RandomProviderID(),
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("4"),
					corev1.ResourceMemory: resource.MustParse("8Gi"),
					corev1.ResourcePods:   resource.MustParse("100"),
				},
			})
			ExpectApplied(ctx, env.Client, nodePool, node)
			ExpectReconcileSucceeded(ctx, nodeController, client.ObjectKeyFromObject(node))

			pod := test.UnschedulablePod(test.PodOptions{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						v1.SteadyStateCPUAnnotationKey:    "500m",
						v1.SteadyStateMemoryAnnotationKey: "512Mi",
					},
				},
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
			})

			// First call: pod fits at steady-state, should be deferred
			ExpectApplied(ctx, env.Client, pod)
			results, err := prov.Schedule(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(results.NewNodeClaims).To(HaveLen(0))

			// Advance clock past patience duration
			fakeClock.Step(31 * time.Second)

			// Second call: patience expired, should provision with current spec requests
			ExpectProvisioned(ctx, env.Client, cluster, cloudProvider, prov, pod)
			Expect(cloudProvider.CreateCalls).To(HaveLen(1))
		})

		It("should not defer pods without steady-state annotations", func() {
			// Pods without steady-state annotations should always be provisioned immediately
			ctx = options.ToContext(ctx, test.Options(test.OptionsFields{
				FeatureGates: test.FeatureGates{
					InPlacePodVerticalScaling: lo.ToPtr(true),
				},
			}))
			ExpectApplied(ctx, env.Client, test.NodePool())
			pod := test.UnschedulablePod(test.PodOptions{
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
				},
			})
			ExpectProvisioned(ctx, env.Client, cluster, cloudProvider, prov, pod)
			Expect(cloudProvider.CreateCalls).To(HaveLen(1))
		})

		It("should not defer when IPVS gate is disabled even with steady-state annotations", func() {
			// Gate disabled: steady-state annotations are ignored
			ctx = options.ToContext(ctx, test.Options(test.OptionsFields{
				FeatureGates: test.FeatureGates{
					InPlacePodVerticalScaling: lo.ToPtr(false),
				},
			}))
			ExpectApplied(ctx, env.Client, test.NodePool())
			pod := test.UnschedulablePod(test.PodOptions{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						v1.SteadyStateCPUAnnotationKey:    "500m",
						v1.SteadyStateMemoryAnnotationKey: "512Mi",
					},
				},
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
			})
			ExpectProvisioned(ctx, env.Client, cluster, cloudProvider, prov, pod)
			Expect(cloudProvider.CreateCalls).To(HaveLen(1))
		})

		It("should provision immediately when no existing nodes are available", func() {
			// No existing nodes: pod can't fit anywhere at steady-state, provision immediately
			ctx = options.ToContext(ctx, test.Options(test.OptionsFields{
				FeatureGates: test.FeatureGates{
					InPlacePodVerticalScaling: lo.ToPtr(true),
				},
			}))
			ExpectApplied(ctx, env.Client, test.NodePool())
			pod := test.UnschedulablePod(test.PodOptions{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						v1.SteadyStateCPUAnnotationKey:    "500m",
						v1.SteadyStateMemoryAnnotationKey: "512Mi",
					},
				},
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
			})
			ExpectProvisioned(ctx, env.Client, cluster, cloudProvider, prov, pod)
			Expect(cloudProvider.CreateCalls).To(HaveLen(1))
		})
	})
})
