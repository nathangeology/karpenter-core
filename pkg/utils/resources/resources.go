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
	resourcehelper "k8s.io/component-helpers/resource"
	"k8s.io/klog/v2"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/metrics"
	"sigs.k8s.io/karpenter/pkg/utils/pretty"
)

var Node = v1.ResourceName("nodes")

// RequestsForPods returns the total resources of a variadic list of podspecs.
func RequestsForPods(pods ...*v1.Pod) v1.ResourceList {
	var resources []v1.ResourceList
	for _, pod := range pods {
		resources = append(resources, Ceiling(pod).Requests)
	}
	merged := Merge(resources...)
	merged[v1.ResourcePods] = *resource.NewQuantity(int64(len(pods)), resource.DecimalExponent)
	return merged
}

// LimitsForPods returns the total resources of a variadic list of podspecs
func LimitsForPods(pods ...*v1.Pod) v1.ResourceList {
	var resources []v1.ResourceList
	for _, pod := range pods {
		resources = append(resources, Ceiling(pod).Limits)
	}
	merged := Merge(resources...)
	merged[v1.ResourcePods] = *resource.NewQuantity(int64(len(pods)), resource.DecimalExponent)
	return merged
}

// IPVSAwareRequestsForPod computes the effective resource requests for a pod
// under IPVS awareness. For each resource type, it returns the maximum of:
//  1. pod.Spec.Containers[i].Resources.Requests (via Ceiling)
//  2. pod.Status.ContainerStatuses[i].AllocatedResources (if present)
//  3. Peak annotation values (karpenter.sh/peak-cpu, karpenter.sh/peak-memory)
//
// This ensures Karpenter never underestimates the resources a pod may consume.
func IPVSAwareRequestsForPod(pod *v1.Pod) v1.ResourceList {
	// 1. Spec-based requests using existing Ceiling helper
	specRequests := Ceiling(pod).Requests

	// 2. Sum AllocatedResources across all container statuses
	allocatedResources := v1.ResourceList{}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.AllocatedResources != nil {
			allocatedResources = MergeInto(allocatedResources, cs.AllocatedResources)
		}
	}

	// 3. Parse peak annotations
	peakResources := v1.ResourceList{}
	annotationParseFailed := false
	if pod.Annotations != nil {
		if cpuStr, ok := pod.Annotations[karpv1.PeakCPUAnnotationKey]; ok {
			cpuQty, err := resource.ParseQuantity(cpuStr)
			if err != nil {
				klog.Warning(fmt.Sprintf("failed to parse %s annotation %q on pod %s/%s: %v, falling back to max(spec.requests, allocatedResources) for cpu",
					karpv1.PeakCPUAnnotationKey, cpuStr, pod.Namespace, pod.Name, err))
				annotationParseFailed = true
			} else {
				peakResources[v1.ResourceCPU] = cpuQty
			}
		}
		if memStr, ok := pod.Annotations[karpv1.PeakMemoryAnnotationKey]; ok {
			memQty, err := resource.ParseQuantity(memStr)
			if err != nil {
				klog.Warning(fmt.Sprintf("failed to parse %s annotation %q on pod %s/%s: %v, falling back to max(spec.requests, allocatedResources) for memory",
					karpv1.PeakMemoryAnnotationKey, memStr, pod.Namespace, pod.Name, err))
				annotationParseFailed = true
			} else {
				peakResources[v1.ResourceMemory] = memQty
			}
		}
	}

	// Compute the effective resources
	var effective v1.ResourceList
	if len(peakResources) == 0 && annotationParseFailed {
		effective = MaxResources(specRequests, allocatedResources)
	} else {
		effective = MaxResources(specRequests, allocatedResources, peakResources)
	}

	// Emit resource adjustment metric and debug log when IPVS-aware result differs from spec-based
	cpuAdjusted := false
	memAdjusted := false
	if cpuEffective, ok := effective[v1.ResourceCPU]; ok {
		if cpuSpec, specOk := specRequests[v1.ResourceCPU]; !specOk || cpuEffective.Cmp(cpuSpec) != 0 {
			metrics.IPVSResourceAdjustmentTotal.Inc(map[string]string{metrics.ResourceTypeLabel: "cpu"})
			cpuAdjusted = true
		}
	}
	if memEffective, ok := effective[v1.ResourceMemory]; ok {
		if memSpec, specOk := specRequests[v1.ResourceMemory]; !specOk || memEffective.Cmp(memSpec) != 0 {
			metrics.IPVSResourceAdjustmentTotal.Inc(map[string]string{metrics.ResourceTypeLabel: "memory"})
			memAdjusted = true
		}
	}
	if cpuAdjusted || memAdjusted {
		klog.V(4).Infof("IPVS resource adjustment for pod %s/%s: spec requests=%s, effective requests=%s",
			pod.Namespace, pod.Name, pretty.Concise(specRequests), pretty.Concise(effective))
	}

	return effective
}

