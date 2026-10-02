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
	"math"
	"testing"

	disruptionutils "sigs.k8s.io/karpenter/pkg/utils/disruption"
)

// IsEmpty is inclusive at PerNodeBaseDisruptionCost: that value is the floor
// ComputeRescheduleDisruptionCost returns for a node with no reschedulable pods,
// so a node sitting exactly on it is the empty case rather than the first
// non-empty one. The exact-boundary rows are what fail if the comparator is
// changed from <= to <; the rows either side of it only check the direction.
func TestCandidateIsEmptyAtBaseDisruptionCostBoundary(t *testing.T) {
	tests := []struct {
		name     string
		cost     float64
		expected bool
	}{
		{
			name:     "zero cost",
			cost:     0,
			expected: true,
		},
		{
			name:     "below the base cost",
			cost:     disruptionutils.PerNodeBaseDisruptionCost / 2,
			expected: true,
		},
		{
			name:     "exactly the base cost",
			cost:     disruptionutils.PerNodeBaseDisruptionCost,
			expected: true,
		},
		{
			name:     "one float step above the base cost",
			cost:     math.Nextafter(disruptionutils.PerNodeBaseDisruptionCost, math.Inf(1)),
			expected: false,
		},
		{
			name:     "well above the base cost",
			cost:     disruptionutils.PerNodeBaseDisruptionCost + 1,
			expected: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// IsEmpty reads RescheduleDisruptionCost only, so the rest of the
			// Candidate stays zero-valued.
			c := &Candidate{RescheduleDisruptionCost: tc.cost}
			if got := c.IsEmpty(); got != tc.expected {
				t.Errorf("IsEmpty() with RescheduleDisruptionCost %v = %v, want %v", tc.cost, got, tc.expected)
			}
		})
	}
}
