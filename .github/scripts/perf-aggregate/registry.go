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

package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

// The registry is the committed record of every key this gate knows about:
// which ones it compares, at what threshold, and which ones it deliberately
// does not compare and why.
//
// It exists because benchmark-action silently skips any key it cannot match in
// the stored history. A key that is absent from the emitted file and a key that
// was screened on purpose produce the same green step, and so does a key whose
// baseline was evicted. Without a list held outside the cache those three are
// indistinguishable. With one, an unregistered key is an error, a screened key
// is named in the log, and the expected key set is a git diff rather than a
// property of a cache blob.
//
// Grammar. One record per non-blank line, exactly three whitespace-separated
// fields, optional trailing # comment:
//
//	<phase-key>  <field>  <value>
//
// <field> is either a report JSON field, in which case <value> is a
// benchmark-action alert ratio above 1 or the literal screenSentinel, or one of
// the reserved fields below.
const (
	// screenSentinel screens a key out of the gating steps and records that the
	// screen was deliberate. The key still reaches the observe-only and CV
	// steps, so its value keeps accumulating history and can be charted; no
	// step can fail on it.
	screenSentinel = "none"

	// fieldTimeout is the timeout argument at the phase's Report* call site, in
	// seconds. It is not recoverable from the reports: PerformanceReport has no
	// timeout field, so the value only exists as a source constant at the call
	// site and the registry has to carry it.
	fieldTimeout = "timeout_seconds"

	// fieldAssertMax is the suite's own absolute total_time bound for the
	// phase, in seconds, from the TotalTimeThreshold call site.
	fieldAssertMax = "assert_max_seconds"

	// registryParamKey is the reserved phase key holding file-level parameters.
	registryParamKey = "_gate"

	// paramCPUCoreCap is the controller CPU limit. Carried so the aggregator can
	// compute each key's ceiling from the batch it just took rather than from a
	// static table: the baseline moves by a factor of two across environments,
	// which moves the ceiling with it.
	paramCPUCoreCap = "cpu_core_cap"

	// paramDerivedForN is the sample count the thresholds were derived for. The
	// thresholds bound the dispersion of the batch MEDIAN at that n, which is 3
	// to 10 times smaller than the dispersion of one sample. Running them
	// against a smaller batch compares a statistic far noisier than the one they
	// were sized for.
	//
	// Measured, and the reason this parameter exists. Replayed on 33 real
	// upstream main runs, which take one iteration each, the shipped thresholds
	// fire on 93.8% of them. The flat 1.50 they replace fires on 40.6% of the
	// same runs. At n=10 the same thresholds carry a 0.49% union false-positive
	// rate. The thresholds are not wrong; they are inseparable from the sample
	// design, and a registry that can be pointed at a one-sample batch is a
	// registry that will be.
	paramDerivedForN = "derived_for_n"
)

// censorFraction is the largest share of a key's samples that may be censored
// before the key is reported unmeasurable and screened out of the gating steps.
// Set so 2 of 10 is tolerable and 3 of 10 is not. A censored sample is not a
// slow measurement; it is a measurement that stopped on a bound and reported
// the bound as its value.
const censorFraction = 0.20

// saturationFraction is the share of the measurement timeout at which a sample
// is treated as censored. Below 1.0 because the recorded total_time straddles
// the bound: ReportConsolidation spends the pod wait before entering the
// monitor loop, so a saturated sample reads A + timeout + overshoot, while the
// loop's own exit can land just under. Measured on the six consolidation keys,
// 0.95 separates the saturated key (19 of 19 samples, 922.9 to 977.9 s against
// a 900 s timeout) from the rest.
const saturationFraction = 0.95

// gateRegistry is the parsed registry.
type gateRegistry struct {
	path string
	// gated[phaseKey][jsonField] is the alert ratio for a key that compares.
	gated map[string]map[string]float64
	// screened[phaseKey][jsonField] is the provenance comment for a key the
	// registry screens with screenSentinel. Present exactly when the key is
	// registered and not gated.
	screened  map[string]map[string]string
	timeouts  map[string]float64
	assertMax map[string]float64
	params    map[string]float64
}

// registered reports whether the registry has any record of the pair.
func (r *gateRegistry) registered(phase, field string) bool {
	if _, ok := r.gated[phase][field]; ok {
		return true
	}
	_, ok := r.screened[phase][field]
	return ok
}

// threshold returns the alert ratio for a gated pair. ok is false when the pair
// is screened.
func (r *gateRegistry) threshold(phase, field string) (float64, bool) {
	t, ok := r.gated[phase][field]
	return t, ok
}

