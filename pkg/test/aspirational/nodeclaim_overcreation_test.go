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
)

// TestNodeClaimOverCreation_RapidSchedulingRounds documents the scenario
// from https://github.com/kubernetes-sigs/karpenter/pull/2947
//
// Between NodeClaim creation and node registration, the cluster state does
// not fully account for "in-flight" capacity in non-static NodePools.
// When multiple scheduling rounds trigger in rapid succession (e.g., pod
// watcher fires for each pod in a batch deployment), each round sees the
// same set of pending pods and independently decides to create NodeClaims.
//
// The result is over-provisioning: N scheduling rounds each create a
// NodeClaim for the same pods, producing N nodes when 1 would suffice.
// The proposed fix uses pessimistic max-capacity reservation so that each
// scheduling round accounts for NodeClaims created by prior rounds even
// before they register.
//
// This test FAILS on current code because non-static NodePool scheduling
// does not reserve capacity during the creation-to-registration window.
func TestNodeClaimOverCreation_RapidSchedulingRounds(t *testing.T) {
	// Scenario:
	// - 1 NodePool allowing up to 10 nodes
	// - 5 pods arrive simultaneously, each requiring 1 full node
	// - Cloud provider registration takes 30s
	// - 3 scheduling rounds fire within 1s
	//
	// Expected: exactly 5 NodeClaims created total
	// Actual (current): up to 15 NodeClaims (5 per round × 3 rounds)
	//
	// The fix requires that round-2 sees round-1's NodeClaims as
	// "pending capacity" and does not re-schedule pods that are already
	// bound to an in-flight NodeClaim.

	t.Skip("aspirational: non-static NodePool scheduling does not account for in-flight NodeClaims between rounds (#2947)")
}

// TestNodeClaimOverCreation_PodDeletedWhilePending documents the variant
// where the original pod is deleted while its NodeClaim is still pending.
//
// If a pod triggers NodeClaim creation, then is deleted before the
// NodeClaim registers, the NodeClaim should be garbage-collected. However,
// if a second scheduling round fires between pod deletion and GC, it may
// see OTHER pending pods and create yet another NodeClaim — because the
// first NodeClaim's reservation wasn't released yet.
//
// This creates a temporarily over-provisioned state where extra nodes sit
// idle until consolidation removes them (if consolidation is enabled).
//
// This test FAILS because the scheduling/GC race window exists in
// current code.
func TestNodeClaimOverCreation_PodDeletedWhilePending(t *testing.T) {
	// Scenario:
	// - Pod-A triggers NodeClaim-1 creation
	// - Pod-A is deleted (job completed/cancelled)
	// - Pod-B arrives, scheduling round fires
	// - NodeClaim-1 is still pending (not yet registered or GC'd)
	//
	// Expected: Pod-B may reuse NodeClaim-1's capacity OR NodeClaim-1
	// is cancelled and Pod-B gets its own NodeClaim — but never both.
	// Actual: Pod-B gets NodeClaim-2 AND NodeClaim-1 eventually
	// registers an idle node.

	t.Skip("aspirational: pod deletion during NodeClaim pending window can leave orphaned capacity (#2947)")
}

// TestNodeClaimOverCreation_NodePoolLimitRespectedUnderConcurrency verifies
// that NodePool limits are never exceeded even under high scheduling
// concurrency.
//
// The provisioner processes scheduling rounds sequentially, but the window
// between CreateNodeClaims and cluster state update is where races occur.
// The proposed fix (pessimistic max-capacity reservation via
// subtractMax/ReserveNodeCount) ensures that even if cluster state hasn't
// updated, the remaining capacity is decremented pessimistically.
//
// This test FAILS on current code under sufficient concurrency because
// non-static NodePools don't track reserved count.
func TestNodeClaimOverCreation_NodePoolLimitRespectedUnderConcurrency(t *testing.T) {
	// Scenario:
	// - NodePool limit: 5 nodes max
	// - 50 pods arrive, each needing its own node
	// - Only 5 should be provisioned; remaining 45 stay pending
	//
	// Under the race condition, scheduling rounds may each see "0 of 5
	// used" and create 5 more, exceeding the limit transiently until
	// the lifecycle controller catches up and sets limits.

	t.Skip("aspirational: non-static NodePool limits can be transiently exceeded under rapid scheduling (#2947)")
}
