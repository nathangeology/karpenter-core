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
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"pgregory.net/rapid"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	karpopts "sigs.k8s.io/karpenter/pkg/operator/options"
	pscheduling "sigs.k8s.io/karpenter/pkg/scheduling"
	"sigs.k8s.io/karpenter/pkg/test"
	"sigs.k8s.io/karpenter/pkg/utils/resources"
)

// Feature: in-place-pod-vertical-scaling, Property 6: Scheduling simulator uses peak envelope for pod fit
// Validates: Requirements 3.2, 3.5
func TestProperty6_SchedulingSimulatorUsesPeakEnvelopeForPodFit(t *testing.T) {
	t.Run("IPVS_enabled_cachedPodData_uses_peak_envelope", func(t *testing.T) {
		rapid.Check(t, func(t *rapid.T) {
			// Generate random number of pods (1-5)
			numPods := rapid.IntRange(1, 5).Draw(t, "numPods")

			// Create context with IPVS feature gate enabled
			ctx := karpopts.ToContext(context.Background(), test.Options(test.OptionsFields{
				FeatureGates: test.FeatureGates{
					InPlacePodVerticalScaling: ptrBool(true),
				},
			}))

			// Create a minimal scheduler with just the cachedPodData map
			s := &Scheduler{
				cachedPodData:   make(map[types.UID]*PodData),
				volumeReqsByPod: make(map[types.UID]pscheduling.Requirements),
			}

			for i := 0; i < numPods; i++ {
				// Generate random CPU values in millicores (100m - 8000m)
				specCPUMilli := rapid.IntRange(100, 8000).Draw(t, fmt.Sprintf("specCPU_%d", i))
				peakCPUMilli := rapid.IntRange(specCPUMilli, 16000).Draw(t, fmt.Sprintf("peakCPU_%d", i))

				// Generate random memory values in MiB (64Mi - 16384Mi)
				specMemMi := rapid.IntRange(64, 16384).Draw(t, fmt.Sprintf("specMem_%d", i))
				peakMemMi := rapid.IntRange(specMemMi, 32768).Draw(t, fmt.Sprintf("peakMem_%d", i))

				// Optionally generate allocated resources
				hasAllocated := rapid.Bool().Draw(t, fmt.Sprintf("hasAlloc_%d", i))
				allocCPUMilli := 0
				allocMemMi := 0
				if hasAllocated {
					allocCPUMilli = rapid.IntRange(100, 8000).Draw(t, fmt.Sprintf("allocCPU_%d", i))
					allocMemMi = rapid.IntRange(64, 16384).Draw(t, fmt.Sprintf("allocMem_%d", i))
				}

				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("pod-%d", i),
						Namespace: "default",
						UID:       types.UID(fmt.Sprintf("uid-%d", i)),
						Annotations: map[string]string{
							karpv1.PeakCPUAnnotationKey:    fmt.Sprintf("%dm", peakCPUMilli),
							karpv1.PeakMemoryAnnotationKey: fmt.Sprintf("%dMi", peakMemMi),
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "main",
								Image: "test:latest",
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%dm", specCPUMilli)),
										corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", specMemMi)),
									},
								},
							},
						},
					},
				}

				// Add allocated resources if generated
				if hasAllocated {
					pod.Status = corev1.PodStatus{
						ContainerStatuses: []corev1.ContainerStatus{
							{
								Name: "main",
								AllocatedResources: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%dm", allocCPUMilli)),
									corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", allocMemMi)),
								},
							},
						},
					}
				}

				// Call updateCachedPodData which is the method under test
				s.updateCachedPodData(ctx, pod)

				// Verify the cached data uses IPVSAwareRequestsForPod
				cachedData, ok := s.cachedPodData[pod.UID]
				if !ok {
					t.Fatalf("pod %s not found in cachedPodData", pod.Name)
				}

				// Compute expected value using IPVSAwareRequestsForPod directly
				expected := resources.IPVSAwareRequestsForPod(pod)

				// Assert CPU matches
				cachedCPU := cachedData.Requests[corev1.ResourceCPU]
				expectedCPU := expected[corev1.ResourceCPU]
				if cachedCPU.Cmp(expectedCPU) != 0 {
					t.Fatalf("pod %s CPU mismatch: cached=%s, expected(IPVSAware)=%s (spec=%dm, peak=%dm)",
						pod.Name, cachedCPU.String(), expectedCPU.String(), specCPUMilli, peakCPUMilli)
				}

				// Assert memory matches
				cachedMem := cachedData.Requests[corev1.ResourceMemory]
				expectedMem := expected[corev1.ResourceMemory]
				if cachedMem.Cmp(expectedMem) != 0 {
					t.Fatalf("pod %s Memory mismatch: cached=%s, expected(IPVSAware)=%s (spec=%dMi, peak=%dMi)",
						pod.Name, cachedMem.String(), expectedMem.String(), specMemMi, peakMemMi)
				}
			}
		})
	})

	t.Run("IPVS_disabled_cachedPodData_uses_spec_requests", func(t *testing.T) {
		rapid.Check(t, func(t *rapid.T) {
			// Generate random number of pods (1-5)
			numPods := rapid.IntRange(1, 5).Draw(t, "numPods")

			// Create context with IPVS feature gate disabled
			ctx := karpopts.ToContext(context.Background(), test.Options(test.OptionsFields{
				FeatureGates: test.FeatureGates{
					InPlacePodVerticalScaling: ptrBool(false),
				},
			}))

			// Create a minimal scheduler with just the cachedPodData map
			s := &Scheduler{
				cachedPodData:   make(map[types.UID]*PodData),
				volumeReqsByPod: make(map[types.UID]pscheduling.Requirements),
			}

			for i := 0; i < numPods; i++ {
				// Generate random CPU values in millicores
				specCPUMilli := rapid.IntRange(100, 8000).Draw(t, fmt.Sprintf("specCPU_%d", i))
				peakCPUMilli := rapid.IntRange(specCPUMilli, 16000).Draw(t, fmt.Sprintf("peakCPU_%d", i))

				// Generate random memory values in MiB
				specMemMi := rapid.IntRange(64, 16384).Draw(t, fmt.Sprintf("specMem_%d", i))
				peakMemMi := rapid.IntRange(specMemMi, 32768).Draw(t, fmt.Sprintf("peakMem_%d", i))

				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("pod-%d", i),
						Namespace: "default",
						UID:       types.UID(fmt.Sprintf("uid-%d", i)),
						Annotations: map[string]string{
							karpv1.PeakCPUAnnotationKey:    fmt.Sprintf("%dm", peakCPUMilli),
							karpv1.PeakMemoryAnnotationKey: fmt.Sprintf("%dMi", peakMemMi),
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "main",
								Image: "test:latest",
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%dm", specCPUMilli)),
										corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", specMemMi)),
									},
								},
							},
						},
					},
				}

				// Call updateCachedPodData
				s.updateCachedPodData(ctx, pod)

				// Verify the cached data uses RequestsForPods (spec-based only)
				cachedData, ok := s.cachedPodData[pod.UID]
				if !ok {
					t.Fatalf("pod %s not found in cachedPodData", pod.Name)
				}

				// When gate is disabled, should use RequestsForPods
				expected := resources.RequestsForPods(pod)

				// Assert CPU matches spec-based computation
				cachedCPU := cachedData.Requests[corev1.ResourceCPU]
				expectedCPU := expected[corev1.ResourceCPU]
				if cachedCPU.Cmp(expectedCPU) != 0 {
					t.Fatalf("pod %s CPU mismatch (gate disabled): cached=%s, expected(spec)=%s (spec=%dm, peak=%dm)",
						pod.Name, cachedCPU.String(), expectedCPU.String(), specCPUMilli, peakCPUMilli)
				}

				// Assert memory matches spec-based computation
				cachedMem := cachedData.Requests[corev1.ResourceMemory]
				expectedMem := expected[corev1.ResourceMemory]
				if cachedMem.Cmp(expectedMem) != 0 {
					t.Fatalf("pod %s Memory mismatch (gate disabled): cached=%s, expected(spec)=%s (spec=%dMi, peak=%dMi)",
						pod.Name, cachedMem.String(), expectedMem.String(), specMemMi, peakMemMi)
				}
			}
		})
	})

	// Specific example: pod with peak annotation of 2 CPU but current requests of 500m
	// should be evaluated as needing 2 CPU when IPVS gate is enabled
	t.Run("specific_example_peak_2CPU_current_500m", func(t *testing.T) {
		rapid.Check(t, func(t *rapid.T) {
			ctx := karpopts.ToContext(context.Background(), test.Options(test.OptionsFields{
				FeatureGates: test.FeatureGates{
					InPlacePodVerticalScaling: ptrBool(true),
				},
			}))

			s := &Scheduler{
				cachedPodData:   make(map[types.UID]*PodData),
				volumeReqsByPod: make(map[types.UID]pscheduling.Requirements),
			}

			// Generate random memory values to ensure the property holds regardless of memory
			specMemMi := rapid.IntRange(64, 16384).Draw(t, "specMemMi")
			peakMemMi := rapid.IntRange(specMemMi, 32768).Draw(t, "peakMemMi")

			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "peak-cpu-pod",
					Namespace: "default",
					UID:       types.UID("uid-peak-cpu"),
					Annotations: map[string]string{
						karpv1.PeakCPUAnnotationKey:    "2",
						karpv1.PeakMemoryAnnotationKey: fmt.Sprintf("%dMi", peakMemMi),
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
									corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", specMemMi)),
								},
							},
						},
					},
				},
			}

			s.updateCachedPodData(ctx, pod)

			cachedData := s.cachedPodData[pod.UID]
			cachedCPU := cachedData.Requests[corev1.ResourceCPU]
			expectedCPU := resource.MustParse("2")

			// The pod with peak annotation of 2 CPU but current requests of 500m
			// must be evaluated as needing 2 CPU
			if cachedCPU.Cmp(expectedCPU) != 0 {
				t.Fatalf("peak CPU example: cached=%s, expected=2 (spec=500m, peak=2)",
					cachedCPU.String())
			}
		})
	})
}

