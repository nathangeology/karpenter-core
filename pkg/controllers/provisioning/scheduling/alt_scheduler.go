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
	"time"

	"github.com/awslabs/operatorpkg/option"
	"github.com/samber/lo"
	"go.uber.org/multierr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	"sigs.k8s.io/karpenter/pkg/events"
	"sigs.k8s.io/karpenter/pkg/metrics"
	"sigs.k8s.io/karpenter/pkg/operator/injection"
	"sigs.k8s.io/karpenter/pkg/scheduling"
	"sigs.k8s.io/karpenter/pkg/utils/resources"
)

// AltScheduler extends the base Scheduler
type AltScheduler struct {
	Scheduler                      // Embedding the base Scheduler
	uuid                 types.UID // Unique UUID attached to this scheduling loop
	newNodeClaims        []*NodeClaim
	existingNodes        []*ExistingNode
	nodeClaimTemplates   []*NodeClaimTemplate
	remainingResources   map[string]corev1.ResourceList // (NodePool name) -> remaining resources for that NodePool
	daemonOverhead       map[*NodeClaimTemplate]corev1.ResourceList
	daemonHostPortUsage  map[*NodeClaimTemplate]*scheduling.HostPortUsage
	cachedPodData        map[types.UID]*PodData // (Pod Namespace/Name) -> pre-computed data for pods to avoid re-computation and memory usage
	topology             *Topology
	cluster              *state.Cluster
	recorder             events.Recorder
	kubeClient           client.Client
	clock                clock.Clock
	reservationManager   *ReservationManager
	reservedOfferingMode ReservedOfferingMode
}

// NewAltScheduler creates a new instance of AltScheduler
func NewAltScheduler(
	ctx context.Context,
	kubeClient client.Client,
	nodePools []*v1.NodePool,
	cluster *state.Cluster,
	stateNodes []*state.StateNode,
	topology *Topology,
	instanceTypes map[string][]*cloudprovider.InstanceType,
	daemonSetPods []*corev1.Pod,
	recorder events.Recorder,
	clock clock.Clock,
	opts ...Options,
) *AltScheduler {
	// Filter out node pools that are not compatible with the instance types
	templates := lo.FilterMap(nodePools, func(np *v1.NodePool, _ int) (*NodeClaimTemplate, bool) {
		nct := NewNodeClaimTemplate(np)
		nct.InstanceTypeOptions, _ = filterInstanceTypesByRequirements(instanceTypes[np.Name], nct.Requirements, corev1.ResourceList{}, corev1.ResourceList{}, corev1.ResourceList{})
		if len(nct.InstanceTypeOptions) == 0 {
			recorder.Publish(NoCompatibleInstanceTypes(np))
			log.FromContext(ctx).WithValues("NodePool", klog.KObj(np)).Info("skipping, nodepool requirements filtered out all instance types")
			return nil, false
		}
		return nct, true
	})
	// Create base scheduler
	s := &AltScheduler{
		uuid:                uuid.NewUUID(),
		kubeClient:          kubeClient,
		nodeClaimTemplates:  templates,
		topology:            topology,
		cluster:             cluster,
		daemonOverhead:      getDaemonOverhead(templates, daemonSetPods),
		daemonHostPortUsage: getDaemonHostPortUsage(templates, daemonSetPods),
		cachedPodData:       map[types.UID]*PodData{}, // cache pod data to avoid having to continually recompute it
		recorder:            recorder,
		remainingResources: lo.SliceToMap(nodePools, func(np *v1.NodePool) (string, corev1.ResourceList) {
			return np.Name, corev1.ResourceList(np.Spec.Limits)
		}),
		clock:                clock,
		reservationManager:   NewReservationManager(instanceTypes),
		reservedOfferingMode: option.Resolve(opts...).reservedOfferingMode,
	}
	return s
}

