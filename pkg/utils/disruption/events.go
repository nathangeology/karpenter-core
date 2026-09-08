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
	"fmt"

	corev1 "k8s.io/api/core/v1"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/events"
)

// MalformedDisruptionCostAnnotationEvent surfaces a parse failure on the
// karpenter.sh/disruption-cost annotation as a Warning on the offending pod.
// The parse-failure path in EvictionCost falls back to the default cost, so
// this event is the sole loud signal that a user-set annotation is being
// ignored.
func MalformedDisruptionCostAnnotationEvent(pod *corev1.Pod, value string, err error) events.Event {
	return events.Event{
		InvolvedObject: pod,
		Type:           corev1.EventTypeWarning,
		Reason:         "MalformedDisruptionCostAnnotation",
		Message: fmt.Sprintf(
			"Annotation %q value %q is not a valid int32 (%s); using default disruption cost",
			v1.DisruptionCostAnnotationKey, value, err.Error(),
		),
		DedupeValues: []string{string(pod.UID), value},
	}
}

// MalformedPodDeletionCostAnnotationEvent surfaces a parse failure on the
// controller.kubernetes.io/pod-deletion-cost annotation. Same fall-back
// semantics as MalformedDisruptionCostAnnotationEvent: default cost is used,
// the reconcile continues, and this event is the loud signal.
func MalformedPodDeletionCostAnnotationEvent(pod *corev1.Pod, value string, err error) events.Event {
	return events.Event{
		InvolvedObject: pod,
		Type:           corev1.EventTypeWarning,
		Reason:         "MalformedPodDeletionCostAnnotation",
		Message: fmt.Sprintf(
			"Annotation %q value %q is not a valid int32 (%s); using default disruption cost",
			corev1.PodDeletionCost, value, err.Error(),
		),
		DedupeValues: []string{string(pod.UID), value},
	}
}
