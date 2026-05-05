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

	"sigs.k8s.io/karpenter/pkg/test"
	"sigs.k8s.io/karpenter/pkg/utils/resources"
)

// TestInPlacePodResize_SchedulerAwareness documents the scenario from
// https://github.com/kubernetes-sigs/karpenter/issues/829
//
// When a pod is resized in place (KEP-1287 InPlacePodVerticalScaling), the
// scheduler and consolidation logic should account for the new resource
// requests — specifically the values reported in
// pod.Status.ContainerStatuses[].Resources — rather than the original
// pod.Spec.Containers[].Resources.
//
// Currently Karpenter calls resourcehelper.PodRequests with
// UseStatusResources=false, so it always uses spec values. This means:
//   - A pod resized UP will be under-accounted, risking node overcommitment.
//   - A pod resized DOWN will be over-accounted, blocking valid consolidation.
//
// This test FAILS on current code and will PASS once Karpenter respects
// in-place resize status.
func TestInPlacePodResize_SchedulerAwareness(t *testing.T) {
	// Original spec: 100m CPU, 128Mi memory
	pod := test.Pod(test.PodOptions{
		ResourceRequirements: v1.ResourceRequirements{
			Requests: v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("100m"),
				v1.ResourceMemory: resource.MustParse("128Mi"),
			},
		},
		Phase: v1.PodRunning,
	})
	pod.Spec.NodeName = "test-node"

	// Simulate in-place resize: kubelet has allocated 500m CPU, 512Mi memory
	pod.Status.ContainerStatuses = []v1.ContainerStatus{
		{
			Name: pod.Spec.Containers[0].Name,
			Resources: &v1.ResourceRequirements{
				Requests: v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("500m"),
					v1.ResourceMemory: resource.MustParse("512Mi"),
				},
			},
			AllocatedResources: v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("500m"),
				v1.ResourceMemory: resource.MustParse("512Mi"),
			},
		},
	}
	pod.Status.Resize = v1.PodResizeStatusInProgress

	// Karpenter's resource accounting should reflect the resized values
	effective := resources.Ceiling(pod)

	cpuReq := effective.Requests[v1.ResourceCPU]
	memReq := effective.Requests[v1.ResourceMemory]

	expectedCPU := resource.MustParse("500m")
	expectedMem := resource.MustParse("512Mi")

	if cpuReq.Cmp(expectedCPU) != 0 {
		t.Errorf("CPU request: got %s, want %s (Karpenter ignores in-place resize status)", &cpuReq, &expectedCPU)
	}
	if memReq.Cmp(expectedMem) != 0 {
		t.Errorf("Memory request: got %s, want %s (Karpenter ignores in-place resize status)", &memReq, &expectedMem)
	}
}

// TestInPlacePodResize_ConsolidationAccountsForDownsize verifies that when a
// pod is resized DOWN in place, consolidation sees the reduced resource usage
// and can pack nodes more tightly.
//
// This test FAILS on current code because Karpenter uses the original (larger)
// spec requests, missing the consolidation opportunity.
func TestInPlacePodResize_ConsolidationAccountsForDownsize(t *testing.T) {
	// Original spec: 2 CPU, 4Gi memory (large)
	pod := test.Pod(test.PodOptions{
		ResourceRequirements: v1.ResourceRequirements{
			Requests: v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("2"),
				v1.ResourceMemory: resource.MustParse("4Gi"),
			},
		},
		Phase: v1.PodRunning,
	})
	pod.Spec.NodeName = "test-node"

	// Simulate in-place downsize: kubelet now reports 500m CPU, 1Gi memory
	pod.Status.ContainerStatuses = []v1.ContainerStatus{
		{
			Name: pod.Spec.Containers[0].Name,
			Resources: &v1.ResourceRequirements{
				Requests: v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("500m"),
					v1.ResourceMemory: resource.MustParse("1Gi"),
				},
			},
			AllocatedResources: v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("500m"),
				v1.ResourceMemory: resource.MustParse("1Gi"),
			},
		},
	}
	pod.Status.Resize = v1.PodResizeStatusInProgress

	effective := resources.Ceiling(pod)

	cpuReq := effective.Requests[v1.ResourceCPU]
	memReq := effective.Requests[v1.ResourceMemory]

	// After downsize, effective requests should be the smaller values
	expectedCPU := resource.MustParse("500m")
	expectedMem := resource.MustParse("1Gi")

	if cpuReq.Cmp(expectedCPU) != 0 {
		t.Errorf("CPU request after downsize: got %s, want %s (consolidation misses opportunity)", &cpuReq, &expectedCPU)
	}
	if memReq.Cmp(expectedMem) != 0 {
		t.Errorf("Memory request after downsize: got %s, want %s (consolidation misses opportunity)", &memReq, &expectedMem)
	}
}
