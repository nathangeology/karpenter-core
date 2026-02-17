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
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"sort"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
)

const (
	baseRank = -1000 // Starting rank for normal nodes
)

// RankingEngine ranks nodes based on a configured strategy
type RankingEngine struct {
	strategy RankingStrategy
}

// NewRankingEngine creates a new ranking engine with the specified strategy
func NewRankingEngine(strategy RankingStrategy) *RankingEngine {
	return &RankingEngine{
		strategy: strategy,
	}
}

// RankNodes ranks the provided nodes and returns them with assigned ranks
func (r *RankingEngine) RankNodes(ctx context.Context, nodes []*state.StateNode) ([]NodeRank, error) {
	if len(nodes) == 0 {
		return []NodeRank{}, nil
	}

	// Partition nodes by do-not-disrupt status
	normalNodes, doNotDisruptNodes := r.partitionNodes(nodes)

	// Rank each partition
	rankedNormal, err := r.rankPartition(normalNodes, baseRank)
	if err != nil {
		return nil, err
	}

	// Do-not-disrupt nodes start after normal nodes
	nextRank := baseRank + len(normalNodes)
	rankedDoNotDisrupt, err := r.rankPartition(doNotDisruptNodes, nextRank)
	if err != nil {
		return nil, err
	}

	// Combine results
	return append(rankedNormal, rankedDoNotDisrupt...), nil
}

// partitionNodes separates nodes into those with and without do-not-disrupt pods
func (r *RankingEngine) partitionNodes(nodes []*state.StateNode) ([]*state.StateNode, []*state.StateNode) {
	var normal, doNotDisrupt []*state.StateNode

	for _, node := range nodes {
		if r.hasDoNotDisruptPod(node) {
			doNotDisrupt = append(doNotDisrupt, node)
		} else {
			normal = append(normal, node)
		}
	}

	return normal, doNotDisrupt
}

// hasDoNotDisruptPod checks if a node hosts any do-not-disrupt pods
func (r *RankingEngine) hasDoNotDisruptPod(node *state.StateNode) bool {
	if node.Node == nil {
		return false
	}

	// Check node annotation
	if node.Node.Annotations != nil {
		if val, ok := node.Node.Annotations[v1.DoNotDisruptAnnotationKey]; ok && val == "true" {
			return true
		}
	}

	// Note: In the actual implementation, we would need to check pods on the node
	// For now, we check the node annotation as a proxy
	return false
}

// rankPartition ranks a partition of nodes and assigns sequential ranks starting from startRank
func (r *RankingEngine) rankPartition(nodes []*state.StateNode, startRank int) ([]NodeRank, error) {
	if len(nodes) == 0 {
		return []NodeRank{}, nil
	}

	// Sort nodes based on strategy
	sortedNodes, err := r.sortNodes(nodes)
	if err != nil {
		return nil, err
	}

	// Assign sequential ranks
	ranks := make([]NodeRank, len(sortedNodes))
	for i, node := range sortedNodes {
		ranks[i] = NodeRank{
			Node:            node,
			Rank:            startRank + i,
			HasDoNotDisrupt: r.hasDoNotDisruptPod(node),
		}
	}

	return ranks, nil
}

// sortNodes sorts nodes based on the configured strategy
func (r *RankingEngine) sortNodes(nodes []*state.StateNode) ([]*state.StateNode, error) {
	switch r.strategy {
	case RankingStrategyRandom:
		return r.sortRandom(nodes)
	case RankingStrategyLargestToSmallest:
		return r.sortBySize(nodes, false)
	case RankingStrategySmallestToLargest:
		return r.sortBySize(nodes, true)
	case RankingStrategyUnallocatedVCPUPerPodCost:
		return r.sortByUnallocatedVCPU(nodes)
	default:
		return nil, fmt.Errorf("invalid ranking strategy: %s", r.strategy)
	}
}

