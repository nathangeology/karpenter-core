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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/controllers/pod/deletioncost"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	"sigs.k8s.io/karpenter/pkg/test"
	. "sigs.k8s.io/karpenter/pkg/test/expectations"
)

var _ = Describe("Annotation", func() {
	var nodePool *v1.NodePool

	BeforeEach(func() {
		nodePool = test.NodePool()
	})

	Context("Sentinel annotation detection", func() {
		It("should add both deletion cost and sentinel annotations to unmanaged pods", func() {
			nodeClaims, nodes := test.NodeClaimsAndNodes(1, v1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{v1.NodePoolLabelKey: nodePool.Name}},
				Status:     v1.NodeClaimStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("8Gi")}},
			})
			ExpectApplied(ctx, env.Client, nodePool)
			for i := range nodeClaims {
				ExpectApplied(ctx, env.Client, nodeClaims[i], nodes[i])
			}
			pod := test.Pod(test.PodOptions{NodeName: nodes[0].Name})
			ExpectApplied(ctx, env.Client, pod)
			ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, nodeStateController, nodeClaimStateController, nodes, nodeClaims)

			var stateNodes []*state.StateNode
			for n := range cluster.Nodes() {
				stateNodes = append(stateNodes, n)
			}

			mgr := deletioncost.NewAnnotationManager(env.Client, recorder)
			nodeRanks := []deletioncost.NodeRank{{Node: stateNodes[0], Rank: -10, HasDoNotDisrupt: false}}
			mgr.UpdatePodDeletionCosts(ctx, nodeRanks)

			// Verify pod has both annotations
			updatedPod := &corev1.Pod{}
			Expect(env.Client.Get(ctx, client.ObjectKeyFromObject(pod), updatedPod)).To(Succeed())
			Expect(updatedPod.Annotations).To(HaveKeyWithValue(deletioncost.PodDeletionCostAnnotation, "-10"))
			Expect(updatedPod.Annotations).To(HaveKey(deletioncost.KarpenterManagedDeletionCostAnnotation))
		})

		It("should skip pods with customer-managed deletion cost (no sentinel)", func() {
			nodeClaims, nodes := test.NodeClaimsAndNodes(1, v1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{v1.NodePoolLabelKey: nodePool.Name}},
				Status:     v1.NodeClaimStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("8Gi")}},
			})
			ExpectApplied(ctx, env.Client, nodePool)
			for i := range nodeClaims {
				ExpectApplied(ctx, env.Client, nodeClaims[i], nodes[i])
			}
			// Pod with customer-set deletion cost but no sentinel
			pod := test.Pod(test.PodOptions{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						deletioncost.PodDeletionCostAnnotation: "100",
					},
				},
				NodeName: nodes[0].Name,
			})
			ExpectApplied(ctx, env.Client, pod)
			ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, nodeStateController, nodeClaimStateController, nodes, nodeClaims)

			var stateNodes []*state.StateNode
			for n := range cluster.Nodes() {
				stateNodes = append(stateNodes, n)
			}

			mgr := deletioncost.NewAnnotationManager(env.Client, recorder)
			nodeRanks := []deletioncost.NodeRank{{Node: stateNodes[0], Rank: -10, HasDoNotDisrupt: false}}
			mgr.UpdatePodDeletionCosts(ctx, nodeRanks)

			// Verify pod still has original customer value
			updatedPod := &corev1.Pod{}
			Expect(env.Client.Get(ctx, client.ObjectKeyFromObject(pod), updatedPod)).To(Succeed())
			Expect(updatedPod.Annotations[deletioncost.PodDeletionCostAnnotation]).To(Equal("100"))
			Expect(updatedPod.Annotations).ToNot(HaveKey(deletioncost.KarpenterManagedDeletionCostAnnotation))
		})

		It("should update pods with sentinel annotation (Karpenter-managed)", func() {
			nodeClaims, nodes := test.NodeClaimsAndNodes(1, v1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{v1.NodePoolLabelKey: nodePool.Name}},
				Status:     v1.NodeClaimStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("8Gi")}},
			})
			ExpectApplied(ctx, env.Client, nodePool)
			for i := range nodeClaims {
				ExpectApplied(ctx, env.Client, nodeClaims[i], nodes[i])
			}
			// Pod already managed by Karpenter (has both annotations)
			pod := test.Pod(test.PodOptions{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						deletioncost.PodDeletionCostAnnotation:              "-5",
						deletioncost.KarpenterManagedDeletionCostAnnotation: "true",
					},
				},
				NodeName: nodes[0].Name,
			})
			ExpectApplied(ctx, env.Client, pod)
			ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, nodeStateController, nodeClaimStateController, nodes, nodeClaims)

			var stateNodes []*state.StateNode
			for n := range cluster.Nodes() {
				stateNodes = append(stateNodes, n)
			}

			mgr := deletioncost.NewAnnotationManager(env.Client, recorder)
			nodeRanks := []deletioncost.NodeRank{{Node: stateNodes[0], Rank: -20, HasDoNotDisrupt: false}}
			mgr.UpdatePodDeletionCosts(ctx, nodeRanks)

			// Verify pod was updated to new rank
			updatedPod := &corev1.Pod{}
			Expect(env.Client.Get(ctx, client.ObjectKeyFromObject(pod), updatedPod)).To(Succeed())
			Expect(updatedPod.Annotations[deletioncost.PodDeletionCostAnnotation]).To(Equal("-20"))
			Expect(updatedPod.Annotations).To(HaveKey(deletioncost.KarpenterManagedDeletionCostAnnotation))
		})

		It("should handle pods without any annotations", func() {
			nodeClaims, nodes := test.NodeClaimsAndNodes(1, v1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{v1.NodePoolLabelKey: nodePool.Name}},
				Status:     v1.NodeClaimStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("8Gi")}},
			})
			ExpectApplied(ctx, env.Client, nodePool)
			for i := range nodeClaims {
				ExpectApplied(ctx, env.Client, nodeClaims[i], nodes[i])
			}
			pod := test.Pod(test.PodOptions{NodeName: nodes[0].Name})
			ExpectApplied(ctx, env.Client, pod)
			ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, nodeStateController, nodeClaimStateController, nodes, nodeClaims)

			var stateNodes []*state.StateNode
			for n := range cluster.Nodes() {
				stateNodes = append(stateNodes, n)
			}

			mgr := deletioncost.NewAnnotationManager(env.Client, recorder)
			nodeRanks := []deletioncost.NodeRank{{Node: stateNodes[0], Rank: -3, HasDoNotDisrupt: false}}
			mgr.UpdatePodDeletionCosts(ctx, nodeRanks)

			updatedPod := &corev1.Pod{}
			Expect(env.Client.Get(ctx, client.ObjectKeyFromObject(pod), updatedPod)).To(Succeed())
			Expect(updatedPod.Annotations[deletioncost.PodDeletionCostAnnotation]).To(Equal("-3"))
			Expect(updatedPod.Annotations[deletioncost.KarpenterManagedDeletionCostAnnotation]).To(Equal("true"))
		})

		It("should update multiple pods on the same node with the same rank", func() {
			nodeClaims, nodes := test.NodeClaimsAndNodes(1, v1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{v1.NodePoolLabelKey: nodePool.Name}},
				Status:     v1.NodeClaimStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("8Gi")}},
			})
			ExpectApplied(ctx, env.Client, nodePool)
			for i := range nodeClaims {
				ExpectApplied(ctx, env.Client, nodeClaims[i], nodes[i])
			}
			pods := make([]*corev1.Pod, 3)
			for i := range pods {
				pods[i] = test.Pod(test.PodOptions{NodeName: nodes[0].Name})
				ExpectApplied(ctx, env.Client, pods[i])
			}
			ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, nodeStateController, nodeClaimStateController, nodes, nodeClaims)

			var stateNodes []*state.StateNode
			for n := range cluster.Nodes() {
				stateNodes = append(stateNodes, n)
			}

			mgr := deletioncost.NewAnnotationManager(env.Client, recorder)
			nodeRanks := []deletioncost.NodeRank{{Node: stateNodes[0], Rank: -7, HasDoNotDisrupt: false}}
			mgr.UpdatePodDeletionCosts(ctx, nodeRanks)

			for _, pod := range pods {
				updatedPod := &corev1.Pod{}
				Expect(env.Client.Get(ctx, client.ObjectKeyFromObject(pod), updatedPod)).To(Succeed())
				Expect(updatedPod.Annotations[deletioncost.PodDeletionCostAnnotation]).To(Equal("-7"))
			}
		})

		It("should handle nodes with no pods", func() {
			nodeClaims, nodes := test.NodeClaimsAndNodes(1, v1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{v1.NodePoolLabelKey: nodePool.Name}},
				Status:     v1.NodeClaimStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("8Gi")}},
			})
			ExpectApplied(ctx, env.Client, nodePool)
			for i := range nodeClaims {
				ExpectApplied(ctx, env.Client, nodeClaims[i], nodes[i])
			}
			ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, nodeStateController, nodeClaimStateController, nodes, nodeClaims)

			var stateNodes []*state.StateNode
			for n := range cluster.Nodes() {
				stateNodes = append(stateNodes, n)
			}

			mgr := deletioncost.NewAnnotationManager(env.Client, recorder)
			nodeRanks := []deletioncost.NodeRank{{Node: stateNodes[0], Rank: -1, HasDoNotDisrupt: false}}
			mgr.UpdatePodDeletionCosts(ctx, nodeRanks)

			// An empty pod list is not a failure: nothing is annotated, so nothing is
			// evented. A conflict or update-failure event here would mean the empty
			// list was walked as if it held pods.
			Expect(recorder.Events()).To(BeEmpty())
		})

		It("should update pods across multiple ranked nodes", func() {
			nodeClaims, nodes := test.NodeClaimsAndNodes(2, v1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{v1.NodePoolLabelKey: nodePool.Name}},
				Status:     v1.NodeClaimStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("8Gi")}},
			})
			ExpectApplied(ctx, env.Client, nodePool)
			for i := range nodeClaims {
				ExpectApplied(ctx, env.Client, nodeClaims[i], nodes[i])
			}
			pod0 := test.Pod(test.PodOptions{NodeName: nodes[0].Name})
			pod1 := test.Pod(test.PodOptions{NodeName: nodes[1].Name})
			ExpectApplied(ctx, env.Client, pod0, pod1)
			ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, nodeStateController, nodeClaimStateController, nodes, nodeClaims)

			var stateNodes []*state.StateNode
			for n := range cluster.Nodes() {
				stateNodes = append(stateNodes, n)
			}
			Expect(stateNodes).To(HaveLen(2))

			mgr := deletioncost.NewAnnotationManager(env.Client, recorder)
			nodeRanks := []deletioncost.NodeRank{
				{Node: stateNodes[0], Rank: -10, HasDoNotDisrupt: false},
				{Node: stateNodes[1], Rank: -9, HasDoNotDisrupt: false},
			}
			mgr.UpdatePodDeletionCosts(ctx, nodeRanks)

			// Both pods should have their respective node's rank
			for _, sn := range stateNodes {
				var expectedRank string
				for _, nr := range nodeRanks {
					if nr.Node.Node.Name == sn.Node.Name {
						expectedRank = fmt.Sprintf("%d", nr.Rank)
					}
				}
				pods, err := sn.Pods(ctx, env.Client)
				Expect(err).ToNot(HaveOccurred())
				for _, p := range pods {
					updatedPod := &corev1.Pod{}
					Expect(env.Client.Get(ctx, client.ObjectKeyFromObject(p), updatedPod)).To(Succeed())
					Expect(updatedPod.Annotations[deletioncost.PodDeletionCostAnnotation]).To(Equal(expectedRank))
				}
			}
		})
	})

	Context("Third-party conflict detection", func() {
		It("should detect externally modified annotation and remove sentinel", func() {
			nodeClaims, nodes := test.NodeClaimsAndNodes(1, v1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{v1.NodePoolLabelKey: nodePool.Name}},
				Status:     v1.NodeClaimStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("8Gi")}},
			})
			ExpectApplied(ctx, env.Client, nodePool)
			for i := range nodeClaims {
				ExpectApplied(ctx, env.Client, nodeClaims[i], nodes[i])
			}
			pod := test.Pod(test.PodOptions{NodeName: nodes[0].Name})
			ExpectApplied(ctx, env.Client, pod)
			ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, nodeStateController, nodeClaimStateController, nodes, nodeClaims)

			var stateNodes []*state.StateNode
			for n := range cluster.Nodes() {
				stateNodes = append(stateNodes, n)
			}

			mgr := deletioncost.NewAnnotationManager(env.Client, recorder)
			nodeRanks := []deletioncost.NodeRank{{Node: stateNodes[0], Rank: -5, HasDoNotDisrupt: false}}

			// First update — Karpenter sets the annotation
			mgr.UpdatePodDeletionCosts(ctx, nodeRanks)
			updatedPod := &corev1.Pod{}
			Expect(env.Client.Get(ctx, client.ObjectKeyFromObject(pod), updatedPod)).To(Succeed())
			Expect(updatedPod.Annotations[deletioncost.PodDeletionCostAnnotation]).To(Equal("-5"))

			// Simulate third-party modifying the annotation
			updatedPod.Annotations[deletioncost.PodDeletionCostAnnotation] = "999"
			Expect(env.Client.Update(ctx, updatedPod)).To(Succeed())

			// Second update — should detect conflict, remove sentinel, skip pod
			mgr.UpdatePodDeletionCosts(ctx, nodeRanks)

			finalPod := &corev1.Pod{}
			Expect(env.Client.Get(ctx, client.ObjectKeyFromObject(pod), finalPod)).To(Succeed())
			// Sentinel should be removed
			Expect(finalPod.Annotations).ToNot(HaveKey(deletioncost.KarpenterManagedDeletionCostAnnotation))
			// Third-party value should be preserved
			Expect(finalPod.Annotations[deletioncost.PodDeletionCostAnnotation]).To(Equal("999"))
		})
	})

	Context("Metric result labels", func() {
		It("should count a customer-managed pod under skipped_customer_managed alone", func() {
			_, nodeRanks := rankedNodeWithPod(nodePool, -10, test.PodOptions{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{deletioncost.PodDeletionCostAnnotation: "100"},
				},
			})

			mgr := deletioncost.NewAnnotationManager(env.Client, recorder)
			before := snapshotPodResults()
			mgr.UpdatePodDeletionCosts(ctx, nodeRanks)

			Expect(podResultsSince(before)).To(Equal(podResultCounts{
				"success":                      0,
				"skipped_customer_managed":     1,
				"skipped_third_party_conflict": 0,
				"skipped_pod_deleted":          0,
				"error":                        0,
			}))
		})

		It("should count a third-party conflict apart from a customer-managed skip", func() {
			pod, nodeRanks := rankedNodeWithPod(nodePool, -5, test.PodOptions{})

			mgr := deletioncost.NewAnnotationManager(env.Client, recorder)
			// First pass makes Karpenter the manager, which is what arms conflict detection.
			mgr.UpdatePodDeletionCosts(ctx, nodeRanks)

			managedPod := &corev1.Pod{}
			Expect(env.Client.Get(ctx, client.ObjectKeyFromObject(pod), managedPod)).To(Succeed())
			managedPod.Annotations[deletioncost.PodDeletionCostAnnotation] = "999"
			Expect(env.Client.Update(ctx, managedPod)).To(Succeed())

			before := snapshotPodResults()
			mgr.UpdatePodDeletionCosts(ctx, nodeRanks)

			// A third party taking the annotation over is not a customer opt-out.
			Expect(podResultsSince(before)).To(Equal(podResultCounts{
				"success":                      0,
				"skipped_customer_managed":     0,
				"skipped_third_party_conflict": 1,
				"skipped_pod_deleted":          0,
				"error":                        0,
			}))
		})

		It("should count a released third-party pod as customer-managed on every later pass", func() {
			pod, nodeRanks := rankedNodeWithPod(nodePool, -5, test.PodOptions{})

			mgr := deletioncost.NewAnnotationManager(env.Client, recorder)
			mgr.UpdatePodDeletionCosts(ctx, nodeRanks)

			managedPod := &corev1.Pod{}
			Expect(env.Client.Get(ctx, client.ObjectKeyFromObject(pod), managedPod)).To(Succeed())
			managedPod.Annotations[deletioncost.PodDeletionCostAnnotation] = "999"
			Expect(env.Client.Update(ctx, managedPod)).To(Succeed())

			// Second pass releases management: the sentinel is deleted and the third party's cost
			// is left in place.
			mgr.UpdatePodDeletionCosts(ctx, nodeRanks)

			before := snapshotPodResults()
			mgr.UpdatePodDeletionCosts(ctx, nodeRanks)

			// From here on the pod is cost-without-sentinel, which shouldUpdatePod cannot tell
			// apart from a customer-set annotation. So skipped_third_party_conflict counts the
			// release once and skipped_customer_managed counts the pod on every cycle after. The
			// behavior is intended; the counter semantics are what this pins.
			Expect(podResultsSince(before)).To(Equal(podResultCounts{
				"success":                      0,
				"skipped_customer_managed":     1,
				"skipped_third_party_conflict": 0,
				"skipped_pod_deleted":          0,
				"error":                        0,
			}))
		})

		It("should count a pod deleted mid-cycle under skipped_pod_deleted, not customer-managed", func() {
			_, nodeRanks := rankedNodeWithPod(nodePool, -10, test.PodOptions{})

			// The pod is listed on the node but gone by the time its annotation is written. Not
			// reachable through the real client: deleting the pod also drops it from the list, so
			// processSinglePod would never run.
			gone := apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, "pod")
			mgr := deletioncost.NewAnnotationManager(failingClient{Client: env.Client, updateErr: gone}, recorder)
			before := snapshotPodResults()
			mgr.UpdatePodDeletionCosts(ctx, nodeRanks)

			Expect(podResultsSince(before)).To(Equal(podResultCounts{
				"success":                      0,
				"skipped_customer_managed":     0,
				"skipped_third_party_conflict": 0,
				"skipped_pod_deleted":          1,
				"error":                        0,
			}))
		})

		It("should count a node pod-list failure on the node counter and not as a pod error", func() {
			_, nodeRanks := rankedNodeWithPod(nodePool, -10, test.PodOptions{})

			listFailed := fmt.Errorf("pod list unavailable")
			mgr := deletioncost.NewAnnotationManager(failingClient{Client: env.Client, listErr: listFailed}, recorder)
			beforePods := snapshotPodResults()
			beforeNodes := nodesFailedTotal(reasonPodListFailed)
			mgr.UpdatePodDeletionCosts(ctx, nodeRanks)

			// One node, not one pod. The pods lost to this fault cannot be counted, because
			// listing them is what failed, so folding it into the pod counter under-reports the
			// blast radius by the node's pod count.
			Expect(nodesFailedTotal(reasonPodListFailed) - beforeNodes).To(BeNumerically("==", 1))
			Expect(podResultsSince(beforePods)).To(Equal(podResultCounts{
				"success":                      0,
				"skipped_customer_managed":     0,
				"skipped_third_party_conflict": 0,
				"skipped_pod_deleted":          0,
				"error":                        0,
			}))
		})
	})
})

