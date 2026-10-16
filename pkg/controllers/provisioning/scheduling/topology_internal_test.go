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

package scheduling

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/cloudprovider/fake"
)

var _ = Describe("Topology Internals", func() {
	Describe("buildDomainGroups", func() {
		// nodePoolsOverTaintSets returns count NodePools sharing distinctTaints distinct taint sets,
		// assigned round robin.  distinctTaints == count gives every NodePool a set of its own, and 1
		// gives them all the same set.
		nodePoolsOverTaintSets := func(count, distinctTaints int) []*v1.NodePool {
			nodePools := make([]*v1.NodePool, count)
			for i := range nodePools {
				np := &v1.NodePool{}
				np.Name = fmt.Sprintf("nodepool-%d", i)
				np.Spec.Template.Spec.Taints = []corev1.Taint{{
					Key:    fmt.Sprintf("example.com/taint-set-%d", i%distinctTaints),
					Value:  "true",
					Effect: corev1.TaintEffectNoSchedule,
				}}
				nodePools[i] = np
			}
			return nodePools
		}

		// oneInstanceTypeEach offers every NodePool the same single instance type.  The fake instance
		// type offers all three test zones, so every NodePool contributes to every zone domain.
		oneInstanceTypeEach := func(nodePools []*v1.NodePool) map[string][]*cloudprovider.InstanceType {
			its := map[string][]*cloudprovider.InstanceType{}
			for _, np := range nodePools {
				its[np.Name] = []*cloudprovider.InstanceType{fake.NewInstanceType("default-instance-type")}
			}
			return its
		}

		// zoneTaintSetCounts reports how many taint sets are tracked against each zone domain.
		zoneTaintSetCounts := func(domainGroups map[string]TopologyDomainGroup) map[string]int {
			counts := map[string]int{}
			for domain, taintSets := range domainGroups[corev1.LabelTopologyZone] {
				counts[domain] = len(taintSets)
			}
			return counts
		}

		It("should track one taint set per distinct set of NodePool taints, however many NodePools carry it", func() {
			// Insert collapses a taint set equal to the one stored last against the domain, so NodePools
			// carrying the same taints have to be visited consecutively for their sets to collapse.  The
			// NodePools are visited in the order buildDomainGroups chooses, not the order they are passed
			// in, so this is a statement about that order: 40 NodePools over two taint sets must store the
			// two, not one per accidentally non-adjacent NodePool.
			for _, nodePoolCount := range []int{4, 40} {
				nodePools := nodePoolsOverTaintSets(nodePoolCount, 2)
				domainGroups := buildDomainGroups(nodePools, oneInstanceTypeEach(nodePools))
				Expect(zoneTaintSetCounts(domainGroups)).To(Equal(map[string]int{
					"test-zone-1": 2,
					"test-zone-2": 2,
					"test-zone-3": 2,
				}), "with %d NodePools over 2 taint sets", nodePoolCount)
			}
		})

		It("should keep every distinct set of NodePool taints", func() {
			// The control on the spec above: a domain reachable under eight different taint sets has to
			// keep all eight, since a pod tolerating only the eighth is still eligible for the domain.
			nodePools := nodePoolsOverTaintSets(8, 8)
			domainGroups := buildDomainGroups(nodePools, oneInstanceTypeEach(nodePools))
			Expect(zoneTaintSetCounts(domainGroups)).To(Equal(map[string]int{
				"test-zone-1": 8,
				"test-zone-2": 8,
				"test-zone-3": 8,
			}))
			Expect(sets.KeySet(domainGroups[corev1.LabelTopologyZone])).To(Equal(
				sets.New("test-zone-1", "test-zone-2", "test-zone-3")))
		})

		It("should track a single empty taint set for a domain an untainted NodePool offers", func() {
			// A pod is eligible for every domain an untainted NodePool offers whatever it tolerates, so
			// the taint sets of the other NodePools offering that domain need not be tracked at all.  This
			// holds however late the untainted NodePool is visited, since an empty set replaces whatever
			// is stored rather than being appended to it.
			nodePools := nodePoolsOverTaintSets(40, 40)
			nodePools[0].Spec.Template.Spec.Taints = nil
			domainGroups := buildDomainGroups(nodePools, oneInstanceTypeEach(nodePools))
			Expect(zoneTaintSetCounts(domainGroups)).To(Equal(map[string]int{
				"test-zone-1": 1,
				"test-zone-2": 1,
				"test-zone-3": 1,
			}))
			Expect(domainGroups[corev1.LabelTopologyZone]["test-zone-1"][0]).To(BeEmpty())
		})
	})
})
