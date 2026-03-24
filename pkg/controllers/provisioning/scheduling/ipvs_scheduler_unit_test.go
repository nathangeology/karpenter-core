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

package scheduling

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	karpopts "sigs.k8s.io/karpenter/pkg/operator/options"
	pscheduling "sigs.k8s.io/karpenter/pkg/scheduling"
	"sigs.k8s.io/karpenter/pkg/test"
)

// Unit tests for scheduling simulator IPVS awareness
// Validates: Requirements 3.2, 3.5

// Test 1: Single pod with peak annotation - verify cached requests use peak values when gate enabled
// Validates: Requirements 3.2, 3.5
func TestUpdateCachedPodData_PeakEnvelope_GateEnabled(t *testing.T) {
	ctx := karpopts.ToContext(context.Background(), test.Options(test.OptionsFields{
		FeatureGates: test.FeatureGates{
			InPlacePodVerticalScaling: ptrBoolUnit(true),
		},
	}))

	s := &Scheduler{
		cachedPodData:   make(map[types.UID]*PodData),
		volumeReqsByPod: make(map[types.UID]pscheduling.Requirements),
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "peak-pod",
			Namespace: "default",
			UID:       types.UID("uid-peak-enabled"),
			Annotations: map[string]string{
				karpv1.PeakCPUAnnotationKey:    "2",
				karpv1.PeakMemoryAnnotationKey: "4Gi",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "main",
					Image: "test:latest",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("500m"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
						},
					},
				},
			},
		},
	}

	s.updateCachedPodData(ctx, pod)

	cachedData, ok := s.cachedPodData[pod.UID]
	if !ok {
		t.Fatal("pod not found in cachedPodData")
	}

	// With gate enabled, should use peak values (2 CPU, 4Gi memory)
	cachedCPU := cachedData.Requests[corev1.ResourceCPU]
	expectedCPU := resource.MustParse("2")
	if cachedCPU.Cmp(expectedCPU) != 0 {
		t.Fatalf("CPU: got %s, want %s (peak=2, spec=500m)", cachedCPU.String(), expectedCPU.String())
	}

	cachedMem := cachedData.Requests[corev1.ResourceMemory]
	expectedMem := resource.MustParse("4Gi")
	if cachedMem.Cmp(expectedMem) != 0 {
		t.Fatalf("Memory: got %s, want %s (peak=4Gi, spec=1Gi)", cachedMem.String(), expectedMem.String())
	}
}

// Test 2: Single pod with peak annotation - verify cached requests use spec values when gate disabled
// Validates: Requirements 3.2, 3.5
func TestUpdateCachedPodData_SpecRequests_GateDisabled(t *testing.T) {
	ctx := karpopts.ToContext(context.Background(), test.Options(test.OptionsFields{
		FeatureGates: test.FeatureGates{
			InPlacePodVerticalScaling: ptrBoolUnit(false),
		},
	}))

	s := &Scheduler{
		cachedPodData:   make(map[types.UID]*PodData),
		volumeReqsByPod: make(map[types.UID]pscheduling.Requirements),
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "spec-pod",
			Namespace: "default",
			UID:       types.UID("uid-spec-disabled"),
			Annotations: map[string]string{
				karpv1.PeakCPUAnnotationKey:    "2",
				karpv1.PeakMemoryAnnotationKey: "4Gi",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "main",
					Image: "test:latest",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("500m"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
						},
					},
				},
			},
		},
	}

	s.updateCachedPodData(ctx, pod)

	cachedData, ok := s.cachedPodData[pod.UID]
	if !ok {
		t.Fatal("pod not found in cachedPodData")
	}

	// With gate disabled, should use spec values (500m CPU, 1Gi memory), ignoring peak annotations
	cachedCPU := cachedData.Requests[corev1.ResourceCPU]
	expectedCPU := resource.MustParse("500m")
	if cachedCPU.Cmp(expectedCPU) != 0 {
		t.Fatalf("CPU: got %s, want %s (gate disabled, spec=500m, peak=2)", cachedCPU.String(), expectedCPU.String())
	}

	cachedMem := cachedData.Requests[corev1.ResourceMemory]
	expectedMem := resource.MustParse("1Gi")
	if cachedMem.Cmp(expectedMem) != 0 {
		t.Fatalf("Memory: got %s, want %s (gate disabled, spec=1Gi, peak=4Gi)", cachedMem.String(), expectedMem.String())
	}
}

