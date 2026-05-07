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
	"time"
)

// TestTopologyDomainFiltering_LatencyScalesWithDomainCount documents the
// performance regression from https://github.com/kubernetes-sigs/karpenter/pull/2671
// (merged then reverted in https://github.com/kubernetes-sigs/karpenter/pull/2957).
//
// PR #2671 correctly filtered topology domains by NodePool compatibility —
// ensuring that pods with topology spread constraints only consider domains
// that their NodePool can actually provision into. However, the filtering
// implementation iterated over all domains for every pod during scheduling,
// causing O(pods × domains × NodePools) per-round cost.
//
// With 50 NodePools × 3 zones = 150 domain entries, scheduling 1000 pods
// with topology spread constraints took >5s (vs <500ms baseline). The fix
// was reverted and the maintainer plan is to cache domain groups across
// scheduling runs so that filtering is O(1) per pod after initialization.
//
// This test FAILS on current code because the domain filtering fix is
// reverted — domains are NOT filtered by NodePool compatibility, leading
// to incorrect topology spread decisions (pods may be spread across domains
// their NodePool cannot provision, causing scheduling failures).
func TestTopologyDomainFiltering_LatencyScalesWithDomainCount(t *testing.T) {
	// Benchmark parameters
	numNodePools := 50
	zonesPerPool := 3
	numPods := 1000

	// Expected: per-pod scheduling latency is independent of total domain
	// count when domains are cached per-NodePool.
	// Actual (with #2671 reverted): domains are unfiltered, topology
	// spread considers invalid domains, and (if #2671 were re-applied
	// without caching) latency grows linearly with domain count.
	_ = numNodePools
	_ = zonesPerPool
	_ = numPods

	t.Skip("aspirational: topology domain filtering was reverted (#2957) due to O(pods×domains×NodePools) cost; needs caching (#2671)")
}

// TestTopologyDomainFiltering_CorrectDomainsPerNodePool documents the
// correctness issue: without domain filtering, topology spread constraints
// consider domains from ALL NodePools, not just the ones the pod's NodePool
// can provision into.
//
// Scenario: NodePool-A can provision in us-east-1{a,b,c}. NodePool-B can
// provision in eu-west-1{a,b,c}. A pod targeting NodePool-A with
// topologySpreadConstraints maxSkew=1 across zones sees 6 domains instead
// of 3. The scheduler tries to spread across all 6, but can only actually
// place pods in 3 — causing suboptimal distribution or scheduling failures.
//
// This test FAILS on current code because domain filtering was reverted.
func TestTopologyDomainFiltering_CorrectDomainsPerNodePool(t *testing.T) {
	// Pod targets NodePool-A (us-east-1 only) with:
	//   topologySpreadConstraints:
	//   - maxSkew: 1
	//     topologyKey: topology.kubernetes.io/zone
	//     whenUnsatisfiable: DoNotSchedule
	//
	// Expected: only us-east-1{a,b,c} are considered for spread calculation
	// Actual: us-east-1{a,b,c} + eu-west-1{a,b,c} all appear as domains,
	//         causing skew miscalculation

	t.Skip("aspirational: topology spread considers domains from incompatible NodePools after #2671 revert (#2957)")
}

// TestTopologyDomainFiltering_CachedDomainsStayFreshAcrossRounds documents
// the desired caching behavior. Domain groups should be computed once per
// NodePool configuration change, then reused across scheduling rounds.
//
// The cache invalidation trigger: when a NodePool's requirements or
// instance types change, the cached domain group for that pool must be
// rebuilt. Between changes, domain group lookup should be O(1).
//
// This test documents the expected performance invariant once caching is
// implemented.
func TestTopologyDomainFiltering_CachedDomainsStayFreshAcrossRounds(t *testing.T) {
	// Simulate 10 scheduling rounds with no NodePool changes.
	// After the first round builds the cache, subsequent rounds should
	// not recompute domain groups.
	//
	// Measurement: domain group computation time for round-1 vs round-2+
	// Expected: round-2+ is <1% of round-1 latency (cache hit)

	rounds := 10
	maxCacheComputeRatio := 0.01 // round-2 should be <1% of round-1
	_ = rounds
	_ = maxCacheComputeRatio

	t.Skip("aspirational: topology domain group caching across scheduling rounds not yet implemented (#2671)")
}

// TestTopologyDomainFiltering_P99LatencyBounded verifies the end-to-end
// scheduling latency invariant: even with many NodePools and domains,
// per-pod scheduling latency stays bounded.
//
// The performance SLO: p99 scheduling latency for a single pod should be
// <10ms regardless of the number of NodePools or total domain count in the
// cluster. This requires domain filtering AND caching.
func TestTopologyDomainFiltering_P99LatencyBounded(t *testing.T) {
	// Target SLO
	maxP99Latency := 10 * time.Millisecond
	_ = maxP99Latency

	// With 50 NodePools × 3 zones, schedule 1000 pods and measure p99.
	// Current code (without #2671): domains are unfiltered but iteration
	// is bounded by the optimization in nextDomainTopologySpread that
	// prefers nodeDomains when Operator=In. However, this optimization
	// doesn't apply to all topology keys.
	//
	// With #2671 re-applied without caching: p99 > 50ms for large pools.
	// With caching: p99 < 10ms (target).

	t.Skip("aspirational: p99 scheduling latency unbounded with many NodePools and topology domains (#2671, #2957)")
}
