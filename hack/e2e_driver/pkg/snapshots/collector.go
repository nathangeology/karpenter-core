package snapshots

import (
	"context"
	"fmt"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ClusterSnapshot represents a complete snapshot of the cluster state at a specific time
type ClusterSnapshot struct {
	Timestamp    time.Time              `json:"timestamp"`
	StepName     string                 `json:"step_name,omitempty"`
	StepNumber   int                    `json:"step_number,omitempty"`
	SnapshotType string                 `json:"snapshot_type"` // "periodic" or "step"
	Nodes        *corev1.NodeList       `json:"nodes"`
	Pods         *corev1.PodList        `json:"pods"`
	Deployments  *appsv1.DeploymentList `json:"deployments"`
	ReplicaSets  *appsv1.ReplicaSetList `json:"replicasets"`
	Events       *corev1.EventList      `json:"events"`
}

// SnapshotCollector periodically captures cluster state snapshots
type SnapshotCollector struct {
	client    *kubernetes.Clientset
	namespace string
	interval  time.Duration
	snapshots []ClusterSnapshot
	mutex     sync.RWMutex
	stopCh    chan struct{}
	running   bool
}

// NewSnapshotCollector creates a new snapshot collector
func NewSnapshotCollector(client *kubernetes.Clientset, namespace string, interval time.Duration) *SnapshotCollector {
	return &SnapshotCollector{
		client:    client,
		namespace: namespace,
		interval:  interval,
		snapshots: make([]ClusterSnapshot, 0),
		stopCh:    make(chan struct{}),
	}
}

// Start begins periodic snapshot collection
func (sc *SnapshotCollector) Start(ctx context.Context) {
	sc.mutex.Lock()
	if sc.running {
		sc.mutex.Unlock()
		return
	}
	sc.running = true
	sc.mutex.Unlock()

	// Take initial snapshot
	sc.takeSnapshot(ctx)

	// Start periodic collection
	ticker := time.NewTicker(sc.interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				sc.takeSnapshot(ctx)
			case <-sc.stopCh:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
}

// Stop stops the snapshot collection
func (sc *SnapshotCollector) Stop() {
	sc.mutex.Lock()
	defer sc.mutex.Unlock()

	if !sc.running {
		return
	}

	sc.running = false
	close(sc.stopCh)
}

// TakeStepSnapshot captures a snapshot for a specific scenario step
func (sc *SnapshotCollector) TakeStepSnapshot(ctx context.Context, stepName string, stepNumber int) {
	fmt.Printf("DEBUG: TakeStepSnapshot called for step '%s' (number %d)\n", stepName, stepNumber)
	sc.takeSnapshotWithContext(ctx, stepName, stepNumber, "step")
	fmt.Printf("DEBUG: Step snapshot completed, total snapshots: %d\n", sc.GetSnapshotCount())
}

// takeSnapshot captures a complete cluster state snapshot (periodic)
func (sc *SnapshotCollector) takeSnapshot(ctx context.Context) {
	sc.takeSnapshotWithContext(ctx, "", 0, "periodic")
}

// takeSnapshotWithContext captures a complete cluster state snapshot with context
func (sc *SnapshotCollector) takeSnapshotWithContext(ctx context.Context, stepName string, stepNumber int, snapshotType string) {
	snapshot := ClusterSnapshot{
		Timestamp:    time.Now(),
		StepName:     stepName,
		StepNumber:   stepNumber,
		SnapshotType: snapshotType,
	}

	// Collect all nodes
	nodes, err := sc.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		// Log error but continue with partial snapshot
		nodes = &corev1.NodeList{}
	}
	snapshot.Nodes = nodes

	// Collect all pods (across all namespaces to get complete picture)
	pods, err := sc.client.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		pods = &corev1.PodList{}
	}
	snapshot.Pods = pods

	// Collect deployments (focus on managed ones but include all for context)
	deployments, err := sc.client.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})
	if err != nil {
		deployments = &appsv1.DeploymentList{}
	}
	snapshot.Deployments = deployments

	// Collect replicasets
	replicasets, err := sc.client.AppsV1().ReplicaSets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		replicasets = &appsv1.ReplicaSetList{}
	}
	snapshot.ReplicaSets = replicasets

	// Collect recent events (last 10 minutes)
	events, err := sc.client.CoreV1().Events("").List(ctx, metav1.ListOptions{})
	if err != nil {
		events = &corev1.EventList{}
	}
	// Filter events to recent ones only
	recentEvents := &corev1.EventList{}
	cutoff := time.Now().Add(-10 * time.Minute)
	for _, event := range events.Items {
		if event.LastTimestamp.Time.After(cutoff) || event.FirstTimestamp.Time.After(cutoff) {
			recentEvents.Items = append(recentEvents.Items, event)
		}
	}
	snapshot.Events = recentEvents

	// Store the snapshot
	sc.mutex.Lock()
	sc.snapshots = append(sc.snapshots, snapshot)
	sc.mutex.Unlock()
}

// GetSnapshots returns all collected snapshots
func (sc *SnapshotCollector) GetSnapshots() []ClusterSnapshot {
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()

	// Return a copy to avoid race conditions
	snapshots := make([]ClusterSnapshot, len(sc.snapshots))
	copy(snapshots, sc.snapshots)
	return snapshots
}

// GetSnapshotCount returns the number of snapshots collected
func (sc *SnapshotCollector) GetSnapshotCount() int {
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()
	return len(sc.snapshots)
}

// GetSnapshotSummary returns a summary of the snapshots
func (sc *SnapshotCollector) GetSnapshotSummary() map[string]interface{} {
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()

	if len(sc.snapshots) == 0 {
		return map[string]interface{}{
			"snapshot_count": 0,
			"duration":       "0s",
		}
	}

	first := sc.snapshots[0]
	last := sc.snapshots[len(sc.snapshots)-1]
	duration := last.Timestamp.Sub(first.Timestamp)

	// Calculate resource counts from the latest snapshot
	var nodeCount, podCount, deploymentCount int
	if len(sc.snapshots) > 0 {
		latest := sc.snapshots[len(sc.snapshots)-1]
		nodeCount = len(latest.Nodes.Items)
		podCount = len(latest.Pods.Items)
		deploymentCount = len(latest.Deployments.Items)
	}

	return map[string]interface{}{
		"snapshot_count":          len(sc.snapshots),
		"duration":                duration.String(),
		"interval":                sc.interval.String(),
		"first_snapshot":          first.Timestamp.Format(time.RFC3339),
		"last_snapshot":           last.Timestamp.Format(time.RFC3339),
		"latest_node_count":       nodeCount,
		"latest_pod_count":        podCount,
		"latest_deployment_count": deploymentCount,
	}
}
