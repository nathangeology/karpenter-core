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
	"testing"

	"k8s.io/apimachinery/pkg/util/sets"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

func TestSortCandidatesBySavingsRatio(t *testing.T) {
	np := &v1.NodePool{}
	np.Name = "test"

	// A: price=10 disruption=2 -> ratio=5
	a := &Candidate{NodePool: np, Price: 10, RescheduleDisruptionCost: 2}
	// B: price=1 disruption=5 -> ratio=0.2
	b := &Candidate{NodePool: np, Price: 1, RescheduleDisruptionCost: 5}
	// C: price=6 disruption=3 -> ratio=2
	c := &Candidate{NodePool: np, Price: 6, RescheduleDisruptionCost: 3}

	candidates := []*Candidate{b, c, a}
	result := SortCandidatesBySavingsRatio(candidates)

	if result[0] != a {
		t.Errorf("expected first candidate to be A (ratio 5), got ratio %f", result[0].SavingsRatio())
	}
	if result[1] != c {
		t.Errorf("expected second candidate to be C (ratio 2), got ratio %f", result[1].SavingsRatio())
	}
	if result[2] != b {
		t.Errorf("expected third candidate to be B (ratio 0.2), got ratio %f", result[2].SavingsRatio())
	}
}

func TestInterleaveCandidatesByNodePool(t *testing.T) {
	npA := &v1.NodePool{}
	npA.Name = "pool-a"
	npB := &v1.NodePool{}
	npB.Name = "pool-b"

	a1 := &Candidate{NodePool: npA, Price: 10, RescheduleDisruptionCost: 1}
	a2 := &Candidate{NodePool: npA, Price: 8, RescheduleDisruptionCost: 1}
	b1 := &Candidate{NodePool: npB, Price: 5, RescheduleDisruptionCost: 1}

	candidates := []*Candidate{a1, a2, b1}

	t.Run("no priority pools", func(t *testing.T) {
		result := InterleaveCandidatesByNodePool(candidates, sets.New[string]())
		if len(result) != 3 {
			t.Fatalf("expected 3 candidates, got %d", len(result))
		}
		// Round-robin interleaving: first from each pool, then second from each
		pools := make([]string, len(result))
		for i, c := range result {
			pools[i] = c.NodePool.Name
		}
		// Both pools should appear in first two positions (round-robin)
		if pools[0] == pools[1] {
			t.Errorf("first two candidates should be from different pools, got %v", pools)
		}
	})

	t.Run("priority pool first", func(t *testing.T) {
		result := InterleaveCandidatesByNodePool(candidates, sets.New("pool-b"))
		if result[0].NodePool.Name != "pool-b" {
			t.Errorf("expected priority pool first, got %s", result[0].NodePool.Name)
		}
	})

	t.Run("empty input", func(t *testing.T) {
		result := InterleaveCandidatesByNodePool(nil, sets.New[string]())
		if len(result) != 0 {
			t.Errorf("expected empty result for nil input, got %d", len(result))
		}
	})
}
