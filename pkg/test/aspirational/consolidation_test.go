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

// TestMultiNodeConsolidation_CheaperCombination documents the scenario from
// https://github.com/kubernetes-sigs/karpenter/issues/1962
//
// Two c5a.xlarge nodes (4 vCPU, 8 GiB each) at ~50% CPU utilization should
// consolidate into a single m6a.xlarge (4 vCPU, 16 GiB) which is cheaper than
// running two c5a.xlarge instances. Currently Karpenter reports "Can't replace
// with a cheaper node" because multi-node consolidation doesn't explore all
// valid instance type combinations.
//
// This test will pass once multi-node consolidation considers cross-family
// replacements that are cheaper in aggregate.
func TestMultiNodeConsolidation_CheaperCombination(t *testing.T) {
	t.Skip("aspirational: blocked on multi-node consolidation cross-family replacement logic (#1962)")
}

// TestMultiNodeConsolidation_IncompatiblePoolPodsBlocking documents the scenario
// where multi-node consolidation considers candidates from multiple NodePools that
// have mutually incompatible pod scheduling constraints.
//
// Current behavior: When SimulateScheduling fails because pods from NodePool-A
// have nodeSelectors/affinities that are incompatible with the replacement node
// (which might only satisfy NodePool-B's requirements), multi-node consolidation
// silently returns an empty command. This is correct (it doesn't force an invalid
// consolidation), but it provides zero observability. Unlike single-node
// consolidation which emits an Unconsolidatable event with the scheduling error,
// multi-node consolidation swallows the failure.
//
// The binary search in firstNConsolidationOption (multinodeconsolidation.go:118)
// treats scheduling failures as "try fewer nodes" (max = mid - 1). If the
// incompatible pods are on the lowest-disruption-cost nodes (sorted first),
// the binary search will never find a valid consolidation because it always
// includes them. The algorithm cannot skip problematic nodes in the middle of
// the sorted list.
//
// Desired behavior: Multi-node consolidation should either:
// 1. Emit an event explaining why consolidation failed (observability), and/or
// 2. Allow the binary search to exclude specific problematic candidates rather
//    than only trimming from the end of the sorted list.
//
// This test will pass once multi-node consolidation can consolidate compatible
// subsets of candidates even when some candidates have incompatible pods.
func TestMultiNodeConsolidation_IncompatiblePoolPodsBlocking(t *testing.T) {
	t.Skip("aspirational: blocked on multi-node consolidation cross-pool pod incompatibility handling")
}

// TestMultiNodeConsolidation_IncompatiblePodsObservability documents that
// multi-node consolidation should emit events when it skips consolidation
// due to incompatible pod scheduling constraints.
//
// In consolidation.go:149-155, when AllNonPendingPodsScheduled() returns false,
// single-node consolidation (len(candidates) == 1) publishes an Unconsolidatable
// event with the scheduling error details. Multi-node consolidation silently
// returns an empty Command.
//
// This makes debugging difficult: a cluster operator sees nodes that "should"
// consolidate but don't, with no indication why. The only way to diagnose is
// to read controller logs at V(1) verbosity.
//
// Desired behavior: Multi-node consolidation should emit a consolidated event
// (not per-node to avoid noise) explaining which pods blocked the consolidation
// and what scheduling constraints were unsatisfied.
func TestMultiNodeConsolidation_IncompatiblePodsObservability(t *testing.T) {
	t.Skip("aspirational: blocked on multi-node consolidation event emission for scheduling failures")
}
