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

package state

import (
	"reflect"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	clock "k8s.io/utils/clock/testing"
)

// resetClearedFields are the Cluster fields Reset must leave empty, each mapped to a check that the field holds no
// entries. resetRetainedFields are the fields Reset deliberately leaves alone: the constructor dependencies and the
// mutexes guarding the rest. Every field of Cluster must appear in exactly one of the two, which the completeness spec
// below enforces, so a field added to Cluster cannot silently escape Reset the way podHealthyNodePoolScheduledTime did.
// repairPolicyMatcher is retained, not cleared: NewCluster resolves it from ClusterOptions and matchRepairPolicies
// reads it on every Node update, so clearing it in a teardown would silently stop repair matching for the rest of the
// suite. It is a dependency, like the client and the cloud provider, not per-spec state.
var resetRetainedFields = sets.New(
	"kubeClient", "cloudProvider", "clock", "repairPolicyMatcher",
	"mu", "clusterStateMu", "unsyncedTimeMu", "bufferPodCountsMu",
)

func clusterResetClearedFields(c *Cluster) map[string]func() bool {
	return map[string]func() bool{
		"hasSynced":                       func() bool { return !c.hasSynced.Load() },
		"nodes":                           func() bool { return len(c.nodes) == 0 },
		"bindings":                        func() bool { return len(c.bindings) == 0 },
		"nodeNameToProviderID":            func() bool { return len(c.nodeNameToProviderID) == 0 },
		"nodeClaimNameToProviderID":       func() bool { return len(c.nodeClaimNameToProviderID) == 0 },
		"nodePoolResources":               func() bool { return len(c.nodePoolResources) == 0 },
		"daemonSetPods":                   func() bool { return syncMapEmpty(&c.daemonSetPods) },
		"NodePoolState":                   func() bool { return nodePoolStateEmpty(c.NodePoolState) },
		"podAcks":                         func() bool { return syncMapEmpty(&c.podAcks) },
		"podsSchedulingAttempted":         func() bool { return syncMapEmpty(&c.podsSchedulingAttempted) },
		"podsSchedulableTimes":            func() bool { return syncMapEmpty(&c.podsSchedulableTimes) },
		"podHealthyNodePoolScheduledTime": func() bool { return syncMapEmpty(&c.podHealthyNodePoolScheduledTime) },
		"podToNodeClaim":                  func() bool { return syncMapEmpty(&c.podToNodeClaim) },
		"clusterState":                    func() bool { return c.clusterState.IsZero() },
		"unsyncedStartTime":               func() bool { return c.unsyncedStartTime.IsZero() },
		"lastUnsyncedLogTime":             func() bool { return c.lastUnsyncedLogTime.IsZero() },
		"antiAffinityPods":                func() bool { return syncMapEmpty(&c.antiAffinityPods) },
		"bufferPodCounts":                 func() bool { return len(c.bufferPodCounts) == 0 },
	}
}

func syncMapEmpty(m *sync.Map) bool {
	empty := true
	m.Range(func(_, _ any) bool {
		empty = false
		return false
	})
	return empty
}

func nodePoolStateEmpty(n *NodePoolState) bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return len(n.nodePoolNameToNodeClaimState) == 0 && len(n.nodeClaimNameToNodePoolName) == 0 &&
		len(n.nodePoolNameToNodePoolLimit) == 0
}

// populateCluster writes one entry into every field Reset is expected to clear. The pod-keyed maps are written the way
// their production writers do: AckPods (:459) for podAcks, MarkPodSchedulingDecisions (:512, :513, :524) for the three
// scheduling maps, UpdatePodToNodeClaimMapping (:538) for podToNodeClaim. Those three are stored directly rather than
// through MarkPodSchedulingDecisions because that path reads a NodePool through the kube client, which this spec has no
// use for otherwise; the writers themselves are already covered by the "Pod Healthy NodePool" specs in suite_test.go.
func populateCluster(c *Cluster, now time.Time) {
	nn := types.NamespacedName{Namespace: "default", Name: "pod"}

	c.hasSynced.Store(true)
	c.nodes["provider-id"] = &StateNode{}
	c.bindings[nn] = "node"
	c.nodeNameToProviderID["node"] = "provider-id"
	c.nodeClaimNameToProviderID["nodeclaim"] = "provider-id"
	c.nodePoolResources["nodepool"] = corev1.ResourceList{}
	c.daemonSetPods.Store(nn, &corev1.Pod{})

	c.NodePoolState.SetNodeClaimMapping("nodepool", "nodeclaim")
	c.NodePoolState.MarkNodeClaimActive("nodepool", "nodeclaim")

	c.podAcks.Store(nn, now)
	c.podsSchedulingAttempted.Store(nn, now)
	c.podsSchedulableTimes.Store(nn, now)
	c.podHealthyNodePoolScheduledTime.Store(nn, now)
	c.podToNodeClaim.Store(nn, "nodeclaim")

	c.clusterState = now
	c.unsyncedStartTime = now
	c.lastUnsyncedLogTime = now
	c.antiAffinityPods.Store(nn, &corev1.Pod{})
	c.bufferPodCounts["provider-id"] = 1
}

var _ = Describe("Cluster State Reset", func() {
	var clk *clock.FakeClock
	var cluster *Cluster

	BeforeEach(func() {
		clk = clock.NewFakeClock(time.Now())
		// Nil dependencies: Reset, ConsolidationState and BufferPodCount are the only methods these specs call, and
		// none of them reads the kube client or the cloud provider. Keeping them nil keeps the specs off envtest.
		cluster = NewCluster(clk, nil, nil)
	})

	It("should clear every field it owns, including podHealthyNodePoolScheduledTime", func() {
		populateCluster(cluster, clk.Now())
		// Assert the precondition, so a spec that populates nothing cannot pass vacuously.
		for name, isEmpty := range clusterResetClearedFields(cluster) {
			Expect(isEmpty()).To(BeFalse(), "%s was not populated, so Reset over it proves nothing", name)
		}

		cluster.Reset()

		for name, isEmpty := range clusterResetClearedFields(cluster) {
			Expect(isEmpty()).To(BeTrue(), "Reset left %s populated", name)
		}
	})

	It("should classify every Cluster field as either cleared or retained", func() {
		cleared := sets.KeySet(clusterResetClearedFields(cluster))
		Expect(cleared.Intersection(resetRetainedFields)).To(BeEmpty())

		clusterType := reflect.TypeOf(Cluster{})
		fields := sets.New[string]()
		for i := range clusterType.NumField() {
			fields.Insert(clusterType.Field(i).Name)
		}
		// A field in neither set is a field whose Reset behavior nobody decided. Add it to one of the two.
		Expect(fields.Difference(cleared.Union(resetRetainedFields)).UnsortedList()).To(BeEmpty())
		// Both sets name real fields, so a rename cannot leave a check pointing at nothing.
		Expect(cleared.Union(resetRetainedFields).Difference(fields).UnsortedList()).To(BeEmpty())
	})

	It("should not race a concurrent ConsolidationState or BufferPodCount read", func() {
		// Reset writes clusterState and bufferPodCounts, which clusterStateMu and bufferPodCountsMu guard everywhere
		// else. Holding only mu makes both writes unsynchronized against their own readers.
		const iterations = 1000
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range iterations {
				cluster.ConsolidationState()
				cluster.BufferPodCount("provider-id")
			}
		}()
		for range iterations {
			cluster.Reset()
		}
		wg.Wait()
	})
})
