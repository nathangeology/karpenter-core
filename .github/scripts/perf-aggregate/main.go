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
//
// Four benchmark-action files are emitted. Direction is a per-tool rather than
// a per-metric property in github-action-benchmark, so utilization and
// efficiency metrics need their own bigger-is-better file. The smaller-is-better
// metrics are split again by threshold tier, and batch CV goes to its own
// informational file.
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

// tier controls which gating benchmark-action step consumes a metric.
// benchmark-action takes one alert-threshold per step, so a metric can only
// carry its own threshold by getting its own step. The split is the mechanism
// @ryan-mist's per-metric stddev gating ask needs (PR#2994 comment
// 3961447028); the per-metric values it should carry are still open, because
// setting them needs the cross-run CV of the batch median per key and that has
// not been measured. Both gating tiers sit at 150% until it is.
type tier int

const (
	// tierTight metrics gate at 150%: Duration, Controller Peak Memory, Final
	// Nodes.
	tierTight tier = iota
	// tierLoose metrics gate at their own threshold in the yaml, also 150%
	// today. Controller CPU is the one such metric. The tier is kept separate
	// from tierTight rather than merged at the shared value so this metric can
	// be retuned without disturbing the other three, which is the pending
	// per-key work.
	tierLoose
	// tierInformational metrics skip the gating benchmark-action steps and
	// only emit into the informational CV file. Consolidation Rounds falls
	// in this bucket: the integer 0-9 range and per-test P90 CV of 149% make
	// any relative-threshold gate an FP generator.
	tierInformational
)

// metricSpec describes how to extract, label, and classify one field from a
// performance report.
type metricSpec struct {
	jsonField string
	display   string
	unit      string
	dir       direction
	tier      tier
}

var metrics = []metricSpec{
	{"total_time", "Duration", "seconds", smallerIsBetter, tierTight},
	{"karpenter_p95_memory_mb", "Controller Peak Memory", "MB", smallerIsBetter, tierTight},
	{"karpenter_p95_cpu_cores", "Controller CPU", "cores", smallerIsBetter, tierLoose},
	// Sustained controller CPU, emitted for visibility only. @ryan-mist's
	// per-test threshold proposal (PR#2994 comment 4012042309) needs this
	// statistic in the report; which CPU key gates, and at what threshold,
	// is still open, so it stays out of both gate files for now.
	{"karpenter_p50_cpu_cores", "Controller CPU P50", "cores", smallerIsBetter, tierInformational},
	{"total_nodes", "Final Nodes", "nodes", smallerIsBetter, tierTight},
	{"total_reserved_cpu_utilization", "CPU Utilization", "percent", biggerIsBetter, tierTight},
	{"resource_efficiency_score", "Efficiency Score", "score", biggerIsBetter, tierTight},
	{"total_reserved_memory_utilization", "Memory Utilization", "percent", biggerIsBetter, tierTight},
	{"rounds", "Consolidation Rounds", "rounds", smallerIsBetter, tierInformational},
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
	if err := run(outputDir, iterations, minIterations, os.Stdout); err != nil {
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
func run(outputDir string, iterations, minIterations int, out io.Writer) error {
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

	summary, results := buildResults(testKeys, reportsByTest)

	if err := writeJSON(filepath.Join(outputDir, "benchmark-results-smaller-tight.json"), results.smallerTight); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(outputDir, "benchmark-results-smaller-loose.json"), results.smallerLoose); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(outputDir, "benchmark-results-bigger.json"), results.bigger); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(outputDir, "benchmark-results-cv.json"), results.cv); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(outputDir, "aggregated_summary.json"), summary); err != nil {
		return err
	}
	printTable(out, testKeys, summary)
	fmt.Fprintf(out, "\nEmitted %d smaller-tight, %d smaller-loose, %d bigger-is-better, %d CV metrics\n",
		len(results.smallerTight), len(results.smallerLoose), len(results.bigger), len(results.cv))
	return nil
}

// benchmarkResults holds the four parallel benchmark-action arrays this
// aggregator emits: one gating file per (direction, tier) pair plus one
// informational CV file. Splitting smaller-is-better into tight and loose
// tiers lets the yaml apply per-metric-key thresholds without moving the gate
// decision into Go: benchmark-action still owns the compare against cached
// batch median, but each tier feeds its own step with its own threshold.
type benchmarkResults struct {
	smallerTight []benchmarkEntry
	smallerLoose []benchmarkEntry
	bigger       []benchmarkEntry
	cv           []benchmarkEntry
}

