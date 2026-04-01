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
	"math"

	corev1 "k8s.io/api/core/v1"

	disruptionutils "sigs.k8s.io/karpenter/pkg/utils/disruption"
)

// ComputeNodePoolMetrics computes aggregate cost and disruption across all candidates.
func ComputeNodePoolMetrics(ctx context.Context, candidates []*Candidate) (totalCost, totalDisruption float64) {
	for _, candidate := range candidates {
		if candidate.instanceType != nil && len(candidate.instanceType.Offerings) > 0 {
			totalCost += candidate.instanceType.Offerings[0].Price
		}
		totalDisruption += ComputeNodeDisruptionCost(ctx, candidate.reschedulablePods)
	}
	return totalCost, totalDisruption
}

// ComputeMoveScore computes the RFC-aligned per-move consolidation score.
//
//	score = savings_fraction / disruption_fraction
//
// Where:
//   - savings_fraction = (deleted_cost - replacement_cost) / nodepool_total_cost
//   - disruption_fraction = move_disruption_cost / nodepool_total_disruption_cost
//
// For DELETE moves, replacement_cost is 0.
// Returns +Inf when disruption is zero (move is free).
func ComputeMoveScore(
	ctx context.Context,
	deletedCost, replacementCost, totalCost float64,
	candidates []*Candidate,
	totalDisruption float64,
) float64 {
	if totalCost == 0 {
		return 0
	}
	savingsFraction := (deletedCost - replacementCost) / totalCost
	if savingsFraction <= 0 {
		return 0
	}

	// Sum disruption cost of all pods being moved
	moveDisruption := 0.0
	for _, c := range candidates {
		moveDisruption += ComputeNodeDisruptionCost(ctx, c.reschedulablePods)
	}

	if totalDisruption == 0 || moveDisruption == 0 {
		return math.Inf(1)
	}

	disruptionFraction := moveDisruption / totalDisruption
	return savingsFraction / disruptionFraction
}

// ComputeNodeDisruptionCost computes the total disruption cost for a node's pods.
// Per the RFC, there is no per-node baseline cost — only pod eviction costs.
func ComputeNodeDisruptionCost(ctx context.Context, pods []*corev1.Pod) float64 {
	cost := 0.0
	for _, p := range pods {
		evictionCost := disruptionutils.EvictionCost(ctx, p)
		if evictionCost > 0 {
			cost += evictionCost
		}
	}
	return cost
}
