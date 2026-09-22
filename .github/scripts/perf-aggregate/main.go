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

// Command perf-aggregate consumes per-iteration performance reports produced
// by the Karpenter e2e performance suite and emits benchmark-action inputs
// plus a detailed statistical summary.
//
// Environment:
//
//	OUTPUT_DIR          required; holds one directory per sample, each with
//	                    *_performance_report.json inputs, and receives the
//	                    emitted benchmark-action files
//	ITERATIONS          expected sample count, default 1
//	MIN_ITERATIONS      quorum; refuse to gate below this many samples per
//	                    test key. Defaults to ITERATIONS, which is the
//	                    all-or-nothing rule. The workflow sets it lower,
//	                    because samples now arrive from independent runners
//	                    and one lost runner must not forfeit the batch
//	BASELINE_DIR        optional; restored actions/cache tree holding the
//	                    per-tier benchmark-action history. When set, the
//	                    baseline check runs and can fail the command
//	BASELINE_STATE_DIR  optional; where the baseline state file lives.
//	                    Defaults to BASELINE_DIR, which is where it used to
//	                    live and is the wrong place: a cache eviction then
//	                    takes the history and the record of what was gated
//	                    together, and every key reads as a legitimate first
//	                    seed. The workflow points this at a separately cached
//	                    directory
//	BASELINE_CACHE_HIT  optional; the cache-matched-key output of the
//	                    actions/cache step that restored BASELINE_DIR. When
//	                    non-empty the scope is known to be populated, so an
//	                    absent state file is eviction rather than a first run
//	                    and is an error
//	BENCH_NAME_PREFIX   required when BASELINE_DIR is set; the chart-name
//	                    prefix the workflow passes to benchmark-action
//	GITHUB_RUN_ID       recorded in the baseline state file for provenance
//	GATE_KEYS_FILE      optional; the committed gate-key registry. When set and
//	                    present, every gateable field of every phase key in the
//	                    batch must have an entry, and the entry decides whether
//	                    the key gates and at what threshold. Absent file means
//	                    no registry: nothing gates, everything is observed. See
//	                    registry.go
//	ALLOW_BASELINE_SEED optional; "true" permits a run to seed a key that has no
//	                    stored baseline. Default is to refuse, which is what
//	                    turns a double cache eviction from a green gate that
//	                    compared nothing into a red one
//
// One benchmark-action file is emitted per gated field, plus one observe-only
// file for the fields the registry screens and one informational file for batch
// CV. Direction is a per-tool rather than a per-field property in
// github-action-benchmark, so each step names the tool its field needs.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
)

type direction int

const (
	smallerIsBetter direction = iota
	biggerIsBetter
)

// metricSpec describes how to extract, label, and route one field from a
// performance report.
//
// benchmark-action takes one alert-threshold per step, so a field can only
// carry its own threshold by getting its own step. Grouping fields into two
// shared tiers, which is what this file did before, forces every field in a
// tier to the loosest threshold any of its keys needs. Measured over the seven
// gated phase keys that is the difference between a median threshold of 1.09
// and the flat 1.50 the gate shipped, and the flat 1.50 fires on 40.6% of clean
// upstream main runs. One step per field is the smallest change that lets the
// registry's numbers reach benchmark-action.
type metricSpec struct {
	jsonField string
	display   string
	unit      string
	dir       direction
	// slug names the Compare step, its results file, and its cache subdirectory.
	// Empty means the field cannot gate under any registry: it reaches the CV
	// step only. Those two are kept out of the registry entirely rather than
	// registered and screened, because no evidence would ever move them.
	slug string
	// cappedBy names the registry parameter that bounds this field from above,
	// if any. A bounded field's largest reportable ratio is cap/baseline, so a
	// threshold at or above that makes the step unfalsifiable.
	cappedBy string
}

var metrics = []metricSpec{
	{jsonField: "total_time", display: "Duration", unit: "seconds", dir: smallerIsBetter, slug: "duration"},
	{jsonField: "karpenter_p95_memory_mb", display: "Controller Peak Memory", unit: "MB", dir: smallerIsBetter, slug: "peak_memory"},
	{jsonField: "karpenter_p95_cpu_cores", display: "Controller CPU", unit: "cores", dir: smallerIsBetter, slug: "controller_cpu", cappedBy: paramCPUCoreCap},
	// Sustained controller CPU, emitted for visibility only. @ryan-mist's
	// per-test threshold proposal (PR#2994 comment 4012042309) needs this
	// statistic in the report; which CPU key gates, and at what threshold,
	// is still open, so it stays out of the registry and out of every gate file.
	{jsonField: "karpenter_p50_cpu_cores", display: "Controller CPU P50", unit: "cores", dir: smallerIsBetter},
	{jsonField: "total_nodes", display: "Final Nodes", unit: "nodes", dir: smallerIsBetter, slug: "final_nodes"},
	{jsonField: "total_reserved_cpu_utilization", display: "CPU Utilization", unit: "percent", dir: biggerIsBetter, slug: "cpu_util"},
	{jsonField: "resource_efficiency_score", display: "Efficiency Score", unit: "score", dir: biggerIsBetter, slug: "efficiency_score"},
	{jsonField: "total_reserved_memory_utilization", display: "Memory Utilization", unit: "percent", dir: biggerIsBetter, slug: "memory_util"},
	// Consolidation Rounds: integer 0-9 with a per-test P90 CV of 149%, and it
	// is the poll counter the consolidation duration aliases rather than an
	// independent measurement. CV step only.
	{jsonField: "rounds", display: "Consolidation Rounds", unit: "rounds", dir: smallerIsBetter},
}

