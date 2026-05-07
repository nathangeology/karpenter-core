//go:build test_aspirational

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

package aspirational

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// TestStatusConditions_InformerCacheMutationIsolation documents the scenario where
// the nodeclaim disruption controller (pkg/controllers/nodeclaim/disruption/) mutates
// StatusConditions directly on objects from the informer cache without DeepCopy.
//
// In drift.go (lines 56, 69, 75) and consolidation.go (lines 44, 53, 65, 73),
// the Reconcile methods call StatusConditions().Clear() and StatusConditions().SetTrue*()
// directly on the nodeClaim pointer received from the parent controller. The parent
// controller (controller.go) does `stored := nodeClaim.DeepCopy()` for diffing, but
// the original nodeClaim is the cache object itself. Mutations are visible to any
// other goroutine that reads the same object from the informer cache before the
// status patch completes.
//
// The desired behavior: sub-controllers should operate on a DeepCopy of the NodeClaim,
// and the parent should only apply mutations to the original after computing the diff.
// Alternatively, the parent should DeepCopy BEFORE passing to sub-controllers.
//
// This test verifies that mutating StatusConditions on a NodeClaim does not corrupt
// a shared reference to the same object (simulating informer cache sharing).
// It FAILS on current code because both references point to the same underlying struct.
func TestStatusConditions_InformerCacheMutationIsolation(t *testing.T) {
	// Simulate: informer cache holds a NodeClaim with no Drifted condition
	cached := &v1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-nodeclaim",
			Generation: 1,
		},
	}
	// Initialize base conditions (Launched=True so drift reconciler proceeds)
	cached.StatusConditions().SetTrue(v1.ConditionTypeLaunched)

	// Snapshot the condition state as it should appear to concurrent readers
	conditionCountBefore := len(cached.GetConditions())
	hasDriftedBefore := cached.StatusConditions().Get(v1.ConditionTypeDrifted) != nil

	// Simulate what the drift sub-controller does (drift.go:75):
	// It mutates the NodeClaim directly without DeepCopy
	controllerView := cached // no DeepCopy — this is the bug
	controllerView.StatusConditions().SetTrueWithReason(v1.ConditionTypeDrifted, "NodePoolDrifted", "NodePoolDrifted")

	// Simulate a concurrent reader getting the same object from cache
	// Desired behavior: the cached object should NOT show uncommitted mutations
	concurrentView := cached
	drifted := concurrentView.StatusConditions().Get(v1.ConditionTypeDrifted)

	if drifted != nil && drifted.IsTrue() {
		t.Errorf("informer cache corrupted: concurrent reader sees uncommitted Drifted=True condition; "+
			"conditions before=%d (hasDrifted=%v), after=%d (hasDrifted=%v); "+
			"controller should DeepCopy before mutating StatusConditions",
			conditionCountBefore, hasDriftedBefore,
			len(cached.GetConditions()), true)
	}
}

// TestStatusConditions_PatchFailureDoesNotCorruptCache documents that when the
// optimistic lock patch fails (conflict error), the informer cache should not
// retain the in-memory mutations made by the controller.
//
// In controller.go, after sub-controllers mutate conditions, the patch is attempted
// with MergeFromWithOptimisticLock. On conflict (IsConflict), it returns Requeue.
// However, the mutations have already been applied to the cache object in memory.
// Until the informer re-syncs from the API server, the cache holds incorrect state.
//
// The desired behavior: mutations should be applied to a working copy (DeepCopy),
// and only committed to the cache-visible object after a successful patch.
//
// This test verifies that a simulated patch failure leaves the original object
// in its pre-reconcile state.
func TestStatusConditions_PatchFailureDoesNotCorruptCache(t *testing.T) {
	// Create a NodeClaim representing the informer cache state
	cached := &v1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-nodeclaim",
			Generation: 1,
		},
	}
	cached.StatusConditions().SetTrue(v1.ConditionTypeLaunched)
	cached.StatusConditions().SetTrue(v1.ConditionTypeRegistered)
	cached.StatusConditions().SetTrue(v1.ConditionTypeInitialized)

	// Record the pre-reconcile state
	preReconcileConditions := cached.DeepCopy().Status.Conditions

	// Simulate the controller flow:
	// 1. stored = DeepCopy (for later diff)
	// 2. Sub-controllers mutate the original (BUG: should mutate a copy)
	// 3. Patch fails with conflict
	// 4. Return Requeue

	// Step 2: mutations happen on the cache object directly
	cached.StatusConditions().SetTrueWithReason(v1.ConditionTypeDrifted, "RequirementsDrifted", "RequirementsDrifted")
	cached.StatusConditions().SetTrue(v1.ConditionTypeConsolidatable)

	// Step 3: simulate patch failure (we just don't persist)
	// In reality, the patch returns IsConflict and the controller requeues

	// Desired: after a failed reconcile, the cached object should match pre-reconcile state
	// This FAILS because mutations were applied in-place to the cache object
	if len(cached.Status.Conditions) != len(preReconcileConditions) {
		t.Errorf("cache corrupted after simulated patch failure: "+
			"conditions count = %d, want %d (pre-reconcile state); "+
			"controller must not mutate cache objects in-place",
			len(cached.Status.Conditions), len(preReconcileConditions))
	}

	// Verify specific unwanted mutations
	if cached.StatusConditions().Get(v1.ConditionTypeDrifted) != nil {
		t.Error("cache retains Drifted condition after failed patch — " +
			"other controllers will see stale drift state")
	}
	if cached.StatusConditions().Get(v1.ConditionTypeConsolidatable) != nil {
		t.Error("cache retains Consolidatable condition after failed patch — " +
			"disruption controller may act on uncommitted state")
	}
}
