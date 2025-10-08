package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"time"

	"sigs.k8s.io/karpenter/hack/e2e_driver/pkg/snapshots"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Logger handles audit log configuration and collection
type Logger struct {
	client        *kubernetes.Clientset
	auditLogDir   string
	runID         string
	collectedLogs []byte
	snapshots     []snapshots.ClusterSnapshot
}

// NewLogger creates a new audit logger
func NewLogger(client *kubernetes.Clientset, auditLogDir string, runID string) *Logger {
	return &Logger{
		client:      client,
		auditLogDir: auditLogDir,
		runID:       runID,
	}
}

// ConfigureAuditPolicy configures the audit policy for the Kubernetes cluster
// Note: In a KIND cluster, this might require modifying the API server configuration
func (l *Logger) ConfigureAuditPolicy(ctx context.Context) error {
	// Create an audit policy that captures deployment, node, and pod events
	auditPolicy := `
apiVersion: audit.k8s.io/v1
kind: Policy
rules:
# Log deployment-related operations at the RequestResponse level
- level: RequestResponse
  resources:
  - group: "apps"
    resources: ["deployments", "replicasets"]
  verbs: ["create", "update", "patch", "delete", "scale"]

# Log pod operations at the Metadata level
- level: Metadata
  resources:
  - group: ""
    resources: ["pods"]

# Log node operations at the RequestResponse level
- level: RequestResponse
  resources:
  - group: ""
    resources: ["nodes"]
  verbs: ["create", "update", "patch", "delete"]

# Log node status changes
- level: Metadata
  resources:
  - group: ""
    resources: ["nodes/status"]

# The default fallback rule
- level: Metadata
  omitStages:
  - "RequestReceived"
`
	// Save the policy to a ConfigMap
	_, err := l.client.CoreV1().ConfigMaps("kube-system").Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "k8s-sim-audit-policy",
		},
		Data: map[string]string{
			"policy.yaml": auditPolicy,
		},
	}, metav1.CreateOptions{})

	if err != nil {
		// If ConfigMap already exists, try to update it
		if err.Error() != "configmaps \"k8s-sim-audit-policy\" already exists" {
			return fmt.Errorf("failed to create audit policy ConfigMap: %w", err)
		}

		// Get existing ConfigMap
		cm, err := l.client.CoreV1().ConfigMaps("kube-system").Get(ctx, "k8s-sim-audit-policy", metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get existing audit policy ConfigMap: %w", err)
		}

		// Update ConfigMap
		cm.Data["policy.yaml"] = auditPolicy
		_, err = l.client.CoreV1().ConfigMaps("kube-system").Update(ctx, cm, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update audit policy ConfigMap: %w", err)
		}
	}

	// Note: For a real implementation, you would also need to modify the API server arguments
	// to include the audit policy and log path. In a KIND cluster, this would typically be done
	// during cluster creation or by modifying the kind-config.yaml before cluster creation.
	//
	// For this simulation, we assume the KIND cluster is already configured with appropriate
	// audit logging settings or would be reconfigured out-of-band.

	return nil
}

// CollectLogs retrieves the audit logs from the cluster
func (l *Logger) CollectLogs(ctx context.Context) error {
	// Create a log collection structure that includes the snapshots
	type LogCollection struct {
		RunID     string                      `json:"run_id"`
		Timestamp string                      `json:"timestamp"`
		Snapshots []snapshots.ClusterSnapshot `json:"cluster_snapshots,omitempty"`
	}

	fmt.Printf("DEBUG: CollectLogs called with %d snapshots in logger\n", len(l.snapshots))

	// Create the log collection object with snapshots
	logCollection := LogCollection{
		RunID:     l.runID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Snapshots: l.snapshots,
	}

	fmt.Printf("DEBUG: LogCollection created with %d snapshots\n", len(logCollection.Snapshots))

	// Marshal to JSON
	var err error
	l.collectedLogs, err = json.MarshalIndent(logCollection, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal log data: %w", err)
	}

	fmt.Printf("DEBUG: JSON marshaled successfully, size: %d bytes\n", len(l.collectedLogs))

	return nil
}

// SaveLogs saves the collected logs to a file
func (l *Logger) SaveLogs(ctx context.Context) (string, error) {
	if l.collectedLogs == nil {
		return "", fmt.Errorf("no logs collected yet, call CollectLogs first")
	}

	// Create the directory if it doesn't exist
	if err := os.MkdirAll(l.auditLogDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create audit log directory: %w", err)
	}

	// Create the log filename with timestamp
	timestamp := time.Now().UTC().Format("20060102-150405")
	filename := fmt.Sprintf("audit-log-%s-%s.json", l.runID, timestamp)
	fullPath := filepath.Join(l.auditLogDir, filename)

	// Write the logs to file
	if err := ioutil.WriteFile(fullPath, l.collectedLogs, 0644); err != nil {
		return "", fmt.Errorf("failed to write audit log file: %w", err)
	}

	return fullPath, nil
}

// AddSnapshots adds cluster snapshots to the audit logs
func (l *Logger) AddSnapshots(clusterSnapshots []snapshots.ClusterSnapshot) {
	l.snapshots = clusterSnapshots
	fmt.Printf("DEBUG: AddSnapshots called with %d snapshots\n", len(clusterSnapshots))
	for i, snapshot := range clusterSnapshots {
		fmt.Printf("DEBUG: Snapshot %d - Type: %s, Step: %s, Nodes: %d, Pods: %d\n",
			i, snapshot.SnapshotType, snapshot.StepName,
			len(snapshot.Nodes.Items), len(snapshot.Pods.Items))
	}
}

// GetLogs returns the collected logs
func (l *Logger) GetLogs() []byte {
	return l.collectedLogs
}
