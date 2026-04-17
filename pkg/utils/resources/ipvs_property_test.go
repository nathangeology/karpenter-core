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

package resources_test

import (
	"fmt"
	"math/rand"
	"testing"

	opmetrics "github.com/awslabs/operatorpkg/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"pgregory.net/rapid"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/metrics"
	"sigs.k8s.io/karpenter/pkg/utils/resources"
)

// Feature: in-place-pod-vertical-scaling, Property 1: IPVS-aware resource computation returns the maximum across all sources
// Validates: Requirements 1.1, 1.2, 2.1, 2.4
func TestProperty1_IPVSAwareResourceComputationReturnsMaximum(t *testing.T) {
	// Resize statuses to test: InProgress, Infeasible, Proposed (string literal), and empty
	resizeStatuses := []v1.PodResizeStatus{
		v1.PodResizeStatusInProgress,
		v1.PodResizeStatusInfeasible,
		v1.PodResizeStatus("Proposed"),
		v1.PodResizeStatus(""),
	}

	rapid.Check(t, func(t *rapid.T) {
		// Generate random CPU values in millicores (100m - 8000m)
		specCPUMilli := rapid.IntRange(100, 8000).Draw(t, "specCPUMilli")
		allocCPUMilli := rapid.IntRange(100, 8000).Draw(t, "allocCPUMilli")
		peakCPUMilli := rapid.IntRange(100, 8000).Draw(t, "peakCPUMilli")

		// Generate random memory values in MiB (64Mi - 16384Mi = 16Gi)
		specMemMi := rapid.IntRange(64, 16384).Draw(t, "specMemMi")
		allocMemMi := rapid.IntRange(64, 16384).Draw(t, "allocMemMi")
		peakMemMi := rapid.IntRange(64, 16384).Draw(t, "peakMemMi")

		// Pick a random resize status
		resizeStatus := resizeStatuses[rapid.IntRange(0, len(resizeStatuses)-1).Draw(t, "resizeStatusIdx")]

		specCPU := resource.MustParse(fmt.Sprintf("%dm", specCPUMilli))
		specMem := resource.MustParse(fmt.Sprintf("%dMi", specMemMi))
		allocCPU := resource.MustParse(fmt.Sprintf("%dm", allocCPUMilli))
		allocMem := resource.MustParse(fmt.Sprintf("%dMi", allocMemMi))
		peakCPU := resource.MustParse(fmt.Sprintf("%dm", peakCPUMilli))
		peakMem := resource.MustParse(fmt.Sprintf("%dMi", peakMemMi))

		// Build a pod with spec requests, allocated resources, and peak annotations
		pod := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod",
				Namespace: "default",
				Annotations: map[string]string{
					karpv1.PeakCPUAnnotationKey:    peakCPU.String(),
					karpv1.PeakMemoryAnnotationKey: peakMem.String(),
				},
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name:  "main",
						Image: "test:latest",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU:    specCPU,
								v1.ResourceMemory: specMem,
							},
						},
					},
				},
			},
			Status: v1.PodStatus{
				Resize: resizeStatus,
				ContainerStatuses: []v1.ContainerStatus{
					{
						Name: "main",
						AllocatedResources: v1.ResourceList{
							v1.ResourceCPU:    allocCPU,
							v1.ResourceMemory: allocMem,
						},
					},
				},
			},
		}

		result := resources.IPVSAwareRequestsForPod(pod)

		// Compute expected max for CPU
		expectedCPU := specCPU.DeepCopy()
		if allocCPU.Cmp(expectedCPU) > 0 {
			expectedCPU = allocCPU.DeepCopy()
		}
		if peakCPU.Cmp(expectedCPU) > 0 {
			expectedCPU = peakCPU.DeepCopy()
		}

		// Compute expected max for memory
		expectedMem := specMem.DeepCopy()
		if allocMem.Cmp(expectedMem) > 0 {
			expectedMem = allocMem.DeepCopy()
		}
		if peakMem.Cmp(expectedMem) > 0 {
			expectedMem = peakMem.DeepCopy()
		}

		resultCPU := result[v1.ResourceCPU]
		resultMem := result[v1.ResourceMemory]

		if resultCPU.Cmp(expectedCPU) != 0 {
			t.Fatalf("CPU mismatch: got %s, expected %s (spec=%s, alloc=%s, peak=%s, resize=%q)",
				resultCPU.String(), expectedCPU.String(),
				specCPU.String(), allocCPU.String(), peakCPU.String(), resizeStatus)
		}
		if resultMem.Cmp(expectedMem) != 0 {
			t.Fatalf("Memory mismatch: got %s, expected %s (spec=%s, alloc=%s, peak=%s, resize=%q)",
				resultMem.String(), expectedMem.String(),
				specMem.String(), allocMem.String(), peakMem.String(), resizeStatus)
		}
	})
}