// sortRandom randomly shuffles nodes
func (r *RankingEngine) sortRandom(nodes []*state.StateNode) ([]*state.StateNode, error) {
	shuffled := make([]*state.StateNode, len(nodes))
	copy(shuffled, nodes)

	// Fisher-Yates shuffle using crypto/rand for determinism
	for i := len(shuffled) - 1; i > 0; i-- {
		jBig, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return nil, fmt.Errorf("generating random number: %w", err)
		}
		j := int(jBig.Int64())
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	}

	return shuffled, nil
}

// sortBySize sorts nodes by total capacity
func (r *RankingEngine) sortBySize(nodes []*state.StateNode, ascending bool) ([]*state.StateNode, error) {
	sorted := make([]*state.StateNode, len(nodes))
	copy(sorted, nodes)

	sort.Slice(sorted, func(i, j int) bool {
		capacityI := r.getTotalCapacity(sorted[i])
		capacityJ := r.getTotalCapacity(sorted[j])

		if capacityI == capacityJ {
			// Deterministic ordering for ties
			return r.getNodeName(sorted[i]) < r.getNodeName(sorted[j])
		}

		if ascending {
			return capacityI < capacityJ
		}
		return capacityI > capacityJ
	})

	return sorted, nil
}

// sortByUnallocatedVCPU sorts nodes by unallocated vCPU per pod ratio
func (r *RankingEngine) sortByUnallocatedVCPU(nodes []*state.StateNode) ([]*state.StateNode, error) {
	sorted := make([]*state.StateNode, len(nodes))
	copy(sorted, nodes)

	sort.Slice(sorted, func(i, j int) bool {
		ratioI := r.getUnallocatedVCPUPerPod(sorted[i])
		ratioJ := r.getUnallocatedVCPUPerPod(sorted[j])

		if ratioI == ratioJ {
			// Deterministic ordering for ties
			return r.getNodeName(sorted[i]) < r.getNodeName(sorted[j])
		}

		// Higher ratio = lower rank (deleted first)
		return ratioI > ratioJ
	})

	return sorted, nil
}

// getTotalCapacity calculates normalized total capacity (CPU + Memory)
func (r *RankingEngine) getTotalCapacity(node *state.StateNode) float64 {
	if node.Node == nil {
		return 0
	}

	allocatable := node.Node.Status.Allocatable

	// Normalize CPU (in cores) and Memory (in GB)
	cpu := float64(allocatable.Cpu().MilliValue()) / 1000.0
	memory := float64(allocatable.Memory().Value()) / (1024 * 1024 * 1024)

	// Weighted sum: CPU is weighted more heavily
	return cpu*10 + memory
}

// getUnallocatedVCPUPerPod calculates the ratio of unallocated vCPU to pod count
func (r *RankingEngine) getUnallocatedVCPUPerPod(node *state.StateNode) float64 {
	if node.Node == nil {
		return 0
	}

	// Get total allocatable CPU
	allocatable := node.Node.Status.Allocatable
	totalCPU := float64(allocatable.Cpu().MilliValue()) / 1000.0

	// Get pod count (simplified - in real implementation would count actual pods)
	podCount := r.getPodCount(node)
	if podCount == 0 {
		// Nodes with no pods have infinite ratio (highest priority for deletion)
		return 1000000.0
	}

	// Calculate unallocated CPU (simplified - assumes some usage)
	// In real implementation, would calculate actual allocated vs allocatable
	unallocatedCPU := totalCPU * 0.5 // Placeholder

	return unallocatedCPU / float64(podCount)
}

// getPodCount returns the number of pods on a node
func (r *RankingEngine) getPodCount(node *state.StateNode) int {
	// In real implementation, would count actual pods from cluster state
	// For now, return a placeholder
	if node.Node == nil {
		return 0
	}

	// Check if we can get pod count from node status
	if node.Node.Status.Allocatable.Pods().Value() > 0 {
		// Estimate based on allocatable pods (placeholder)
		return int(node.Node.Status.Allocatable.Pods().Value() / 10)
	}

	return 1 // Default to 1 to avoid division by zero
}

// getNodeName returns the node name for deterministic ordering
func (r *RankingEngine) getNodeName(node *state.StateNode) string {
	if node.Node != nil {
		return node.Node.Name
	}
	if node.NodeClaim != nil {
		return node.NodeClaim.Name
	}
	return ""
}