// Test 3: Multi-pod scenario - pods that together exceed node capacity at peak but fit at current requests.
// Verifies the scheduler uses peak values when gate is enabled, preventing unsafe consolidation.
// Validates: Requirements 3.2, 3.5
func TestUpdateCachedPodData_MultiPod_PeakPreventsUnsafeConsolidation(t *testing.T) {
	ctx := karpopts.ToContext(context.Background(), test.Options(test.OptionsFields{
		FeatureGates: test.FeatureGates{
			InPlacePodVerticalScaling: ptrBoolUnit(true),
		},
	}))

	s := &Scheduler{
		cachedPodData:   make(map[types.UID]*PodData),
		volumeReqsByPod: make(map[types.UID]pscheduling.Requirements),
	}

	// Pod A: current 500m CPU, peak 2 CPU
	podA := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pod-a", Namespace: "default",
			UID: types.UID("uid-a"),
			Annotations: map[string]string{
				karpv1.PeakCPUAnnotationKey:    "2",
				karpv1.PeakMemoryAnnotationKey: "2Gi",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name: "main", Image: "test:latest",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("512Mi"),
					},
				},
			}},
		},
	}

	// Pod B: current 500m CPU, peak 2 CPU
	podB := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pod-b", Namespace: "default",
			UID: types.UID("uid-b"),
			Annotations: map[string]string{
				karpv1.PeakCPUAnnotationKey:    "2",
				karpv1.PeakMemoryAnnotationKey: "2Gi",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name: "main", Image: "test:latest",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("512Mi"),
					},
				},
			}},
		},
	}

	// Pod C: current 500m CPU, peak 2 CPU
	podC := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pod-c", Namespace: "default",
			UID: types.UID("uid-c"),
			Annotations: map[string]string{
				karpv1.PeakCPUAnnotationKey:    "2",
				karpv1.PeakMemoryAnnotationKey: "2Gi",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name: "main", Image: "test:latest",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("512Mi"),
					},
				},
			}},
		},
	}

	s.updateCachedPodData(ctx, podA)
	s.updateCachedPodData(ctx, podB)
	s.updateCachedPodData(ctx, podC)

	// At current requests: 3 pods × 500m = 1.5 CPU total (fits on a 2-CPU node)
	// At peak: 3 pods × 2 CPU = 6 CPU total (does NOT fit on a 2-CPU node)
	// The scheduler must use peak values to prevent unsafe consolidation.

	totalPeakCPU := resource.Quantity{}
	for _, uid := range []types.UID{"uid-a", "uid-b", "uid-c"} {
		cachedData, ok := s.cachedPodData[uid]
		if !ok {
			t.Fatalf("pod %s not found in cachedPodData", uid)
		}
		cpu := cachedData.Requests[corev1.ResourceCPU]
		totalPeakCPU.Add(cpu)
	}

	// Total should be 6 CPU (3 × 2 CPU peak), not 1.5 CPU (3 × 500m spec)
	expectedTotal := resource.MustParse("6")
	if totalPeakCPU.Cmp(expectedTotal) != 0 {
		t.Fatalf("total CPU across 3 pods: got %s, want %s (each pod: spec=500m, peak=2)",
			totalPeakCPU.String(), expectedTotal.String())
	}

	// Verify this exceeds a hypothetical 4-CPU node capacity
	nodeCapacity := resource.MustParse("4")
	if totalPeakCPU.Cmp(nodeCapacity) <= 0 {
		t.Fatal("total peak CPU should exceed 4-CPU node capacity to prevent unsafe consolidation")
	}
}

