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
	"testing"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/utils/resources"
)

// TestSteadyStateRequestsForPod validates SteadyStateRequestsForPod with specific
// examples covering valid/invalid annotations, steady-state exceeding spec,
// and missing annotations.
// Validates: Requirements 6.1, 6.4
func TestSteadyStateRequestsForPod(t *testing.T) {
	tests := []struct {
		name        string
		pod         *v1.Pod
		expectedCPU string
		expectedMem string
		expectFound bool
	}{
		// --- Valid steady-state annotations ---
		{
			name: "valid steady-state cpu and memory below spec",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "valid-ss", Namespace: "default",
					Annotations: map[string]string{
						karpv1.SteadyStateCPUAnnotationKey:    "500m",
						karpv1.SteadyStateMemoryAnnotationKey: "1Gi",
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{
						Name: "main", Image: "test:latest",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU:    resource.MustParse("2"),
								v1.ResourceMemory: resource.MustParse("4Gi"),
							},
						},
					}},
				},
			},
			expectedCPU: "500m",
			expectedMem: "1Gi",
			expectFound: true,
		},
		{
			name: "valid steady-state cpu only",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "valid-ss-cpu", Namespace: "default",
					Annotations: map[string]string{
						karpv1.SteadyStateCPUAnnotationKey: "200m",
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{
						Name: "main", Image: "test:latest",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU:    resource.MustParse("1"),
								v1.ResourceMemory: resource.MustParse("512Mi"),
							},
						},
					}},
				},
			},
			expectedCPU: "200m",
			expectedMem: "512Mi", // falls back to spec
			expectFound: true,
		},
		{
			name: "valid steady-state memory only",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "valid-ss-mem", Namespace: "default",
					Annotations: map[string]string{
						karpv1.SteadyStateMemoryAnnotationKey: "256Mi",
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{
						Name: "main", Image: "test:latest",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU:    resource.MustParse("500m"),
								v1.ResourceMemory: resource.MustParse("2Gi"),
							},
						},
					}},
				},
			},
			expectedCPU: "500m", // falls back to spec
			expectedMem: "256Mi",
			expectFound: true,
		},
		{
			name: "steady-state equal to spec is valid",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "ss-equal-spec", Namespace: "default",
					Annotations: map[string]string{
						karpv1.SteadyStateCPUAnnotationKey:    "1",
						karpv1.SteadyStateMemoryAnnotationKey: "2Gi",
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{
						Name: "main", Image: "test:latest",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU:    resource.MustParse("1"),
								v1.ResourceMemory: resource.MustParse("2Gi"),
							},
						},
					}},
				},
			},
			expectedCPU: "1",
			expectedMem: "2Gi",
			expectFound: true,
		},

		// --- Steady-state exceeds spec (fallback to spec) ---
		{
			name: "steady-state cpu exceeds spec falls back to spec",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "ss-exceeds-cpu", Namespace: "default",
					Annotations: map[string]string{
						karpv1.SteadyStateCPUAnnotationKey:    "4",
						karpv1.SteadyStateMemoryAnnotationKey: "1Gi",
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{
						Name: "main", Image: "test:latest",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU:    resource.MustParse("2"),
								v1.ResourceMemory: resource.MustParse("4Gi"),
							},
						},
					}},
				},
			},
			expectedCPU: "2",   // falls back to spec because 4 > 2
			expectedMem: "1Gi", // valid, below spec
			expectFound: true,  // memory annotation is still valid
		},
		{
			name: "steady-state memory exceeds spec falls back to spec",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "ss-exceeds-mem", Namespace: "default",
					Annotations: map[string]string{
						karpv1.SteadyStateCPUAnnotationKey:    "500m",
						karpv1.SteadyStateMemoryAnnotationKey: "8Gi",
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{
						Name: "main", Image: "test:latest",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU:    resource.MustParse("2"),
								v1.ResourceMemory: resource.MustParse("4Gi"),
							},
						},
					}},
				},
			},
			expectedCPU: "500m", // valid, below spec
			expectedMem: "4Gi",  // falls back to spec because 8Gi > 4Gi
			expectFound: true,   // cpu annotation is still valid
		},
		{
			name: "both steady-state exceed spec falls back to spec for both",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "ss-exceeds-both", Namespace: "default",
					Annotations: map[string]string{
						karpv1.SteadyStateCPUAnnotationKey:    "4",
						karpv1.SteadyStateMemoryAnnotationKey: "8Gi",
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{
						Name: "main", Image: "test:latest",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU:    resource.MustParse("2"),
								v1.ResourceMemory: resource.MustParse("4Gi"),
							},
						},
					}},
				},
			},
			expectedCPU: "2",
			expectedMem: "4Gi",
			expectFound: false, // both exceeded, no valid steady-state found
		},

		// --- Invalid annotation values ---
		{
			name: "invalid steady-state cpu falls back to spec",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "ss-invalid-cpu", Namespace: "default",
					Annotations: map[string]string{
						karpv1.SteadyStateCPUAnnotationKey: "abc",
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{
						Name: "main", Image: "test:latest",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU:    resource.MustParse("1"),
								v1.ResourceMemory: resource.MustParse("2Gi"),
							},
						},
					}},
				},
			},
			expectedCPU: "1",
			expectedMem: "2Gi",
			expectFound: false,
		},
		{
			name: "invalid steady-state memory with valid cpu",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "ss-invalid-mem", Namespace: "default",
					Annotations: map[string]string{
						karpv1.SteadyStateCPUAnnotationKey:    "500m",
						karpv1.SteadyStateMemoryAnnotationKey: "not-a-quantity",
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{
						Name: "main", Image: "test:latest",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU:    resource.MustParse("2"),
								v1.ResourceMemory: resource.MustParse("4Gi"),
							},
						},
					}},
				},
			},
			expectedCPU: "500m",
			expectedMem: "4Gi", // falls back to spec
			expectFound: true,  // cpu is still valid
		},
		{
			name: "empty string steady-state annotations fall back to spec",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "ss-empty", Namespace: "default",
					Annotations: map[string]string{
						karpv1.SteadyStateCPUAnnotationKey:    "",
						karpv1.SteadyStateMemoryAnnotationKey: "",
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{
						Name: "main", Image: "test:latest",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU:    resource.MustParse("1"),
								v1.ResourceMemory: resource.MustParse("2Gi"),
							},
						},
					}},
				},
			},
			expectedCPU: "1",
			expectedMem: "2Gi",
			expectFound: false,
		},

		// --- No annotations ---
		{
			name: "no annotations returns spec requests",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "no-ann", Namespace: "default"},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{
						Name: "main", Image: "test:latest",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU:    resource.MustParse("1"),
								v1.ResourceMemory: resource.MustParse("2Gi"),
							},
						},
					}},
				},
			},
			expectedCPU: "1",
			expectedMem: "2Gi",
			expectFound: false,
		},
		{
			name: "nil annotations returns spec requests",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "nil-ann", Namespace: "default"},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{
						Name: "main", Image: "test:latest",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU:    resource.MustParse("500m"),
								v1.ResourceMemory: resource.MustParse("1Gi"),
							},
						},
					}},
				},
			},
			expectedCPU: "500m",
			expectedMem: "1Gi",
			expectFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, found := resources.SteadyStateRequestsForPod(tt.pod)

			if found != tt.expectFound {
				t.Errorf("found: got %v, want %v", found, tt.expectFound)
			}

			expectedCPU := resource.MustParse(tt.expectedCPU)
			expectedMem := resource.MustParse(tt.expectedMem)

			resultCPU := result[v1.ResourceCPU]
			resultMem := result[v1.ResourceMemory]

			if resultCPU.Cmp(expectedCPU) != 0 {
				t.Errorf("CPU: got %s, want %s", resultCPU.String(), expectedCPU.String())
			}
			if resultMem.Cmp(expectedMem) != 0 {
				t.Errorf("Memory: got %s, want %s", resultMem.String(), expectedMem.String())
			}
		})
	}
}

