//go:build perf_inject

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

// Package perfinject holds synthetic performance-regression injections used to
// calibrate the minimum detectable effect of the kind performance e2e.
//
// EXPERIMENT-ONLY. This file is compiled only under the perf_inject build tag,
// which no release, presubmit, or default make target sets. Even when it is
// compiled in, every injection defaults to zero, so a tagged binary with no
// environment set behaves like an untagged one.
//
// Four injections, one per measurable channel:
//
//	env var                     shape                          channel it moves
//	--------------------------  -----------------------------  ---------------------------
//	KPERF_SCALEOUT_SLEEP_MS     sleep per Provisioner.Schedule scale-out wall time
//	KPERF_CONSOL_SLEEP_MS       sleep per computeConsolidation consolidation wall time
//	KPERF_RETAIN_MB             retained, touched allocation   controller RSS
//	KPERF_CPU_FRAC              background duty-cycle spin     controller CPU cores
//
// A sleep consumes no CPU and allocates nothing, so KPERF_SCALEOUT_SLEEP_MS and
// KPERF_CONSOL_SLEEP_MS are the negative control for the memory channel: if the
// memory instrument moves under a pause, the movement is the instrument's, not
// the workload's.
//
// Every injection maintains cumulative counters, and a reporter goroutine prints
// them on a fixed cadence as a single greppable line. The counters are what make
// the results interpretable: a per-call injection multiplied by an unknown call
// count says nothing, so the call count is measured rather than modelled, and
// the phase boundaries in the performance report are used to difference the
// cumulative counters into per-phase totals.
package perfinject

