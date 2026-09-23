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
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"time"
)

// Absolute per-sample thresholds are OPT-IN, per test case, through the
// KARPENTER_PERF_THRESHOLDS environment variable, keyed by "<testCase>/<phase>".
//
// A test case named in the table is asserted. A metric the entry sets uses that
// value; a metric it omits uses the inline base default at the call site. So
//   {"basic/scaleOut": {}}                     assert basic/scaleOut at its inline defaults
//   {"basic/scaleOut": {"memory_mb": 400}}     assert it, memory bound raised to 400 MB
//
// A test case ABSENT from the table is not asserted at all, and a batch with no
// variable set asserts nothing. Upstream gates on the relative, multi-test
// statistic in .github/scripts/perf-aggregate instead, which decides on a batch
// median rather than on one sample. Providers that need a hard per-sample bound,
// notably the downstream Hydra suites, opt in by naming their test cases here.
//
// Why opt-in. A one-sample absolute bound is a hypothesis test with n=1 whose
// size is set by the bound's headroom over the clean median, not by any chosen
// alpha. Measured on the Basic suite that headroom is +7.18 pct on scale-out p95
// memory and +0.50 pct on consolidation p95 memory, so 4 of 10 clean legs tripped
// it; and a tripped assertion deletes the whole iteration directory on retry, so
// it destroys the sample the relative gate needed. See
// drafts/2026-09-23/mde-unclamped/unclamped-mde.md.

// thresholdOverride is the per-test-case, per-metric absolute override. A nil
// field means "no override for this metric; use the inline base default".
type thresholdOverride struct {
	MemoryMB         *float64 `json:"memory_mb,omitempty"`          // peak memory upper bound, MB
	CPUCores         *float64 `json:"cpu_cores,omitempty"`          // avg CPU upper bound, cores
	TotalTimeMinutes *float64 `json:"total_time_minutes,omitempty"` // total time upper bound, minutes
	CPUUtil          *float64 `json:"cpu_util,omitempty"`           // reserved CPU utilization lower bound, fraction
	MemoryUtil       *float64 `json:"memory_util,omitempty"`        // reserved memory utilization lower bound, fraction
}

var (
	overridesOnce sync.Once
	overrides     map[string]thresholdOverride
	overridesErr  error
)

// loadOverrides parses the override table once from the KARPENTER_PERF_THRESHOLDS
// environment variable. When it is unset the table is empty and all thresholds
// use their defaults.
func loadOverrides() (map[string]thresholdOverride, error) {
	overridesOnce.Do(func() {
		inline, ok := os.LookupEnv("KARPENTER_PERF_THRESHOLDS")
		if !ok || inline == "" {
			overrides = map[string]thresholdOverride{}
			return
		}
		dec := json.NewDecoder(strings.NewReader(inline))
		dec.DisallowUnknownFields()
		if overridesErr = dec.Decode(&overrides); overridesErr != nil {
			overridesErr = fmt.Errorf("parsing performance threshold overrides: %w", overridesErr)
		}
	})
	return overrides, overridesErr
}

// override returns the parsed override for a key. It panics on malformed
// configuration so a typo surfaces as an immediate suite failure rather than
// silently letting tests run against the wrong thresholds.
func override(key string) (thresholdOverride, bool) {
	table, err := loadOverrides()
	if err != nil {
		panic(err)
	}
	o, ok := table[key]
	return o, ok
}

// Sentinels an assertion cannot fail, returned for a test case that has not
// opted in. The upper-bound helpers return +Inf and the lower-bound helpers
// -Inf, so the Expect stays in the source, documenting the bound, without
// deciding anything. neverSlower is the Duration equivalent of +Inf, about 292
// years, because a Duration is an int64 of nanoseconds and cannot hold an
// infinity.
const neverSlower = time.Duration(math.MaxInt64)

var (
	neverAbove = math.Inf(1)
	neverBelow = math.Inf(-1)
)

func MemoryThreshold(key string, base float64) float64 {
	o, ok := override(key)
	if !ok {
		return neverAbove
	}
	if o.MemoryMB != nil {
		return *o.MemoryMB
	}
	return base
}

func CPUThreshold(key string, base float64) float64 {
	o, ok := override(key)
	if !ok {
		return neverAbove
	}
	if o.CPUCores != nil {
		return *o.CPUCores
	}
	return base
}

func TotalTimeThreshold(key string, base time.Duration) time.Duration {
	o, ok := override(key)
	if !ok {
		return neverSlower
	}
	if o.TotalTimeMinutes != nil {
		return time.Duration(*o.TotalTimeMinutes * float64(time.Minute))
	}
	return base
}

func CPUUtilThreshold(key string, base float64) float64 {
	o, ok := override(key)
	if !ok {
		return neverBelow
	}
	if o.CPUUtil != nil {
		return *o.CPUUtil
	}
	return base
}

func MemoryUtilThreshold(key string, base float64) float64 {
	o, ok := override(key)
	if !ok {
		return neverBelow
	}
	if o.MemoryUtil != nil {
		return *o.MemoryUtil
	}
	return base
}
