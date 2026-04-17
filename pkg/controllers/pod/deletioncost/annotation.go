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

package deletioncost

import (
	"context"
	"fmt"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"sigs.k8s.io/karpenter/pkg/events"
	"sigs.k8s.io/karpenter/pkg/metrics"
)

const (
	// PodDeletionCostAnnotation is the Kubernetes annotation that influences pod termination priority
	// Lower values indicate higher deletion priority
	PodDeletionCostAnnotation = "controller.kubernetes.io/pod-deletion-cost"
	// KarpenterManagedDeletionCostAnnotation tracks whether Karpenter is managing the deletion cost
	KarpenterManagedDeletionCostAnnotation = "karpenter.sh/managed-deletion-cost"
)

// PodUpdate represents a pod that needs its deletion cost annotation updated
type PodUpdate struct {
	Pod       *corev1.Pod
	NewRank   int
	ShouldAdd bool // true if adding annotation, false if updating
}

// AnnotationManager handles pod deletion cost annotation updates
type AnnotationManager struct {
	kubeClient client.Client
	recorder   events.Recorder

	mu                 sync.Mutex
	lastAssignedValues map[types.UID]string
}

// NewAnnotationManager creates a new AnnotationManager
func NewAnnotationManager(kubeClient client.Client, recorder events.Recorder) *AnnotationManager {
	return &AnnotationManager{
		kubeClient:         kubeClient,
		recorder:           recorder,
		lastAssignedValues: make(map[types.UID]string),
	}
}

// UpdatePodDeletionCosts updates pod deletion cost annotations for all pods on the ranked nodes
func (a *AnnotationManager) UpdatePodDeletionCosts(ctx context.Context, nodeRanks []NodeRank) error {
	// Measure annotation update duration
	defer metrics.Measure(AnnotationDurationSeconds, map[string]string{})()

	var successCount, skippedCount, errorCount int

	// Track which pod UIDs we see this cycle for cleanup
	seenUIDs := make(map[types.UID]bool)

	// Process each ranked node
	for _, nodeRank := range nodeRanks {
		pods, err := nodeRank.Node.Pods(ctx, a.kubeClient)
		if err != nil {
			log.FromContext(ctx).WithValues("node", nodeRank.Node.Name()).Error(err, "failed to list pods on node")
			errorCount++
			continue
		}

		for _, pod := range pods {
			seenUIDs[pod.UID] = true

			// Check for third-party modification
			if a.detectExternalModification(ctx, pod) {
				skippedCount++
				continue
			}

			if !shouldUpdatePod(pod) {
				skippedCount++
				continue
			}

			podUpdate := PodUpdate{
				Pod:       pod,
				NewRank:   nodeRank.Rank,
				ShouldAdd: !hasDeletionCostAnnotation(pod),
			}

			if err := a.updatePodAnnotation(ctx, podUpdate); err != nil {
				if apierrors.IsNotFound(err) {
					log.FromContext(ctx).V(1).WithValues("pod", klog.KObj(podUpdate.Pod)).Info("pod not found, skipping annotation update")
					continue
				}
				if apierrors.IsConflict(err) {
					log.FromContext(ctx).V(1).WithValues("pod", klog.KObj(podUpdate.Pod)).Info("conflict updating pod annotation, will retry on next reconcile")
					errorCount++
					continue
				}
				log.FromContext(ctx).WithValues("pod", klog.KObj(podUpdate.Pod)).Error(err, "failed to update pod deletion cost annotation")
				a.recorder.Publish(UpdateFailedEvent(podUpdate.Pod, err))
				errorCount++
				continue
			}
			successCount++
		}
	}

	// Clean up lastAssignedValues for pods no longer seen (deleted pods)
	a.mu.Lock()
	for uid := range a.lastAssignedValues {
		if !seenUIDs[uid] {
			delete(a.lastAssignedValues, uid)
		}
	}
	a.mu.Unlock()

	// Record metrics
	PodsUpdatedTotal.Add(float64(successCount), map[string]string{resultLabel: "success"})
	PodsUpdatedTotal.Add(float64(skippedCount), map[string]string{resultLabel: "skipped_customer_managed"})
	PodsUpdatedTotal.Add(float64(errorCount), map[string]string{resultLabel: "error"})

	if successCount > 0 || errorCount > 0 {
		log.FromContext(ctx).WithValues(
			"success", successCount,
			"skipped", skippedCount,
			"errors", errorCount,
		).V(1).Info("pod deletion cost annotation update completed")
	}

	return nil
}