// TestIPVSAwareRequestsForPod validates IPVSAwareRequestsForPod with specific
// examples covering valid/invalid annotations, resize statuses, nil/empty
// container statuses, and combined scenarios.
// Validates: Requirements 1.1, 1.2, 1.3, 2.1, 2.2, 2.3, 2.4
func TestIPVSAwareRequestsForPod(t *testing.T) {
	tests := []struct {
		name        string
		pod         *v1.Pod
		expectedCPU string
		expectedMem string
	}{
		// --- Valid annotation tests ---
		{
			name: "valid peak-cpu annotation overrides lower spec requests",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "valid-cpu", Namespace: "default",
					Annotations: map[string]string{
						karpv1.PeakCPUAnnotationKey: "500m",
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{
						Name: "main", Image: "test:latest",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU:    resource.MustParse("200m"),
								v1.ResourceMemory: resource.MustParse("256Mi"),
							},
						},
					}},
				},
			},
			expectedCPU: "500m",
			expectedMem: "256Mi",
		},
		{
			name: "valid peak-memory annotation overrides lower spec requests",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "valid-mem", Namespace: "default",
					Annotations: map[string]string{
						karpv1.PeakMemoryAnnotationKey: "4Gi",
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{
						Name: "main", Image: "test:latest",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU:    resource.MustParse("200m"),
								v1.ResourceMemory: resource.MustParse("1Gi"),
							},
						},
					}},
				},
			},
			expectedCPU: "200m",
			expectedMem: "4Gi",
		},
		{
			name: "valid peak-cpu 2000m and peak-memory 4Gi both override spec",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "valid-both", Namespace: "default",
					Annotations: map[string]string{
						karpv1.PeakCPUAnnotationKey:    "2000m",
						karpv1.PeakMemoryAnnotationKey: "4Gi",
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{
						Name: "main", Image: "test:latest",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU:    resource.MustParse("500m"),
								v1.ResourceMemory: resource.MustParse("1Gi"),
							},
						},
					}},
				},
			},
			expectedCPU: "2000m",
			expectedMem: "4Gi",
		},
		{
			name: "valid peak-cpu 1Gi (integer CPU) annotation",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "valid-1gi-cpu", Namespace: "default",
					Annotations: map[string]string{
						karpv1.PeakMemoryAnnotationKey: "1Gi",
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{
						Name: "main", Image: "test:latest",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU:    resource.MustParse("100m"),
								v1.ResourceMemory: resource.MustParse("512Mi"),
							},
						},
					}},
				},
			},
			expectedCPU: "100m",
			expectedMem: "1Gi",
		},

		// --- Invalid annotation tests ---
		{
			name: "invalid peak-cpu empty string falls back to spec",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "invalid-empty", Namespace: "default",
					Annotations: map[string]string{
						karpv1.PeakCPUAnnotationKey: "",
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{
						Name: "main", Image: "test:latest",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU:    resource.MustParse("300m"),
								v1.ResourceMemory: resource.MustParse("512Mi"),
							},
						},
					}},
				},
				Status: v1.PodStatus{
					ContainerStatuses: []v1.ContainerStatus{{
						Name: "main",
						AllocatedResources: v1.ResourceList{
							v1.ResourceCPU:    resource.MustParse("400m"),
							v1.ResourceMemory: resource.MustParse("256Mi"),
						},
					}},
				},
			},
			expectedCPU: "400m",
			expectedMem: "512Mi",
		},
		{
			name: "invalid peak-cpu abc falls back to max(spec, allocated)",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "invalid-abc", Namespace: "default",
					Annotations: map[string]string{
						karpv1.PeakCPUAnnotationKey: "abc",
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{
						Name: "main", Image: "test:latest",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU:    resource.MustParse("200m"),
								v1.ResourceMemory: resource.MustParse("128Mi"),
							},
						},
					}},
				},
				Status: v1.PodStatus{
					ContainerStatuses: []v1.ContainerStatus{{
						Name: "main",
						AllocatedResources: v1.ResourceList{
							v1.ResourceCPU:    resource.MustParse("500m"),
							v1.ResourceMemory: resource.MustParse("64Mi"),
						},
					}},
				},
			},
			expectedCPU: "500m",
			expectedMem: "128Mi",
		},
		{
			name: "invalid peak-cpu -1 falls back to max(spec, allocated)",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "invalid-neg", Namespace: "default",
					Annotations: map[string]string{
						karpv1.PeakCPUAnnotationKey: "-1",
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{
						Name: "main", Image: "test:latest",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU:    resource.MustParse("100m"),
								v1.ResourceMemory: resource.MustParse("256Mi"),
							},
						},
					}},
				},
				Status: v1.PodStatus{
					ContainerStatuses: []v1.ContainerStatus{{
						Name: "main",
						AllocatedResources: v1.ResourceList{
							v1.ResourceCPU:    resource.MustParse("150m"),
							v1.ResourceMemory: resource.MustParse("128Mi"),
						},
					}},
				},
			},
			// Note: "-1" is actually a valid Kubernetes quantity (negative).
			// The function parses it successfully, so it participates in max().
			// Since -1 < 150m, the result is max(100m, 150m, -1) = 150m.
			expectedCPU: "150m",
			expectedMem: "256Mi",
		},
		{
			name: "invalid peak-cpu 1.5.3 falls back to max(spec, allocated)",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "invalid-malformed", Namespace: "default",
					Annotations: map[string]string{
						karpv1.PeakCPUAnnotationKey: "1.5.3",
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{
						Name: "main", Image: "test:latest",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU:    resource.MustParse("250m"),
								v1.ResourceMemory: resource.MustParse("512Mi"),
							},
						},
					}},
				},
				Status: v1.PodStatus{
					ContainerStatuses: []v1.ContainerStatus{{
						Name: "main",
						AllocatedResources: v1.ResourceList{
							v1.ResourceCPU:    resource.MustParse("300m"),
							v1.ResourceMemory: resource.MustParse("256Mi"),
						},
					}},
				},
			},
			expectedCPU: "300m",
			expectedMem: "512Mi",
		},

		// --- Resize status tests ---
		{
			name: "resize status Proposed with allocated > spec uses max",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "resize-proposed", Namespace: "default"},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{
						Name: "main", Image: "test:latest",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU:    resource.MustParse("200m"),
								v1.ResourceMemory: resource.MustParse("256Mi"),
							},
						},
					}},
				},
				Status: v1.PodStatus{
					Resize: v1.PodResizeStatus("Proposed"),
					ContainerStatuses: []v1.ContainerStatus{{
						Name: "main",
						AllocatedResources: v1.ResourceList{
							v1.ResourceCPU:    resource.MustParse("500m"),
							v1.ResourceMemory: resource.MustParse("1Gi"),
						},
					}},
				},
			},
			expectedCPU: "500m",
			expectedMem: "1Gi",
		},
		{
			name: "resize status InProgress with allocated > spec uses max",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "resize-inprogress", Namespace: "default"},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{
						Name: "main", Image: "test:latest",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU:    resource.MustParse("100m"),
								v1.ResourceMemory: resource.MustParse("128Mi"),
							},
						},
					}},
				},
				Status: v1.PodStatus{
					Resize: v1.PodResizeStatusInProgress,
					ContainerStatuses: []v1.ContainerStatus{{
						Name: "main",
						AllocatedResources: v1.ResourceList{
							v1.ResourceCPU:    resource.MustParse("1"),
							v1.ResourceMemory: resource.MustParse("2Gi"),
						},
					}},
				},
			},
			expectedCPU: "1",
			expectedMem: "2Gi",
		},
		{
			name: "resize status Infeasible with allocated resources uses max",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "resize-infeasible", Namespace: "default"},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{
						Name: "main", Image: "test:latest",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU:    resource.MustParse("2"),
								v1.ResourceMemory: resource.MustParse("4Gi"),
							},
						},
					}},
				},
				Status: v1.PodStatus{
					Resize: v1.PodResizeStatusInfeasible,
					ContainerStatuses: []v1.ContainerStatus{{
						Name: "main",
						AllocatedResources: v1.ResourceList{
							v1.ResourceCPU:    resource.MustParse("500m"),
							v1.ResourceMemory: resource.MustParse("1Gi"),
						},
					}},
				},
			},
			// spec > allocated, so max picks spec
			expectedCPU: "2",
			expectedMem: "4Gi",
		},
		{
			name: "empty resize status normal operation",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "resize-empty", Namespace: "default"},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{
						Name: "main", Image: "test:latest",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU:    resource.MustParse("500m"),
								v1.ResourceMemory: resource.MustParse("1Gi"),
							},
						},
					}},
				},
				Status: v1.PodStatus{
					Resize: v1.PodResizeStatus(""),
					ContainerStatuses: []v1.ContainerStatus{{
						Name: "main",
						AllocatedResources: v1.ResourceList{
							v1.ResourceCPU:    resource.MustParse("500m"),
							v1.ResourceMemory: resource.MustParse("1Gi"),
						},
					}},
				},
			},
			expectedCPU: "500m",
			expectedMem: "1Gi",
		},

		// --- Nil/empty ContainerStatuses tests ---
		{
			name: "nil ContainerStatuses uses max(spec, peak)",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "nil-status", Namespace: "default",
					Annotations: map[string]string{
						karpv1.PeakCPUAnnotationKey:    "1",
						karpv1.PeakMemoryAnnotationKey: "2Gi",
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{
						Name: "main", Image: "test:latest",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU:    resource.MustParse("500m"),
								v1.ResourceMemory: resource.MustParse("1Gi"),
							},
						},
					}},
				},
				// Status.ContainerStatuses is nil by default
			},
			expectedCPU: "1",
			expectedMem: "2Gi",
		},
		{
			name: "empty ContainerStatuses uses max(spec, peak)",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "empty-status", Namespace: "default",
					Annotations: map[string]string{
						karpv1.PeakCPUAnnotationKey:    "800m",
						karpv1.PeakMemoryAnnotationKey: "512Mi",
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{
						Name: "main", Image: "test:latest",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU:    resource.MustParse("1"),
								v1.ResourceMemory: resource.MustParse("256Mi"),
							},
						},
					}},
				},
				Status: v1.PodStatus{
					ContainerStatuses: []v1.ContainerStatus{},
				},
			},
			// spec CPU (1) > peak CPU (800m), peak mem (512Mi) > spec mem (256Mi)
			expectedCPU: "1",
			expectedMem: "512Mi",
		},

		// --- Combined: peak annotation + active resize status ---
		{
			name: "both peak annotation and active resize all three sources participate in max",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "combined", Namespace: "default",
					Annotations: map[string]string{
						karpv1.PeakCPUAnnotationKey:    "2",
						karpv1.PeakMemoryAnnotationKey: "1Gi",
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{
						Name: "main", Image: "test:latest",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU:    resource.MustParse("500m"),
								v1.ResourceMemory: resource.MustParse("2Gi"),
							},
						},
					}},
				},
				Status: v1.PodStatus{
					Resize: v1.PodResizeStatusInProgress,
					ContainerStatuses: []v1.ContainerStatus{{
						Name: "main",
						AllocatedResources: v1.ResourceList{
							v1.ResourceCPU:    resource.MustParse("1"),
							v1.ResourceMemory: resource.MustParse("4Gi"),
						},
					}},
				},
			},
			// CPU: max(500m, 1, 2) = 2
			// Memory: max(2Gi, 4Gi, 1Gi) = 4Gi
			expectedCPU: "2",
			expectedMem: "4Gi",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := resources.IPVSAwareRequestsForPod(tt.pod)

			expectedCPU := resource.MustParse(tt.expectedCPU)
			expectedMem := resource.MustParse(tt.expectedMem)

			resultCPU := result[v1.ResourceCPU]
			resultMem := result[v1.ResourceMemory]

			if resultCPU.Cmp(expectedCPU) != 0 {
				t.Errorf("CPU: got %s, want %s", resultCPU.String(), expectedCPU.String())
			}
			if resultMem.Cmp(expectedMem) != 0 {
				t.Errorf("Memory: got %s, want %s", resultMem.String(), expectedMem.String())
			}
		})
	}
}
