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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/controllers/pod/deletioncost"
	"sigs.k8s.io/karpenter/pkg/metrics"
	"sigs.k8s.io/karpenter/pkg/test"
	. "sigs.k8s.io/karpenter/pkg/test/expectations"
)

// crmetrics.Registry is process-global, so capture pre and assert on the delta.
func podAnnotationWritesDelta(labels map[string]string) float64 {
	GinkgoHelper()
	metric, ok := FindMetricWithLabelValues("karpenter_pod_deletion_cost_pod_annotation_writes_total", labels)
	if !ok || metric == nil {
		return 0
	}
	return lo.FromPtr(metric.Counter.Value)
}

// Reset+Set per cycle, so the value is absolute.
func nodesWithPendingAnnotationWritesGauge(labels map[string]string) float64 {
	GinkgoHelper()
	metric, ok := FindMetricWithLabelValues("karpenter_pod_deletion_cost_nodes_with_pending_annotation_writes", labels)
	Expect(ok).To(BeTrue(), "karpenter_pod_deletion_cost_nodes_with_pending_annotation_writes should be available")
	return lo.FromPtr(metric.Gauge.Value)
}

var _ = Describe("Metrics", func() {
	var nodePool *v1.NodePool

	BeforeEach(func() {
		nodePool = test.NodePool()
		nodePool.Spec.Disruption.ConsolidateAfter = v1.MustParseNillableDuration("0s")
		nodePool.Spec.Disruption.Budgets = []v1.Budget{{Nodes: "100%"}}
	})

	It("should set nodes_with_pending_annotation_writes to the number of nodes enqueued in the last reconcile", func() {
		nodeClaims, nodes := test.NodeClaimsAndNodes(3, v1.NodeClaim{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{v1.NodePoolLabelKey: nodePool.Name}},
			Status:     v1.NodeClaimStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("8Gi")}},
		})
		ExpectApplied(ctx, env.Client, nodePool)
		for i := range nodeClaims {
			ExpectApplied(ctx, env.Client, nodeClaims[i], nodes[i])
		}
		for i := range nodes {
			ExpectApplied(ctx, env.Client, rsOwnedPod(test.PodOptions{NodeName: nodes[i].Name}))
		}
		ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, env.Clock, nodeStateController, nodeClaimStateController, nodes, nodeClaims)

		controller := deletioncost.NewController(env.Clock, env.Client, cloudProvider, cluster, queue)
		_, err := controller.Reconcile(ctx)
		Expect(err).ToNot(HaveOccurred())

		Expect(nodesWithPendingAnnotationWritesGauge(map[string]string{metrics.NodePoolLabel: nodePool.Name})).To(Equal(3.0))
	})

	It("should clear nodes_with_pending_annotation_writes when a cycle finds the cluster drained to zero nodes", func() {
		nodeClaims, nodes := test.NodeClaimsAndNodes(3, v1.NodeClaim{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{v1.NodePoolLabelKey: nodePool.Name}},
			Status:     v1.NodeClaimStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("8Gi")}},
		})
		ExpectApplied(ctx, env.Client, nodePool)
		for i := range nodeClaims {
			ExpectApplied(ctx, env.Client, nodeClaims[i], nodes[i])
			ExpectApplied(ctx, env.Client, rsOwnedPod(test.PodOptions{NodeName: nodes[i].Name}))
		}
		ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, env.Clock, nodeStateController, nodeClaimStateController, nodes, nodeClaims)

		// One controller instance across both cycles, so lastConsolidationState survives.
		controller := deletioncost.NewController(env.Clock, env.Client, cloudProvider, cluster, queue)
		_, err := controller.Reconcile(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(nodesWithPendingAnnotationWritesGauge(map[string]string{metrics.NodePoolLabel: nodePool.Name})).To(Equal(3.0))

		// Step before the delete so the cleanup's MarkUnconsolidated lands on a later
		// stamp than the cursor cycle 1 stored. Keep it under 5 minutes:
		// ConsolidationState() re-stamps itself past that, which would advance the
		// cursor whether the drain did anything or not.
		cursor := cluster.ConsolidationState()
		env.Clock.Step(time.Minute)
		for i := range nodes {
			// Both halves: cleanupNodeClaim leaves the StateNode in the map with
			// NodeClaim = nil, and only the second delete drops the entry.
			ExpectDeleted(ctx, env.Client, nodeClaims[i], nodes[i])
			ExpectReconcileSucceeded(ctx, nodeClaimStateController, client.ObjectKeyFromObject(nodeClaims[i]))
			ExpectReconcileSucceeded(ctx, nodeStateController, client.ObjectKeyFromObject(nodes[i]))
		}

		// Preconditions. Without them a green run could be the unchanged-state
		// short-circuit, which skips the gauge write for a different reason and wears
		// the same symptom.
		Expect(cluster.Nodes()).To(BeEmpty(),
			"the drain must empty state, or Reconcile never reaches the zero-node return")
		Expect(cluster.ConsolidationState()).ToNot(Equal(cursor),
			"the drain must advance consolidation state, or the next cycle takes the unchanged short-circuit instead of the zero-node return")

		_, err = controller.Reconcile(ctx)
		Expect(err).ToNot(HaveOccurred())

		_, found := FindMetricWithLabelValues(
			"karpenter_pod_deletion_cost_nodes_with_pending_annotation_writes",
			map[string]string{metrics.NodePoolLabel: nodePool.Name},
		)
		Expect(found).To(BeFalse(),
			"a cycle that finds zero nodes must clear the gauge, or a drained nodepool reports pending annotation writes forever")
	})

	It("should increment pod_annotation_writes_total{result=updated} on a successful annotation write", func() {
		nodeClaims, nodes := test.NodeClaimsAndNodes(1, v1.NodeClaim{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{v1.NodePoolLabelKey: nodePool.Name}},
			Status:     v1.NodeClaimStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("8Gi")}},
		})
		ExpectApplied(ctx, env.Client, nodePool)
		for i := range nodeClaims {
			ExpectApplied(ctx, env.Client, nodeClaims[i], nodes[i])
		}
		pod := rsOwnedPod(test.PodOptions{NodeName: nodes[0].Name})
		ExpectApplied(ctx, env.Client, pod)
		ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, env.Clock, nodeStateController, nodeClaimStateController, nodes, nodeClaims)

		before := podAnnotationWritesDelta(map[string]string{deletioncost.Result.Name: deletioncost.ResultUpdated.Name})
		queue.Add(pod, -13, false)
		ExpectObjectReconciled(ctx, env.Client, queue, pod)
		after := podAnnotationWritesDelta(map[string]string{deletioncost.Result.Name: deletioncost.ResultUpdated.Name})
		Expect(after-before).To(Equal(1.0),
			"pod_annotation_writes_total{result=updated} should increment on a successful patch")
	})

	It("should increment pod_annotation_writes_total{result=error} when the patch surfaces a retryable error", func() {
		nodeClaims, nodes := test.NodeClaimsAndNodes(1, v1.NodeClaim{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{v1.NodePoolLabelKey: nodePool.Name}},
			Status:     v1.NodeClaimStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("8Gi")}},
		})
		ExpectApplied(ctx, env.Client, nodePool)
		for i := range nodeClaims {
			ExpectApplied(ctx, env.Client, nodeClaims[i], nodes[i])
		}
		pod := rsOwnedPod(test.PodOptions{NodeName: nodes[0].Name})
		ExpectApplied(ctx, env.Client, pod)
		ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, env.Clock, nodeStateController, nodeClaimStateController, nodes, nodeClaims)

		throttler := newThrottlingClient(env.Client, 1)
		q := deletioncost.NewQueue(throttler)
		before := podAnnotationWritesDelta(map[string]string{deletioncost.Result.Name: deletioncost.ResultError.Name})
		q.Add(pod, -7, false)
		_ = ExpectObjectReconcileFailed(ctx, env.Client, q, pod)
		after := podAnnotationWritesDelta(map[string]string{deletioncost.Result.Name: deletioncost.ResultError.Name})
		Expect(after-before).To(Equal(1.0),
			"pod_annotation_writes_total{result=error} should increment on a per-pod patch failure")
	})
})