// buildResults computes stats for every (test, metric) pair and appends
// the corresponding gating and CV benchmark entries. It also returns the
// summary map keyed by test then metric display name.
func buildResults(testKeys []string, reportsByTest map[string][]map[string]any) (map[string]map[string]stats, benchmarkResults) {
	summary := map[string]map[string]stats{}
	var results benchmarkResults
	for _, testKey := range testKeys {
		datas := reportsByTest[testKey]
		if len(datas) == 0 {
			continue
		}
		name := prettifyTestName(testKey)
		testSummary := map[string]stats{}
		for _, m := range metrics {
			values := extractValues(datas, m.jsonField)
			if len(values) == 0 {
				continue
			}
			s := computeStats(values)
			testSummary[m.display] = s
			results.appendMetric(name, m, s)
		}
		summary[testKey] = testSummary
	}
	return summary, results
}

// appendMetric records both the gating (median-based) entry and the
// informational CV entry for a single (test, metric) pair. Gating entries
// route to smaller-tight, smaller-loose, or bigger by (direction, tier);
// tierInformational metrics skip gating entirely and land in the CV list
// only. CV entries share a single smaller-is-better list because lower batch
// variance is always better.
func (r *benchmarkResults) appendMetric(name string, m metricSpec, s stats) {
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
	switch {
	case m.tier == tierInformational:
		// Do not emit a gating entry. The CV entry below still fires so the
		// metric shows up in the batch-CV chart.
	case m.dir == biggerIsBetter:
		r.bigger = append(r.bigger, gate)
	case m.tier == tierLoose:
		r.smallerLoose = append(r.smallerLoose, gate)
	default:
		r.smallerTight = append(r.smallerTight, gate)
	}
	// CV entries feed an informational-only benchmark-action invocation
	// (fail-on-alert: false). They surface when a batch's within-batch
	// coefficient of variation grows beyond its historical envelope, so a
	// reviewer can notice noise-floor regressions without gating the
	// pipeline — the informational answer to Ryan's PR#2994 variance
	// question (comment 3857936029).
	r.cv = append(r.cv, benchmarkEntry{
		Name:  fmt.Sprintf("%s - %s (CV%%)", name, m.display),
		Unit:  "cv-percent",
		Value: s.CVPct,
		Extra: fmt.Sprintf("median=%v stddev=%v n=%d", s.Median, s.Stddev, s.N),
	})
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
// .github/workflows/e2e.yaml to the files it touches. cacheSubdir must match
// that step's external-data-json-path and nameSuffix must match the suffix on
// its name input, otherwise the baseline lookup reads the wrong history. Only
// blocking tiers are listed: the informational CV step cannot fail a job, so a
// missing CV baseline is not a gate defect.
type gateTier struct {
	resultsFile string
	cacheSubdir string
	nameSuffix  string
}

var gateTiers = []gateTier{
	{"benchmark-results-smaller-tight.json", "smaller-tight", " - smaller-tight"},
	{"benchmark-results-smaller-loose.json", "smaller-loose", " - smaller-loose"},
	{"benchmark-results-bigger.json", "bigger", " - bigger-is-better"},
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
	// A restored history entry with no state file is eviction, not a first run.
	// Without this the two cases are indistinguishable and the second one
	// green-seeds every key. cacheHit is the cache-matched-key output, so it is
	// non-empty exactly when a prior run's entry was found.
	if cfg.cacheHit != "" && prior.LastRun == "" {
		fmt.Fprintf(out, "::error title=Performance baseline state missing::actions/cache restored %s but %s is absent, so the record of which keys were gated is gone. Refusing to seed over a populated baseline scope.\n",
			cfg.cacheHit, statePath)
		return fmt.Errorf("baseline cache matched %q but no state file at %s: cannot tell a first run from an eviction", cfg.cacheHit, statePath)
	}
	next := baselineState{FirstSeedRun: prior.FirstSeedRun, LastRun: cfg.runID, GatedKeys: map[string][]string{}}
	if next.FirstSeedRun == "" {
		next.FirstSeedRun = cfg.runID
	}

	var compared, seeded, missing []string
	for _, t := range gateTiers {
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
			switch {
			case slices.Contains(baseline, key):
				compared = append(compared, label)
			case slices.Contains(prior.GatedKeys[t.cacheSubdir], key):
				missing = append(missing, label)
			default:
				seeded = append(seeded, label)
			}
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
		fmt.Fprintf(out, "::warning title=Performance baseline seeded::%d of %d gated key(s) had no stored baseline and were seeded by run %s. Those keys were not compared. Every later run on this cache scope must find a baseline for them.\n",
			len(seeded), len(seeded)+len(compared)+len(missing), cfg.runID)
	}
	if len(missing) > 0 {
		fmt.Fprintf(out, "::error title=Performance baseline missing::%d gated key(s) were gated by run %s but have no stored baseline now, so they would pass without being compared. Refusing to report a green gate.\n",
			len(missing), prior.LastRun)
		return fmt.Errorf("baseline missing for %d gated key(s) last gated by run %s: %s",
			len(missing), prior.LastRun, strings.Join(missing, ", "))
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
