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
	"sort"

	"k8s.io/apimachinery/pkg/util/sets"
)

// SortCandidatesBySavingsRatio sorts candidates by price/disruption ratio
// descending (highest savings per unit disruption first). This is the canonical
// consolidation priority: nodes that save the most relative to their disruption
// cost are attempted first.
func SortCandidatesBySavingsRatio(candidates []*Candidate) []*Candidate {
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].SavingsRatio() > candidates[j].SavingsRatio()
	})
	return candidates
}

// InterleaveCandidatesByNodePool distributes candidates across NodePools in
// round-robin order so that a timeout doesn't starve any single pool.
// priorityPools are placed first in the interleaving order.
func InterleaveCandidatesByNodePool(candidates []*Candidate, priorityPools sets.Set[string]) []*Candidate {
	nodePoolCandidates := map[string][]*Candidate{}
	for _, c := range candidates {
		name := c.NodePool.Name
		nodePoolCandidates[name] = append(nodePoolCandidates[name], c)
	}

	sortedNodePools := priorityPools.UnsortedList()
	for name := range nodePoolCandidates {
		if !priorityPools.Has(name) {
			sortedNodePools = append(sortedNodePools, name)
		}
	}

	var maxLen int
	for _, cs := range nodePoolCandidates {
		if len(cs) > maxLen {
			maxLen = len(cs)
		}
	}

	result := make([]*Candidate, 0, len(candidates))
	for i := range maxLen {
		for _, nodePoolName := range sortedNodePools {
			if i < len(nodePoolCandidates[nodePoolName]) {
				result = append(result, nodePoolCandidates[nodePoolName][i])
			}
		}
	}
	return result
}
