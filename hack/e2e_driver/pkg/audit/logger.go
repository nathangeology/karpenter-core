package audit

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// AuditEvent represents a single Kubernetes audit event
type AuditEvent struct {
	Kind                     string          `json:"kind"`
	APIVersion               string          `json:"apiVersion"`
	Level                    string          `json:"level"`
	AuditID                  string          `json:"auditID"`
	Stage                    string          `json:"stage"`
	RequestURI               string          `json:"requestURI"`
	Verb                     string          `json:"verb"`
	User                     UserInfo        `json:"user"`
	ImpersonatedUser         *UserInfo       `json:"impersonatedUser,omitempty"`
	SourceIPs                []string        `json:"sourceIPs"`
	ObjectRef                ObjectRef       `json:"objectRef,omitempty"`
	ResponseStatus           *ResponseStatus `json:"responseStatus,omitempty"`
	RequestObject            interface{}     `json:"requestObject,omitempty"`
	ResponseObject           interface{}     `json:"responseObject,omitempty"`
	RequestReceivedTimestamp string          `json:"requestReceivedTimestamp"`
	StageTimestamp           string          `json:"stageTimestamp"`
}

// UserInfo represents user information in an audit event
type UserInfo struct {
	Username string   `json:"username"`
	UID      string   `json:"uid,omitempty"`
	Groups   []string `json:"groups,omitempty"`
}

// ObjectRef represents an object reference in an audit event
type ObjectRef struct {
	Resource        string `json:"resource"`
	Namespace       string `json:"namespace,omitempty"`
	Name            string `json:"name,omitempty"`
	UID             string `json:"uid,omitempty"`
	APIGroup        string `json:"apiGroup,omitempty"`
	APIVersion      string `json:"apiVersion,omitempty"`
	ResourceVersion string `json:"resourceVersion,omitempty"`
	Subresource     string `json:"subresource,omitempty"`
}

