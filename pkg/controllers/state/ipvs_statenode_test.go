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

package state_test

import (
	"time"

	"github.com/samber/lo"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/operator/options"
	"sigs.k8s.io/karpenter/pkg/test"
	. "sigs.k8s.io/karpenter/pkg/test/expectations"
)

var _ = Describe("IPVS StateNode", func() {
	var node *corev1.Node

	BeforeEach(func() {
		node = test.Node(test.NodeOptions{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
				v1.NodePoolLabelKey:            nodePool.Name,
				corev1.LabelInstanceTypeStable: cloudProvider.InstanceTypes[0].Name,
			}},
			Allocatable: map[corev1.ResourceName]resource.Quantity{
				corev1.ResourceCPU:    resource.MustParse("8"),
				corev1.ResourceMemory: resource.MustParse("16Gi"),
			},
			ProviderID: test.RandomProviderID(),
		})
		ExpectApplied(ctx, env.Client, node)
		ExpectReconcileSucceeded(ctx, nodeController, client.ObjectKeyFromObject(node))
	})

	Context("updateForPod with IPVS gate enabled", func() {
		BeforeEach(func() {
			ctx = options.ToContext(ctx, test.Options(test.OptionsFields{
				FeatureGates: test.FeatureGates{
					InPlacePodVerticalScaling: lo.ToPtr(true),
				},
			}))
		})
		AfterEach(func() {
			ctx = options.ToContext(ctx, test.Options())
		})

		It("should use IPVSAwareRequestsForPod when gate is enabled", func() {
			// Create a pod with spec requests of 500m CPU and allocated resources of 1 CPU.
			// With IPVS enabled, the effective request should be max(500m, 1) = 1 CPU.
			pod := test.Pod(test.PodOptions{
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("256Mi"),
					},
				},
			})
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{
				{
					Name: pod.Spec.Containers[0].Name,
					AllocatedResources: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("256Mi"),
					},
				},
			}
			ExpectApplied(ctx, env.Client, pod)
			ExpectManualBinding(ctx, env.Client, pod, node)
			ExpectReconcileSucceeded(ctx, podController, client.ObjectKeyFromObject(pod))

			stateNode := ExpectStateNodeExists(cluster, node)
			podReqs := stateNode.PodRequests()
			// With IPVS gate enabled, effective CPU should be max(500m, 1) = 1
			Expect(podReqs.Cpu().Cmp(resource.MustParse("1"))).To(Equal(0))
		})

		It("should use peak annotations when they exceed spec and allocated resources", func() {
			pod := test.Pod(test.PodOptions{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						v1.PeakCPUAnnotationKey:    "2",
						v1.PeakMemoryAnnotationKey: "1Gi",
					},
				},
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("256Mi"),
					},
				},
			})
			ExpectApplied(ctx, env.Client, pod)
			ExpectManualBinding(ctx, env.Client, pod, node)
			ExpectReconcileSucceeded(ctx, podController, client.ObjectKeyFromObject(pod))

			stateNode := ExpectStateNodeExists(cluster, node)
			podReqs := stateNode.PodRequests()
			// Peak annotation of 2 CPU should be used since it exceeds spec 500m
			Expect(podReqs.Cpu().Cmp(resource.MustParse("2"))).To(Equal(0))
			// Peak annotation of 1Gi should be used since it exceeds spec 256Mi
			Expect(podReqs.Memory().Cmp(resource.MustParse("1Gi"))).To(Equal(0))
		})

		It("should include pod count of 1 in resource requests", func() {
			pod := test.Pod(test.PodOptions{
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("500m"),
					},
				},
			})
			ExpectApplied(ctx, env.Client, pod)
			ExpectManualBinding(ctx, env.Client, pod, node)
			ExpectReconcileSucceeded(ctx, podController, client.ObjectKeyFromObject(pod))

			stateNode := ExpectStateNodeExists(cluster, node)
			podReqs := stateNode.PodRequests()
			Expect(podReqs.Pods().Value()).To(Equal(int64(1)))
		})
	})

	Context("updateForPod with IPVS gate disabled", func() {
		BeforeEach(func() {
			ctx = options.ToContext(ctx, test.Options(test.OptionsFields{
				FeatureGates: test.FeatureGates{
					InPlacePodVerticalScaling: lo.ToPtr(false),
				},
			}))
		})
		AfterEach(func() {
			ctx = options.ToContext(ctx, test.Options())
		})

		It("should use RequestsForPods when gate is disabled", func() {
			// Create a pod with spec requests of 500m CPU and allocated resources of 1 CPU.
			// With IPVS disabled, the effective request should be spec-based only: 500m.
			pod := test.Pod(test.PodOptions{
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("256Mi"),
					},
				},
			})
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{
				{
					Name: pod.Spec.Containers[0].Name,
					AllocatedResources: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("512Mi"),
					},
				},
			}
			ExpectApplied(ctx, env.Client, pod)
			ExpectManualBinding(ctx, env.Client, pod, node)
			ExpectReconcileSucceeded(ctx, podController, client.ObjectKeyFromObject(pod))

			stateNode := ExpectStateNodeExists(cluster, node)
			podReqs := stateNode.PodRequests()
			// With IPVS gate disabled, effective CPU should be spec-based: 500m
			Expect(podReqs.Cpu().Cmp(resource.MustParse("500m"))).To(Equal(0))
			// Memory should also be spec-based: 256Mi
			Expect(podReqs.Memory().Cmp(resource.MustParse("256Mi"))).To(Equal(0))
		})

		It("should ignore peak annotations when gate is disabled", func() {
			pod := test.Pod(test.PodOptions{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						v1.PeakCPUAnnotationKey:    "4",
						v1.PeakMemoryAnnotationKey: "8Gi",
					},
				},
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("256Mi"),
					},
				},
			})
			ExpectApplied(ctx, env.Client, pod)
			ExpectManualBinding(ctx, env.Client, pod, node)
			ExpectReconcileSucceeded(ctx, podController, client.ObjectKeyFromObject(pod))

			stateNode := ExpectStateNodeExists(cluster, node)
			podReqs := stateNode.PodRequests()
			// Peak annotations should be ignored; effective CPU = spec 500m
			Expect(podReqs.Cpu().Cmp(resource.MustParse("500m"))).To(Equal(0))
			Expect(podReqs.Memory().Cmp(resource.MustParse("256Mi"))).To(Equal(0))
		})
	})

	Context("lastResizeCompletionTime tracking", func() {
		BeforeEach(func() {
			ctx = options.ToContext(ctx, test.Options(test.OptionsFields{
				FeatureGates: test.FeatureGates{
					InPlacePodVerticalScaling: lo.ToPtr(true),
				},
			}))
		})
		AfterEach(func() {
			ctx = options.ToContext(ctx, test.Options())
		})

		It("should update lastResizeCompletionTime when resize transitions from InProgress to empty", func() {
			pod := test.Pod(test.PodOptions{
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("500m"),
					},
				},
			})
			pod.Status.Resize = corev1.PodResizeStatusInProgress
			ExpectApplied(ctx, env.Client, pod)
			ExpectManualBinding(ctx, env.Client, pod, node)
			ExpectReconcileSucceeded(ctx, podController, client.ObjectKeyFromObject(pod))

			stateNode := ExpectStateNodeExists(cluster, node)
			// Initially, no resize has completed yet
			Expect(stateNode.LastResizeCompletionTime().IsZero()).To(BeTrue())

			// Now transition the pod's resize status from InProgress to empty (resize complete)
			pod.Status.Resize = ""
			ExpectApplied(ctx, env.Client, pod)
			ExpectReconcileSucceeded(ctx, podController, client.ObjectKeyFromObject(pod))

			stateNode = ExpectStateNodeExists(cluster, node)
			// lastResizeCompletionTime should now be set
			Expect(stateNode.LastResizeCompletionTime().IsZero()).To(BeFalse())
			Expect(time.Since(stateNode.LastResizeCompletionTime())).To(BeNumerically("<", 5*time.Second))
		})

		It("should not update lastResizeCompletionTime when resize status stays InProgress", func() {
			pod := test.Pod(test.PodOptions{
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("500m"),
					},
				},
			})
			pod.Status.Resize = corev1.PodResizeStatusInProgress
			ExpectApplied(ctx, env.Client, pod)
			ExpectManualBinding(ctx, env.Client, pod, node)
			ExpectReconcileSucceeded(ctx, podController, client.ObjectKeyFromObject(pod))

			// Reconcile again with the same InProgress status
			ExpectReconcileSucceeded(ctx, podController, client.ObjectKeyFromObject(pod))

			stateNode := ExpectStateNodeExists(cluster, node)
			// No transition happened, so lastResizeCompletionTime should still be zero
			Expect(stateNode.LastResizeCompletionTime().IsZero()).To(BeTrue())
		})

		It("should track the most recent completion time across multiple pod resizes", func() {
			pod1 := test.Pod(test.PodOptions{
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("500m"),
					},
				},
			})
			pod1.Status.Resize = corev1.PodResizeStatusInProgress
			ExpectApplied(ctx, env.Client, pod1)
			ExpectManualBinding(ctx, env.Client, pod1, node)
			ExpectReconcileSucceeded(ctx, podController, client.ObjectKeyFromObject(pod1))

			// Complete the first pod's resize
			pod1.Status.Resize = ""
			ExpectApplied(ctx, env.Client, pod1)
			ExpectReconcileSucceeded(ctx, podController, client.ObjectKeyFromObject(pod1))

			stateNode := ExpectStateNodeExists(cluster, node)
			firstCompletionTime := stateNode.LastResizeCompletionTime()
			Expect(firstCompletionTime.IsZero()).To(BeFalse())

			// Start and complete a second pod's resize
			pod2 := test.Pod(test.PodOptions{
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("1"),
					},
				},
			})
			pod2.Status.Resize = corev1.PodResizeStatusInProgress
			ExpectApplied(ctx, env.Client, pod2)
			ExpectManualBinding(ctx, env.Client, pod2, node)
			ExpectReconcileSucceeded(ctx, podController, client.ObjectKeyFromObject(pod2))

			// Complete the second pod's resize
			pod2.Status.Resize = ""
			ExpectApplied(ctx, env.Client, pod2)
			ExpectReconcileSucceeded(ctx, podController, client.ObjectKeyFromObject(pod2))

			stateNode = ExpectStateNodeExists(cluster, node)
			secondCompletionTime := stateNode.LastResizeCompletionTime()
			// The second completion time should be >= the first
			Expect(secondCompletionTime.After(firstCompletionTime) || secondCompletionTime.Equal(firstCompletionTime)).To(BeTrue())
		})

		It("should not update lastResizeCompletionTime when IPVS gate is disabled", func() {
			// Override context with gate disabled
			ctx = options.ToContext(ctx, test.Options(test.OptionsFields{
				FeatureGates: test.FeatureGates{
					InPlacePodVerticalScaling: lo.ToPtr(false),
				},
			}))

			pod := test.Pod(test.PodOptions{
				ResourceRequirements: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("500m"),
					},
				},
			})
			pod.Status.Resize = corev1.PodResizeStatusInProgress
			ExpectApplied(ctx, env.Client, pod)
			ExpectManualBinding(ctx, env.Client, pod, node)
			ExpectReconcileSucceeded(ctx, podController, client.ObjectKeyFromObject(pod))

			// Transition resize to empty
			pod.Status.Resize = ""
			ExpectApplied(ctx, env.Client, pod)
			ExpectReconcileSucceeded(ctx, podController, client.ObjectKeyFromObject(pod))

			stateNode := ExpectStateNodeExists(cluster, node)
			// With gate disabled, resize tracking should not happen
			Expect(stateNode.LastResizeCompletionTime().IsZero()).To(BeTrue())
		})
	})
})
