//go:build test_aspirational

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

package aspirational

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clock "k8s.io/utils/clock/testing"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/utils/pod"
)

// TestDoNotDisrupt_InvalidValueFailSafe documents the scenario from
// https://github.com/kubernetes-sigs/karpenter/pull/2874
//
// The RFC specifies that invalid do-not-disrupt annotation values should be
// treated as fail-safe (pod remains protected indefinitely), since a typo in
// a protection annotation should not accidentally expose a pod to disruption.
//
// The current implementation treats invalid values as "annotation doesn't exist"
// — the pod becomes immediately disruptable. This is the opposite of fail-safe.
//
// This test FAILS on current code because parseDoNotDisrupt returns error on
// invalid values and IsDoNotDisruptActive returns false (pod is disruptable).
func TestDoNotDisrupt_InvalidValueFailSafe(t *testing.T) {
	fakeClock := clock.NewFakeClock(time.Now())

	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				v1.DoNotDisruptAnnotationKey: "invalid-format",
			},
		},
		Status: corev1.PodStatus{
			Phase:     corev1.PodRunning,
			StartTime: &metav1.Time{Time: fakeClock.Now().Add(-5 * time.Minute)},
		},
	}

	// RFC says: invalid values should fail safe (pod stays protected)
	if !pod.IsDoNotDisruptActive(p, fakeClock, nil) {
		t.Error("IsDoNotDisruptActive() = false for invalid annotation value, want true (fail-safe behavior per RFC)")
	}
}

// TestDoNotDisrupt_ZeroDurationFailSafe documents that a zero-duration value
// should also be treated as fail-safe (protected indefinitely), not as
// "annotation doesn't exist". A user writing "0s" likely intends protection,
// not immediate disruption eligibility.
//
// This test FAILS on current code because parseDoNotDisrupt rejects d <= 0.
func TestDoNotDisrupt_ZeroDurationFailSafe(t *testing.T) {
	fakeClock := clock.NewFakeClock(time.Now())

	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				v1.DoNotDisruptAnnotationKey: "0s",
			},
		},
		Status: corev1.PodStatus{
			Phase:     corev1.PodRunning,
			StartTime: &metav1.Time{Time: fakeClock.Now().Add(-5 * time.Minute)},
		},
	}

	if !pod.IsDoNotDisruptActive(p, fakeClock, nil) {
		t.Error("IsDoNotDisruptActive() = false for zero duration, want true (fail-safe behavior per RFC)")
	}
}

// TestDoNotDisrupt_NegativeDurationFailSafe documents that negative duration
// values like "-5m" should fail safe. A negative duration has no logical
// interpretation as a grace period, so treating it as "no protection" is
// surprising and dangerous.
//
// This test FAILS on current code because parseDoNotDisrupt rejects d <= 0.
func TestDoNotDisrupt_NegativeDurationFailSafe(t *testing.T) {
	fakeClock := clock.NewFakeClock(time.Now())

	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				v1.DoNotDisruptAnnotationKey: "-5m",
			},
		},
		Status: corev1.PodStatus{
			Phase:     corev1.PodRunning,
			StartTime: &metav1.Time{Time: fakeClock.Now().Add(-5 * time.Minute)},
		},
	}

	if !pod.IsDoNotDisruptActive(p, fakeClock, nil) {
		t.Error("IsDoNotDisruptActive() = false for negative duration, want true (fail-safe behavior per RFC)")
	}
}