// Feature: in-place-pod-vertical-scaling, Property 7: Provisioning uses peak envelope for instance type selection
// Validates: Requirements 4.1, 4.2, 4.3
func TestProperty7_ProvisioningUsesPeakEnvelopeForInstanceTypeSelection(t *testing.T) {
	t.Run("total_resource_requirement_equals_sum_of_peak_envelopes", func(t *testing.T) {
		rapid.Check(t, func(t *rapid.T) {
			numPods := rapid.IntRange(1, 5).Draw(t, "numPods")

			ctx := karpopts.ToContext(context.Background(), test.Options(test.OptionsFields{
				FeatureGates: test.FeatureGates{
					InPlacePodVerticalScaling: ptrBool(true),
				},
			}))

			s := &Scheduler{
				cachedPodData:   make(map[types.UID]*PodData),
				volumeReqsByPod: make(map[types.UID]pscheduling.Requirements),
			}

			// Track expected totals manually
			expectedTotalCPU := resource.Quantity{}
			expectedTotalMem := resource.Quantity{}

			var pods []*corev1.Pod
			for i := 0; i < numPods; i++ {
				specCPUMilli := rapid.IntRange(100, 8000).Draw(t, fmt.Sprintf("specCPU_%d", i))
				specMemMi := rapid.IntRange(64, 16384).Draw(t, fmt.Sprintf("specMem_%d", i))

				hasAnnotation := rapid.Bool().Draw(t, fmt.Sprintf("hasAnnotation_%d", i))

				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("pod-%d", i),
						Namespace: "default",
						UID:       types.UID(fmt.Sprintf("uid-p7-%d", i)),
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "main",
								Image: "test:latest",
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%dm", specCPUMilli)),
										corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", specMemMi)),
									},
								},
							},
						},
					},
				}

				if hasAnnotation {
					// Peak values >= spec values to ensure max(spec, peak) is meaningful
					peakCPUMilli := rapid.IntRange(specCPUMilli, 16000).Draw(t, fmt.Sprintf("peakCPU_%d", i))
					peakMemMi := rapid.IntRange(specMemMi, 32768).Draw(t, fmt.Sprintf("peakMem_%d", i))

					pod.Annotations = map[string]string{
						karpv1.PeakCPUAnnotationKey:    fmt.Sprintf("%dm", peakCPUMilli),
						karpv1.PeakMemoryAnnotationKey: fmt.Sprintf("%dMi", peakMemMi),
					}

					// Expected: max(spec, peak) for each resource
					effectiveCPU := specCPUMilli
					if peakCPUMilli > specCPUMilli {
						effectiveCPU = peakCPUMilli
					}
					effectiveMem := specMemMi
					if peakMemMi > specMemMi {
						effectiveMem = peakMemMi
					}
					cpuQ := resource.MustParse(fmt.Sprintf("%dm", effectiveCPU))
					memQ := resource.MustParse(fmt.Sprintf("%dMi", effectiveMem))
					expectedTotalCPU.Add(cpuQ)
					expectedTotalMem.Add(memQ)
				} else {
					// Unannotated pods contribute their spec requests
					cpuQ := resource.MustParse(fmt.Sprintf("%dm", specCPUMilli))
					memQ := resource.MustParse(fmt.Sprintf("%dMi", specMemMi))
					expectedTotalCPU.Add(cpuQ)
					expectedTotalMem.Add(memQ)
				}

				pods = append(pods, pod)
			}

			// Cache all pods via updateCachedPodData
			for _, pod := range pods {
				s.updateCachedPodData(ctx, pod)
			}

			// Sum the cached requests across all pods
			actualTotalCPU := resource.Quantity{}
			actualTotalMem := resource.Quantity{}
			for _, pod := range pods {
				cachedData, ok := s.cachedPodData[pod.UID]
				if !ok {
					t.Fatalf("pod %s not found in cachedPodData", pod.Name)
				}
				cpu := cachedData.Requests[corev1.ResourceCPU]
				mem := cachedData.Requests[corev1.ResourceMemory]
				actualTotalCPU.Add(cpu)
				actualTotalMem.Add(mem)
			}

			// Assert total resource requirement matches expected sum of peak envelopes
			if actualTotalCPU.Cmp(expectedTotalCPU) != 0 {
				t.Fatalf("total CPU mismatch: actual=%s, expected=%s",
					actualTotalCPU.String(), expectedTotalCPU.String())
			}
			if actualTotalMem.Cmp(expectedTotalMem) != 0 {
				t.Fatalf("total Memory mismatch: actual=%s, expected=%s",
					actualTotalMem.String(), expectedTotalMem.String())
			}
		})
	})

	t.Run("unannotated_pods_contribute_spec_requests_only", func(t *testing.T) {
		rapid.Check(t, func(t *rapid.T) {
			numPods := rapid.IntRange(1, 5).Draw(t, "numPods")

			ctx := karpopts.ToContext(context.Background(), test.Options(test.OptionsFields{
				FeatureGates: test.FeatureGates{
					InPlacePodVerticalScaling: ptrBool(true),
				},
			}))

			s := &Scheduler{
				cachedPodData:   make(map[types.UID]*PodData),
				volumeReqsByPod: make(map[types.UID]pscheduling.Requirements),
			}

			for i := 0; i < numPods; i++ {
				specCPUMilli := rapid.IntRange(100, 8000).Draw(t, fmt.Sprintf("specCPU_%d", i))
				specMemMi := rapid.IntRange(64, 16384).Draw(t, fmt.Sprintf("specMem_%d", i))

				// No annotations on any pod
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("pod-%d", i),
						Namespace: "default",
						UID:       types.UID(fmt.Sprintf("uid-p7-unann-%d", i)),
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "main",
								Image: "test:latest",
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%dm", specCPUMilli)),
										corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", specMemMi)),
									},
								},
							},
						},
					},
				}

				s.updateCachedPodData(ctx, pod)

				// Each unannotated pod should use spec requests (same as RequestsForPods)
				cachedData := s.cachedPodData[pod.UID]
				specBased := resources.RequestsForPods(pod)

				cachedCPU := cachedData.Requests[corev1.ResourceCPU]
				specCPU := specBased[corev1.ResourceCPU]
				if cachedCPU.Cmp(specCPU) != 0 {
					t.Fatalf("unannotated pod %s CPU mismatch: cached=%s, spec=%s",
						pod.Name, cachedCPU.String(), specCPU.String())
				}

				cachedMem := cachedData.Requests[corev1.ResourceMemory]
				specMem := specBased[corev1.ResourceMemory]
				if cachedMem.Cmp(specMem) != 0 {
					t.Fatalf("unannotated pod %s Memory mismatch: cached=%s, spec=%s",
						pod.Name, cachedMem.String(), specMem.String())
				}
			}
		})
	})

	t.Run("mixed_annotated_and_unannotated_pods_sum_correctly", func(t *testing.T) {
		rapid.Check(t, func(t *rapid.T) {
			ctx := karpopts.ToContext(context.Background(), test.Options(test.OptionsFields{
				FeatureGates: test.FeatureGates{
					InPlacePodVerticalScaling: ptrBool(true),
				},
			}))

			s := &Scheduler{
				cachedPodData:   make(map[types.UID]*PodData),
				volumeReqsByPod: make(map[types.UID]pscheduling.Requirements),
			}

			// Generate a mix: at least one annotated and one unannotated pod
			numAnnotated := rapid.IntRange(1, 3).Draw(t, "numAnnotated")
			numUnannotated := rapid.IntRange(1, 3).Draw(t, "numUnannotated")

			var pods []*corev1.Pod
			expectedTotalCPU := resource.Quantity{}
			expectedTotalMem := resource.Quantity{}
			podIdx := 0

			// Create annotated pods
			for i := 0; i < numAnnotated; i++ {
				specCPUMilli := rapid.IntRange(100, 4000).Draw(t, fmt.Sprintf("annSpecCPU_%d", i))
				specMemMi := rapid.IntRange(64, 8192).Draw(t, fmt.Sprintf("annSpecMem_%d", i))
				peakCPUMilli := rapid.IntRange(specCPUMilli, 16000).Draw(t, fmt.Sprintf("annPeakCPU_%d", i))
				peakMemMi := rapid.IntRange(specMemMi, 32768).Draw(t, fmt.Sprintf("annPeakMem_%d", i))

				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("ann-pod-%d", podIdx),
						Namespace: "default",
						UID:       types.UID(fmt.Sprintf("uid-p7-mix-%d", podIdx)),
						Annotations: map[string]string{
							karpv1.PeakCPUAnnotationKey:    fmt.Sprintf("%dm", peakCPUMilli),
							karpv1.PeakMemoryAnnotationKey: fmt.Sprintf("%dMi", peakMemMi),
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "main",
								Image: "test:latest",
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%dm", specCPUMilli)),
										corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", specMemMi)),
									},
								},
							},
						},
					},
				}

				effectiveCPU := specCPUMilli
				if peakCPUMilli > specCPUMilli {
					effectiveCPU = peakCPUMilli
				}
				effectiveMem := specMemMi
				if peakMemMi > specMemMi {
					effectiveMem = peakMemMi
				}
				cpuQ := resource.MustParse(fmt.Sprintf("%dm", effectiveCPU))
				memQ := resource.MustParse(fmt.Sprintf("%dMi", effectiveMem))
				expectedTotalCPU.Add(cpuQ)
				expectedTotalMem.Add(memQ)

				pods = append(pods, pod)
				podIdx++
			}

			// Create unannotated pods
			for i := 0; i < numUnannotated; i++ {
				specCPUMilli := rapid.IntRange(100, 4000).Draw(t, fmt.Sprintf("unannSpecCPU_%d", i))
				specMemMi := rapid.IntRange(64, 8192).Draw(t, fmt.Sprintf("unannSpecMem_%d", i))

				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("unann-pod-%d", podIdx),
						Namespace: "default",
						UID:       types.UID(fmt.Sprintf("uid-p7-mix-%d", podIdx)),
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "main",
								Image: "test:latest",
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%dm", specCPUMilli)),
										corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", specMemMi)),
									},
								},
							},
						},
					},
				}

				cpuQ := resource.MustParse(fmt.Sprintf("%dm", specCPUMilli))
				memQ := resource.MustParse(fmt.Sprintf("%dMi", specMemMi))
				expectedTotalCPU.Add(cpuQ)
				expectedTotalMem.Add(memQ)

				pods = append(pods, pod)
				podIdx++
			}

			// Cache all pods
			for _, pod := range pods {
				s.updateCachedPodData(ctx, pod)
			}

			// Sum cached requests
			actualTotalCPU := resource.Quantity{}
			actualTotalMem := resource.Quantity{}
			for _, pod := range pods {
				cachedData := s.cachedPodData[pod.UID]
				cpu := cachedData.Requests[corev1.ResourceCPU]
				mem := cachedData.Requests[corev1.ResourceMemory]
				actualTotalCPU.Add(cpu)
				actualTotalMem.Add(mem)
			}

			if actualTotalCPU.Cmp(expectedTotalCPU) != 0 {
				t.Fatalf("mixed pods total CPU mismatch: actual=%s, expected=%s",
					actualTotalCPU.String(), expectedTotalCPU.String())
			}
			if actualTotalMem.Cmp(expectedTotalMem) != 0 {
				t.Fatalf("mixed pods total Memory mismatch: actual=%s, expected=%s",
					actualTotalMem.String(), expectedTotalMem.String())
			}
		})
	})
}

// ptrBool returns a pointer to a bool value.
func ptrBool(b bool) *bool {
	return &b
}