const (
	podsUpdatedTotalName = "karpenter_pod_deletion_cost_pods_updated_total"
	nodesFailedTotalName = "karpenter_pod_deletion_cost_nodes_failed_total"
	reasonPodListFailed  = "pod_list_failed"
)

// podResultCounts holds one pods_updated_total value per result label. Asserting on the whole map
// rather than one label catches a count that moved between labels, which is the defect class these
// specs cover.
type podResultCounts map[string]float64

// snapshotPodResults reads every pods_updated_total series. The registry is process-global and is
// not reset between specs, so assertions are on the delta across one UpdatePodDeletionCosts call.
func snapshotPodResults() podResultCounts {
	GinkgoHelper()
	counts := podResultCounts{}
	for _, result := range []string{"success", "skipped_customer_managed", "skipped_third_party_conflict", "skipped_pod_deleted", "error"} {
		counts[result] = counterValue(podsUpdatedTotalName, map[string]string{"result": result})
	}
	return counts
}

// podResultsSince returns how much each pods_updated_total series moved since before.
func podResultsSince(before podResultCounts) podResultCounts {
	GinkgoHelper()
	delta := podResultCounts{}
	for result, after := range snapshotPodResults() {
		delta[result] = after - before[result]
	}
	return delta
}

func nodesFailedTotal(reason string) float64 {
	GinkgoHelper()
	return counterValue(nodesFailedTotalName, map[string]string{"reason": reason})
}

