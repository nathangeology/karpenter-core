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
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"sigs.k8s.io/karpenter/pkg/events"
)

// AnnotationManager manages pod deletion cost annotations
type AnnotationManager struct {
	kubeClient client.Client
	recorder   events.Recorder
}

// NewAnnotationManager creates a new annotation manager
func NewAnnotationManager(kubeClient client.Client, recorder events.Recorder) *AnnotationManager {
	return &AnnotationManager{
		kubeClient: kubeClient,
		recorder:   recorder,
	}
}

// UpdatePodDeletionCosts updates pod deletion cost annotations based on node ranks
func (a *AnnotationManager) UpdatePodDeletionCosts(ctx context.Context, nodeRanks []NodeRank) error {
	logger := log.FromContext(ctx)
	var errors []error

	for _, nodeRank := range nodeRanks {
		if nodeRank.Node == nil || nodeRank.Node.Node == nil {
			continue
		}

		// Get all pods on this node
		pods, err := a.getPodsOnNode(ctx, nodeRank.Node.Node.Name)
		if err != nil {
			logger.Error(err, "failed to list pods on node", "node", nodeRank.Node.Node.Name)
			errors = append(errors, err)
			continue
		}

		// Update each pod
		for i := range pods {
			pod := &pods[i]
			if err := a.updatePodAnnotation(ctx, pod, nodeRank.Rank); err != nil {
				if !isNotFoundError(err) {
					logger.V(1).Info("failed to update pod deletion cost", "pod", pod.Name, "error", err)
					errors = append(errors, err)
				}
				// Continue with other pods even if one fails
			}
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("failed to update %d pod annotations", len(errors))
	}

	return nil
}

// getPodsOnNode retrieves all pods running on a specific node
func (a *AnnotationManager) getPodsOnNode(ctx context.Context, nodeName string) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}
	if err := a.kubeClient.List(ctx, podList, client.MatchingFields{"spec.nodeName": nodeName}); err != nil {
		return nil, err
	}

	return podList.Items, nil
}

// updatePodAnnotation updates a single pod's deletion cost annotation
func (a *AnnotationManager) updatePodAnnotation(ctx context.Context, pod *corev1.Pod, rank int) error {
	// Check if we should update this pod
	if !a.shouldUpdatePod(pod) {
		return nil
	}

	// Create a copy for patching
	stored := pod.DeepCopy()

	// Initialize annotations if needed
	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}

	// Set deletion cost and management tracking
	pod.Annotations[PodDeletionCostAnnotation] = strconv.Itoa(rank)
	pod.Annotations[KarpenterManagedDeletionCostKey] = "true"

	// Patch the pod
	if err := a.kubeClient.Patch(ctx, pod, client.MergeFrom(stored)); err != nil {
		return fmt.Errorf("patching pod: %w", err)
	}

	return nil
}

// shouldUpdatePod determines if a pod's deletion cost should be updated
func (a *AnnotationManager) shouldUpdatePod(pod *corev1.Pod) bool {
	if pod.Annotations == nil {
		// No annotations, safe to add
		return true
	}

	existingCost, hasCost := pod.Annotations[PodDeletionCostAnnotation]
	managedByKarpenter, isManaged := pod.Annotations[KarpenterManagedDeletionCostKey]

	// If no deletion cost exists, we can add it
	if !hasCost {
		return true
	}

	// If deletion cost exists but is managed by Karpenter, we can update it
	if isManaged && managedByKarpenter == "true" {
		return true
	}

	// If deletion cost exists but no management annotation, it's customer-managed
	// Don't update customer-managed annotations
	if hasCost && !isManaged {
		return false
	}

	// If deletion cost exists and management annotation says it's not managed by Karpenter
	if hasCost && isManaged && managedByKarpenter != "true" {
		return false
	}

	// Default: check if we have the existing cost
	_ = existingCost
	return true
}

// isNotFoundError checks if an error is a NotFound error
func isNotFoundError(err error) bool {
	return errors.IsNotFound(err)
}