// Feature: in-place-pod-vertical-scaling, Property 2: Feature gate disabled preserves current behavior
// Validates: Requirements 1.4, 5.2
func TestProperty2_FeatureGateDisabledPreservesCurrentBehavior(t *testing.T) {
	// Resize statuses to exercise: InProgress, Infeasible, Proposed, and empty
	resizeStatuses := []v1.PodResizeStatus{
		v1.PodResizeStatusInProgress,
		v1.PodResizeStatusInfeasible,
		v1.PodResizeStatus("Proposed"),
		v1.PodResizeStatus(""),
	}

	rapid.Check(t, func(t *rapid.T) {
		// Generate random spec requests (CPU 100-8000m, memory 64Mi-16Gi)
		specCPUMilli := rapid.IntRange(100, 8000).Draw(t, "specCPUMilli")
		specMemMi := rapid.IntRange(64, 16384).Draw(t, "specMemMi")

		// Generate random AllocatedResources (may differ from spec)
		allocCPUMilli := rapid.IntRange(100, 8000).Draw(t, "allocCPUMilli")
		allocMemMi := rapid.IntRange(64, 16384).Draw(t, "allocMemMi")

		// Generate random peak annotations
		peakCPUMilli := rapid.IntRange(100, 8000).Draw(t, "peakCPUMilli")
		peakMemMi := rapid.IntRange(64, 16384).Draw(t, "peakMemMi")

		// Pick a random resize status
		resizeStatus := resizeStatuses[rapid.IntRange(0, len(resizeStatuses)-1).Draw(t, "resizeStatusIdx")]

		specCPU := resource.MustParse(fmt.Sprintf("%dm", specCPUMilli))
		specMem := resource.MustParse(fmt.Sprintf("%dMi", specMemMi))
		allocCPU := resource.MustParse(fmt.Sprintf("%dm", allocCPUMilli))
		allocMem := resource.MustParse(fmt.Sprintf("%dMi", allocMemMi))
		peakCPU := resource.MustParse(fmt.Sprintf("%dm", peakCPUMilli))
		peakMem := resource.MustParse(fmt.Sprintf("%dMi", peakMemMi))

		// Randomly decide whether to include init containers (to exercise Ceiling logic)
		hasInitContainer := rapid.Bool().Draw(t, "hasInitContainer")

		// Build a pod with IPVS data: spec requests, allocated resources, peak annotations, resize status
		pod := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("test-pod-%d", rand.Intn(10000)),
				Namespace: "default",
				Annotations: map[string]string{
					karpv1.PeakCPUAnnotationKey:    peakCPU.String(),
					karpv1.PeakMemoryAnnotationKey: peakMem.String(),
				},
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name:  "main",
						Image: "test:latest",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU:    specCPU,
								v1.ResourceMemory: specMem,
							},
						},
					},
				},
			},
			Status: v1.PodStatus{
				Resize: resizeStatus,
				ContainerStatuses: []v1.ContainerStatus{
					{
						Name: "main",
						AllocatedResources: v1.ResourceList{
							v1.ResourceCPU:    allocCPU,
							v1.ResourceMemory: allocMem,
						},
					},
				},
			},
		}

		// Optionally add an init container with higher resources to exercise Ceiling logic
		if hasInitContainer {
			initCPUMilli := rapid.IntRange(100, 8000).Draw(t, "initCPUMilli")
			initMemMi := rapid.IntRange(64, 16384).Draw(t, "initMemMi")
			pod.Spec.InitContainers = []v1.Container{
				{
					Name:  "init",
					Image: "init:latest",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%dm", initCPUMilli)),
							v1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", initMemMi)),
						},
					},
				},
			}
		}

		// When the feature gate is disabled, callers use RequestsForPods (not IPVSAwareRequestsForPod).
		// RequestsForPods computes Ceiling(pod).Requests + pod count, ignoring all IPVS data.
		result := resources.RequestsForPods(pod)

		// Compute expected: Ceiling(pod).Requests is the spec-based computation
		// (which accounts for init containers via PodRequests), plus pods=1
		expectedRequests := resources.Ceiling(pod).Requests
		expectedRequests[v1.ResourcePods] = *resource.NewQuantity(1, resource.DecimalExponent)

		// Assert CPU matches spec-based computation (ignoring AllocatedResources and peak annotations)
		resultCPU := result[v1.ResourceCPU]
		expectedCPU := expectedRequests[v1.ResourceCPU]
		if resultCPU.Cmp(expectedCPU) != 0 {
			t.Fatalf("CPU mismatch: RequestsForPods returned %s, expected Ceiling-based %s "+
				"(spec=%s, alloc=%s, peak=%s, resize=%q)",
				resultCPU.String(), expectedCPU.String(),
				specCPU.String(), allocCPU.String(), peakCPU.String(), resizeStatus)
		}

		// Assert memory matches spec-based computation
		resultMem := result[v1.ResourceMemory]
		expectedMem := expectedRequests[v1.ResourceMemory]
		if resultMem.Cmp(expectedMem) != 0 {
			t.Fatalf("Memory mismatch: RequestsForPods returned %s, expected Ceiling-based %s "+
				"(spec=%s, alloc=%s, peak=%s, resize=%q)",
				resultMem.String(), expectedMem.String(),
				specMem.String(), allocMem.String(), peakMem.String(), resizeStatus)
		}

		// Assert pod count is 1
		resultPods := result[v1.ResourcePods]
		expectedPods := expectedRequests[v1.ResourcePods]
		if resultPods.Cmp(expectedPods) != 0 {
			t.Fatalf("Pods count mismatch: got %s, expected %s",
				resultPods.String(), expectedPods.String())
		}
	})
}