import (
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// reportInterval is the cadence of the counter line. The performance report's
// phase boundaries are derived from its timestamp and total_time, so the cadence
// bounds the error when the cumulative counters are differenced across a phase
// boundary. Ten seconds against phases of 79 s and 541 s is under 2 pct.
const reportInterval = 10 * time.Second

// dutyCycle is the period of the background CPU load's spin/idle alternation. It
// has to be well under the metrics poller's 5 s sample interval so a sampled
// window sees the intended average rather than a whole spin or a whole idle.
const dutyCycle = 100 * time.Millisecond

// touchStride is the interval at which the retained allocation is written. Go
// zeroes a make([]byte, n), but a large allocation can be served from mapped
// pages the kernel has not faulted in, and the memory instrument reads
// process_resident_memory_bytes. Writing one byte per page forces residency.
const touchStride = 4096

type config struct {
	scaleOutSleep time.Duration
	consolSleep   time.Duration
	retainMB      int
	cpuFrac       float64
}

//nolint:gochecknoglobals // experiment-only build, process-wide by design
var (
	cfg     config
	cfgOnce sync.Once

	scaleOutCalls   atomic.Int64
	scaleOutSleepNs atomic.Int64
	consolCalls     atomic.Int64
	consolSleepNs   atomic.Int64
	retainedBytes   atomic.Int64
	spinNs          atomic.Int64

	// retained holds the injected allocation for the process lifetime. Nothing
	// reads it; the point is that the garbage collector cannot take it.
	retained [][]byte
)

func envDuration(name string) time.Duration {
	v, ok := os.LookupEnv(name)
	if !ok {
		return 0
	}
	ms, err := strconv.ParseFloat(v, 64)
	if err != nil || ms <= 0 {
		return 0
	}
	return time.Duration(ms * float64(time.Millisecond))
}

func envInt(name string) int {
	v, ok := os.LookupEnv(name)
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func envFloat(name string) float64 {
	v, ok := os.LookupEnv(name)
	if !ok {
		return 0
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f <= 0 {
		return 0
	}
	return f
}

// start reads the configuration once, applies the one-shot injections, and
// launches the reporter. It runs on the first hook call rather than in an init
// function so the injection begins with the measured work rather than with
// process start, which keeps a retained allocation out of the controller's
// pre-test baseline.
func start() {
	cfg = config{
		scaleOutSleep: envDuration("KPERF_SCALEOUT_SLEEP_MS"),
		consolSleep:   envDuration("KPERF_CONSOL_SLEEP_MS"),
		retainMB:      envInt("KPERF_RETAIN_MB"),
		cpuFrac:       envFloat("KPERF_CPU_FRAC"),
	}
	fmt.Printf("PERFINJECT config scaleout_sleep_ms=%.3f consol_sleep_ms=%.3f retain_mb=%d cpu_frac=%.4f\n",
		float64(cfg.scaleOutSleep.Nanoseconds())/1e6, float64(cfg.consolSleep.Nanoseconds())/1e6,
		cfg.retainMB, cfg.cpuFrac)

	if cfg.retainMB > 0 {
		retain(cfg.retainMB)
	}
	if cfg.cpuFrac > 0 {
		go burn(cfg.cpuFrac)
	}
	go report()
}

// retain allocates mb megabytes in 1 MB chunks, writes one byte per page so the
// pages become resident, and holds the result for the process lifetime.
func retain(mb int) {
	const chunk = 1 << 20
	retained = make([][]byte, 0, mb)
	for i := 0; i < mb; i++ {
		b := make([]byte, chunk)
		for j := 0; j < chunk; j += touchStride {
			b[j] = byte(i + 1)
		}
		retained = append(retained, b)
	}
	retainedBytes.Store(int64(mb) * chunk)
}

// burn holds the process at approximately frac cores by alternating a spin with
// an idle inside a fixed period. frac above 1 is served by that many goroutines.
func burn(frac float64) {
	whole := int(frac)
	rem := frac - float64(whole)
	for i := 0; i < whole; i++ {
		go spinLoop(1.0)
	}
	if rem > 0 {
		spinLoop(rem)
	}
}

func spinLoop(frac float64) {
	spin := time.Duration(frac * float64(dutyCycle))
	idle := dutyCycle - spin
	for {
		deadline := time.Now().Add(spin)
		var x uint64 = 1
		for time.Now().Before(deadline) {
			// A loop the compiler cannot hoist. 4096 iterations between clock
			// reads keeps time.Now off the hot path without overshooting spin.
			for i := 0; i < 4096; i++ {
				x = x*6364136223846793005 + 1442695040888963407
			}
		}
		atomic.AddInt64(&sink, int64(x&1))
		spinNs.Add(spin.Nanoseconds())
		if idle > 0 {
			time.Sleep(idle)
		}
	}
}

//nolint:gochecknoglobals // keeps the spin loop's result observable so it cannot be optimised out
var sink int64

// report prints one cumulative counter line per interval. The line is the only
// channel by which the call counts leave the controller, so it is printed to
// stdout rather than through the logger: the logger's level and sampling are
// test-configurable and the counts must not be.
func report() {
	t := time.NewTicker(reportInterval)
	defer t.Stop()
	for range t.C {
		emit()
	}
}

func emit() {
	fmt.Printf("PERFINJECT tick ts=%s scaleout_calls=%d scaleout_sleep_ms=%.1f consol_calls=%d consol_sleep_ms=%.1f retained_mb=%.1f spin_ms=%.1f\n",
		time.Now().UTC().Format(time.RFC3339Nano),
		scaleOutCalls.Load(), float64(scaleOutSleepNs.Load())/1e6,
		consolCalls.Load(), float64(consolSleepNs.Load())/1e6,
		float64(retainedBytes.Load())/(1<<20), float64(spinNs.Load())/1e6,
	)
}

// ScaleOut is called at the top of Provisioner.Schedule, whose only production
// caller is Provisioner.Reconcile. It is unreachable from the disruption path:
// disruption enters the shared scheduler one level lower, at
// Provisioner.NewScheduler. Exactly one call per provisioning reconcile that
// passes the batcher.
func ScaleOut() {
	cfgOnce.Do(start)
	scaleOutCalls.Add(1)
	if cfg.scaleOutSleep > 0 {
		time.Sleep(cfg.scaleOutSleep)
		scaleOutSleepNs.Add(cfg.scaleOutSleep.Nanoseconds())
	}
}

// Consolidation is called at the top of consolidation.computeConsolidation,
// whose only callers are SingleNodeConsolidation.ComputeCommands and
// MultiNodeConsolidation.ComputeCommands. Drift and emptiness do not reach it,
// which matters because the same suite contains a drift test.
func Consolidation() {
	cfgOnce.Do(start)
	consolCalls.Add(1)
	if cfg.consolSleep > 0 {
		time.Sleep(cfg.consolSleep)
		consolSleepNs.Add(cfg.consolSleep.Nanoseconds())
	}
}
