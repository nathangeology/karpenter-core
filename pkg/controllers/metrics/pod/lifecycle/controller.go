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

package lifecycle

import (
	"context"
	"time"

	gocache "github.com/patrickmn/go-cache"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/samber/lo"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	"sigs.k8s.io/karpenter/pkg/operator/injection"
	nodeutils "sigs.k8s.io/karpenter/pkg/utils/node"
)

// Controller watches pod lifecycle transitions and records per-phase duration histograms.
type Controller struct {
	kubeClient client.Client
	cluster    *state.Cluster
	// Track pods we've already recorded startup metrics for (TTL-bounded to prevent unbounded growth)
	recordedStartup *gocache.Cache
	// Track pods we've already recorded shutdown metrics for (TTL-bounded to prevent unbounded growth)
	recordedShutdown *gocache.Cache
}

func NewController(kubeClient client.Client, cluster *state.Cluster) *Controller {
	return &Controller{
		kubeClient:       kubeClient,
		cluster:          cluster,
		recordedStartup:  gocache.New(time.Hour, 10*time.Minute),
		recordedShutdown: gocache.New(time.Hour, 10*time.Minute),
	}
}

func (c *Controller) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	ctx = injection.WithControllerName(ctx, c.Name())

	pod := &corev1.Pod{}
	if err := c.kubeClient.Get(ctx, req.NamespacedName, pod); err != nil {
		if errors.IsNotFound(err) {
			c.recordedStartup.Delete(req.String())
			c.recordedShutdown.Delete(req.String())
		}
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	key := client.ObjectKeyFromObject(pod).String()
	labels := c.labelsForPod(ctx, pod)

	c.recordStartupMetrics(ctx, pod, key, labels)
	c.recordShutdownMetrics(ctx, pod, key, labels)

	return reconcile.Result{}, nil
}

func (c *Controller) recordStartupMetrics(ctx context.Context, pod *corev1.Pod, key string, labels prometheus.Labels) {
	if _, found := c.recordedStartup.Get(key); found {
		return
	}
	// Only record when pod becomes Ready
	readyCond, ready := lo.Find(pod.Status.Conditions, func(cond corev1.PodCondition) bool {
		return cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue
	})
	if !ready {
		return
	}
	c.recordedStartup.SetDefault(key, true)

	readyTime := readyCond.LastTransitionTime.Time
	creationTime := pod.CreationTimestamp.Time

	// Total startup: creation → ready
	LifecycleStartupTotalDurationSeconds.Observe(readyTime.Sub(creationTime).Seconds(), labels)

	// Scheduling decision duration from cluster state
	nn := types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}
	if decisionTime := c.cluster.PodSchedulingDecisionTime(nn); !decisionTime.IsZero() {
		LifecycleSchedulingDecisionDurationSeconds.Observe(decisionTime.Sub(creationTime).Seconds(), labels)
	}

	// Pod condition-based phases
	scheduledCond, hasScheduled := lo.Find(pod.Status.Conditions, func(cond corev1.PodCondition) bool {
		return cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionTrue
	})
	initCond, hasInit := lo.Find(pod.Status.Conditions, func(cond corev1.PodCondition) bool {
		return cond.Type == corev1.PodInitialized && cond.Status == corev1.ConditionTrue
	})
	containerReadyCond, hasContainerReady := lo.Find(pod.Status.Conditions, func(cond corev1.PodCondition) bool {
		return cond.Type == corev1.ContainersReady && cond.Status == corev1.ConditionTrue
	})

	// Init containers duration: scheduled → initialized
	if hasScheduled && hasInit {
		dur := initCond.LastTransitionTime.Sub(scheduledCond.LastTransitionTime.Time).Seconds()
		if dur >= 0 {
			LifecycleInitContainersDurationSeconds.Observe(dur, labels)
		}
	}

	// Container start duration: initialized → containers ready
	if hasInit && hasContainerReady {
		dur := containerReadyCond.LastTransitionTime.Sub(initCond.LastTransitionTime.Time).Seconds()
		if dur >= 0 {
			LifecycleContainerStartDurationSeconds.Observe(dur, labels)
		}
	}

	// Readiness duration: containers ready → pod ready
	if hasContainerReady {
		dur := readyTime.Sub(containerReadyCond.LastTransitionTime.Time).Seconds()
		if dur >= 0 {
			LifecycleReadinessDurationSeconds.Observe(dur, labels)
		}
	}

	// NodeClaim-based phases (provisioning sub-phases)
	// NOTE: This lookup fires at most once per pod due to the recordedStartup guard above,
	// so the extra API call is acceptable and does not create per-reconcile overhead.
	if pod.Spec.NodeName == "" {
		return
	}
	node := &corev1.Node{}
	if err := c.kubeClient.Get(ctx, types.NamespacedName{Name: pod.Spec.NodeName}, node); err != nil {
		return
	}
	nodeClaim, err := nodeutils.NodeClaimForNode(ctx, c.kubeClient, node)
	if err != nil {
		return
	}

	launched := nodeClaim.StatusConditions().Get(v1.ConditionTypeLaunched)
	registered := nodeClaim.StatusConditions().Get(v1.ConditionTypeRegistered)
	initialized := nodeClaim.StatusConditions().Get(v1.ConditionTypeInitialized)

	ncCreation := nodeClaim.CreationTimestamp.Time

	// Instance launch: NodeClaim creation → Launched
	if launched != nil && launched.IsTrue() {
		LifecycleInstanceLaunchDurationSeconds.Observe(launched.LastTransitionTime.Sub(ncCreation).Seconds(), labels)
	}

	// Node registration: Launched → Registered
	if launched != nil && launched.IsTrue() && registered != nil && registered.IsTrue() {
		LifecycleNodeRegistrationDurationSeconds.Observe(registered.LastTransitionTime.Sub(launched.LastTransitionTime.Time).Seconds(), labels)
	}

	// Total node provisioning: NodeClaim creation → Initialized
	if initialized != nil && initialized.IsTrue() {
		LifecycleNodeProvisioningDurationSeconds.Observe(initialized.LastTransitionTime.Sub(ncCreation).Seconds(), labels)
	}

	// Image pull estimate: Scheduled → Initialized (approximation)
	// After a pod is scheduled to a node, the kubelet pulls images before init containers run.
	// We approximate image pull as the gap between pod scheduled and pod initialized.
	if hasScheduled && hasInit {
		dur := initCond.LastTransitionTime.Sub(scheduledCond.LastTransitionTime.Time).Seconds()
		// Only record if positive and we haven't already attributed this to init containers.
		// For pods with no init containers, PodInitialized is set immediately, so this captures image pull.
		// For pods with init containers, this overlaps with init container time — the image pull
		// component is not separable from pod conditions alone. We skip recording in that case
		// to avoid double-counting.
		if dur >= 0 && len(pod.Spec.InitContainers) == 0 {
			LifecycleImagePullDurationSeconds.Observe(dur, labels)
		}
	}
}