// detectExternalModification checks if a third party modified the deletion cost annotation.
// If the sentinel annotation is present and the current value differs from what Karpenter
// last set, it removes the sentinel (releasing management) and emits a warning event.
func (a *AnnotationManager) detectExternalModification(ctx context.Context, pod *corev1.Pod) bool {
	if pod.Annotations == nil {
		return false
	}

	// Only check pods that have the sentinel annotation (Karpenter-managed)
	if _, hasSentinel := pod.Annotations[KarpenterManagedDeletionCostAnnotation]; !hasSentinel {
		return false
	}

	currentValue, hasCost := pod.Annotations[PodDeletionCostAnnotation]
	if !hasCost {
		return false
	}

	a.mu.Lock()
	lastValue, tracked := a.lastAssignedValues[pod.UID]
	a.mu.Unlock()

	if !tracked {
		// First time seeing this pod — not a conflict
		return false
	}

	if currentValue == lastValue {
		// Value unchanged — no external modification
		return false
	}

	// External modification detected: remove sentinel annotation
	log.FromContext(ctx).WithValues("pod", klog.KObj(pod), "expected", lastValue, "actual", currentValue).
		Info("external modification detected on pod-deletion-cost annotation, releasing management")

	patchPod := pod.DeepCopy()
	delete(patchPod.Annotations, KarpenterManagedDeletionCostAnnotation)
	if err := a.kubeClient.Patch(ctx, patchPod, client.MergeFrom(pod)); err != nil {
		if !apierrors.IsNotFound(err) {
			log.FromContext(ctx).WithValues("pod", klog.KObj(pod)).Error(err, "failed to remove sentinel annotation")
		}
	}

	a.recorder.Publish(ExternalAnnotationModificationEvent(pod))

	// Remove from tracking
	a.mu.Lock()
	delete(a.lastAssignedValues, pod.UID)
	a.mu.Unlock()

	return true
}

// CleanupNodeAnnotations removes both deletion cost and sentinel annotations from all
// Karpenter-managed pods on the given node.
func (a *AnnotationManager) CleanupNodeAnnotations(ctx context.Context, node *NodeRank) error {
	pods, err := node.Node.Pods(ctx, a.kubeClient)
	if err != nil {
		return err
	}

	for _, pod := range pods {
		if pod.Annotations == nil {
			continue
		}
		if _, hasSentinel := pod.Annotations[KarpenterManagedDeletionCostAnnotation]; !hasSentinel {
			continue
		}

		patchPod := pod.DeepCopy()
		delete(patchPod.Annotations, PodDeletionCostAnnotation)
		delete(patchPod.Annotations, KarpenterManagedDeletionCostAnnotation)
		if err := a.kubeClient.Patch(ctx, patchPod, client.MergeFrom(pod)); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			log.FromContext(ctx).WithValues("pod", klog.KObj(pod)).Error(err, "failed to clean up annotations")
		}

		// Remove from tracking
		a.mu.Lock()
		delete(a.lastAssignedValues, pod.UID)
		a.mu.Unlock()
	}
	return nil
}

// hasDeletionCostAnnotation checks if a pod has the deletion cost annotation
func hasDeletionCostAnnotation(pod *corev1.Pod) bool {
	if pod.Annotations == nil {
		return false
	}
	_, exists := pod.Annotations[PodDeletionCostAnnotation]
	return exists
}

// shouldUpdatePod determines if a pod should have its deletion cost updated
func shouldUpdatePod(pod *corev1.Pod) bool {
	if pod.Annotations == nil {
		return true
	}

	_, hasDeletionCost := pod.Annotations[PodDeletionCostAnnotation]
	_, hasManagedAnnotation := pod.Annotations[KarpenterManagedDeletionCostAnnotation]

	// If pod has deletion cost but no management annotation, it's customer-managed
	if hasDeletionCost && !hasManagedAnnotation {
		return false
	}

	return true
}

// updatePodAnnotation updates a single pod's deletion cost annotation
func (a *AnnotationManager) updatePodAnnotation(ctx context.Context, podUpdate PodUpdate) error {
	pod := podUpdate.Pod.DeepCopy()

	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}

	newValue := fmt.Sprintf("%d", podUpdate.NewRank)
	pod.Annotations[PodDeletionCostAnnotation] = newValue
	pod.Annotations[KarpenterManagedDeletionCostAnnotation] = "true"

	if err := a.kubeClient.Update(ctx, pod); err != nil {
		return err
	}

	// Record what we set so we can detect external modifications
	a.mu.Lock()
	a.lastAssignedValues[podUpdate.Pod.UID] = newValue
	a.mu.Unlock()

	return nil
}
