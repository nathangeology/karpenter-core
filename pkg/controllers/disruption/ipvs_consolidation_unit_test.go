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

package disruption

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clocktesting "k8s.io/utils/clock/testing"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	"sigs.k8s.io/karpenter/pkg/operator/options"
	"sigs.k8s.io/karpenter/pkg/test"
)

// Requirements: 3.1, 3.3, 3.4, 5.2

// --- hasActiveResize tests ---

func TestHasActiveResize_InProgress(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{Resize: corev1.PodResizeStatusInProgress},
	}
	if !hasActiveResize(pod) {
		t.Fatal("expected hasActiveResize=true for InProgress")
	}
}

func TestHasActiveResize_Proposed(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{Resize: corev1.PodResizeStatus("Proposed")},
	}
	if !hasActiveResize(pod) {
		t.Fatal("expected hasActiveResize=true for Proposed")
	}
}

func TestHasActiveResize_Infeasible(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{Resize: corev1.PodResizeStatusInfeasible},
	}
	if hasActiveResize(pod) {
		t.Fatal("expected hasActiveResize=false for Infeasible")
	}
}

func TestHasActiveResize_Empty(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{Resize: corev1.PodResizeStatus("")},
	}
	if hasActiveResize(pod) {
		t.Fatal("expected hasActiveResize=false for empty resize status")
	}
}

func TestHasActiveResize_Deferred(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{Resize: corev1.PodResizeStatus("Deferred")},
	}
	if hasActiveResize(pod) {
		t.Fatal("expected hasActiveResize=false for Deferred")
	}
}

// --- Grace period default tests ---

func TestGracePeriodDefault_IsUsedWhenNodePoolDoesNotConfigure(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	fakeClock := clocktesting.NewFakeClock(now)

	// Resize completed 3 minutes ago — within the default 5m grace period
	stateNode := state.NewNode()
	stateNode.Node = &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n"}}
	stateNode.NodeClaim = &v1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: "nc"}}
	stateNode.SetLastResizeCompletionTime(now.Add(-3 * time.Minute))

	nodePool := &v1.NodePool{
		ObjectMeta: metav1.ObjectMeta{Name: "np"},
		Spec: v1.NodePoolSpec{
			Disruption: v1.Disruption{
				// No IPVSConsolidationGracePeriod set
			},
		},
	}

	candidate := &Candidate{StateNode: stateNode, NodePool: nodePool}
	c := &consolidation{clock: fakeClock}

	if !c.isInResizeGracePeriod(candidate) {
		t.Fatal("expected isInResizeGracePeriod=true with default 5m when elapsed=3m")
	}
}

func TestGracePeriodDefault_ExpiredAfter5Minutes(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	fakeClock := clocktesting.NewFakeClock(now)

	// Resize completed 6 minutes ago — past the default 5m grace period
	stateNode := state.NewNode()
	stateNode.Node = &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n"}}
	stateNode.NodeClaim = &v1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: "nc"}}
	stateNode.SetLastResizeCompletionTime(now.Add(-6 * time.Minute))

	nodePool := &v1.NodePool{
		ObjectMeta: metav1.ObjectMeta{Name: "np"},
		Spec: v1.NodePoolSpec{
			Disruption: v1.Disruption{},
		},
	}

	candidate := &Candidate{StateNode: stateNode, NodePool: nodePool}
	c := &consolidation{clock: fakeClock}

	if c.isInResizeGracePeriod(candidate) {
		t.Fatal("expected isInResizeGracePeriod=false when elapsed=6m > default 5m")
	}
}

func TestGracePeriodDefault_ExactlyAtBoundary(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	fakeClock := clocktesting.NewFakeClock(now)

	// Resize completed exactly 5 minutes ago — at the boundary
	stateNode := state.NewNode()
	stateNode.Node = &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n"}}
	stateNode.NodeClaim = &v1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: "nc"}}
	stateNode.SetLastResizeCompletionTime(now.Add(-5 * time.Minute))

	nodePool := &v1.NodePool{
		ObjectMeta: metav1.ObjectMeta{Name: "np"},
		Spec: v1.NodePoolSpec{
			Disruption: v1.Disruption{},
		},
	}

	candidate := &Candidate{StateNode: stateNode, NodePool: nodePool}
	c := &consolidation{clock: fakeClock}

	// elapsed == gracePeriod → not strictly less than, so grace period is over
	if c.isInResizeGracePeriod(candidate) {
		t.Fatal("expected isInResizeGracePeriod=false when elapsed == 5m (boundary)")
	}
}

// --- Grace period with custom NodePool configuration ---