// Solve overrides the base Scheduler's Solve method
//
//nolint:gocyclo
func (s *AltScheduler) Solve(ctx context.Context, pods []*corev1.Pod) (Results, error) {
	// Setup for solving
	defer metrics.Measure(DurationSeconds, map[string]string{ControllerLabel: injection.GetControllerName(ctx)})()
	podErrors := map[*corev1.Pod]error{}
	// Reset the metric for the controller, so we don't keep old ids around
	UnschedulablePodsCount.DeletePartialMatch(map[string]string{ControllerLabel: injection.GetControllerName(ctx)})
	QueueDepth.DeletePartialMatch(map[string]string{ControllerLabel: injection.GetControllerName(ctx)})

	// Example of custom scheduling logic:
	for _, p := range pods {
		s.updateCachedPodData(p)
	}

	q := NewQueue(pods, s.cachedPodData)
	startTime := s.clock.Now()
	lastLogTime := s.clock.Now()
	batchSize := len(q.pods)

	// Example of how you might implement different scheduling logic
	for {
		if ctx.Err() != nil {
			log.FromContext(ctx).V(1).WithValues("duration", s.clock.Since(startTime).Truncate(time.Second), "scheduling-id", string(s.uuid)).Info("scheduling simulation timed out")
			break
		}
		if s.clock.Since(lastLogTime) > time.Minute {
			log.FromContext(ctx).WithValues("pods-scheduled", batchSize-len(q.pods), "pods-remaining", len(q.pods), "existing-nodes", len(s.existingNodes), "simulated-nodes", len(s.newNodeClaims), "duration", s.clock.Since(startTime).Truncate(time.Second), "scheduling-id", string(s.uuid)).Info("computing pod scheduling...")
			lastLogTime = s.clock.Now()
		}
		pod, ok := q.Pop()
		if !ok {
			break
		}
		// Implement your custom pod scheduling logic here
		// You can still use the base scheduler's helper methods
		// For example:
		// - s.findNodeForPod(ctx, pod)
		// - s.simulateNewNode(ctx, pod)
		// etc.

		// Add your results
		// results.Add(...)
		err := s.add(ctx, pod)
		//^^ Note: Most likely you'll implement at least some custom logic in the scheduler.add function
		if err != nil {
			podErrors[pod] = err
			continue
		}

	}
	UnfinishedWorkSeconds.Delete(map[string]string{ControllerLabel: injection.GetControllerName(ctx), schedulingIDLabel: string(s.uuid)})
	for _, m := range s.newNodeClaims {
		m.FinalizeScheduling()
	}
	return Results{
		NewNodeClaims: s.newNodeClaims,
		ExistingNodes: s.existingNodes,
		PodErrors:     podErrors,
	}, ctx.Err()
}

//nolint:gocyclo
func (s *AltScheduler) add(ctx context.Context, pod *corev1.Pod) error {
	// For single pod per node (SPPN) scheduling, we can just create a new node claim for each pod
	// Create new node
	var errs error
	for _, nodeClaimTemplate := range s.nodeClaimTemplates {
		instanceTypes := nodeClaimTemplate.InstanceTypeOptions
		if remaining, ok := s.remainingResources[nodeClaimTemplate.NodePoolName]; ok {
			instanceTypes = filterByRemainingResources(instanceTypes, remaining)
			if len(instanceTypes) == 0 {
				errs = multierr.Append(errs, fmt.Errorf("all available instance types exceed limits for nodepool %q", nodeClaimTemplate.NodePoolName))
				continue
			} else if len(nodeClaimTemplate.InstanceTypeOptions) != len(instanceTypes) {
				log.FromContext(ctx).V(1).WithValues(
					"NodePool", klog.KRef("", nodeClaimTemplate.NodePoolName),
				).Info(fmt.Sprintf(
					"%d out of %d instance types were excluded because they would breach limits",
					len(nodeClaimTemplate.InstanceTypeOptions)-len(instanceTypes),
					len(nodeClaimTemplate.InstanceTypeOptions),
				))
			}
		}
		nodeClaim := NewNodeClaim(nodeClaimTemplate, s.topology, s.daemonOverhead[nodeClaimTemplate], s.daemonHostPortUsage[nodeClaimTemplate], instanceTypes, s.reservationManager, s.reservedOfferingMode)
		if err := nodeClaim.Add(ctx, pod, s.cachedPodData[pod.UID]); err != nil {
			nodeClaim.Destroy()
			if IsReservedOfferingError(err) {
				errs = multierr.Append(errs, fmt.Errorf(
					"compatible with nodepool %q but failed to add pod while adhering to reservation fallback policy, %w",
					nodeClaimTemplate.NodePoolName,
					err,
				))
				// If the pod is compatible with a NodePool with reserved offerings available, we shouldn't fall back to a NodePool
				// with a lower weight. We could consider allowing "fallback" to NodePools with equal weight if they also have
				// reserved capacity in the future if scheduling latency becomes an issue.
				break
			}
			errs = multierr.Append(errs, fmt.Errorf(
				"incompatible with nodepool %q, daemonset overhead=%s, %w",
				nodeClaimTemplate.NodePoolName,
				resources.String(s.daemonOverhead[nodeClaimTemplate]),
				err,
			))
			continue
		}
		// we will launch this nodeClaim and need to track its maximum possible resource usage against our remaining resources
		s.newNodeClaims = append(s.newNodeClaims, nodeClaim)
		s.remainingResources[nodeClaimTemplate.NodePoolName] = subtractMax(s.remainingResources[nodeClaimTemplate.NodePoolName], nodeClaim.InstanceTypeOptions)
		return nil
	}
	return errs
}

// You can also override other methods as needed
// For example:
func (s *AltScheduler) findNodeForPod(ctx context.Context, pod *corev1.Pod) (*NodeClaim, error) {
	// Your custom node finding logic
	return nil, nil
}
