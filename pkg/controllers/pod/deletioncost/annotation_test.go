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

package deletioncost_test

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/karpenter/pkg/controllers/pod/deletioncost"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	"sigs.k8s.io/karpenter/pkg/test"
	. "sigs.k8s.io/karpenter/pkg/utils/testing"
)

var _ = Describe("Pod Annotation Manager", func() {
	var ctx context.Context
	var env *Environment
	var annotationMgr *deletioncost.AnnotationManager

	BeforeEach(func() {
		ctx = TestContextWithLogger(GinkgoT())
		env = NewEnvironment(ctx, GinkgoT())
		annotationMgr = deletioncost.NewAnnotationManager(env.Client)
	})

	AfterEach(func() {
		env.Cleanup()
	})

	Context("Basic Annotation Updates", func() {
		It("should add deletion cost annotation to pods without existing annotation", func() {
			node := test.Node()
			env.ExpectCreated(node)

			pod := test.Pod(test.PodOptions{
				NodeName: node.Name,
			})
			env.ExpectCreated(pod)

			nodeRanks := []deletioncost.NodeRank{
				{
					Node: state.NewNode(node),
					Rank: 100,
				},
			}

			err := annotationMgr.UpdatePodDeletionCosts(ctx, nodeRanks)
			Expect(err).ToNot(HaveOccurred())

			// Verify pod has deletion cost annotation
			updatedPod := &corev1.Pod{}
			Expect(env.Client.Get(ctx, client.ObjectKeyFromObject(pod), updatedPod)).To(Succeed())
			Expect(updatedPod.Annotations).To(HaveKey(deletioncost.PodDeletionCostAnnotation))
			Expect(updatedPod.Annotations[deletioncost.PodDeletionCostAnnotation]).To(Equal("100"))

			// Verify management tracking annotation was added
			Expect(updatedPod.Annotations).To(HaveKey(deletioncost.KarpenterManagedDeletionCostKey))
			Expect(updatedPod.Annotations[deletioncost.KarpenterManagedDeletionCostKey]).To(Equal("true"))
		})

		It("should update deletion cost for Karpenter-managed pods", func() {
			node := test.Node()
			env.ExpectCreated(node)

			pod := test.Pod(test.PodOptions{
				NodeName: node.Name,
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						deletioncost.PodDeletionCostAnnotation:       "50",
						deletioncost.KarpenterManagedDeletionCostKey: "true",
					},
				},
			})
			env.ExpectCreated(pod)

			nodeRanks := []deletioncost.NodeRank{
				{
					Node: state.NewNode(node),
					Rank: 200,
				},
			}

			err := annotationMgr.UpdatePodDeletionCosts(ctx, nodeRanks)
			Expect(err).ToNot(HaveOccurred())

			// Verify pod deletion cost was updated
			updatedPod := &corev1.Pod{}
			Expect(env.Client.Get(ctx, client.ObjectKeyFromObject(pod), updatedPod)).To(Succeed())
			Expect(updatedPod.Annotations[deletioncost.PodDeletionCostAnnotation]).To(Equal("200"))
		})

		It("should handle multiple pods on same node", func() {
			node := test.Node()
			env.ExpectCreated(node)

			pods := []*corev1.Pod{
				test.Pod(test.PodOptions{NodeName: node.Name}),
				test.Pod(test.PodOptions{NodeName: node.Name}),
				test.Pod(test.PodOptions{NodeName: node.Name}),
			}
			for _, pod := range pods {
				env.ExpectCreated(pod)
			}

			nodeRanks := []deletioncost.NodeRank{
				{
					Node: state.NewNode(node),
					Rank: 150,
				},
			}

			err := annotationMgr.UpdatePodDeletionCosts(ctx, nodeRanks)
			Expect(err).ToNot(HaveOccurred())

			// Verify all pods have the same deletion cost
			for _, pod := range pods {
				updatedPod := &corev1.Pod{}
				Expect(env.Client.Get(ctx, client.ObjectKeyFromObject(pod), updatedPod)).To(Succeed())
				Expect(updatedPod.Annotations[deletioncost.PodDeletionCostAnnotation]).To(Equal("150"))
				Expect(updatedPod.Annotations[deletioncost.KarpenterManagedDeletionCostKey]).To(Equal("true"))
			}
		})
	})

	Context("Customer-Managed Annotation Protection", func() {
		It("should NOT override customer-set deletion cost without management annotation", func() {
			node := test.Node()
			env.ExpectCreated(node)

			pod := test.Pod(test.PodOptions{
				NodeName: node.Name,
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						deletioncost.PodDeletionCostAnnotation: "999", // Customer set
						// No KarpenterManagedDeletionCostKey
					},
				},
			})
			env.ExpectCreated(pod)

			nodeRanks := []deletioncost.NodeRank{
				{
					Node: state.NewNode(node),
					Rank: 100,
				},
			}

			err := annotationMgr.UpdatePodDeletionCosts(ctx, nodeRanks)
			Expect(err).ToNot(HaveOccurred())

			// Verify pod deletion cost was NOT changed
			updatedPod := &corev1.Pod{}
			Expect(env.Client.Get(ctx, client.ObjectKeyFromObject(pod), updatedPod)).To(Succeed())
			Expect(updatedPod.Annotations[deletioncost.PodDeletionCostAnnotation]).To(Equal("999"))

			// Verify management annotation was NOT added
			Expect(updatedPod.Annotations).ToNot(HaveKey(deletioncost.KarpenterManagedDeletionCostKey))
		})

		It("should respect customer annotation even with negative values", func() {
			node := test.Node()
			env.ExpectCreated(node)

			pod := test.Pod(test.PodOptions{
				NodeName: node.Name,
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						deletioncost.PodDeletionCostAnnotation: "-500",
					},
				},
			})
			env.ExpectCreated(pod)

			nodeRanks := []deletioncost.NodeRank{
				{
					Node: state.NewNode(node),
					Rank: 100,
				},
			}

			err := annotationMgr.UpdatePodDeletionCosts(ctx, nodeRanks)
			Expect(err).ToNot(HaveOccurred())

			updatedPod := &corev1.Pod{}
			Expect(env.Client.Get(ctx, client.ObjectKeyFromObject(pod), updatedPod)).To(Succeed())
			Expect(updatedPod.Annotations[deletioncost.PodDeletionCostAnnotation]).To(Equal("-500"))
		})
	})

	Context("Multiple Nodes with Different Ranks", func() {
		It("should assign different deletion costs based on node ranks", func() {
			node1 := test.Node(test.NodeOptions{
				ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
			})
			node2 := test.Node(test.NodeOptions{
				ObjectMeta: metav1.ObjectMeta{Name: "node-2"},
			})
			node3 := test.Node(test.NodeOptions{
				ObjectMeta: metav1.ObjectMeta{Name: "node-3"},
			})
			env.ExpectCreated(node1, node2, node3)

			pod1 := test.Pod(test.PodOptions{NodeName: node1.Name})
			pod2 := test.Pod(test.PodOptions{NodeName: node2.Name})
			pod3 := test.Pod(test.PodOptions{NodeName: node3.Name})
			env.ExpectCreated(pod1, pod2, pod3)

			nodeRanks := []deletioncost.NodeRank{
				{Node: state.NewNode(node1), Rank: -100},
				{Node: state.NewNode(node2), Rank: 0},
				{Node: state.NewNode(node3), Rank: 100},
			}

			err := annotationMgr.UpdatePodDeletionCosts(ctx, nodeRanks)
			Expect(err).ToNot(HaveOccurred())

			// Verify each pod has correct deletion cost
			updatedPod1 := &corev1.Pod{}
			Expect(env.Client.Get(ctx, client.ObjectKeyFromObject(pod1), updatedPod1)).To(Succeed())
			Expect(updatedPod1.Annotations[deletioncost.PodDeletionCostAnnotation]).To(Equal("-100"))

			updatedPod2 := &corev1.Pod{}
			Expect(env.Client.Get(ctx, client.ObjectKeyFromObject(pod2), updatedPod2)).To(Succeed())
			Expect(updatedPod2.Annotations[deletioncost.PodDeletionCostAnnotation]).To(Equal("0"))

			updatedPod3 := &corev1.Pod{}
			Expect(env.Client.Get(ctx, client.ObjectKeyFromObject(pod3), updatedPod3)).To(Succeed())
			Expect(updatedPod3.Annotations[deletioncost.PodDeletionCostAnnotation]).To(Equal("100"))
		})
	})

	Context("Error Handling", func() {
		It("should continue processing other pods if one pod fails", func() {
			node := test.Node()
			env.ExpectCreated(node)

			pod1 := test.Pod(test.PodOptions{NodeName: node.Name})
			pod2 := test.Pod(test.PodOptions{NodeName: node.Name})
			env.ExpectCreated(pod1, pod2)

			// Delete pod1 to simulate it being removed during update
			env.ExpectDeleted(pod1)

			nodeRanks := []deletioncost.NodeRank{
				{
					Node: state.NewNode(node),
					Rank: 100,
				},
			}

			// Should not error even though pod1 is gone
			err := annotationMgr.UpdatePodDeletionCosts(ctx, nodeRanks)
			Expect(err).ToNot(HaveOccurred())

			// pod2 should still be updated
			updatedPod2 := &corev1.Pod{}
			Expect(env.Client.Get(ctx, client.ObjectKeyFromObject(pod2), updatedPod2)).To(Succeed())
			Expect(updatedPod2.Annotations[deletioncost.PodDeletionCostAnnotation]).To(Equal("100"))
		})

		It("should handle nodes with no pods", func() {
			node := test.Node()
			env.ExpectCreated(node)

			nodeRanks := []deletioncost.NodeRank{
				{
					Node: state.NewNode(node),
					Rank: 100,
				},
			}

			err := annotationMgr.UpdatePodDeletionCosts(ctx, nodeRanks)
			Expect(err).ToNot(HaveOccurred())
		})

		It("should handle empty node ranks list", func() {
			nodeRanks := []deletioncost.NodeRank{}

			err := annotationMgr.UpdatePodDeletionCosts(ctx, nodeRanks)
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Context("Annotation Format", func() {
		It("should format deletion cost as string integer", func() {
			node := test.Node()
			env.ExpectCreated(node)

			pod := test.Pod(test.PodOptions{NodeName: node.Name})
			env.ExpectCreated(pod)

			nodeRanks := []deletioncost.NodeRank{
				{Node: state.NewNode(node), Rank: 12345},
			}

			err := annotationMgr.UpdatePodDeletionCosts(ctx, nodeRanks)
			Expect(err).ToNot(HaveOccurred())

			updatedPod := &corev1.Pod{}
			Expect(env.Client.Get(ctx, client.ObjectKeyFromObject(pod), updatedPod)).To(Succeed())
			Expect(updatedPod.Annotations[deletioncost.PodDeletionCostAnnotation]).To(Equal("12345"))
		})

		It("should handle negative deletion costs", func() {
			node := test.Node()
			env.ExpectCreated(node)

			pod := test.Pod(test.PodOptions{NodeName: node.Name})
			env.ExpectCreated(pod)

			nodeRanks := []deletioncost.NodeRank{
				{Node: state.NewNode(node), Rank: -500},
			}

			err := annotationMgr.UpdatePodDeletionCosts(ctx, nodeRanks)
			Expect(err).ToNot(HaveOccurred())

			updatedPod := &corev1.Pod{}
			Expect(env.Client.Get(ctx, client.ObjectKeyFromObject(pod), updatedPod)).To(Succeed())
			Expect(updatedPod.Annotations[deletioncost.PodDeletionCostAnnotation]).To(Equal("-500"))
		})
	})

	Context("Mixed Scenarios", func() {
		It("should handle mix of new, Karpenter-managed, and customer-managed pods", func() {
			node := test.Node()
			env.ExpectCreated(node)

			// New pod (no annotations)
			newPod := test.Pod(test.PodOptions{
				NodeName: node.Name,
				ObjectMeta: metav1.ObjectMeta{
					Name: "new-pod",
				},
			})

			// Karpenter-managed pod
			karpenterPod := test.Pod(test.PodOptions{
				NodeName: node.Name,
				ObjectMeta: metav1.ObjectMeta{
					Name: "karpenter-pod",
					Annotations: map[string]string{
						deletioncost.PodDeletionCostAnnotation:       "50",
						deletioncost.KarpenterManagedDeletionCostKey: "true",
					},
				},
			})

			// Customer-managed pod
			customerPod := test.Pod(test.PodOptions{
				NodeName: node.Name,
				ObjectMeta: metav1.ObjectMeta{
					Name: "customer-pod",
					Annotations: map[string]string{
						deletioncost.PodDeletionCostAnnotation: "999",
					},
				},
			})

			env.ExpectCreated(newPod, karpenterPod, customerPod)

			nodeRanks := []deletioncost.NodeRank{
				{Node: state.NewNode(node), Rank: 200},
			}

			err := annotationMgr.UpdatePodDeletionCosts(ctx, nodeRanks)
			Expect(err).ToNot(HaveOccurred())

			// New pod should get annotation
			updatedNewPod := &corev1.Pod{}
			Expect(env.Client.Get(ctx, client.ObjectKey{Name: "new-pod", Namespace: newPod.Namespace}, updatedNewPod)).To(Succeed())
			Expect(updatedNewPod.Annotations[deletioncost.PodDeletionCostAnnotation]).To(Equal("200"))
			Expect(updatedNewPod.Annotations[deletioncost.KarpenterManagedDeletionCostKey]).To(Equal("true"))

			// Karpenter-managed pod should be updated
			updatedKarpenterPod := &corev1.Pod{}
			Expect(env.Client.Get(ctx, client.ObjectKey{Name: "karpenter-pod", Namespace: karpenterPod.Namespace}, updatedKarpenterPod)).To(Succeed())
			Expect(updatedKarpenterPod.Annotations[deletioncost.PodDeletionCostAnnotation]).To(Equal("200"))

			// Customer-managed pod should NOT be changed
			updatedCustomerPod := &corev1.Pod{}
			Expect(env.Client.Get(ctx, client.ObjectKey{Name: "customer-pod", Namespace: customerPod.Namespace}, updatedCustomerPod)).To(Succeed())
			Expect(updatedCustomerPod.Annotations[deletioncost.PodDeletionCostAnnotation]).To(Equal("999"))
			Expect(updatedCustomerPod.Annotations).ToNot(HaveKey(deletioncost.KarpenterManagedDeletionCostKey))
		})
	})

	Context("Performance", func() {
		It("should handle large number of pods efficiently", func() {
			node := test.Node()
			env.ExpectCreated(node)

			// Create 100 pods
			pods := make([]*corev1.Pod, 100)
			for i := 0; i < 100; i++ {
				pods[i] = test.Pod(test.PodOptions{
					NodeName: node.Name,
					ObjectMeta: metav1.ObjectMeta{
						Name: fmt.Sprintf("pod-%d", i),
					},
				})
				env.ExpectCreated(pods[i])
			}

			nodeRanks := []deletioncost.NodeRank{
				{Node: state.NewNode(node), Rank: 100},
			}

			err := annotationMgr.UpdatePodDeletionCosts(ctx, nodeRanks)
			Expect(err).ToNot(HaveOccurred())

			// Spot check a few pods
			for i := 0; i < 10; i++ {
				updatedPod := &corev1.Pod{}
				Expect(env.Client.Get(ctx, client.ObjectKey{Name: fmt.Sprintf("pod-%d", i*10), Namespace: pods[0].Namespace}, updatedPod)).To(Succeed())
				Expect(updatedPod.Annotations[deletioncost.PodDeletionCostAnnotation]).To(Equal("100"))
			}
		})
	})
})