// Feature: in-place-pod-vertical-scaling, Property 3: Invalid peak annotations fall back to effective requests
// Validates: Requirements 2.2, 2.3
func TestProperty3_InvalidPeakAnnotationsFallBackToEffectiveRequests(t *testing.T) {
	// Pool of invalid annotation strings that cannot be parsed as Kubernetes resource quantities
	invalidAnnotations := []string{
		"",
		"abc",
		"-1",
		"1.5.3",
		"not-a-number",
		"foo bar",
		"12x34",
		"Mi",
		"!!invalid!!",
	}

	t.Run("BothAnnotationsInvalid", func(t *testing.T) {
		rapid.Check(t, func(t *rapid.T) {
			// Generate random spec requests (CPU 100-8000m, memory 64Mi-16Gi)
			specCPUMilli := rapid.IntRange(100, 8000).Draw(t, "specCPUMilli")
			specMemMi := rapid.IntRange(64, 16384).Draw(t, "specMemMi")

			// Generate random AllocatedResources
			allocCPUMilli := rapid.IntRange(100, 8000).Draw(t, "allocCPUMilli")
			allocMemMi := rapid.IntRange(64, 16384).Draw(t, "allocMemMi")

			// Pick random invalid annotation strings
			invalidCPUIdx := rapid.IntRange(0, len(invalidAnnotations)-1).Draw(t, "invalidCPUIdx")
			invalidMemIdx := rapid.IntRange(0, len(invalidAnnotations)-1).Draw(t, "invalidMemIdx")
			invalidCPU := invalidAnnotations[invalidCPUIdx]
			invalidMem := invalidAnnotations[invalidMemIdx]

			specCPU := resource.MustParse(fmt.Sprintf("%dm", specCPUMilli))
			specMem := resource.MustParse(fmt.Sprintf("%dMi", specMemMi))
			allocCPU := resource.MustParse(fmt.Sprintf("%dm", allocCPUMilli))
			allocMem := resource.MustParse(fmt.Sprintf("%dMi", allocMemMi))

			pod := &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
					Annotations: map[string]string{
						karpv1.PeakCPUAnnotationKey:    invalidCPU,
						karpv1.PeakMemoryAnnotationKey: invalidMem,
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  "main",
							Image: "test:latest",
							Resources: v1.ResourceRequirements{
								Requests: v1.ResourceList{
									v1.ResourceCPU:    specCPU,
									v1.ResourceMemory: specMem,
								},
							},
						},
					},
				},
				Status: v1.PodStatus{
					ContainerStatuses: []v1.ContainerStatus{
						{
							Name: "main",
							AllocatedResources: v1.ResourceList{
								v1.ResourceCPU:    allocCPU,
								v1.ResourceMemory: allocMem,
							},
						},
					},
				},
			}

			result := resources.IPVSAwareRequestsForPod(pod)

			// When both annotations are invalid, effective = max(spec.requests, allocatedResources)
			expectedCPU := specCPU.DeepCopy()
			if allocCPU.Cmp(expectedCPU) > 0 {
				expectedCPU = allocCPU.DeepCopy()
			}
			expectedMem := specMem.DeepCopy()
			if allocMem.Cmp(expectedMem) > 0 {
				expectedMem = allocMem.DeepCopy()
			}

			resultCPU := result[v1.ResourceCPU]
			resultMem := result[v1.ResourceMemory]

			if resultCPU.Cmp(expectedCPU) != 0 {
				t.Fatalf("CPU mismatch with both invalid annotations: got %s, expected %s "+
					"(spec=%s, alloc=%s, invalidCPUAnnotation=%q, invalidMemAnnotation=%q)",
					resultCPU.String(), expectedCPU.String(),
					specCPU.String(), allocCPU.String(), invalidCPU, invalidMem)
			}
			if resultMem.Cmp(expectedMem) != 0 {
				t.Fatalf("Memory mismatch with both invalid annotations: got %s, expected %s "+
					"(spec=%s, alloc=%s, invalidCPUAnnotation=%q, invalidMemAnnotation=%q)",
					resultMem.String(), expectedMem.String(),
					specMem.String(), allocMem.String(), invalidCPU, invalidMem)
			}
		})
	})

	t.Run("OnlyMemoryAnnotationInvalid_CPUValid", func(t *testing.T) {
		rapid.Check(t, func(t *rapid.T) {
			// Generate random spec requests
			specCPUMilli := rapid.IntRange(100, 8000).Draw(t, "specCPUMilli")
			specMemMi := rapid.IntRange(64, 16384).Draw(t, "specMemMi")

			// Generate random AllocatedResources
			allocCPUMilli := rapid.IntRange(100, 8000).Draw(t, "allocCPUMilli")
			allocMemMi := rapid.IntRange(64, 16384).Draw(t, "allocMemMi")

			// Valid peak CPU annotation
			peakCPUMilli := rapid.IntRange(100, 8000).Draw(t, "peakCPUMilli")
			// Invalid memory annotation
			invalidMemIdx := rapid.IntRange(0, len(invalidAnnotations)-1).Draw(t, "invalidMemIdx")
			invalidMem := invalidAnnotations[invalidMemIdx]

			specCPU := resource.MustParse(fmt.Sprintf("%dm", specCPUMilli))
			specMem := resource.MustParse(fmt.Sprintf("%dMi", specMemMi))
			allocCPU := resource.MustParse(fmt.Sprintf("%dm", allocCPUMilli))
			allocMem := resource.MustParse(fmt.Sprintf("%dMi", allocMemMi))
			peakCPU := resource.MustParse(fmt.Sprintf("%dm", peakCPUMilli))

			pod := &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
					Annotations: map[string]string{
						karpv1.PeakCPUAnnotationKey:    peakCPU.String(),
						karpv1.PeakMemoryAnnotationKey: invalidMem,
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  "main",
							Image: "test:latest",
							Resources: v1.ResourceRequirements{
								Requests: v1.ResourceList{
									v1.ResourceCPU:    specCPU,
									v1.ResourceMemory: specMem,
								},
							},
						},
					},
				},
				Status: v1.PodStatus{
					ContainerStatuses: []v1.ContainerStatus{
						{
							Name: "main",
							AllocatedResources: v1.ResourceList{
								v1.ResourceCPU:    allocCPU,
								v1.ResourceMemory: allocMem,
							},
						},
					},
				},
			}

			result := resources.IPVSAwareRequestsForPod(pod)

			// CPU: valid peak annotation participates in max(spec, alloc, peak)
			expectedCPU := specCPU.DeepCopy()
			if allocCPU.Cmp(expectedCPU) > 0 {
				expectedCPU = allocCPU.DeepCopy()
			}
			if peakCPU.Cmp(expectedCPU) > 0 {
				expectedCPU = peakCPU.DeepCopy()
			}

			// Memory: invalid annotation is ignored, so effective = max(spec, alloc)
			expectedMem := specMem.DeepCopy()
			if allocMem.Cmp(expectedMem) > 0 {
				expectedMem = allocMem.DeepCopy()
			}

			resultCPU := result[v1.ResourceCPU]
			resultMem := result[v1.ResourceMemory]

			if resultCPU.Cmp(expectedCPU) != 0 {
				t.Fatalf("CPU mismatch (valid CPU annotation, invalid mem): got %s, expected %s "+
					"(spec=%s, alloc=%s, peak=%s)",
					resultCPU.String(), expectedCPU.String(),
					specCPU.String(), allocCPU.String(), peakCPU.String())
			}
			if resultMem.Cmp(expectedMem) != 0 {
				t.Fatalf("Memory mismatch (valid CPU annotation, invalid mem): got %s, expected %s "+
					"(spec=%s, alloc=%s, invalidMemAnnotation=%q)",
					resultMem.String(), expectedMem.String(),
					specMem.String(), allocMem.String(), invalidMem)
			}
		})
	})

	t.Run("OnlyCPUAnnotationInvalid_MemoryValid", func(t *testing.T) {
		rapid.Check(t, func(t *rapid.T) {
			// Generate random spec requests
			specCPUMilli := rapid.IntRange(100, 8000).Draw(t, "specCPUMilli")
			specMemMi := rapid.IntRange(64, 16384).Draw(t, "specMemMi")

			// Generate random AllocatedResources
			allocCPUMilli := rapid.IntRange(100, 8000).Draw(t, "allocCPUMilli")
			allocMemMi := rapid.IntRange(64, 16384).Draw(t, "allocMemMi")

			// Invalid CPU annotation
			invalidCPUIdx := rapid.IntRange(0, len(invalidAnnotations)-1).Draw(t, "invalidCPUIdx")
			invalidCPU := invalidAnnotations[invalidCPUIdx]
			// Valid peak memory annotation
			peakMemMi := rapid.IntRange(64, 16384).Draw(t, "peakMemMi")

			specCPU := resource.MustParse(fmt.Sprintf("%dm", specCPUMilli))
			specMem := resource.MustParse(fmt.Sprintf("%dMi", specMemMi))
			allocCPU := resource.MustParse(fmt.Sprintf("%dm", allocCPUMilli))
			allocMem := resource.MustParse(fmt.Sprintf("%dMi", allocMemMi))
			peakMem := resource.MustParse(fmt.Sprintf("%dMi", peakMemMi))

			pod := &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
					Annotations: map[string]string{
						karpv1.PeakCPUAnnotationKey:    invalidCPU,
						karpv1.PeakMemoryAnnotationKey: peakMem.String(),
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  "main",
							Image: "test:latest",
							Resources: v1.ResourceRequirements{
								Requests: v1.ResourceList{
									v1.ResourceCPU:    specCPU,
									v1.ResourceMemory: specMem,
								},
							},
						},
					},
				},
				Status: v1.PodStatus{
					ContainerStatuses: []v1.ContainerStatus{
						{
							Name: "main",
							AllocatedResources: v1.ResourceList{
								v1.ResourceCPU:    allocCPU,
								v1.ResourceMemory: allocMem,
							},
						},
					},
				},
			}

			result := resources.IPVSAwareRequestsForPod(pod)

			// CPU: invalid annotation is ignored, so effective = max(spec, alloc)
			expectedCPU := specCPU.DeepCopy()
			if allocCPU.Cmp(expectedCPU) > 0 {
				expectedCPU = allocCPU.DeepCopy()
			}

			// Memory: valid peak annotation participates in max(spec, alloc, peak)
			expectedMem := specMem.DeepCopy()
			if allocMem.Cmp(expectedMem) > 0 {
				expectedMem = allocMem.DeepCopy()
			}
			if peakMem.Cmp(expectedMem) > 0 {
				expectedMem = peakMem.DeepCopy()
			}

			resultCPU := result[v1.ResourceCPU]
			resultMem := result[v1.ResourceMemory]

			if resultCPU.Cmp(expectedCPU) != 0 {
				t.Fatalf("CPU mismatch (invalid CPU annotation, valid mem): got %s, expected %s "+
					"(spec=%s, alloc=%s, invalidCPUAnnotation=%q)",
					resultCPU.String(), expectedCPU.String(),
					specCPU.String(), allocCPU.String(), invalidCPU)
			}
			if resultMem.Cmp(expectedMem) != 0 {
				t.Fatalf("Memory mismatch (invalid CPU annotation, valid mem): got %s, expected %s "+
					"(spec=%s, alloc=%s, peak=%s)",
					resultMem.String(), expectedMem.String(),
					specMem.String(), allocMem.String(), peakMem.String())
			}
		})
	})
}

