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

package performance

import (
	"math"
	"sync"
	"testing"
	"time"
)

// resetOverrides clears the memoised table so each case parses its own
// environment. loadOverrides goes through a sync.Once, so without this the first
// case would fix the table for the rest.
func resetOverrides() {
	overridesOnce = sync.Once{}
	overrides = nil
	overridesErr = nil
}

// TestOptInThresholds covers the three states of the opt-in table on the five
// helpers: no entry means no assertion, an empty entry means assert at the inline
// base default, and a set metric wins while its siblings still fall back.
func TestOptInThresholds(t *testing.T) {
	const key = "basic/scaleOut"
	cases := []struct {
		name  string
		env   string
		mem   float64
		cpu   float64
		cpuU  float64
		memU  float64
		total time.Duration
	}{
		{
			name: "unset table asserts nothing",
			env:  "",
			mem:  math.Inf(1), cpu: math.Inf(1),
			cpuU: math.Inf(-1), memU: math.Inf(-1),
			total: neverSlower,
		},
		{
			name: "empty entry opts in at the inline defaults",
			env:  `{"basic/scaleOut":{}}`,
			mem:  260, cpu: 0.50, cpuU: 0.53, memU: 0.65,
			total: 2 * time.Minute,
		},
		{
			name: "set metric wins, siblings fall back to the inline default",
			env:  `{"basic/scaleOut":{"memory_mb":400}}`,
			mem:  400, cpu: 0.50, cpuU: 0.53, memU: 0.65,
			total: 2 * time.Minute,
		},
		{
			name: "a key absent from a non-empty table still asserts nothing",
			env:  `{"basic/consolidation":{}}`,
			mem:  math.Inf(1), cpu: math.Inf(1),
			cpuU: math.Inf(-1), memU: math.Inf(-1),
			total: neverSlower,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetOverrides()
			t.Setenv("KARPENTER_PERF_THRESHOLDS", tc.env)
			checkFloat(t, "MemoryThreshold", MemoryThreshold(key, 260), tc.mem)
			checkFloat(t, "CPUThreshold", CPUThreshold(key, 0.50), tc.cpu)
			checkFloat(t, "CPUUtilThreshold", CPUUtilThreshold(key, 0.53), tc.cpuU)
			checkFloat(t, "MemoryUtilThreshold", MemoryUtilThreshold(key, 0.65), tc.memU)
			if got := TotalTimeThreshold(key, 2*time.Minute); got != tc.total {
				t.Errorf("TotalTimeThreshold = %v, want %v", got, tc.total)
			}
		})
	}
}

func checkFloat(t *testing.T, name string, got, want float64) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

// TestOptInListedKeyStillAsserts pins the half of the last table case that the
// table itself cannot express: the key that IS listed keeps its bounds.
func TestOptInListedKeyStillAsserts(t *testing.T) {
	resetOverrides()
	t.Setenv("KARPENTER_PERF_THRESHOLDS", `{"basic/consolidation":{}}`)
	if got := MemoryThreshold("basic/consolidation", 260); got != 260 {
		t.Errorf("listed key MemoryThreshold = %v, want 260", got)
	}
}
