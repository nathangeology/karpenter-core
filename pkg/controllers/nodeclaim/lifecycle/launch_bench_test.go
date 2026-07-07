//go:build test_performance

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

package lifecycle

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/patrickmn/go-cache"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	fakecr "sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider/fake"
	"sigs.k8s.io/karpenter/pkg/test"
)

// BenchmarkLaunchCacheUnderBurst exercises the Launch reconciler's in-memory
// nodeclaim cache under a burst that exceeds the cache's TTL, mirroring the
// production hot-burst scenario reported in #2358 that motivated the cache
// TTL bump in #2307. The cache is constructed with the production parameters
// (cache.New(time.Hour, time.Minute)) and the benchmark does NOT advance the
// clock, so entries accumulate for the duration of the run.
//
// A regression that increases per-entry retention (e.g. the 60s -> 1h TTL bump
// in #2307) shows up as cache_size proportional to burst size and as heap_b
// growth proportional to cache_size at the largest sub-bench.
//
// To run:
//
//	go test -tags=test_performance -run=XXX -bench=BenchmarkLaunchCacheUnderBurst -count=10 -benchmem ./pkg/controllers/nodeclaim/lifecycle/
//
// To compare before/after with benchstat:
//
//	go test -tags=test_performance -run=XXX -bench=BenchmarkLaunchCacheUnderBurst -count=10 -benchmem | tee /tmp/old
//	# revert TTL to 60s on launch cache
//	go test -tags=test_performance -run=XXX -bench=BenchmarkLaunchCacheUnderBurst -count=10 -benchmem | tee /tmp/new
//	benchstat /tmp/old /tmp/new
func BenchmarkLaunchCacheUnderBurst(b *testing.B) {
	for _, burst := range []int{100, 500, 1500} {
		b.Run(fmt.Sprintf("Burst%d", burst), func(b *testing.B) {
			benchmarkLaunchCacheBurst(b, burst)
		})
	}
}

func benchmarkLaunchCacheBurst(b *testing.B, burst int) {
	ctx := context.Background()
	clk := clocktesting.NewFakeClock(time.Now())
	launch := &Launch{
		kubeClient:    fakecr.NewFakeClient(),
		cloudProvider: fake.NewCloudProvider(),
		cache:         cache.New(time.Hour, time.Minute),
		recorder:      test.NewEventRecorder(),
		clock:         clk,
	}

	// Pre-build the per-iteration claim slice so that allocation cost is not
	// folded into the per-op signal. Each claim has a unique UID so cache
	// entries do not collide across iterations.
	claims := make([]*v1.NodeClaim, burst)
	for i := range claims {
		nc := test.NodeClaim()
		nc.UID = types.UID(fmt.Sprintf("bench-launch-%d", i))
		claims[i] = nc
	}

	// Reset the timer after fixture build so only the burst loop is measured.
	b.ResetTimer()
	b.ReportAllocs()

	var ms runtime.MemStats
	for i := 0; i < b.N; i++ {
		launch.cache.Flush()
		for j := 0; j < burst; j++ {
			nc := claims[j]
			// Reset the launched condition so each iteration enters the cache
			// path through the same branch (cond.IsUnknown() == true).
			nc.Status.Conditions = nil
			launch.cache.SetDefault(string(nc.UID), nc)
			if _, err := launch.Reconcile(ctx, nc); err != nil {
				b.Fatalf("reconcile: %s", err)
			}
		}
	}
	b.StopTimer()

	runtime.ReadMemStats(&ms)
	b.ReportMetric(float64(launch.cache.ItemCount()), "cache_size")
	b.ReportMetric(float64(ms.HeapInuse), "heap_b")
}

// BenchmarkLaunchCacheUnderBurstHold fills the cache to burst size, then
// tests whether entries survive past the TTL by calling DeleteExpired(). Under
// the pre-#2307 60s TTL, items expire and cache_size drops to 0; under the
// current 1h TTL, items persist. This directly exercises the janitor-based
// expiry mechanism that #2307 changed.
//
// The bench parameterizes over TTL (short=1ms vs long=1h) so benchstat can
// compare cache_size between the two states as a deterministic signal.
func BenchmarkLaunchCacheUnderBurstHold(b *testing.B) {
	for _, tc := range []struct {
		name string
		ttl  time.Duration
	}{
		{"TTL1ms", time.Millisecond},
		{"TTL1h", time.Hour},
	} {
		for _, burst := range []int{100, 500, 1500} {
			b.Run(fmt.Sprintf("%s/Burst%d", tc.name, burst), func(b *testing.B) {
				benchmarkLaunchCacheBurstHold(b, burst, tc.ttl)
			})
		}
	}
}

