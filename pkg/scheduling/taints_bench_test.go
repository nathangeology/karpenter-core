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

package scheduling_test

import (
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"sigs.k8s.io/karpenter/pkg/scheduling"
)

// BenchmarkTaintsToleration isolates the toleration predicate from any caller. The error
// arm is Tolerates, which reports every taint it rejects and so formats a message per
// rejected taint. The bool arm is IsToleratedBy, which short circuits on the first
// rejection. Callers that only test the result against nil pay the error arm today, and
// the gap between the arms is what they pay for a message they discard.
//
// The reject arm separates them. The tolerate arm is the control: both arms exit on a
// match and neither allocates, so they should agree there.
func BenchmarkTaintsToleration(b *testing.B) {
	for _, taintCount := range []int{1, 5, 20} {
		for _, tolerationCount := range []int{1, 5, 20} {
			taints := benchTaints(taintCount)
			reject := benchTolerations(tolerationCount, false)
			tolerate := benchTolerations(tolerationCount, true)

			name := fmt.Sprintf("taints=%d/tolerations=%d", taintCount, tolerationCount)
			b.Run("reject/error/"+name, func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if err := taints.Tolerates(reject); err == nil {
						b.Fatal("expected the reject arm not to tolerate")
					}
				}
			})
			b.Run("reject/bool/"+name, func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if taints.IsToleratedBy(reject) {
						b.Fatal("expected the reject arm not to tolerate")
					}
				}
			})
			b.Run("tolerate/error/"+name, func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if err := taints.Tolerates(tolerate); err != nil {
						b.Fatalf("expected the tolerate arm to tolerate: %s", err)
					}
				}
			})
			b.Run("tolerate/bool/"+name, func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if !taints.IsToleratedBy(tolerate) {
						b.Fatal("expected the tolerate arm to tolerate")
					}
				}
			})
		}
	}
}

func benchTaints(count int) scheduling.Taints {
	taints := make(scheduling.Taints, 0, count)
	for i := range count {
		taints = append(taints, corev1.Taint{
			Key:    fmt.Sprintf("bench.example.com/taint-%d", i),
			Value:  "true",
			Effect: corev1.TaintEffectNoSchedule,
		})
	}
	return taints
}

// benchTolerations builds count tolerations that between them tolerate every taint built by
// benchTaints, or none of them. Each is keyed on a distinct key that benchTaints never
// produces, so the non-tolerating slice rejects every taint whatever its length. When
// tolerates is set, the final entry is an unkeyed Exists that tolerates all of them, which
// keys the slice's tolerating power to its last element rather than its first: both the
// error and bool arms must then scan every entry, so the control arm measures the scan and
// not an early exit.
func benchTolerations(count int, tolerates bool) []corev1.Toleration {
	tolerations := make([]corev1.Toleration, 0, count)
	for i := range count {
		tolerations = append(tolerations, corev1.Toleration{
			Key:      fmt.Sprintf("bench.example.com/unmatched-%d", i),
			Operator: corev1.TolerationOpEqual,
			Value:    "true",
			Effect:   corev1.TaintEffectNoSchedule,
		})
	}
	if tolerates {
		tolerations[count-1] = corev1.Toleration{Operator: corev1.TolerationOpExists}
	}
	return tolerations
}