func (c *Controller) recordShutdownMetrics(ctx context.Context, pod *corev1.Pod, key string, labels prometheus.Labels) {
	if _, found := c.recordedShutdown.Get(key); found {
		return
	}
	if pod.DeletionTimestamp == nil {
		return
	}
	// Only record when pod is terminal (Succeeded or Failed)
	if pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
		return
	}
	c.recordedShutdown.SetDefault(key, true)

	deletionTime := pod.DeletionTimestamp.Time

	// Find the latest container finish time
	var latestFinish time.Time
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Terminated != nil && cs.State.Terminated.FinishedAt.After(latestFinish) {
			latestFinish = cs.State.Terminated.FinishedAt.Time
		}
	}

	// Container stop: deletion → last container finished
	if !latestFinish.IsZero() {
		dur := latestFinish.Sub(deletionTime).Seconds()
		if dur >= 0 {
			LifecycleContainerStopDurationSeconds.Observe(dur, labels)
		}
	}

	// Graceful termination: deletion → pod terminal
	// Use the latest condition transition as proxy for terminal time
	var terminalTime time.Time
	for _, cond := range pod.Status.Conditions {
		if cond.LastTransitionTime.After(terminalTime) {
			terminalTime = cond.LastTransitionTime.Time
		}
	}
	if !terminalTime.IsZero() {
		dur := terminalTime.Sub(deletionTime).Seconds()
		if dur >= 0 {
			LifecycleGracefulTerminationDurationSeconds.Observe(dur, labels)
		}
	}

	// Total shutdown: deletion → last container finished (or latest condition transition)
	shutdownEnd := latestFinish
	if terminalTime.After(shutdownEnd) {
		shutdownEnd = terminalTime
	}
	if !shutdownEnd.IsZero() {
		dur := shutdownEnd.Sub(deletionTime).Seconds()
		if dur >= 0 {
			LifecycleShutdownTotalDurationSeconds.Observe(dur, labels)
		}
	}

	// Volume detach duration from NodeClaim conditions
	if pod.Spec.NodeName != "" {
		node := &corev1.Node{}
		if err := c.kubeClient.Get(ctx, types.NamespacedName{Name: pod.Spec.NodeName}, node); err == nil {
			if nodeClaim, err := nodeutils.NodeClaimForNode(ctx, c.kubeClient, node); err == nil {
				drained := nodeClaim.StatusConditions().Get(v1.ConditionTypeDrained)
				volumesDetached := nodeClaim.StatusConditions().Get(v1.ConditionTypeVolumesDetached)
				if drained != nil && drained.IsTrue() && volumesDetached != nil && volumesDetached.IsTrue() {
					dur := volumesDetached.LastTransitionTime.Sub(drained.LastTransitionTime.Time).Seconds()
					if dur >= 0 {
						LifecycleVolumeDetachDurationSeconds.Observe(dur, labels)
					}
				}
			}
		}
	}
}

// labelsForPod extracts namespace and deployment name from the pod.
// For pods owned by a ReplicaSet, it looks up the ReplicaSet's OwnerReferences
// to find the actual Deployment name rather than string-splitting, which breaks
// for deployment names containing hyphens.
func (c *Controller) labelsForPod(ctx context.Context, pod *corev1.Pod) prometheus.Labels {
	deployment := ""
	for _, ref := range pod.OwnerReferences {
		if ref.Kind == "ReplicaSet" {
			rs := &appsv1.ReplicaSet{}
			if err := c.kubeClient.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: pod.Namespace}, rs); err == nil {
				for _, rsRef := range rs.OwnerReferences {
					if rsRef.Kind == "Deployment" {
						deployment = rsRef.Name
						break
					}
				}
			}
			break
		}
	}
	return prometheus.Labels{
		namespaceLabel:  pod.Namespace,
		deploymentLabel: deployment,
	}
}

func (c *Controller) Name() string {
	return "metrics.pod.lifecycle"
}

func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named(c.Name()).
		For(&corev1.Pod{}).
		Complete(c)
}
