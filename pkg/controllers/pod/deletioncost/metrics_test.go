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

const nodesWithPendingAnnotationWritesName = "karpenter_pod_deletion_cost_nodes_with_pending_annotation_writes"

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
	metric, ok := FindMetricWithLabelValues(nodesWithPendingAnnotationWritesName, labels)
	Expect(ok).To(BeTrue(), "%s should be available", nodesWithPendingAnnotationWritesName)
	return lo.FromPtr(metric.Gauge.Value)
}

func hasNodesWithPendingAnnotationWritesGauge(labels map[string]string) bool {
	GinkgoHelper()
	_, ok := FindMetricWithLabelValues(nodesWithPendingAnnotationWritesName, labels)
	return ok
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

	It("should drop a nodepool's nodes_with_pending_annotation_writes series once that pool's count falls to zero", func() {
		// Two pools, because one pool cannot distinguish Reset-before-Set from Set
		// alone: the Set loop only visits pools present in the current cycle, so a
		// pool that drops out keeps its last value unless the gauge is Reset.
		otherNodePool := test.NodePool()
		otherNodePool.Spec.Disruption.ConsolidateAfter = v1.MustParseNillableDuration("0s")
		otherNodePool.Spec.Disruption.Budgets = []v1.Budget{{Nodes: "100%"}}

		allocatable := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("8Gi")}
		drainedClaims, drainedNodes := test.NodeClaimsAndNodes(2, v1.NodeClaim{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{v1.NodePoolLabelKey: nodePool.Name}},
			Status:     v1.NodeClaimStatus{Allocatable: allocatable},
		})
		survivingClaims, survivingNodes := test.NodeClaimsAndNodes(1, v1.NodeClaim{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{v1.NodePoolLabelKey: otherNodePool.Name}},
			Status:     v1.NodeClaimStatus{Allocatable: allocatable},
		})

		ExpectApplied(ctx, env.Client, nodePool, otherNodePool)
		for i := range drainedClaims {
			ExpectApplied(ctx, env.Client, drainedClaims[i], drainedNodes[i])
		}
		for i := range survivingClaims {
			ExpectApplied(ctx, env.Client, survivingClaims[i], survivingNodes[i])
		}
		for _, node := range append(append([]*corev1.Node{}, drainedNodes...), survivingNodes...) {
			ExpectApplied(ctx, env.Client, rsOwnedPod(test.PodOptions{NodeName: node.Name}))
		}
		ExpectMakeNodesAndNodeClaimsInitializedAndStateUpdated(ctx, env.Client, env.Clock, nodeStateController, nodeClaimStateController,
			append(append([]*corev1.Node{}, drainedNodes...), survivingNodes...),
			append(append([]*v1.NodeClaim{}, drainedClaims...), survivingClaims...))

		controller := deletioncost.NewController(env.Clock, env.Client, cloudProvider, cluster, queue)
		_, err := controller.Reconcile(ctx)
		Expect(err).ToNot(HaveOccurred())

		Expect(nodesWithPendingAnnotationWritesGauge(map[string]string{metrics.NodePoolLabel: nodePool.Name})).To(Equal(2.0))
		Expect(nodesWithPendingAnnotationWritesGauge(map[string]string{metrics.NodePoolLabel: otherNodePool.Name})).To(Equal(1.0))

		// Take the first pool's nodes out of cluster state so the next cycle ranks
		// nothing for it. Step the fake clock first: MarkUnconsolidated stamps
		// ConsolidationState with clock.Now(), so a state change at the same instant
		// leaves the controller's cursor equal and short-circuits the next cycle.
		// Keep the step under five minutes, past which ConsolidationState re-stamps
		// itself and the spec would pass with or without the Reset below.
		cursor := cluster.ConsolidationState()
		env.Clock.Step(time.Minute)
		for i := range drainedClaims {
			ExpectDeleted(ctx, env.Client, drainedClaims[i], drainedNodes[i])
			ExpectNodeClaimsCascadeDeletion(ctx, env.Client, drainedClaims[i])
			ExpectReconcileSucceeded(ctx, nodeStateController, client.ObjectKeyFromObject(drainedNodes[i]))
			ExpectReconcileSucceeded(ctx, nodeClaimStateController, client.ObjectKeyFromObject(drainedClaims[i]))
		}

		// Assert the preconditions rather than trusting the step: the unchanged-state
		// short-circuit and the empty-cluster return are indistinguishable from the
		// gauge alone, and both leave it untouched.
		remaining := 0
		for range cluster.Nodes() {
			remaining++
		}
		Expect(remaining).To(Equal(1), "only the surviving nodepool's node should be left in cluster state")
		Expect(cluster.ConsolidationState()).ToNot(Equal(cursor), "the drain must move the cursor or the next cycle short-circuits")

		_, err = controller.Reconcile(ctx)
		Expect(err).ToNot(HaveOccurred())

		Expect(hasNodesWithPendingAnnotationWritesGauge(map[string]string{metrics.NodePoolLabel: nodePool.Name})).To(BeFalse(),
			"a nodepool with no enqueued writes this cycle should not keep its previous series")
		Expect(nodesWithPendingAnnotationWritesGauge(map[string]string{metrics.NodePoolLabel: otherNodePool.Name})).To(Equal(1.0),
			"the surviving nodepool's series should still be set after the reset")
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
