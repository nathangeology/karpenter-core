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
var sizeClassLockThreshold int = 5

// Pod deletion cost configuration - reads from environment or uses defaults
var (
	// podDeletionCostEnabled controls whether pod deletion cost management is enabled
	// Reads from POD_DELETION_COST_ENABLED environment variable, defaults to true for performance tests
	podDeletionCostEnabled = func() bool {
		if val := os.Getenv("POD_DELETION_COST_ENABLED"); val != "" {
			return val == "true"
		}
		return true // Default for performance tests
	}()

	// podDeletionCostRankingStrategy controls the ranking strategy for pod deletion cost
	// Valid values: "Random", "LargestToSmallest", "SmallestToLargest", "UnallocatedVCPUPerPodCost"
	// Reads from POD_DELETION_COST_RANKING_STRATEGY environment variable, defaults to UnallocatedVCPUPerPodCost
	podDeletionCostRankingStrategy = func() string {
		if val := os.Getenv("POD_DELETION_COST_RANKING_STRATEGY"); val != "" {
			return val
		}
		return "UnallocatedVCPUPerPodCost" // Default for performance tests
	}()

	// podDeletionCostChangeDetection controls whether change detection optimization is enabled
	// Set to true to enable change detection (skip ranking when no changes), false to always rank
	// Reads from POD_DELETION_COST_CHANGE_DETECTION environment variable, defaults to true
	podDeletionCostChangeDetection = func() bool {
		if val := os.Getenv("POD_DELETION_COST_CHANGE_DETECTION"); val != "" {
			return val == "true"
		}
		return true // Default
	}()
)

func init() {
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
