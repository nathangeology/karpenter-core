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

package performance

import (
	"fmt"
	"os"
	"strconv"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"sigs.k8s.io/karpenter/kwok/apis/v1alpha1"
	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/test"
	"sigs.k8s.io/karpenter/test/pkg/environment/common"
)

var nodePool *v1.NodePool
var nodeClass *unstructured.Unstructured
var env *common.Environment

// sizeClassLockThreshold controls the pod count threshold for size class locking
// Set to 0 to disable, or set to a positive value (e.g., 5, 10, 20) to enable
var sizeClassLockThreshold int = 0

// decisionRatioThreshold controls the minimum decision ratio for consolidation
// when using WhenCostJustifiesDisruption policy. Default is 1.5 (conservative)
// Can be overridden via DECISION_RATIO_THRESHOLD environment variable
var decisionRatioThreshold float64 = 1.5

func init() {
	// Initialize decision ratio threshold from environment variable if set
	if val := os.Getenv("DECISION_RATIO_THRESHOLD"); val != "" {
		if threshold, err := strconv.ParseFloat(val, 64); err == nil && threshold > 0 {
			decisionRatioThreshold = threshold
			fmt.Printf("=== Decision Ratio Threshold set to %.2f from environment ===\n", decisionRatioThreshold)
		} else {
			fmt.Printf("WARNING: Invalid DECISION_RATIO_THRESHOLD value '%s', using default %.2f\n", val, decisionRatioThreshold)
		}
	}

	// Initialize pod deletion cost configuration from environment variables
	if val := os.Getenv("POD_DELETION_COST_ENABLED"); val != "" {
		podDeletionCostEnabled = val == "true"
	} else {
		podDeletionCostEnabled = true // Default for performance tests
	}

	if val := os.Getenv("POD_DELETION_COST_RANKING_STRATEGY"); val != "" {
		podDeletionCostRankingStrategy = val
	} else {
		podDeletionCostRankingStrategy = "SmallestToLargest" // Default for performance tests
	}

	if val := os.Getenv("POD_DELETION_COST_CHANGE_DETECTION"); val != "" {
		podDeletionCostChangeDetection = val == "true"
	} else {
		podDeletionCostChangeDetection = true // Default
	}

	// Log configuration at startup for debugging
	fmt.Printf("=== Pod Deletion Cost Configuration ===\n")
	fmt.Printf("  Enabled: %v\n", podDeletionCostEnabled)
	fmt.Printf("  Strategy: %s\n", podDeletionCostRankingStrategy)
	fmt.Printf("  Change Detection: %v\n", podDeletionCostChangeDetection)

	// Validate ranking strategy
	validStrategies := []string{"Random", "LargestToSmallest", "SmallestToLargest", "UnallocatedVCPUPerPodCost"}
	isValid := false
	for _, valid := range validStrategies {
		if podDeletionCostRankingStrategy == valid {
			isValid = true
			break
		}
	}
	if !isValid {
		fmt.Printf("  WARNING: Invalid ranking strategy '%s'. Valid values: %v\n", podDeletionCostRankingStrategy, validStrategies)
	}
	fmt.Printf("========================================\n")
}

func TestIntegration(t *testing.T) {
	RegisterFailHandler(Fail)
	BeforeSuite(func() {
		env = common.NewEnvironment(t)
	})
	AfterSuite(func() {
		env.Stop()
	})
	RunSpecs(t, "Performance")
}

var _ = BeforeEach(func() {
	env.BeforeEach()
	nodeClass = env.DefaultNodeClass.DeepCopy()
	nodeClass.SetName(fmt.Sprintf("%s-%s", nodeClass.GetName(), test.RandomName()))
	nodePool = env.DefaultNodePool(nodeClass)
	if env.IsDefaultNodeClassKWOK() {
		test.ReplaceRequirements(nodePool, v1.NodeSelectorRequirementWithMinValues{
			Key:      v1alpha1.InstanceSizeLabelKey,
			Operator: corev1.NodeSelectorOpLt,
			Values:   []string{"96"},
		})
	}
	nodePool.Spec.Limits = v1.Limits{}
	nodePool.Spec.Disruption.ConsolidationPolicy = v1.ConsolidationPolicyWhenEmptyOrUnderutilized
	nodePool.Spec.Disruption.ConsolidateAfter = v1.MustParseNillableDuration("30s")
	nodePool.Spec.Disruption.Budgets = []v1.Budget{{Nodes: "100%"}}

	// Configure consolidation policy and decision ratio threshold
	nodePool.Spec.Disruption.ConsolidateWhen = v1.ConsolidateWhenCostJustifiesDisruption
	nodePool.Spec.Disruption.DecisionRatioThreshold = &decisionRatioThreshold

	// Configure size class locking if threshold is set
	if sizeClassLockThreshold > 0 {
		if nodePool.Annotations == nil {
			nodePool.Annotations = make(map[string]string)
		}
		nodePool.Annotations[v1.NodeClaimSizeClassLockThresholdAnnotationKey] = fmt.Sprintf("%d", sizeClassLockThreshold)
	}
})

var _ = AfterEach(func() {
	env.Cleanup()
	env.AfterEach()
})

// getConsolidationSettings returns the current consolidation policy and decision ratio threshold
func getConsolidationSettings() (string, float64) {
	consolidateWhen := string(nodePool.Spec.Disruption.ConsolidateWhen)
	decisionRatio := nodePool.Spec.Disruption.GetDecisionRatioThreshold()
	return consolidateWhen, decisionRatio
}

// checkPodDeletionCostAnnotations checks if pods have deletion cost annotations
// Returns true if at least one pod has the annotation, false otherwise
func checkPodDeletionCostAnnotations(env *common.Environment) bool {
	podList := &corev1.PodList{}
	if err := env.Client.List(env.Context, podList); err != nil {
		GinkgoWriter.Printf("Failed to list pods: %v\n", err)
		return false
	}

	annotatedCount := 0
	totalPods := 0

	for _, pod := range podList.Items {
		// Skip system pods
		if pod.Namespace == "kube-system" || pod.Namespace == "karpenter" {
			continue
		}

		totalPods++

		// Check for deletion cost annotation
		if _, hasDeletionCost := pod.Annotations["controller.kubernetes.io/pod-deletion-cost"]; hasDeletionCost {
			annotatedCount++
		}
	}

	if totalPods == 0 {
		GinkgoWriter.Printf("No application pods found to check\n")
		return false
	}

	GinkgoWriter.Printf("Pod deletion cost check: %d/%d pods have annotations (%.1f%%)\n",
		annotatedCount, totalPods, float64(annotatedCount)/float64(totalPods)*100)

	// Consider it detected if at least 50% of pods have the annotation
	// (allows for some pods that might be in transition)
	return annotatedCount > 0 && float64(annotatedCount)/float64(totalPods) >= 0.5
}