func TestGracePeriodCustom_ShorterPeriod(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	fakeClock := clocktesting.NewFakeClock(now)

	stateNode := state.NewNode()
	stateNode.Node = &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n"}}
	stateNode.NodeClaim = &v1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: "nc"}}
	stateNode.SetLastResizeCompletionTime(now.Add(-90 * time.Second))

	// Custom grace period of 2 minutes — elapsed 90s is within it
	nodePool := &v1.NodePool{
		ObjectMeta: metav1.ObjectMeta{Name: "np"},
		Spec: v1.NodePoolSpec{
			Disruption: v1.Disruption{
				IPVSConsolidationGracePeriod: &metav1.Duration{Duration: 2 * time.Minute},
			},
		},
	}

	candidate := &Candidate{StateNode: stateNode, NodePool: nodePool}
	c := &consolidation{clock: fakeClock}

	if !c.isInResizeGracePeriod(candidate) {
		t.Fatal("expected isInResizeGracePeriod=true with custom 2m grace period when elapsed=90s")
	}
}

func TestGracePeriodCustom_LongerPeriod(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	fakeClock := clocktesting.NewFakeClock(now)

	stateNode := state.NewNode()
	stateNode.Node = &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n"}}
	stateNode.NodeClaim = &v1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: "nc"}}
	stateNode.SetLastResizeCompletionTime(now.Add(-8 * time.Minute))

	// Custom grace period of 10 minutes — elapsed 8m is within it
	nodePool := &v1.NodePool{
		ObjectMeta: metav1.ObjectMeta{Name: "np"},
		Spec: v1.NodePoolSpec{
			Disruption: v1.Disruption{
				IPVSConsolidationGracePeriod: &metav1.Duration{Duration: 10 * time.Minute},
			},
		},
	}

	candidate := &Candidate{StateNode: stateNode, NodePool: nodePool}
	c := &consolidation{clock: fakeClock}

	if !c.isInResizeGracePeriod(candidate) {
		t.Fatal("expected isInResizeGracePeriod=true with custom 10m grace period when elapsed=8m")
	}
}

func TestGracePeriodCustom_Expired(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	fakeClock := clocktesting.NewFakeClock(now)

	stateNode := state.NewNode()
	stateNode.Node = &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n"}}
	stateNode.NodeClaim = &v1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: "nc"}}
	stateNode.SetLastResizeCompletionTime(now.Add(-3 * time.Minute))

	// Custom grace period of 2 minutes — elapsed 3m exceeds it
	nodePool := &v1.NodePool{
		ObjectMeta: metav1.ObjectMeta{Name: "np"},
		Spec: v1.NodePoolSpec{
			Disruption: v1.Disruption{
				IPVSConsolidationGracePeriod: &metav1.Duration{Duration: 2 * time.Minute},
			},
		},
	}

	candidate := &Candidate{StateNode: stateNode, NodePool: nodePool}
	c := &consolidation{clock: fakeClock}

	if c.isInResizeGracePeriod(candidate) {
		t.Fatal("expected isInResizeGracePeriod=false with custom 2m grace period when elapsed=3m")
	}
}

// --- Events emitted when consolidation is skipped ---

func TestShouldDisrupt_EmitsEvent_ActiveResize(t *testing.T) {
	pods := []ctrlclient.Object{
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "pod-0", Namespace: "default"},
			Spec: corev1.PodSpec{
				NodeName: "test-node",
				Containers: []corev1.Container{{
					Name: "main", Image: "test:latest",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("128Mi"),
						},
					},
				}},
			},
			Status: corev1.PodStatus{
				Phase:  corev1.PodRunning,
				Resize: corev1.PodResizeStatusInProgress,
			},
		},
	}

	ctx := options.ToContext(context.Background(), test.Options(test.OptionsFields{
		FeatureGates: test.FeatureGates{InPlacePodVerticalScaling: ptrBool(true)},
	}))

	candidate, c := buildTestConsolidationCandidate(pods)
	recorder := c.recorder.(*test.EventRecorder)

	result := c.ShouldDisrupt(ctx, candidate)
	if result {
		t.Fatal("expected ShouldDisrupt=false when pod has active resize")
	}

	if !recorder.DetectedEvent("node has pods with active in-place resize") {
		t.Fatal("expected Unconsolidatable event with active resize message")
	}
}

func TestShouldDisrupt_EmitsEvent_GracePeriod(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	// Pod with no active resize (so the active resize check passes)
	pods := []ctrlclient.Object{
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "pod-0", Namespace: "default"},
			Spec: corev1.PodSpec{
				NodeName: "test-node",
				Containers: []corev1.Container{{
					Name: "main", Image: "test:latest",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("128Mi"),
						},
					},
				}},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, Resize: ""},
		},
	}

	ctx := options.ToContext(context.Background(), test.Options(test.OptionsFields{
		FeatureGates: test.FeatureGates{InPlacePodVerticalScaling: ptrBool(true)},
	}))

	candidate, c := buildGracePeriodTestCandidate(pods, now, 2*time.Minute, 5*time.Minute)
	recorder := c.recorder.(*test.EventRecorder)

	result := c.ShouldDisrupt(ctx, candidate)
	if result {
		t.Fatal("expected ShouldDisrupt=false during grace period")
	}

	if !recorder.DetectedEvent("node is within IPVS consolidation grace period") {
		t.Fatal("expected Unconsolidatable event with grace period message")
	}
}

