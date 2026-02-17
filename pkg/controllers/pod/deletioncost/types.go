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

package deletioncost

import (
	"sigs.k8s.io/karpenter/pkg/controllers/state"
)

// Annotation keys
const (
	PodDeletionCostAnnotation       = "controller.kubernetes.io/pod-deletion-cost"
	KarpenterManagedDeletionCostKey = "karpenter.sh/managed-deletion-cost"
)

// RankingStrategy defines how nodes should be ranked for pod deletion cost
type RankingStrategy string

const (
	RankingStrategyRandom                    RankingStrategy = "Random"
	RankingStrategyLargestToSmallest         RankingStrategy = "LargestToSmallest"
	RankingStrategySmallestToLargest         RankingStrategy = "SmallestToLargest"
	RankingStrategyUnallocatedVCPUPerPodCost RankingStrategy = "UnallocatedVCPUPerPodCost"
)

// NodeRank represents a node with its assigned rank
type NodeRank struct {
	Node            *state.StateNode
	Rank            int
	HasDoNotDisrupt bool
}