// gatedMetrics returns the fields a registry can gate, in declaration order.
func gatedMetrics() []metricSpec {
	out := make([]metricSpec, 0, len(metrics))
	for _, m := range metrics {
		if m.slug != "" {
			out = append(out, m)
		}
	}
	return out
}

type stats struct {
	N      int     `json:"n"`
	Mean   float64 `json:"mean"`
	Median float64 `json:"median"`
	Stddev float64 `json:"stddev"`
	CVPct  float64 `json:"cv_pct"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
}

// benchmarkEntry matches the schema consumed by
// benchmark-action/github-action-benchmark for the customSmallerIsBetter
// and customBiggerIsBetter tools.
type benchmarkEntry struct {
	Name  string  `json:"name"`
	Unit  string  `json:"unit"`
	Value float64 `json:"value"`
	Range string  `json:"range,omitempty"`
	Extra string  `json:"extra,omitempty"`
}

func main() {
	outputDir := os.Getenv("OUTPUT_DIR")
	if outputDir == "" {
		fmt.Fprintln(os.Stderr, "OUTPUT_DIR is required")
		os.Exit(2)
	}
	iterations, err := positiveEnv("ITERATIONS", 1)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	// Quorum defaults to the full count, which is the behaviour before samples
	// were spread across runners.
	minIterations, err := positiveEnv("MIN_ITERATIONS", iterations)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if minIterations > iterations {
		fmt.Fprintf(os.Stderr, "MIN_ITERATIONS=%d exceeds ITERATIONS=%d\n", minIterations, iterations)
		os.Exit(2)
	}
	registry, err := loadRegistry(os.Getenv("GATE_KEYS_FILE"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := run(outputDir, iterations, minIterations, registry, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// BASELINE_DIR is set only by the workflow, after actions/cache has had a
	// chance to restore. Local invocations leave it unset and skip the check.
	if baselineDir := os.Getenv("BASELINE_DIR"); baselineDir != "" {
		stateDir := os.Getenv("BASELINE_STATE_DIR")
		if stateDir == "" {
			stateDir = baselineDir
		}
		err := checkBaseline(baselineConfig{
			outputDir:   outputDir,
			baselineDir: baselineDir,
			stateDir:    stateDir,
			namePrefix:  os.Getenv("BENCH_NAME_PREFIX"),
			runID:       os.Getenv("GITHUB_RUN_ID"),
			cacheHit:    os.Getenv("BASELINE_CACHE_HIT"),
			registry:    registry,
			allowSeed:   os.Getenv("ALLOW_BASELINE_SEED") == "true",
		}, os.Stdout)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}

// positiveEnv reads a positive integer from the environment, falling back to
// def when the variable is unset or empty.
func positiveEnv(name string, def int) (int, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("invalid %s=%q: want a positive integer", name, v)
	}
	return n, nil
}

// run performs the aggregation and returns nil on success. It is separated
// from main so tests can drive it with synthetic input.
func run(outputDir string, iterations, minIterations int, registry *gateRegistry, out io.Writer) error {
	reportsByTest, samples, err := collectReports(outputDir)
	if err != nil {
		return err
	}
	// Fail closed when no reports were found. Emitting empty benchmark files
	// with exit 0 would let the pipeline promote a run that gathered no
	// data, so surface it as an aggregator failure instead.
	if len(reportsByTest) == 0 {
		return fmt.Errorf("no performance reports found under %s (expected %d sample directories)", outputDir, iterations)
	}
	// The censoring screen runs before the quorum check and before any
	// statistic is computed, because a censored sample is not a datum. It is a
	// bound the phase stopped on, and averaging it into a batch median is how a
	// key that measures nothing returns a tight CV. See gateRegistry.censor.
	censored := map[string]censorResult{}
	if registry != nil {
		// The thresholds and the sample design are one artefact. Refuse rather
		// than warn: a warning on a gate that fires on nine runs in ten is a
		// warning nobody will read twice.
		if n, ok := registry.params[paramDerivedForN]; ok && float64(iterations) < n {
			return fmt.Errorf(
				"%s declares %s=%.0f but ITERATIONS=%d: its thresholds bound the dispersion of a %.0f-sample median, which is far tighter than a %d-sample one. Refusing to gate",
				registry.path, paramDerivedForN, n, iterations, n, iterations)
		}
		fmt.Fprintf(out, "Censoring screen against %s\n", registry.path)
		reportsByTest, censored = registry.censor(reportsByTest, out)
		if len(reportsByTest) == 0 {
			return fmt.Errorf("every sample of every key was censored: no key under %s produced a measurement", outputDir)
		}
	}
	// Per-test quorum check. A silent partial batch would let a batch-median
	// regression slip through when half the samples went missing, so a thin
	// batch is refused. The floor is MIN_ITERATIONS rather than ITERATIONS
	// because samples arrive from independent runners: at the upstream 19%
	// per-job failure rate, an all-or-nothing rule leaves a 10-sample batch
	// complete only 12% of the time. A batch between the quorum and the full
	// count gates, and says so, so the n it gated on is on the record.
	fmt.Fprintf(out, "Collected %d sample director%s under %s: %s\n",
		len(samples), plural(len(samples), "y", "ies"), outputDir, strings.Join(samples, " "))
	for _, testKey := range sortedKeys(reportsByTest) {
		n := len(reportsByTest[testKey])
		// An unmeasurable key is exempt. It lost samples to the censoring
		// screen and is already screened out of the gating steps, so failing
		// the whole run on its thin batch would turn a screened key into an
		// outage.
		if censored[phaseKey(testKey)].reason != "" {
			continue
		}
		switch {
		case n < minIterations:
			return fmt.Errorf(
				"%s: got %d/%d samples, below the quorum of %d; refusing to gate on partial data",
				testKey, n, iterations, minIterations,
			)
		case n < iterations:
			fmt.Fprintf(out, "::warning title=Performance batch short::%s gated on %d of %d samples (quorum %d). Its threshold was derived for %d.\n",
				testKey, n, iterations, minIterations, iterations)
		}
	}

	// Iterate test keys in a stable order so the emitted arrays and the
	// printed table are reproducible across runs.
	testKeys := sortedKeys(reportsByTest)

	summary, results, err := buildResults(testKeys, reportsByTest, registry, censored, out)
	if err != nil {
		return err
	}

	for _, m := range gatedMetrics() {
		if err := writeJSON(filepath.Join(outputDir, resultsFileFor(m.slug)), results.gated[m.slug]); err != nil {
			return err
		}
	}
	for _, slug := range []string{observedSmallerSlug, observedBiggerSlug} {
		if err := writeJSON(filepath.Join(outputDir, resultsFileFor(slug)), results.observed[slug]); err != nil {
			return err
		}
	}
	if err := writeJSON(filepath.Join(outputDir, resultsFileFor(cvSlug)), results.cv); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(outputDir, "aggregated_summary.json"), summary); err != nil {
		return err
	}
	// The thresholds reach benchmark-action through the step outputs rather than
	// being written into the yaml a second time. One number, one place: the
	// registry. A value in both files is a value that drifts between them.
	if err := results.writeStepOutputs(os.Getenv("GITHUB_OUTPUT")); err != nil {
		return err
	}
	printTable(out, testKeys, summary)
	results.printRouting(out)
	return nil
}

// The non-gating files. All three run under fail-on-alert: false.
//
// The observed files carry the batch median of every key the registry screens. A
// screened key has to keep accumulating history: that is the difference between
// "screened, and here is the series" and "silently absent". They are split by
// direction because benchmark-action's tool is a per-step choice, so a
// bigger-is-better field in a smaller-is-better step would annotate every
// improvement as a regression.
//
// cv carries the within-batch coefficient of variation of every field, gateable
// or not. Lower is always better there, so it needs no split.
const (
	observedSmallerSlug = "observed_smaller"
	observedBiggerSlug  = "observed_bigger"
	cvSlug              = "cv"
)

func resultsFileFor(slug string) string { return "benchmark-results-" + slug + ".json" }

func observedSlugFor(d direction) string {
	if d == biggerIsBetter {
		return observedBiggerSlug
	}
	return observedSmallerSlug
}

// routing records what happened to one (phase key, field) pair, for the log.
type routing struct {
	key       string
	field     string
	threshold float64
	reason    string
}

// benchmarkResults holds one benchmark-action array per gated field plus the
// two non-gating arrays. benchmark-action still owns the compare against the
// cached batch median; the registry decides which keys reach a comparing step
// and what threshold that step runs at.
type benchmarkResults struct {
	gated    map[string][]benchmarkEntry
	observed map[string][]benchmarkEntry
	cv       []benchmarkEntry
	// thresholds[slug] is the largest registry threshold among the keys routed
	// into that step. benchmark-action takes one per step, so a step holding
	// more than one key runs at the loosest of them. With the registry screening
	// the loop-instrumented phases, six of the seven gather jobs route exactly
	// one key per field and the step threshold is that key's own value.
	thresholds map[string]float64
	routed     []routing
	screened   []routing
}

func newBenchmarkResults() benchmarkResults {
	return benchmarkResults{
		gated:      map[string][]benchmarkEntry{},
		observed:   map[string][]benchmarkEntry{},
		thresholds: map[string]float64{},
	}
}

// buildResults computes stats for every (test, field) pair and routes each one
// to a gating step, to the observe-only step, or to the CV step alone.
//
// An unregistered gateable field is an error rather than a skip. That is the
// point of the registry: benchmark-action skips a key it cannot match without
// saying so, so a key that fell out of the suite, a key somebody forgot to
// register, and a key screened on purpose all produce the same green step.
func buildResults(
	testKeys []string,
	reportsByTest map[string][]map[string]any,
	registry *gateRegistry,
	censored map[string]censorResult,
	out io.Writer,
) (map[string]map[string]stats, benchmarkResults, error) {
	summary := map[string]map[string]stats{}
	results := newBenchmarkResults()
	for _, testKey := range testKeys {
		datas := reportsByTest[testKey]
		if len(datas) == 0 {
			continue
		}
		phase := phaseKey(testKey)
		name := prettifyTestName(testKey)
		testSummary := map[string]stats{}
		for _, m := range metrics {
			values := extractValues(datas, m.jsonField)
			if len(values) == 0 {
				if registry != nil && registry.registered(phase, m.jsonField) {
					// A field the registry expects and the batch did not
					// produce. Left as a skip this is the silent-green case the
					// registry exists to remove: benchmark-action would compare
					// nothing and report a pass.
					return nil, results, fmt.Errorf(
						"%s: %s is registered in %s but absent from every sample; refusing to gate a key whose measurement went missing",
						phase, m.jsonField, registry.path)
				}
				continue
			}
			s := computeStats(values)
			testSummary[m.display] = s
			if err := results.route(phase, name, m, s, registry, censored[phase], out); err != nil {
				return nil, results, err
			}
		}
		summary[testKey] = testSummary
	}
	return summary, results, nil
}

// route decides which file one (phase key, field) pair lands in.
func (r *benchmarkResults) route(phase, name string, m metricSpec, s stats, registry *gateRegistry, cens censorResult, out io.Writer) error {
	entry := r.appendMetric(name, m, s)
	if m.slug == "" {
		return nil // CV only, by declaration. Never in the registry.
	}
	if registry == nil {
		// No registry: nothing gates. Observe everything. This is the local and
		// Regression-suite path, and it is deliberately the safe direction.
		slug := observedSlugFor(m.dir)
		r.observed[slug] = append(r.observed[slug], entry)
		r.screened = append(r.screened, routing{phase, m.jsonField, 0, "no registry"})
		return nil
	}
	if !registry.registered(phase, m.jsonField) {
		return fmt.Errorf(
			"%s: %s has no entry in %s. Add a threshold, or %q with the reason, before this key can reach a gate step",
			phase, m.jsonField, registry.path, screenSentinel)
	}
	screen := func(reason string) {
		slug := observedSlugFor(m.dir)
		r.observed[slug] = append(r.observed[slug], entry)
		r.screened = append(r.screened, routing{phase, m.jsonField, 0, reason})
	}
	if cens.reason != "" {
		screen("censored: " + cens.reason)
		return nil
	}
	t, gated := registry.threshold(phase, m.jsonField)
	if !gated {
		screen("registry: " + registry.screened[phase][m.jsonField])
		return nil
	}
	// The ceiling screen. A bounded field cannot report a ratio above
	// cap/baseline, so a threshold at or above that is a step no regression can
	// trip. Recomputed from this batch, because the baseline moves.
	if ceil, capped := registry.ceiling(m, s.Median); capped && t >= ceil {
		fmt.Fprintf(out, "::warning title=Performance key unfalsifiable::%s %s: threshold %.2f is at or above the ceiling %.2f (cap %.1f / baseline %.3f). No regression can trip it, so it is screened rather than passed.\n",
			phase, m.jsonField, t, ceil, registry.params[m.cappedBy], s.Median)
		screen(fmt.Sprintf("unfalsifiable at run time: ceiling %.2f below threshold %.2f", ceil, t))
		return nil
	}
	r.gated[m.slug] = append(r.gated[m.slug], entry)
	if t > r.thresholds[m.slug] {
		r.thresholds[m.slug] = t
	}
	r.routed = append(r.routed, routing{phase, m.jsonField, t, ""})
	return nil
}

// appendMetric records the informational CV entry for a single (test, field)
// pair and returns the median-based entry its caller routes. CV entries share a
// single smaller-is-better list because lower batch variance is always better.
func (r *benchmarkResults) appendMetric(name string, m metricSpec, s stats) benchmarkEntry {
	// The key name carries no iteration count. benchmark-action matches
	// history by exact key name, so embedding n would void every stored
	// baseline the moment the workflow's repeat input changed, with no signal
	// that history had been dropped. n travels in Extra instead, which is
	// displayed but not part of the match.
	gate := benchmarkEntry{
		Name:  fmt.Sprintf("%s - %s (median)", name, m.display),
		Unit:  m.unit,
		Value: s.Median,
		Range: fmt.Sprintf("%v", s.Stddev),
		Extra: fmt.Sprintf(
			"mean=%v stddev=%v cv=%v%% min=%v max=%v n=%d",
			s.Mean, s.Stddev, s.CVPct, s.Min, s.Max, s.N,
		),
	}
	// CV entries feed an informational-only benchmark-action invocation
	// (fail-on-alert: false). They surface when a batch's within-batch
	// coefficient of variation grows beyond its historical envelope, so a
	// reviewer can notice noise-floor regressions without gating the
	// pipeline, the informational answer to Ryan's PR#2994 variance
	// question (comment 3857936029).
	r.cv = append(r.cv, benchmarkEntry{
		Name:  fmt.Sprintf("%s - %s (CV%%)", name, m.display),
		Unit:  "cv-percent",
		Value: s.CVPct,
		Extra: fmt.Sprintf("median=%v stddev=%v n=%d", s.Median, s.Stddev, s.N),
	})
	return gate
}

// writeStepOutputs publishes each gating step's threshold and whether it has any
// key to compare. The workflow reads both, so the numbers live only in the
// registry and the step list lives only in the yaml.
//
// A step with no gated key must not run at all. benchmark-action on an empty
// array writes an empty entry into the history, which then reads as a baseline
// that legitimately contains no keys.
func (r *benchmarkResults) writeStepOutputs(path string) error {
	if path == "" {
		return nil
	}
	var b strings.Builder
	for _, m := range gatedMetrics() {
		gated := len(r.gated[m.slug]) > 0
		fmt.Fprintf(&b, "gated_%s=%t\n", m.slug, gated)
		if gated {
			// benchmark-action parses alert-threshold as a percentage.
			fmt.Fprintf(&b, "threshold_%s=%.0f%%\n", m.slug, r.thresholds[m.slug]*100)
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600) //nolint:gosec // G304: path is GITHUB_OUTPUT
	if err != nil {
		return err
	}
	if _, err := f.WriteString(b.String()); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// printRouting puts every routing decision on the record. A reviewer reading a
// red gate needs to know which keys were compared and at what threshold, and a
// reviewer reading a green one needs to know which were not.
func (r *benchmarkResults) printRouting(out io.Writer) {
	fmt.Fprintf(out, "\n%-46s %-34s %8s  %s\n", "Phase key", "Field", "Threshold", "Disposition")
	fmt.Fprintln(out, strings.Repeat("-", 120))
	for _, x := range r.routed {
		fmt.Fprintf(out, "%-46s %-34s %8.2f  gated\n", x.key, x.field, x.threshold)
	}
	for _, x := range r.screened {
		fmt.Fprintf(out, "%-46s %-34s %8s  screened, %s\n", x.key, x.field, "-", x.reason)
	}
	fmt.Fprintf(out, "\n%d gated, %d screened, %d CV entries\n", len(r.routed), len(r.screened), len(r.cv))
	for _, m := range gatedMetrics() {
		if n := len(r.gated[m.slug]); n > 0 {
			fmt.Fprintf(out, "  step %-18s %d key(s) at %.2f\n", m.slug, n, r.thresholds[m.slug])
		} else {
			fmt.Fprintf(out, "  step %-18s skipped, no gated key\n", m.slug)
		}
	}
}

// baselineStateFile is the record checkBaseline keeps of which keys the last
// green run gated on. It used to sit inside the restored baseline tree, next to
// the per-tier benchmark-action history files, so one actions/cache entry
// persisted both. That made the audit useless against the failure it exists to
// catch: an eviction takes the history and the record of what was gated
// together, prior.GatedKeys comes back empty, every key falls to the seeded
// branch, and the run reports a green gate having compared nothing.
//
// It now lives under BASELINE_STATE_DIR, which the workflow caches separately,
// so losing the history alone produces the intended missing-baseline failure.
// Losing both entries is still indistinguishable from a first run; the
// BASELINE_CACHE_HIT cross-check below closes the common case of that, where
// the history entry restored but the state did not.
const baselineStateFile = "baseline-state.json"

// gateTier ties one benchmark-action Compare step in
// .github/workflows/perf-gate.yaml to the files it touches. cacheSubdir must
// match that step's external-data-json-path and nameSuffix must match the suffix
// on its name input, otherwise the baseline lookup reads the wrong history. Both
// are derived from the field's slug so the three cannot drift apart.
//
// Only blocking steps are listed. The observe-only and CV steps cannot fail a
// job, so a missing baseline on either is not a gate defect.
type gateTier struct {
	resultsFile string
	cacheSubdir string
	nameSuffix  string
}

func gateTiers() []gateTier {
	gm := gatedMetrics()
	out := make([]gateTier, 0, len(gm))
	for _, m := range gm {
		out = append(out, gateTier{resultsFileFor(m.slug), m.slug, " - " + m.slug})
	}
	return out
}

// baselineState records which keys the last green run gated on, per tier. It is
// what separates "this key never had a baseline" from "this key had one and
// lost it". The first is an unavoidable first run; the second is a gate that
// silently stopped gating.
type baselineState struct {
	FirstSeedRun string              `json:"first_seed_run"`
	LastRun      string              `json:"last_run"`
	GatedKeys    map[string][]string `json:"gated_keys"`
}

// benchmarkHistory is the subset of benchmark-action's external-data JSON the
// baseline check reads. Entries are keyed by chart name; the last element of
// each slice is the entry the action compares the current run against.
type benchmarkHistory struct {
	Entries map[string][]struct {
		Benches []struct {
			Name string `json:"name"`
		} `json:"benches"`
	} `json:"entries"`
}

type baselineConfig struct {
	outputDir   string
	baselineDir string
	// stateDir holds baselineStateFile. Separate from baselineDir so the two
	// are cached independently; see the baselineStateFile comment.
	stateDir   string
	namePrefix string
	runID      string
	// cacheHit is the cache-matched-key output of the actions/cache step that
	// restored baselineDir. Non-empty means the scope is populated.
	cacheHit string
	// registry, when set, is the authority for which keys are supposed to be
	// gated. It is committed, so it survives the loss of every cache entry,
	// which the state file does not. That is what closes the last hole in the
	// eviction audit: the state file could only say "the last green run gated
	// this key", and the last green run's record lives in the same cache.
	registry *gateRegistry
	// allowSeed permits a key with no stored baseline. Establishing a new cache
	// scope needs it once; a run that has it set is not gating those keys.
	allowSeed bool
}

// checkBaseline fails the aggregation when a gated key that used to have a
// baseline no longer has one.
//
// benchmark-action compares each emitted key against the same key in the
// previous entry for its chart, and silently skips any key it cannot match. A
// key with no stored history therefore produces a green compare step that
// compared nothing, which on the wire looks identical to a real pass. This
// separates the two cases: a key with no history that no prior run recorded is
// reported as seeded and allowed, and a key with no history that the last green
// run did record is an error.
func checkBaseline(cfg baselineConfig, out io.Writer) error {
	if cfg.namePrefix == "" {
		return fmt.Errorf("BENCH_NAME_PREFIX is required when BASELINE_DIR is set: the chart name cannot be derived without it, so no baseline can be located")
	}
	stateDir := cfg.stateDir
	if stateDir == "" {
		stateDir = cfg.baselineDir
	}
	statePath := filepath.Join(stateDir, baselineStateFile)
	prior, err := readBaselineState(statePath)
	if err != nil {
		return err
	}
	// Migration. Runs before the state moved out of the baseline tree wrote it
	// to baselineDir, so the first run after that change finds the new location
	// empty and the old one populated. Read the old location rather than treating
	// it as a first run, which would discard the audit history, and rather than
	// treating it as an eviction, which would fail the first run on every
	// existing cache scope.
	if prior.LastRun == "" && stateDir != cfg.baselineDir {
		legacyPath := filepath.Join(cfg.baselineDir, baselineStateFile)
		legacy, lerr := readBaselineState(legacyPath)
		if lerr != nil {
			return lerr
		}
		if legacy.LastRun != "" {
			fmt.Fprintf(out, "Carrying the baseline audit state forward from %s to %s\n", legacyPath, statePath)
			prior = legacy
		}
	}
	// A restored history entry with no state file in either location is eviction,
	// not a first run. Without this the two cases are indistinguishable and the
	// second one green-seeds every key. cacheHit is the cache-matched-key output,
	// so it is non-empty exactly when a prior run's entry was found.
	if cfg.cacheHit != "" && prior.LastRun == "" {
		fmt.Fprintf(out, "::error title=Performance baseline state missing::actions/cache restored %s but %s is absent, so the record of which keys were gated is gone. Refusing to seed over a populated baseline scope.\n",
			cfg.cacheHit, statePath)
		return fmt.Errorf("baseline cache matched %q but no state file at %s: cannot tell a first run from an eviction", cfg.cacheHit, statePath)
	}
	next := baselineState{FirstSeedRun: prior.FirstSeedRun, LastRun: cfg.runID, GatedKeys: map[string][]string{}}
	if next.FirstSeedRun == "" {
		next.FirstSeedRun = cfg.runID
	}

	// First pass: split the emitted keys into those the stored history carries
	// and those it does not. What to do about the second group depends on
	// evidence collected across every tier, so the verdict waits for the split.
	var compared, unbaselined []string
	priorGated := map[string]bool{}
	for _, t := range gateTiers() {
		current, err := readEntryNames(filepath.Join(cfg.outputDir, t.resultsFile))
		if err != nil {
			return err
		}
		sort.Strings(current)
		next.GatedKeys[t.cacheSubdir] = current
		baseline, err := readBaselineKeys(
			filepath.Join(cfg.baselineDir, t.cacheSubdir, "benchmark-data.json"),
			cfg.namePrefix+t.nameSuffix,
		)
		if err != nil {
			return err
		}
		for _, key := range current {
			label := t.cacheSubdir + " / " + key
			if slices.Contains(prior.GatedKeys[t.cacheSubdir], key) {
				priorGated[label] = true
			}
			if slices.Contains(baseline, key) {
				compared = append(compared, label)
				continue
			}
			unbaselined = append(unbaselined, label)
		}
	}
	// A scope nobody has ever written to: nothing compared, no cache entry
	// matched, and no state file in either location. Seeding is the only
	// possible outcome there and it is not a defect.
	//
	// It remains the one case a committed registry cannot decide. The registry
	// says which keys ought to have a baseline; it cannot say whether this scope
	// ever held one. Deciding that needs storage outside actions/cache. What the
	// registry does change is every other case: a partial loss, and a loss on a
	// scope whose state or cache entry survived, are now errors measured against
	// a committed list rather than against a record that shared the cache's fate.
	firstRun := len(compared) == 0 && cfg.cacheHit == "" && prior.LastRun == ""
	var seeded, missing []string
	for _, label := range unbaselined {
		switch {
		case cfg.allowSeed || firstRun:
			seeded = append(seeded, label)
		case cfg.registry != nil:
			// The key reached a gating file, so the registry declares it gated.
			// A committed declaration plus no stored baseline is a loss.
			missing = append(missing, label)
		case priorGated[label]:
			missing = append(missing, label)
		default:
			seeded = append(seeded, label)
		}
	}

	fmt.Fprintf(out, "\nBaseline check against %s: %d compared, %d seeded, %d missing\n",
		cfg.baselineDir, len(compared), len(seeded), len(missing))
	for _, k := range seeded {
		fmt.Fprintf(out, "  seed     %s\n", k)
	}
	for _, k := range missing {
		fmt.Fprintf(out, "  MISSING  %s\n", k)
	}
	if len(seeded) > 0 {
		// A workflow annotation makes a seeded run visible without opening the
		// log. Seeded keys were not compared against anything, which is the one
		// case where a green gate does not mean the run was checked, so this is
		// a warning rather than a notice.
		fmt.Fprintf(out, "::warning title=Performance baseline seeded::%d of %d gated key(s) had no stored baseline and were seeded by run %s under ALLOW_BASELINE_SEED. Those keys were not compared. Clear the flag once this scope is populated.\n",
			len(seeded), len(seeded)+len(compared)+len(missing), cfg.runID)
	}
	if len(missing) > 0 {
		// authority names what said the key should have had a baseline, because
		// the remedy differs. The registry is committed, so a run that trips it
		// has lost a baseline it is contractually meant to have. The state file
		// only records the last green run and shares the cache's fate.
		authority := "the last green run " + prior.LastRun
		if cfg.registry != nil {
			authority = cfg.registry.path
		}
		fmt.Fprintf(out, "::error title=Performance baseline missing::%d gated key(s) are declared gated by %s but have no stored baseline, so they would pass without being compared. Refusing to report a green gate. Set ALLOW_BASELINE_SEED=true only when deliberately establishing a new cache scope.\n",
			len(missing), authority)
		return fmt.Errorf("baseline missing for %d gated key(s) declared by %s: %s",
			len(missing), authority, strings.Join(missing, ", "))
	}
	// Only recorded on a clean check. Overwriting after a missing-baseline
	// failure would erase the evidence of which keys used to be gated, and the
	// next run would read the loss as a legitimate first seed.
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	return writeJSON(statePath, next)
}

// readBaselineState returns a zero state when the file is absent, which is the
// first-run case. A malformed file is an error rather than a reset, because
// treating it as absent would silently downgrade every key to seeded.
func readBaselineState(path string) (baselineState, error) {
	var s baselineState
	b, err := os.ReadFile(path) //nolint:gosec // G304: path is scoped to the CI cache directory
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("parsing %s: %w", path, err)
	}
	return s, nil
}

// readBaselineKeys returns the key names benchmark-action will compare against
// for chartName: the bench names on the most recent stored entry. A missing
// file is the first-run case and yields no keys.
func readBaselineKeys(path, chartName string) ([]string, error) {
	b, err := os.ReadFile(path) //nolint:gosec // G304: path is scoped to the CI cache directory
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var hist benchmarkHistory
	if err := json.Unmarshal(b, &hist); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	entries := hist.Entries[chartName]
	if len(entries) == 0 {
		return nil, nil
	}
	latest := entries[len(entries)-1]
	names := make([]string, 0, len(latest.Benches))
	for _, bench := range latest.Benches {
		names = append(names, bench.Name)
	}
	return names, nil
}

func readEntryNames(path string) ([]string, error) {
	b, err := os.ReadFile(path) //nolint:gosec // G304: path is scoped to CI-created OUTPUT_DIR
	if err != nil {
		return nil, err
	}
	var entries []benchmarkEntry
	if err := json.Unmarshal(b, &entries); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	return names, nil
}

const reportSuffix = "_performance_report.json"

// collectReports finds every *_performance_report.json anywhere under
// outputDir and groups the parsed maps by the file's basename, which stands in
// as the test-key across samples. It also returns the sample directories,
// relative to outputDir, in sorted order.
//
// One directory holding reports is one sample. The directory name is not
// parsed, only used as the sample identity, and the depth is not fixed. Three
// layouts therefore work with no translation step:
//
//	iter_1/, iter_2/, ...                      a single runner looping locally
//	<artifact-name>/                           download-artifact, flat payload
//	<artifact-name>/iter_N/                    download-artifact, nested payload
//
// The nesting in the download case depends on what else the uploaded artifact
// matched, because upload-artifact roots the payload at the least common
// ancestor of the files it found. Walking rather than globbing a fixed depth
// keeps that an implementation detail of the upload rather than a coupling.
//
// Counting sample directories rather than walking a 1..N range is also what
// lets a missing sample read as a gap instead of truncating the batch at the
// first hole, which matters once samples arrive from independent runners.
func collectReports(outputDir string) (map[string][]map[string]any, []string, error) {
	byDir := map[string][]string{}
	err := filepath.WalkDir(outputDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), reportSuffix) {
			return nil
		}
		dir, err := filepath.Rel(outputDir, filepath.Dir(path))
		if err != nil {
			return err
		}
		byDir[dir] = append(byDir[dir], path)
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("walking %s: %w", outputDir, err)
	}

	reportsByTest := map[string][]map[string]any{}
	samples := sortedKeys(byDir)
	for _, dir := range samples {
		paths := byDir[dir]
		sort.Strings(paths)
		for _, path := range paths {
			data, err := readReport(path)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warn: skipping %s: %v\n", path, err)
				continue
			}
			reportsByTest[filepath.Base(path)] = append(reportsByTest[filepath.Base(path)], data)
		}
	}
	return reportsByTest, samples, nil
}

// sortedKeys returns the map's keys in a stable order.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func readReport(path string) (map[string]any, error) {
	b, err := os.ReadFile(path) //nolint:gosec // G304: path is scoped to CI-created OUTPUT_DIR
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func extractValues(datas []map[string]any, jsonField string) []float64 {
	values := make([]float64, 0, len(datas))
	for _, d := range datas {
		raw, ok := d[jsonField]
		if !ok || raw == nil {
			continue
		}
		v, ok := raw.(float64)
		if !ok {
			continue
		}
		// Older performance report writers emit total_time in nanoseconds;
		// newer writers emit seconds. 1e9 is the ns/s conversion factor.
		// Any total_time above 1e9 is interpreted as ns and divided back to
		// seconds so downstream comparisons stay in a single unit. Follow-up:
		// canonicalize the emitters on seconds so this branch can be removed.
		if jsonField == "total_time" && v > 1e9 {
			v = v / 1e9
		}
		values = append(values, v)
	}
	return values
}

func computeStats(values []float64) stats {
	n := len(values)
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	mean := sum / float64(n)
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	var median float64
	if n%2 == 1 {
		median = sorted[n/2]
	} else {
		median = (sorted[n/2-1] + sorted[n/2]) / 2
	}
	variance := 0.0
	for _, v := range values {
		variance += (v - mean) * (v - mean)
	}
	variance /= float64(n)
	stddev := math.Sqrt(variance)
	cv := 0.0
	if mean > 0 {
		cv = stddev / mean * 100
	}
	return stats{
		N:      n,
		Mean:   round2(mean),
		Median: round2(median),
		Stddev: round2(stddev),
		CVPct:  round1(cv),
		Min:    round2(sorted[0]),
		Max:    round2(sorted[n-1]),
	}
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }
func round1(f float64) float64 { return math.Round(f*10) / 10 }

func prettifyTestName(fileBasename string) string {
	base := strings.TrimSuffix(fileBasename, "_performance_report.json")
	words := strings.Split(base, "_")
	for i, w := range words {
		if w == "" {
			continue
		}
		words[i] = strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
	}
	return strings.Join(words, " ")
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	// path is constructed from OUTPUT_DIR (CI-created via mktemp -d) plus a
	// literal filename. Not attacker-controlled.
	return os.WriteFile(path, b, 0o600) //nolint:gosec // G304: path is scoped to CI-created OUTPUT_DIR
}

func printTable(out io.Writer, testKeys []string, summary map[string]map[string]stats) {
	fmt.Fprintf(out, "\n%-55s %3s %10s %10s %10s %6s\n",
		"Test / Metric", "n", "Median", "Mean", "Stddev", "CV")
	fmt.Fprintln(out, "----------------------------------------------------------------------------------------------------")
	for _, testKey := range testKeys {
		testName := strings.TrimSuffix(testKey, "_performance_report.json")
		testSummary, ok := summary[testKey]
		if !ok {
			continue
		}
		// Emit metrics in the canonical order defined by `metrics` so the
		// table matches the benchmark-action file ordering.
		for _, m := range metrics {
			s, ok := testSummary[m.display]
			if !ok {
				continue
			}
			label := fmt.Sprintf("  %s / %s", testName, m.display)
			fmt.Fprintf(out, "%-55s %3d %10.1f %10.1f %10.1f %5.1f%%\n",
				label, s.N, s.Median, s.Mean, s.Stddev, s.CVPct)
		}
	}
}
