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

package resources

import (
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// SteadyStateRequestsForPod parses the steady-state annotations from a pod
// and returns the validated steady-state resource list. The bool return value
// indicates whether valid steady-state annotations were found.
//
// Steady-state values represent the expected resource usage after a pod's
// startup phase. If a steady-state value exceeds the corresponding spec
// request, a warning is logged and the spec request value is used instead.
//
// If no valid steady-state annotations are present, the function returns
// the pod's spec requests and false.
func SteadyStateRequestsForPod(pod *v1.Pod) (v1.ResourceList, bool) {
	specRequests := Ceiling(pod).Requests

	if pod.Annotations == nil {
		return specRequests, false
	}

	cpuStr, hasCPU := pod.Annotations[karpv1.SteadyStateCPUAnnotationKey]
	memStr, hasMem := pod.Annotations[karpv1.SteadyStateMemoryAnnotationKey]

	// No steady-state annotations present at all
	if !hasCPU && !hasMem {
		return specRequests, false
	}

	steadyState := make(v1.ResourceList)
	foundValid := false

	// Parse and validate steady-state CPU
	if hasCPU {
		cpuQty, err := resource.ParseQuantity(cpuStr)
		if err != nil {
			klog.Warning(fmt.Sprintf("failed to parse %s annotation %q on pod %s/%s: %v, using spec requests for cpu",
				karpv1.SteadyStateCPUAnnotationKey, cpuStr, pod.Namespace, pod.Name, err))
			steadyState[v1.ResourceCPU] = specRequests[v1.ResourceCPU]
		} else {
			specCPU := specRequests[v1.ResourceCPU]
			if cpuQty.Cmp(specCPU) > 0 {
				klog.Warning(fmt.Sprintf("steady-state cpu %s exceeds spec request %s on pod %s/%s, using spec requests for cpu",
					cpuQty.String(), specCPU.String(), pod.Namespace, pod.Name))
				steadyState[v1.ResourceCPU] = specCPU
			} else {
				steadyState[v1.ResourceCPU] = cpuQty
				foundValid = true
			}
		}
	} else {
		// No steady-state CPU annotation; use spec
		steadyState[v1.ResourceCPU] = specRequests[v1.ResourceCPU]
	}

	// Parse and validate steady-state memory
	if hasMem {
		memQty, err := resource.ParseQuantity(memStr)
		if err != nil {
			klog.Warning(fmt.Sprintf("failed to parse %s annotation %q on pod %s/%s: %v, using spec requests for memory",
				karpv1.SteadyStateMemoryAnnotationKey, memStr, pod.Namespace, pod.Name, err))
			steadyState[v1.ResourceMemory] = specRequests[v1.ResourceMemory]
		} else {
			specMem := specRequests[v1.ResourceMemory]
			if memQty.Cmp(specMem) > 0 {
				klog.Warning(fmt.Sprintf("steady-state memory %s exceeds spec request %s on pod %s/%s, using spec requests for memory",
					memQty.String(), specMem.String(), pod.Namespace, pod.Name))
				steadyState[v1.ResourceMemory] = specMem
			} else {
				steadyState[v1.ResourceMemory] = memQty
				foundValid = true
			}
		}
	} else {
		// No steady-state memory annotation; use spec
		steadyState[v1.ResourceMemory] = specRequests[v1.ResourceMemory]
	}

	return steadyState, foundValid
}