// Merge the resources from the variadic into a single v1.ResourceList
func Merge(resources ...v1.ResourceList) v1.ResourceList {
	if len(resources) == 0 {
		return v1.ResourceList{}
	}
	result := make(v1.ResourceList, len(resources[0]))
	for _, resourceList := range resources {
		for resourceName, quantity := range resourceList {
			current := result[resourceName]
			current.Add(quantity)
			result[resourceName] = current
		}
	}
	return result
}

// MergeInto sums the resources from src into dest, modifying dest. If you need to repeatedly sum
// multiple resource lists, it allocates less to continually sum into an existing list as opposed to
// constructing a new one for each sum like Merge
func MergeInto(dest v1.ResourceList, src v1.ResourceList) v1.ResourceList {
	if dest == nil {
		sz := len(src)
		dest = make(v1.ResourceList, sz)
	}
	for resourceName, quantity := range src {
		current := dest[resourceName]
		current.Add(quantity)
		dest[resourceName] = current
	}
	return dest
}

func Subtract(lhs, rhs v1.ResourceList) v1.ResourceList {
	result := make(v1.ResourceList, len(lhs))
	for k, v := range lhs {
		result[k] = v.DeepCopy()
	}
	for resourceName := range lhs {
		current := lhs[resourceName]
		if rhsValue, ok := rhs[resourceName]; ok {
			current.Sub(rhsValue)
		}
		result[resourceName] = current
	}
	return result
}

// SubtractFrom subtracts the src v1.ResourceList from the dest v1.ResourceList in-place
func SubtractFrom(dest v1.ResourceList, src v1.ResourceList) {
	if dest == nil {
		sz := len(src)
		dest = make(v1.ResourceList, sz)
	}
	for resourceName, quantity := range src {
		current := dest[resourceName]
		current.Sub(quantity)
		dest[resourceName] = current
	}
}

// Ceiling computes the effective resource requirements for a given Pod,
// using the same logic as the scheduler.
func Ceiling(pod *v1.Pod) v1.ResourceRequirements {
	return v1.ResourceRequirements{
		Requests: resourcehelper.PodRequests(pod, resourcehelper.PodResourcesOptions{}),
		Limits:   resourcehelper.PodLimits(pod, resourcehelper.PodResourcesOptions{}),
	}
}

// MaxResources returns the maximum quantities for a given list of resources
func MaxResources(resources ...v1.ResourceList) v1.ResourceList {
	resourceList := v1.ResourceList{}
	for _, resource := range resources {
		for resourceName, quantity := range resource {
			if value, ok := resourceList[resourceName]; !ok || quantity.Cmp(value) > 0 {
				resourceList[resourceName] = quantity
			}
		}
	}
	return resourceList
}

// Quantity parses the string value into a *Quantity
func Quantity(value string) *resource.Quantity {
	r := resource.MustParse(value)
	return &r
}

// IsZero implements r.IsZero(). This method is provided to make some code a bit cleaner as the Quantity.IsZero() takes
// a pointer receiver and map index expressions aren't addressable, so it can't be called directly.
func IsZero(r resource.Quantity) bool {
	return r.IsZero()
}

func Cmp(lhs resource.Quantity, rhs resource.Quantity) int {
	return lhs.Cmp(rhs)
}

// Fits returns true if the candidate set of resources is less than or equal to the total set of resources.
func Fits(candidate, total v1.ResourceList) bool {
	// If any of the total resource values are negative then the resource will never fit
	for _, quantity := range total {
		if Cmp(*resource.NewScaledQuantity(0, resource.Kilo), quantity) > 0 {
			return false
		}
	}
	for resourceName, quantity := range candidate {
		if Cmp(quantity, total[resourceName]) > 0 {
			return false
		}
	}
	return true
}

// String returns a string version of the resource list suitable for presenting in a log
func String(list v1.ResourceList) string {
	if len(list) == 0 {
		return "{}"
	}
	return pretty.Concise(list)
}