// Test 4: Pod with no peak annotation - verify it uses spec requests regardless of gate state
// Validates: Requirements 3.2, 3.5
func TestUpdateCachedPodData_NoPeakAnnotation_UsesSpecRequests(t *testing.T) {
	for _, gateEnabled := range []bool{true, false} {
		name := "gate_enabled"
		if !gateEnabled {
			name = "gate_disabled"
		}
		t.Run(name, func(t *testing.T) {
			enabled := gateEnabled
			ctx := karpopts.ToContext(context.Background(), test.Options(test.OptionsFields{
				FeatureGates: test.FeatureGates{
					InPlacePodVerticalScaling: &enabled,
				},
			}))

			s := &Scheduler{
				cachedPodData:   make(map[types.UID]*PodData),
				volumeReqsByPod: make(map[types.UID]pscheduling.Requirements),
			}

			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "no-annotation-pod",
					Namespace: "default",
					UID:       types.UID("uid-no-annotation"),
					// No peak annotations
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name: "main", Image: "test:latest",
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("2Gi"),
							},
						},
					}},
				},
			}

			s.updateCachedPodData(ctx, pod)

			cachedData, ok := s.cachedPodData[pod.UID]
			if !ok {
				t.Fatal("pod not found in cachedPodData")
			}

			// Without peak annotations, spec requests should be used regardless of gate state
			cachedCPU := cachedData.Requests[corev1.ResourceCPU]
			expectedCPU := resource.MustParse("1")
			if cachedCPU.Cmp(expectedCPU) != 0 {
				t.Fatalf("CPU: got %s, want %s (no peak annotation)", cachedCPU.String(), expectedCPU.String())
			}

			cachedMem := cachedData.Requests[corev1.ResourceMemory]
			expectedMem := resource.MustParse("2Gi")
			if cachedMem.Cmp(expectedMem) != 0 {
				t.Fatalf("Memory: got %s, want %s (no peak annotation)", cachedMem.String(), expectedMem.String())
			}
		})
	}
}

// Test 5: Pod with allocated resources higher than spec but lower than peak -
// verify max of all three is used (peak wins)
// Validates: Requirements 3.2, 3.5
func TestUpdateCachedPodData_AllocatedHigherThanSpec_PeakWins(t *testing.T) {
	ctx := karpopts.ToContext(context.Background(), test.Options(test.OptionsFields{
		FeatureGates: test.FeatureGates{
			InPlacePodVerticalScaling: ptrBoolUnit(true),
		},
	}))

	s := &Scheduler{
		cachedPodData:   make(map[types.UID]*PodData),
		volumeReqsByPod: make(map[types.UID]pscheduling.Requirements),
	}

	// spec=500m, allocated=1, peak=2 → should use peak (2 CPU)
	// spec=512Mi, allocated=1Gi, peak=4Gi → should use peak (4Gi)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "alloc-pod",
			Namespace: "default",
			UID:       types.UID("uid-alloc"),
			Annotations: map[string]string{
				karpv1.PeakCPUAnnotationKey:    "2",
				karpv1.PeakMemoryAnnotationKey: "4Gi",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name: "main", Image: "test:latest",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("512Mi"),
					},
				},
			}},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "main",
				AllocatedResources: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
				},
			}},
		},
	}

	s.updateCachedPodData(ctx, pod)

	cachedData, ok := s.cachedPodData[pod.UID]
	if !ok {
		t.Fatal("pod not found in cachedPodData")
	}

	// Peak (2 CPU) > Allocated (1 CPU) > Spec (500m) → should use 2 CPU
	cachedCPU := cachedData.Requests[corev1.ResourceCPU]
	expectedCPU := resource.MustParse("2")
	if cachedCPU.Cmp(expectedCPU) != 0 {
		t.Fatalf("CPU: got %s, want %s (spec=500m, allocated=1, peak=2)", cachedCPU.String(), expectedCPU.String())
	}

	// Peak (4Gi) > Allocated (1Gi) > Spec (512Mi) → should use 4Gi
	cachedMem := cachedData.Requests[corev1.ResourceMemory]
	expectedMem := resource.MustParse("4Gi")
	if cachedMem.Cmp(expectedMem) != 0 {
		t.Fatalf("Memory: got %s, want %s (spec=512Mi, allocated=1Gi, peak=4Gi)", cachedMem.String(), expectedMem.String())
	}
}

// ptrBoolUnit returns a pointer to a bool value (local to unit test file to avoid redeclaration).
func ptrBoolUnit(b bool) *bool {
	return &b
}
