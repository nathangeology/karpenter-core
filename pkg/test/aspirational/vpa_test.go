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

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"sigs.k8s.io/karpenter/pkg/utils/resources"
)

// TestInPlacePodResize_SchedulerAwareness documents the scenario from
// https://github.com/kubernetes-sigs/karpenter/issues/829
//
// When a pod is resized DOWN in place (KEP-1287), the spec shows the new
// (smaller) requests, but the node still has the old (larger) resources
// allocated until kubelet completes the resize. Karpenter's Ceiling() only
// reads spec, so it underestimates actual node usage during a pending
// downsize. This can cause the scheduler to place new pods on a node that
// doesn't actually have the capacity yet, leading to overcommitment.
//
// The correct behavior is: effective requests = max(spec.requests,
// status.allocatedResources) per resource, ensuring the scheduler never
// underestimates committed resources during a resize transition.
//
// This test FAILS on current code because Ceiling() ignores
// Status.ContainerStatuses[].AllocatedResources.
func TestInPlacePodResize_SchedulerAwareness(t *testing.T) {
	// Scenario: pod spec was updated to request 200m CPU (resize down from 1000m),
	// but kubelet hasn't released the old allocation yet.
	pod := &v1.Pod{
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: "app",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							// New (desired) value after resize-down request
							v1.ResourceCPU:    resource.MustParse("200m"),
							v1.ResourceMemory: resource.MustParse("128Mi"),
						},
					},
				},
			},
		},
		Status: v1.PodStatus{
			Resize: v1.PodResizeStatusInProgress,
			ContainerStatuses: []v1.ContainerStatus{
				{
					Name: "app",
					// Node still has the OLD (larger) allocation committed
					AllocatedResources: v1.ResourceList{
						v1.ResourceCPU:    resource.MustParse("1000m"),
						v1.ResourceMemory: resource.MustParse("512Mi"),
					},
					Resources: &v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceCPU:    resource.MustParse("1000m"),
							v1.ResourceMemory: resource.MustParse("512Mi"),
						},
					},
				},
			},
		},
	}

	ceiling := resources.Ceiling(pod)
	cpuReq := ceiling.Requests[v1.ResourceCPU]
	memReq := ceiling.Requests[v1.ResourceMemory]

	// During a pending resize-down, the scheduler must use the LARGER of
	// spec vs allocated to avoid overcommitting the node.
	expectedCPU := resource.MustParse("1000m")
	expectedMem := resource.MustParse("512Mi")

	if cpuReq.Cmp(expectedCPU) < 0 {
		t.Errorf("Ceiling() CPU = %s, want >= %s (must not underestimate during pending resize-down)",
			cpuReq.String(), expectedCPU.String())
	}
	if memReq.Cmp(expectedMem) < 0 {
		t.Errorf("Ceiling() Memory = %s, want >= %s (must not underestimate during pending resize-down)",
			memReq.String(), expectedMem.String())
	}
}

// TestInPlacePodResize_ConsolidationAwareness verifies that consolidation
// correctly accounts for in-place resize when evaluating node utilization.
//
// After a successful resize-up (pod grew from 100m to 500m CPU), the node
// has less free capacity. If Karpenter's state still reflects the old spec
// (because it cached the pod before the spec update propagated), it will
// overestimate free capacity and may attempt an invalid consolidation.
//
// This test FAILS on current code because the cluster state uses
// RequestsForPods which delegates to Ceiling with no resize awareness.
func TestInPlacePodResize_ConsolidationAwareness(t *testing.T) {
	// Scenario: pod was resized UP. Spec now says 500m (new value).
	// AllocatedResources confirms 500m is committed on the node.
	// A second pod needs 600m. Node has 1000m allocatable.
	// Available = 1000m - 500m = 500m. The second pod should NOT fit.
	resizedPod := &v1.Pod{
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: "app",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceCPU: resource.MustParse("500m"),
						},
					},
				},
			},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{
				{
					Name: "app",
					AllocatedResources: v1.ResourceList{
						v1.ResourceCPU: resource.MustParse("500m"),
					},
					Resources: &v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceCPU: resource.MustParse("500m"),
						},
					},
				},
			},
		},
	}

	podRequests := resources.RequestsForPods(resizedPod)
	cpuUsed := podRequests[v1.ResourceCPU]
	expectedCPU := resource.MustParse("500m")

	if cpuUsed.Cmp(expectedCPU) != 0 {
		t.Errorf("RequestsForPods() CPU = %s, want %s (must reflect post-resize allocation)",
			cpuUsed.String(), expectedCPU.String())
	}

	// The critical check: available capacity on a 1000m node with this pod
	// should be 500m, not 900m (which would be the case if old 100m spec was cached).
	nodeAllocatable := v1.ResourceList{
		v1.ResourceCPU: resource.MustParse("1000m"),
	}
	available := resources.Subtract(nodeAllocatable, podRequests)
	availCPU := available[v1.ResourceCPU]

	candidateRequest := resource.MustParse("600m")
	if availCPU.Cmp(candidateRequest) >= 0 {
		t.Errorf("Available CPU = %s, candidate needs %s — should not fit on node with resized pod using 500m of 1000m",
			availCPU.String(), candidateRequest.String())
	}
}