// counterValue reads one counter series, returning 0 when it has not been created yet.
func counterValue(name string, labels map[string]string) float64 {
	GinkgoHelper()
	metric, ok := FindMetricWithLabelValues(name, labels)
	if !ok {
		return 0
	}
	return metric.GetCounter().GetValue()
}

// rankedNodeWithPod applies the nodepool, one initialized node, and one pod scheduled to it,
// returning the pod and a single-entry rank list ready for UpdatePodDeletionCosts.
func rankedNodeWithPod(nodePool *v1.NodePool, rank int, podOpts test.PodOptions) (*corev1.Pod, []deletioncost.NodeRank) {
	GinkgoHelper()
	nodeClaims, nodes := test.NodeClaimsAndNodes(1, v1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{v1.NodePoolLabelKey: nodePool.Name}},
		Status:     v1.NodeClaimStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("8Gi")}},
	})
	ExpectApplied(ctx, env.Client, nodePool)
	for i := range nodeClaims {
		ExpectApplied(ctx, env.Client, nodeClaims[i], nodes[i])
	}
	podOpts.NodeName = nodes[0].Name
	pod := test.Pod(podOpts)
	ExpectApplied(ctx, env.Client, pod)
	ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, nodeStateController, nodeClaimStateController, nodes, nodeClaims)

	var stateNodes []*state.StateNode
	for n := range cluster.Nodes() {
		stateNodes = append(stateNodes, n)
	}
	Expect(stateNodes).To(HaveLen(1))
	return pod, []deletioncost.NodeRank{{Node: stateNodes[0], Rank: rank, HasDoNotDisrupt: false}}
}

// failingClient forces one operation to fail so the specs can reach the node-scoped pod-list
// failure and the pod-deleted-mid-cycle paths. Both are unreachable through the real client.
type failingClient struct {
	client.Client
	listErr   error
	updateErr error
}

func (c failingClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if c.listErr != nil {
		return c.listErr
	}
	return c.Client.List(ctx, list, opts...)
}

func (c failingClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if c.updateErr != nil {
		return c.updateErr
	}
	return c.Client.Update(ctx, obj, opts...)
}