// phaseKeys returns every phase key the registry mentions, sorted.
func (r *gateRegistry) phaseKeys() []string {
	seen := map[string]bool{}
	for k := range r.gated {
		seen[k] = true
	}
	for k := range r.screened {
		seen[k] = true
	}
	for k := range r.timeouts {
		seen[k] = true
	}
	for k := range r.assertMax {
		seen[k] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// loadRegistry parses the registry at path. A missing file yields a nil
// registry and no error, which disables every registry-driven behaviour and
// leaves the aggregator at its pre-registry defaults. That keeps local
// invocations and the Regression suite's caller working with no file present.
func loadRegistry(path string) (*gateRegistry, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path) //nolint:gosec // G304: path comes from the workflow, not from report data
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // read-only
	reg, err := parseRegistry(path, f)
	if err != nil {
		return nil, err
	}
	return reg, nil
}

func parseRegistry(path string, in io.Reader) (*gateRegistry, error) {
	r := &gateRegistry{
		path:      path,
		gated:     map[string]map[string]float64{},
		screened:  map[string]map[string]string{},
		timeouts:  map[string]float64{},
		assertMax: map[string]float64{},
		params:    map[string]float64{},
	}
	sc := bufio.NewScanner(in)
	for lineNo := 1; sc.Scan(); lineNo++ {
		line := sc.Text()
		// The comment is provenance, not decoration: it is what a reviewer
		// reads to find out why a key is screened, so it is kept rather than
		// discarded.
		var comment string
		if i := strings.Index(line, "#"); i >= 0 {
			comment = strings.TrimSpace(line[i+1:])
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 3 {
			return nil, fmt.Errorf("%s:%d: got %d fields, want 3 (<phase-key> <field> <value>)", path, lineNo, len(fields))
		}
		phase, field, value := fields[0], fields[1], fields[2]
		if phase == registryParamKey {
			if field != paramCPUCoreCap && field != paramDerivedForN {
				return nil, fmt.Errorf("%s:%d: unknown %s parameter %q", path, lineNo, registryParamKey, field)
			}
			v, err := strconv.ParseFloat(value, 64)
			if err != nil || v <= 0 {
				return nil, fmt.Errorf("%s:%d: %s %s=%q: want a positive number", path, lineNo, registryParamKey, field, value)
			}
			r.params[field] = v
			continue
		}
		switch field {
		case fieldTimeout, fieldAssertMax:
			v, err := strconv.ParseFloat(value, 64)
			if err != nil || v <= 0 {
				return nil, fmt.Errorf("%s:%d: %s %s=%q: want a positive number of seconds", path, lineNo, phase, field, value)
			}
			dst := r.timeouts
			if field == fieldAssertMax {
				dst = r.assertMax
			}
			if _, dup := dst[phase]; dup {
				return nil, fmt.Errorf("%s:%d: duplicate %s for %s", path, lineNo, field, phase)
			}
			dst[phase] = v
			continue
		}
		if r.registered(phase, field) {
			return nil, fmt.Errorf("%s:%d: duplicate entry for %s %s", path, lineNo, phase, field)
		}
		if value == screenSentinel {
			if r.screened[phase] == nil {
				r.screened[phase] = map[string]string{}
			}
			// An empty provenance comment is accepted but recorded as such, so
			// the log says the screen was deliberate even when nobody said why.
			if comment == "" {
				comment = "no reason recorded"
			}
			r.screened[phase][field] = comment
			continue
		}
		t, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %s %s=%q: want a ratio above 1 or %q", path, lineNo, phase, field, value, screenSentinel)
		}
		// A threshold of exactly 1 alerts on a ratio of 1.000, which several
		// keys report on every sample because their value is quantized. Reject
		// it at parse time rather than shipping a step that fires on no change.
		if t <= 1.0 {
			return nil, fmt.Errorf("%s:%d: %s %s=%v: a threshold at or below 1 alerts on no change", path, lineNo, phase, field, t)
		}
		if r.gated[phase] == nil {
			r.gated[phase] = map[string]float64{}
		}
		r.gated[phase][field] = t
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if len(r.gated) == 0 && len(r.screened) == 0 {
		return nil, fmt.Errorf("%s: no key entries found", path)
	}
	return r, nil
}

// censorResult records what the censoring screen did to one phase key.
type censorResult struct {
	kept     int
	censored int
	// reason is non-empty when the key is unmeasurable, meaning too large a
	// share of its samples stopped on a bound for the batch median to mean
	// anything. Such a key is screened out of the gating steps at run time.
	reason string
}

// censor drops samples whose total_time reached a bound, and reports which keys
// lost enough samples to be unmeasurable.
//
// Two bounds, because there are two ways a recorded total_time can be a bound
// rather than a measurement.
//
// The measurement timeout. ReportConsolidation's monitor loop and ReportDrift's
// both exit on `for time.Since(start) < timeout` and then return normally, so
// the phase records the clock. Measured on self_antiaffinity_interference_-
// consolidation: 19 of 19 samples between 922.9 and 977.9 s against a 900 s
// timeout, sample CV 1.23%. A CV screen cannot catch that, which is the reason
// this screen is not a CV screen. ReportScaleOut instead blocks on
// EventuallyExpectHealthyPodCountWithTimeout, which aborts the spec, so a
// saturated scale-out sample is a lost leg and never reaches the batch.
//
// The suite's own absolute bound. A sample above TotalTimeThreshold would have
// failed the leg's inline assertion, so it reaches the batch only when a
// provider has widened that bound through KARPENTER_PERF_THRESHOLDS. Measured
// on self_antiaffinity_scale_out_small: one sample at 456.4 s against a 300 s
// bound, in a batch whose other nine sit between 123.4 and 149.6 s. Including
// it moves the sample CV from 6.6% to 59.3%. Censoring on this bound is what
// keeps the relative gate's statistic independent of whether the absolute
// thresholds were overridden.
func (r *gateRegistry) censor(byTest map[string][]map[string]any, out io.Writer) (map[string][]map[string]any, map[string]censorResult) {
	results := map[string]censorResult{}
	kept := map[string][]map[string]any{}
	for _, testKey := range sortedKeys(byTest) {
		phase := phaseKey(testKey)
		satAt, hasSat := 0.0, false
		if t, ok := r.timeouts[phase]; ok {
			satAt, hasSat = saturationFraction*t, true
		}
		capAt, hasCap := r.assertMax[phase]
		var keep []map[string]any
		res := censorResult{}
		for _, data := range byTest[testKey] {
			vals := extractValues([]map[string]any{data}, "total_time")
			if len(vals) == 1 {
				tt := vals[0]
				switch {
				case hasSat && tt >= satAt:
					res.censored++
					fmt.Fprintf(out, "  censored %s total_time=%.1fs at or above %.0f%% of the %.0fs measurement timeout\n",
						phase, tt, saturationFraction*100, r.timeouts[phase])
					continue
				case hasCap && tt >= capAt:
					res.censored++
					fmt.Fprintf(out, "  censored %s total_time=%.1fs at or above the suite's own %.0fs bound\n", phase, tt, capAt)
					continue
				}
			}
			keep = append(keep, data)
		}
		res.kept = len(keep)
		total := res.kept + res.censored
		if res.censored > 0 && float64(res.censored) > censorFraction*float64(total) {
			res.reason = fmt.Sprintf("%d of %d samples censored, above the %.0f%% ceiling", res.censored, total, censorFraction*100)
			fmt.Fprintf(out, "::warning title=Performance key unmeasurable::%s: %s. Screened out of the gate for this run.\n", phase, res.reason)
		}
		if res.censored > 0 {
			results[phase] = res
		}
		if len(keep) > 0 {
			kept[testKey] = keep
		} else if total > 0 {
			// Nothing survived. The key emits nothing at all, which is the
			// correct disposition for a phase that only ever records its
			// timeout: not a screened comparison, no comparison.
			fmt.Fprintf(out, "::warning title=Performance key unmeasurable::%s: every one of its %d samples was censored. It emits no entry.\n", phase, total)
		}
	}
	return kept, results
}

// ceiling returns the largest ratio a capped metric can report given the batch
// it just measured, and whether the metric is capped at all.
//
// HELM_OPTS limits the controller to cpu_core_cap cores (Makefile:9), so
// karpenter_p95_cpu_cores is a rate bounded above. The largest ratio a key can
// ever report is cap/baseline. When the shipped threshold sits at or above that,
// no regression however large can trip the step and its pass carries no
// information. Recomputed per run rather than tabulated because the baseline is
// not stable across environments: hostname_spread_consolidation reads 1.456
// cores on the fresh-runner batch, 0.752 in the positive control and 1.560 on
// upstream main, which moves its ceiling from 1.37 to 2.66.
func (r *gateRegistry) ceiling(m metricSpec, median float64) (float64, bool) {
	if m.cappedBy == "" || median <= 0 {
		return 0, false
	}
	limit, ok := r.params[m.cappedBy]
	if !ok {
		return 0, false
	}
	return limit / median, true
}

func phaseKey(testKey string) string {
	return strings.TrimSuffix(testKey, reportSuffix)
}