// ResponseStatus represents the status in an audit event
type ResponseStatus struct {
	Code    int    `json:"code"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// LogCollection represents the full collection of audit data
type LogCollection struct {
	RunID              string                 `json:"run_id"`
	Timestamp          string                 `json:"timestamp"`
	CurrentDeployments interface{}            `json:"current_deployments"`
	CurrentNodes       interface{}            `json:"current_nodes"`
	CurrentPods        interface{}            `json:"current_pods"`
	AuditEvents        []*AuditEvent          `json:"audit_events"`
	ResourceHistory    map[string]interface{} `json:"resource_history"`
}

// Logger handles audit log configuration and collection
type Logger struct {
	client        *kubernetes.Clientset
	auditLogDir   string
	runID         string
	collectedLogs []byte
	auditEvents   []*AuditEvent
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
// Note: With the new kind-config.yaml approach, we don't need to configure the audit policy here
// as it's already done during cluster creation
func (l *Logger) ConfigureAuditPolicy(ctx context.Context) error {
	// With our custom kind configuration, the audit policy is already set up
	// We just return success here
	fmt.Println("Using preconfigured audit policy from kind-config.yaml")
	return nil
}

// fetchKubernetesAuditLogs attempts to retrieve audit logs from the Kind control plane
func (l *Logger) fetchKubernetesAuditLogs(ctx context.Context) ([]byte, error) {
	// Check if we're in a GitHub Actions environment
	if os.Getenv("GITHUB_ACTIONS") != "" {
		fmt.Println("Attempting to fetch audit logs from Kind control plane node")

		// In GitHub Actions, we'll use kubectl cp to get logs from the container
		// First, determine the Kind cluster name
		kindClusterName := os.Getenv("KIND_CLUSTER_NAME")
		if kindClusterName == "" {
			kindClusterName = "chart-testing" // Default name used in the workflow
		}

		containerName := kindClusterName + "-control-plane"
		rawLogsPath := filepath.Join(l.auditLogDir, "raw_audit.log")

		// Create command to copy logs
		cmd := exec.Command("kubectl", "cp",
			fmt.Sprintf("%s:/var/log/kubernetes/audit/audit.log", containerName),
			rawLogsPath,
			"-n", "kube-system")

		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		fmt.Println("Running command:", cmd.String())
		err := cmd.Run()
		if err != nil {
			return nil, fmt.Errorf("failed to copy audit logs: %v, stderr: %s", err, stderr.String())
		}

		// Read the copied file
		return ioutil.ReadFile(rawLogsPath)
	} else {
		// For local development, we might handle this differently
		return nil, fmt.Errorf("audit log fetching only implemented for GitHub Actions environment")
	}
}

// parseAuditLogs parses raw audit logs into structured AuditEvent objects
func (l *Logger) parseAuditLogs(rawLogs []byte) ([]*AuditEvent, error) {
	var events []*AuditEvent

	// The audit log file has one JSON object per line
	scanner := bufio.NewScanner(bytes.NewReader(rawLogs))
	for scanner.Scan() {
		line := scanner.Text()
		if len(strings.TrimSpace(line)) == 0 {
			continue
		}

		var event AuditEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			fmt.Printf("Warning: Could not parse audit log line: %v\n", err)
			continue
		}

		// Add the event to our collection if it's relevant
		if isRelevantEvent(&event) {
			events = append(events, &event)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error scanning audit log: %w", err)
	}

	return events, nil
}

// isRelevantEvent determines if an audit event should be included in our logs
func isRelevantEvent(event *AuditEvent) bool {
	// Check if this is a resource we care about
	if event.ObjectRef.Resource == "" {
		return false
	}

	relevantResources := map[string]bool{
		"pods":        true,
		"nodes":       true,
		"deployments": true,
		"replicasets": true,
		"nodepools":   true,
		"nodeclaims":  true,
		"events":      true,
	}

	// Filter by resource type
	if !relevantResources[event.ObjectRef.Resource] {
		return false
	}

	// Focus on create/update/delete operations
	relevantVerbs := map[string]bool{
		"create": true,
		"update": true,
		"patch":  true,
		"delete": true,
	}

	return relevantVerbs[event.Verb] || event.Verb == "get" && event.Stage == "ResponseComplete"
}

// buildResourceHistory creates a history of resources based on audit events
func (l *Logger) buildResourceHistory(events []*AuditEvent) map[string]interface{} {
	history := make(map[string]interface{})

	// Group by resource type
	podEvents := make(map[string][]*AuditEvent)
	nodeEvents := make(map[string][]*AuditEvent)
	deploymentEvents := make(map[string][]*AuditEvent)

	for _, event := range events {
		key := fmt.Sprintf("%s/%s", event.ObjectRef.Namespace, event.ObjectRef.Name)

		switch event.ObjectRef.Resource {
		case "pods":
			if event.ObjectRef.Name != "" {
				podEvents[key] = append(podEvents[key], event)
			}
		case "nodes":
			if event.ObjectRef.Name != "" {
				nodeEvents[key] = append(nodeEvents[key], event)
			}
		case "deployments":
			if event.ObjectRef.Name != "" {
				deploymentEvents[key] = append(deploymentEvents[key], event)
			}
		}
	}

	// Process pod history
	podHistory := make(map[string]interface{})
	for key, events := range podEvents {
		podHistory[key] = extractResourceHistory(events)
	}
	history["pods"] = podHistory

	// Process node history
	nodeHistory := make(map[string]interface{})
	for key, events := range nodeEvents {
		nodeHistory[key] = extractResourceHistory(events)
	}
	history["nodes"] = nodeHistory

	// Process deployment history
	deploymentHistory := make(map[string]interface{})
	for key, events := range deploymentEvents {
		deploymentHistory[key] = extractResourceHistory(events)
	}
	history["deployments"] = deploymentHistory

	return history
}

// extractResourceHistory extracts the history of a resource from its audit events
func extractResourceHistory(events []*AuditEvent) []map[string]interface{} {
	var history []map[string]interface{}

	// Sort events by timestamp
	// (In a real implementation, we would sort by timestamp here)

	for _, event := range events {
		entry := make(map[string]interface{})
		entry["verb"] = event.Verb
		entry["timestamp"] = event.StageTimestamp

		// Include the object state at this point if available
		if event.ResponseObject != nil {
			entry["object"] = event.ResponseObject
		} else if event.RequestObject != nil {
			entry["object"] = event.RequestObject
		}

		if event.ResponseStatus != nil {
			entry["status"] = event.ResponseStatus.Status
			entry["code"] = event.ResponseStatus.Code
		}

		history = append(history, entry)
	}

	return history
}

// CollectLogs retrieves the audit logs from the cluster
func (l *Logger) CollectLogs(ctx context.Context) error {
	fmt.Println("Collecting audit logs and current cluster state...")

	var auditEvents []*AuditEvent

	// Try to fetch Kubernetes audit logs
	auditLogData, err := l.fetchKubernetesAuditLogs(ctx)
	if err != nil {
		fmt.Printf("Warning: Could not fetch Kubernetes audit logs: %v\n", err)
		fmt.Println("Falling back to current state only")
	} else {
		// Parse the audit logs
		auditEvents, err = l.parseAuditLogs(auditLogData)
		if err != nil {
			fmt.Printf("Warning: Could not parse audit logs: %v\n", err)
		} else {
			fmt.Printf("Successfully parsed %d audit events\n", len(auditEvents))
		}
	}

	// Keep the audit events for later use
	l.auditEvents = auditEvents

	// Collect current state as before
	// Collect deployment data
	deployments, err := l.client.AppsV1().Deployments("").List(ctx, metav1.ListOptions{
		LabelSelector: "managed-by=k8s-sim-driver",
	})
	if err != nil {
		return fmt.Errorf("failed to collect deployment logs: %w", err)
	}

	// Collect node data
	nodes, err := l.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to collect node logs: %w", err)
	}

	// Collect pod data
	pods, err := l.client.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		LabelSelector: "managed-by=k8s-sim-driver",
	})
	if err != nil {
		return fmt.Errorf("failed to collect pod logs: %w", err)
	}

	// Build resource history from audit events
	resourceHistory := l.buildResourceHistory(auditEvents)

	// Create the log collection object
	logCollection := LogCollection{
		RunID:              l.runID,
		Timestamp:          time.Now().UTC().Format(time.RFC3339),
		CurrentDeployments: deployments,
		CurrentNodes:       nodes,
		CurrentPods:        pods,
		AuditEvents:        auditEvents,
		ResourceHistory:    resourceHistory,
	}

	// Marshal to JSON
	l.collectedLogs, err = json.MarshalIndent(logCollection, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal log data: %w", err)
	}

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

	fmt.Printf("Saved comprehensive audit logs to: %s\n", fullPath)

	// If we have audit events, write a summary to stdout
	if len(l.auditEvents) > 0 {
		fmt.Printf("\nAudit Summary:\n")
		fmt.Printf("  - Collected %d total audit events\n", len(l.auditEvents))

		// Count events by resource type
		counts := make(map[string]int)
		for _, event := range l.auditEvents {
			counts[event.ObjectRef.Resource]++
		}

		for resource, count := range counts {
			fmt.Printf("  - %s events: %d\n", resource, count)
		}

		fmt.Println("\nFull audit history is available in the log file.")
	}

	return fullPath, nil
}

// GetLogs returns the collected logs
func (l *Logger) GetLogs() []byte {
	return l.collectedLogs
}