// Feature: in-place-pod-vertical-scaling, Property 8: Steady-state annotation validation
// Validates: Requirements 6.4
func TestProperty8_SteadyStateAnnotationValidation(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate random spec CPU/memory values
		specCPUMilli := rapid.IntRange(100, 8000).Draw(t, "specCPUMilli")
		specMemMi := rapid.IntRange(64, 16384).Draw(t, "specMemMi")

		// Generate random steady-state CPU/memory values (some may exceed spec, some may not)
		steadyStateCPUMilli := rapid.IntRange(50, 12000).Draw(t, "steadyStateCPUMilli")
		steadyStateMemMi := rapid.IntRange(32, 24000).Draw(t, "steadyStateMemMi")

		specCPU := resource.MustParse(fmt.Sprintf("%dm", specCPUMilli))
		specMem := resource.MustParse(fmt.Sprintf("%dMi", specMemMi))
		steadyStateCPU := resource.MustParse(fmt.Sprintf("%dm", steadyStateCPUMilli))
		steadyStateMem := resource.MustParse(fmt.Sprintf("%dMi", steadyStateMemMi))

		pod := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod",
				Namespace: "default",
				Annotations: map[string]string{
					karpv1.SteadyStateCPUAnnotationKey:    steadyStateCPU.String(),
					karpv1.SteadyStateMemoryAnnotationKey: steadyStateMem.String(),
				},
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name:  "main",
						Image: "test:latest",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU:    specCPU,
								v1.ResourceMemory: specMem,
							},
						},
					},
				},
			},
		}

		result, foundValid := resources.SteadyStateRequestsForPod(pod)

		resultCPU := result[v1.ResourceCPU]
		resultMem := result[v1.ResourceMemory]

		cpuExceedsSpec := steadyStateCPU.Cmp(specCPU) > 0
		memExceedsSpec := steadyStateMem.Cmp(specMem) > 0

		// When steady-state exceeds spec, the returned value must equal spec (capped)
		// When steady-state <= spec, the returned value must equal steady-state
		if cpuExceedsSpec {
			if resultCPU.Cmp(specCPU) != 0 {
				t.Fatalf("CPU should be capped to spec when steady-state exceeds spec: got %s, expected %s "+
					"(steadyState=%s, spec=%s)",
					resultCPU.String(), specCPU.String(),
					steadyStateCPU.String(), specCPU.String())
			}
		} else {
			if resultCPU.Cmp(steadyStateCPU) != 0 {
				t.Fatalf("CPU should equal steady-state when it does not exceed spec: got %s, expected %s "+
					"(steadyState=%s, spec=%s)",
					resultCPU.String(), steadyStateCPU.String(),
					steadyStateCPU.String(), specCPU.String())
			}
		}

		if memExceedsSpec {
			if resultMem.Cmp(specMem) != 0 {
				t.Fatalf("Memory should be capped to spec when steady-state exceeds spec: got %s, expected %s "+
					"(steadyState=%s, spec=%s)",
					resultMem.String(), specMem.String(),
					steadyStateMem.String(), specMem.String())
			}
		} else {
			if resultMem.Cmp(steadyStateMem) != 0 {
				t.Fatalf("Memory should equal steady-state when it does not exceed spec: got %s, expected %s "+
					"(steadyState=%s, spec=%s)",
					resultMem.String(), steadyStateMem.String(),
					steadyStateMem.String(), specMem.String())
			}
		}

		// Verify foundValid: should be true only when at least one steady-state value
		// does NOT exceed its corresponding spec request
		expectedFoundValid := !cpuExceedsSpec || !memExceedsSpec
		if foundValid != expectedFoundValid {
			t.Fatalf("foundValid mismatch: got %v, expected %v "+
				"(cpuExceedsSpec=%v, memExceedsSpec=%v, steadyStateCPU=%s, specCPU=%s, steadyStateMem=%s, specMem=%s)",
				foundValid, expectedFoundValid,
				cpuExceedsSpec, memExceedsSpec,
				steadyStateCPU.String(), specCPU.String(),
				steadyStateMem.String(), specMem.String())
		}
	})
}