// --- Consolidation proceeds normally when IPVS gate is disabled ---

func TestShouldDisrupt_GateDisabled_IgnoresActiveResize(t *testing.T) {
	pods := []ctrlclient.Object{
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "pod-0", Namespace: "default"},
			Spec: corev1.PodSpec{
				NodeName: "test-node",
				Containers: []corev1.Container{{
					Name: "main", Image: "test:latest",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("128Mi"),
						},
					},
				}},
			},
			Status: corev1.PodStatus{
				Phase:  corev1.PodRunning,
				Resize: corev1.PodResizeStatusInProgress,
			},
		},
	}

	ctx := options.ToContext(context.Background(), test.Options(test.OptionsFields{
		FeatureGates: test.FeatureGates{InPlacePodVerticalScaling: ptrBool(false)},
	}))

	candidate, c := buildTestConsolidationCandidate(pods)
	recorder := c.recorder.(*test.EventRecorder)

	// ShouldDisrupt will return true (passing the IPVS checks) and then
	// proceed to other checks. The candidate has all required labels and
	// conditions set, so it should pass through to the consolidatable check.
	result := c.ShouldDisrupt(ctx, candidate)

	// With gate disabled, the IPVS active resize event should NOT be emitted
	if recorder.DetectedEvent("node has pods with active in-place resize") {
		t.Fatal("IPVS active resize event should not be emitted when gate is disabled")
	}
	if recorder.DetectedEvent("node is within IPVS consolidation grace period") {
		t.Fatal("IPVS grace period event should not be emitted when gate is disabled")
	}

	// The candidate is fully configured (instanceType is nil in our test helper,
	// so it will fail at the instanceType check), but the key assertion is that
	// the IPVS checks were skipped entirely.
	_ = result
}

func TestShouldDisrupt_GateDisabled_IgnoresGracePeriod(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	pods := []ctrlclient.Object{
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "pod-0", Namespace: "default"},
			Spec: corev1.PodSpec{
				NodeName: "test-node",
				Containers: []corev1.Container{{
					Name: "main", Image: "test:latest",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("128Mi"),
						},
					},
				}},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, Resize: ""},
		},
	}

	ctx := options.ToContext(context.Background(), test.Options(test.OptionsFields{
		FeatureGates: test.FeatureGates{InPlacePodVerticalScaling: ptrBool(false)},
	}))

	candidate, c := buildGracePeriodTestCandidate(pods, now, 1*time.Minute, 5*time.Minute)
	recorder := c.recorder.(*test.EventRecorder)

	result := c.ShouldDisrupt(ctx, candidate)

	// Grace period event should NOT be emitted when gate is disabled
	if recorder.DetectedEvent("node is within IPVS consolidation grace period") {
		t.Fatal("IPVS grace period event should not be emitted when gate is disabled")
	}
	_ = result
}

// --- Helpers ---

// buildGracePeriodTestCandidate creates a Candidate and consolidation struct
// with a node that has a recent resize completion time, for testing grace period behavior.
func buildGracePeriodTestCandidate(pods []ctrlclient.Object, now time.Time, elapsed, gracePeriod time.Duration) (*Candidate, *consolidation) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	kubeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pods...).
		WithIndex(&corev1.Pod{}, "spec.nodeName", func(o ctrlclient.Object) []string {
			pod := o.(*corev1.Pod)
			return []string{pod.Spec.NodeName}
		}).
		Build()

	nodePool := &v1.NodePool{
		ObjectMeta: metav1.ObjectMeta{Name: "test-nodepool"},
		Spec: v1.NodePoolSpec{
			Disruption: v1.Disruption{
				ConsolidationPolicy:          v1.ConsolidationPolicyWhenEmptyOrUnderutilized,
				ConsolidateAfter:             v1.MustParseNillableDuration("0s"),
				IPVSConsolidationGracePeriod: &metav1.Duration{Duration: gracePeriod},
			},
		},
	}

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-node",
			Labels: map[string]string{
				v1.NodePoolLabelKey:            nodePool.Name,
				corev1.LabelInstanceTypeStable: "m5.large",
				v1.CapacityTypeLabelKey:        v1.CapacityTypeOnDemand,
				corev1.LabelTopologyZone:       "us-west-2a",
			},
		},
	}

	nodeClaim := &v1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "test-nodeclaim",
			Labels: map[string]string{v1.NodePoolLabelKey: nodePool.Name},
		},
	}
	nodeClaim.StatusConditions().SetTrue(v1.ConditionTypeConsolidatable)

	stateNode := state.NewNode()
	stateNode.Node = node
	stateNode.NodeClaim = nodeClaim
	stateNode.SetLastResizeCompletionTime(now.Add(-elapsed))

	candidate := &Candidate{StateNode: stateNode, NodePool: nodePool}

	fakeClock := clocktesting.NewFakeClock(now)
	recorder := test.NewEventRecorder()
	c := &consolidation{
		clock:      fakeClock,
		kubeClient: kubeClient,
		recorder:   recorder,
	}

	return candidate, c
}


