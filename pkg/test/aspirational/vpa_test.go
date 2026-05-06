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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	karpresources "sigs.k8s.io/karpenter/pkg/utils/resources"
)

// TestInPlacePodResize_ClusterStateTracking documents the scenario from
// https://github.com/kubernetes-sigs/karpenter/issues/829
//
// When a pod is resized in place (KEP-1287 InPlacePodVerticalScaling), the
// kubelet updates status.containerStatuses[].allocatedResources to reflect
// the actual resources allocated to the container. During the resize
// transition, the allocated resources may differ from spec.requests.
//
// Karpenter computes pod resource usage via:
//
//	resources.Ceiling(pod) → resourcehelper.PodRequests(pod, PodResourcesOptions{})
//
// With UseStatusResources=false (the default), this ONLY reads
// spec.containers[].resources.requests and ignores the status entirely.
//
// This means:
// - Resize UP (1 CPU → 3 CPU): If spec is updated but kubelet hasn't fully
//   actuated, Karpenter sees the new spec value. OK in this case.
// - Resize DOWN (3 CPU → 1 CPU): Spec shows 1 CPU but kubelet still has
//   3 CPU allocated. Karpenter thinks only 1 CPU is used → may consolidate
//   the node prematurely, causing OOM or throttling.
// - Status.AllocatedResources tracking: Karpenter never reads this field,
//   so it can't make informed decisions during resize transitions.
//
// This test verifies that Karpenter's resource calculation considers
// status.containerStatuses[].allocatedResources when present. It will pass
// once Karpenter uses UseStatusResources=true or implements equivalent logic.
func TestInPlacePodResize_ClusterStateTracking(t *testing.T) {
	// Simulate a pod that has been resized DOWN: spec says 1 CPU but
	// the kubelet still has 3 CPU allocated (resize in progress).
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "resizable-pod",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name: "app",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"), // NEW (resized down)
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
				},
			}},
		},
		Status: corev1.PodStatus{
			// Resize is in progress — kubelet still allocates the OLD (higher) resources
			Resize: corev1.PodResizeStatusInProgress,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "app",
				AllocatedResources: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("3"), // OLD (not yet released)
					corev1.ResourceMemory: resource.MustParse("1Gi"),
				},
			}},
		},
	}

	// Karpenter's Ceiling() function computes what it thinks the pod uses
	computed := karpresources.Ceiling(pod)
	computedCPU := computed.Requests[corev1.ResourceCPU]

	// The SAFE value during a resize-down is max(spec.requests, status.allocated)
	// = max(1, 3) = 3 CPU. Using the spec value (1 CPU) is UNSAFE because the
	// kubelet still has 3 CPU allocated — consolidating based on 1 CPU could
	// cause resource pressure.
	expectedSafeCPU := resource.MustParse("3")

	if computedCPU.Cmp(expectedSafeCPU) != 0 {
		t.Fatalf("Karpenter does not account for in-place pod resize status: "+
			"pod spec.requests shows %s CPU but status.allocatedResources shows %s CPU "+
			"(resize in progress). Karpenter's Ceiling() reports %s CPU. "+
			"During resize-down, the safe value is max(spec, allocated) = %s CPU, "+
			"but Karpenter only reads spec.requests (UseStatusResources=false in PodResourcesOptions). "+
			"This can cause premature consolidation during resize transitions. "+
			"See https://github.com/kubernetes-sigs/karpenter/issues/829",
			pod.Spec.Containers[0].Resources.Requests.Cpu().String(),
			pod.Status.ContainerStatuses[0].AllocatedResources.Cpu().String(),
			computedCPU.String(),
			expectedSafeCPU.String())
	}
}