// Feature: in-place-pod-vertical-scaling, Property 10: IPVS resource adjustment metric increments on adjustment
// Validates: Requirements 7.1
func TestProperty10_IPVSResourceAdjustmentMetricIncrementsOnAdjustment(t *testing.T) {
	t.Run("MetricIncrementsWhenAdjustmentDiffers", func(t *testing.T) {
		rapid.Check(t, func(t *rapid.T) {
			// Reset the metric before each iteration
			metrics.IPVSResourceAdjustmentTotal.Reset()

			// Generate spec requests
			specCPUMilli := rapid.IntRange(100, 4000).Draw(t, "specCPUMilli")
			specMemMi := rapid.IntRange(64, 8192).Draw(t, "specMemMi")

			// Generate allocated or peak values that are strictly greater than spec
			// to guarantee the metric fires
			extraCPUMilli := rapid.IntRange(1, 4000).Draw(t, "extraCPUMilli")
			extraMemMi := rapid.IntRange(1, 8192).Draw(t, "extraMemMi")

			// Decide whether the override comes from allocated resources or peak annotations
			usePeakCPU := rapid.Bool().Draw(t, "usePeakCPU")
			usePeakMem := rapid.Bool().Draw(t, "usePeakMem")

			specCPU := resource.MustParse(fmt.Sprintf("%dm", specCPUMilli))
			specMem := resource.MustParse(fmt.Sprintf("%dMi", specMemMi))

			overrideCPU := resource.MustParse(fmt.Sprintf("%dm", specCPUMilli+extraCPUMilli))
			overrideMem := resource.MustParse(fmt.Sprintf("%dMi", specMemMi+extraMemMi))

			annotations := map[string]string{}
			allocCPU := specCPU.DeepCopy()
			allocMem := specMem.DeepCopy()

			if usePeakCPU {
				annotations[karpv1.PeakCPUAnnotationKey] = overrideCPU.String()
			} else {
				allocCPU = overrideCPU.DeepCopy()
			}
			if usePeakMem {
				annotations[karpv1.PeakMemoryAnnotationKey] = overrideMem.String()
			} else {
				allocMem = overrideMem.DeepCopy()
			}

			pod := &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test-pod",
					Namespace:   "default",
					Annotations: annotations,
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  "main",
							Image: "test:latest",
							Resources: v1.ResourceRequirements{
								Requests: v1.ResourceList{
									v1.ResourceCPU:    specCPU,
									v1.ResourceMemory: specMem,
								},
							},
						},
					},
				},
				Status: v1.PodStatus{
					ContainerStatuses: []v1.ContainerStatus{
						{
							Name: "main",
							AllocatedResources: v1.ResourceList{
								v1.ResourceCPU:    allocCPU,
								v1.ResourceMemory: allocMem,
							},
						},
					},
				},
			}

			// Read metric values before the call
			counter := metrics.IPVSResourceAdjustmentTotal.(*opmetrics.PrometheusCounter)
			cpuBefore := testutil.ToFloat64(counter.With(prometheus.Labels{metrics.ResourceTypeLabel: "cpu"}))
			memBefore := testutil.ToFloat64(counter.With(prometheus.Labels{metrics.ResourceTypeLabel: "memory"}))

			resources.IPVSAwareRequestsForPod(pod)

			cpuAfter := testutil.ToFloat64(counter.With(prometheus.Labels{metrics.ResourceTypeLabel: "cpu"}))
			memAfter := testutil.ToFloat64(counter.With(prometheus.Labels{metrics.ResourceTypeLabel: "memory"}))

			// Both CPU and memory should have been adjusted (override > spec)
			if cpuAfter-cpuBefore != 1 {
				t.Fatalf("expected cpu metric to increment by 1, got delta %v (before=%v, after=%v, spec=%s, override=%s, usePeak=%v)",
					cpuAfter-cpuBefore, cpuBefore, cpuAfter, specCPU.String(), overrideCPU.String(), usePeakCPU)
			}
			if memAfter-memBefore != 1 {
				t.Fatalf("expected memory metric to increment by 1, got delta %v (before=%v, after=%v, spec=%s, override=%s, usePeak=%v)",
					memAfter-memBefore, memBefore, memAfter, specMem.String(), overrideMem.String(), usePeakMem)
			}
		})
	})

	t.Run("MetricDoesNotIncrementWhenNoAdjustment", func(t *testing.T) {
		rapid.Check(t, func(t *rapid.T) {
			// Reset the metric before each iteration
			metrics.IPVSResourceAdjustmentTotal.Reset()

			// Generate spec requests
			specCPUMilli := rapid.IntRange(100, 8000).Draw(t, "specCPUMilli")
			specMemMi := rapid.IntRange(64, 16384).Draw(t, "specMemMi")

			specCPU := resource.MustParse(fmt.Sprintf("%dm", specCPUMilli))
			specMem := resource.MustParse(fmt.Sprintf("%dMi", specMemMi))

			// Allocated resources equal to or less than spec (no adjustment)
			allocCPUMilli := rapid.IntRange(1, specCPUMilli).Draw(t, "allocCPUMilli")
			allocMemMi := rapid.IntRange(1, specMemMi).Draw(t, "allocMemMi")
			allocCPU := resource.MustParse(fmt.Sprintf("%dm", allocCPUMilli))
			allocMem := resource.MustParse(fmt.Sprintf("%dMi", allocMemMi))

			// No peak annotations — so effective == spec
			pod := &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  "main",
							Image: "test:latest",
							Resources: v1.ResourceRequirements{
								Requests: v1.ResourceList{
									v1.ResourceCPU:    specCPU,
									v1.ResourceMemory: specMem,
								},
							},
						},
					},
				},
				Status: v1.PodStatus{
					ContainerStatuses: []v1.ContainerStatus{
						{
							Name: "main",
							AllocatedResources: v1.ResourceList{
								v1.ResourceCPU:    allocCPU,
								v1.ResourceMemory: allocMem,
							},
						},
					},
				},
			}

			counter := metrics.IPVSResourceAdjustmentTotal.(*opmetrics.PrometheusCounter)
			cpuBefore := testutil.ToFloat64(counter.With(prometheus.Labels{metrics.ResourceTypeLabel: "cpu"}))
			memBefore := testutil.ToFloat64(counter.With(prometheus.Labels{metrics.ResourceTypeLabel: "memory"}))

			resources.IPVSAwareRequestsForPod(pod)

			cpuAfter := testutil.ToFloat64(counter.With(prometheus.Labels{metrics.ResourceTypeLabel: "cpu"}))
			memAfter := testutil.ToFloat64(counter.With(prometheus.Labels{metrics.ResourceTypeLabel: "memory"}))

			// No adjustment should have occurred
			if cpuAfter-cpuBefore != 0 {
				t.Fatalf("expected cpu metric NOT to increment, got delta %v (spec=%s, alloc=%s)",
					cpuAfter-cpuBefore, specCPU.String(), allocCPU.String())
			}
			if memAfter-memBefore != 0 {
				t.Fatalf("expected memory metric NOT to increment, got delta %v (spec=%s, alloc=%s)",
					memAfter-memBefore, specMem.String(), allocMem.String())
			}
		})
	})
}