func benchmarkLaunchCacheBurstHold(b *testing.B, burst int, ttl time.Duration) {
	ctx := context.Background()
	clk := clocktesting.NewFakeClock(time.Now())

	claims := make([]*v1.NodeClaim, burst)
	for i := range claims {
		nc := test.NodeClaim()
		nc.UID = types.UID(fmt.Sprintf("bench-hold-%d", i))
		claims[i] = nc
	}

	b.ResetTimer()
	b.ReportAllocs()

	var lastCacheSize int
	var ms runtime.MemStats
	for i := 0; i < b.N; i++ {
		// Construct a fresh cache each iteration with the parameterized TTL.
		// Janitor cleanup interval set to 0 (disabled) so we control expiry
		// explicitly via DeleteExpired().
		c := cache.New(ttl, 0)
		launch := &Launch{
			kubeClient:    fakecr.NewFakeClient(),
			cloudProvider: fake.NewCloudProvider(),
			cache:         c,
			recorder:      test.NewEventRecorder(),
			clock:         clk,
		}

		// Fill cache with burst entries via reconcile.
		for j := 0; j < burst; j++ {
			nc := claims[j]
			nc.Status.Conditions = nil
			launch.cache.SetDefault(string(nc.UID), nc)
			if _, err := launch.Reconcile(ctx, nc); err != nil {
				b.Fatalf("reconcile: %s", err)
			}
		}

		// Wait for short TTL items to expire. For TTL=1ms this is sufficient;
		// for TTL=1h items remain unexpired.
		time.Sleep(5 * time.Millisecond)
		c.DeleteExpired()
		lastCacheSize = c.ItemCount()
	}
	b.StopTimer()

	runtime.ReadMemStats(&ms)
	b.ReportMetric(float64(lastCacheSize), "cache_size")
	b.ReportMetric(float64(ms.HeapInuse), "heap_b")
}

// BenchmarkLaunchCacheUnderBurstNoFlush is the same shape as the original
// BenchmarkLaunchCacheUnderBurst but omits cache.Flush() between b.N
// iterations. Under a long TTL (1h), cache_size grows monotonically across
// iterations (b.N * burst) because no entries expire during the run. Under a
// short TTL (60s in production pre-#2307), the janitor would delete older
// entries mid-run, causing cache_size to plateau near `burst`.
//
// This is a closer proxy to issue #2358's actual mechanism: sustained refill
// under long retention causes unbounded heap growth.
func BenchmarkLaunchCacheUnderBurstNoFlush(b *testing.B) {
	for _, burst := range []int{100, 500, 1500} {
		b.Run(fmt.Sprintf("Burst%d", burst), func(b *testing.B) {
			benchmarkLaunchCacheBurstNoFlush(b, burst)
		})
	}
}

func benchmarkLaunchCacheBurstNoFlush(b *testing.B, burst int) {
	ctx := context.Background()
	clk := clocktesting.NewFakeClock(time.Now())
	launch := &Launch{
		kubeClient:    fakecr.NewFakeClient(),
		cloudProvider: fake.NewCloudProvider(),
		cache:         cache.New(time.Hour, time.Minute),
		recorder:      test.NewEventRecorder(),
		clock:         clk,
	}

	// Each iteration uses unique UIDs so entries accumulate across b.N.
	b.ResetTimer()
	b.ReportAllocs()

	var ms runtime.MemStats
	for i := 0; i < b.N; i++ {
		for j := 0; j < burst; j++ {
			nc := test.NodeClaim()
			nc.UID = types.UID(fmt.Sprintf("bench-noflush-%d-%d", i, j))
			nc.Status.Conditions = nil
			launch.cache.SetDefault(string(nc.UID), nc)
			if _, err := launch.Reconcile(ctx, nc); err != nil {
				b.Fatalf("reconcile: %s", err)
			}
		}
	}
	b.StopTimer()

	runtime.ReadMemStats(&ms)
	b.ReportMetric(float64(launch.cache.ItemCount()), "cache_size")
	b.ReportMetric(float64(ms.HeapInuse), "heap_b")
}

// Sanity-check helper: ensures the test fixture actually builds claims that
// enter the cache path (cond.IsUnknown() == true) so the benchmark exercises
// the intended branch even if the upstream Launched condition default ever
// changes. Run as a regular unit test (no test_performance tag needed for the
// assertion itself, but the file is gated to keep the bench binary slim).
func TestBenchLaunchCacheFixtureEntersUnknownPath(t *testing.T) {
	nc := test.NodeClaim()
	nc.UID = types.UID("fixture-check")
	nc.Status.Conditions = nil
	nc.ObjectMeta.CreationTimestamp = metav1.Time{Time: time.Now()}
	cond := nc.StatusConditions().Get(v1.ConditionTypeLaunched)
	if cond == nil || !cond.IsUnknown() {
		t.Fatalf("expected fresh nodeclaim Launched condition to be Unknown so the bench enters the cache path; got %+v", cond)
	}
}
