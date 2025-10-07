package driver

import (
	"context"
	"fmt"
	"time"

	"sigs.k8s.io/karpenter/hack/e2e_driver/pkg/audit"
	"sigs.k8s.io/karpenter/hack/e2e_driver/pkg/config"
	"sigs.k8s.io/karpenter/hack/e2e_driver/pkg/deployment"
	"sigs.k8s.io/karpenter/hack/e2e_driver/pkg/s3"
	"sigs.k8s.io/karpenter/hack/e2e_driver/pkg/tracking"
)

// Driver orchestrates the scenario execution
type Driver struct {
	config        *config.SimulatorConfig
	steps         *config.ScenarioConfig
	deploymentMgr *deployment.Manager
	auditLogger   *audit.Logger
	tracker       *tracking.ResourceTracker
	s3Uploader    *s3.Uploader
	timestep      time.Duration
	auditLogDir   string
	s3BucketName  string
	s3Region      string
	scenarioDir   string // Path to the scenario directory
	logResults    bool
	stepsExecuted int
	startTime     time.Time
}

// DriverConfig holds the configuration for the driver
type DriverConfig struct {
	ScenarioDir    string
	Namespace      string
	AuditLogDir    string
	S3BucketName   string
	S3Region       string
	LogResults     bool
	KubeconfigPath string // Path to the kubeconfig file
}

// NewDriver creates a new scenario driver
func NewDriver(cfg DriverConfig) (*Driver, error) {
	// Load scenario configuration
	simConfig, steps, err := config.LoadScenario(cfg.ScenarioDir)
	if err != nil {
		return nil, fmt.Errorf("failed to load scenario: %w", err)
	}

	// Create resource tracker
	tracker := tracking.NewResourceTracker()

	// Create deployment manager
	deploymentMgr, err := deployment.NewManager(cfg.Namespace, cfg.KubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create deployment manager: %w", err)
	}

	// Set the tracker in the deployment manager
	deploymentMgr.SetTracker(tracker)

	// Create audit logger
	auditLogger := audit.NewLogger(deploymentMgr.GetClientset(), cfg.AuditLogDir, simConfig.Simulator.RunID)

	return &Driver{
		config:        simConfig,
		steps:         steps,
		deploymentMgr: deploymentMgr,
		auditLogger:   auditLogger,
		tracker:       tracker,
		timestep:      time.Duration(simConfig.Simulator.Timestep) * time.Second,
		auditLogDir:   cfg.AuditLogDir,
		s3BucketName:  cfg.S3BucketName,
		s3Region:      cfg.S3Region,
		scenarioDir:   cfg.ScenarioDir,
		logResults:    cfg.LogResults,
	}, nil
}

// Run executes the scenario
func (d *Driver) Run(ctx context.Context) error {
	d.startTime = time.Now()
	fmt.Printf("Starting scenario: %s\n", d.config.Simulator.RunID)

	// Configure audit logging
	if err := d.auditLogger.ConfigureAuditPolicy(ctx); err != nil {
		return fmt.Errorf("failed to configure audit policy: %w", err)
	}

	// Check if this is a Kubernetes-style scenario
	if config.IsKubernetesScenario(d.config) {
		fmt.Println("Detected Kubernetes-style scenario")

		// Load Kubernetes manifests
		if err := d.deploymentMgr.LoadKubernetesManifests(
			d.scenarioDir,
			d.config.Simulator.DeploymentsDirectory,
			d.config.Simulator.Deployments,
		); err != nil {
			return fmt.Errorf("failed to load Kubernetes manifests: %w", err)
		}

		// Apply the manifests to the cluster
		if err := d.deploymentMgr.ApplyKubernetesManifests(ctx); err != nil {
			return fmt.Errorf("failed to apply Kubernetes manifests: %w", err)
		}
	} else {
		// Legacy ECS-style scenario
		fmt.Println("Using legacy ECS-style scenario format")
		// Create deployments for each workload
		for _, workload := range d.config.Simulator.Workloads {
			fmt.Printf("Creating deployment for workload: %s\n", workload.ServiceOwnedWorkload.Name)
			if err := d.deploymentMgr.CreateDeployment(ctx, workload); err != nil {
				return fmt.Errorf("failed to create deployment: %w", err)
			}
		}
	}

	// Wait for deployments to be stable
	fmt.Println("Waiting for initial deployments to stabilize...")
	if err := d.waitForStableDeployments(ctx); err != nil {
		return err
	}

	// Execute scenario steps
	fmt.Println("Starting scenario step execution...")
	startStep := d.config.Simulator.StartStep
	endStep := startStep + d.config.Simulator.Limit - 1

	for i := startStep; i <= endStep; i++ {
		stepIndex := (i - 1) % len(d.steps.Scenario)
		step := d.steps.Scenario[stepIndex]

		if err := d.executeStep(ctx, step); err != nil {
			return fmt.Errorf("failed to execute step %s: %w", step.Step.Name, err)
		}

		d.stepsExecuted++
		fmt.Printf("Completed step %s (%d/%d)\n", step.Step.Name, d.stepsExecuted, d.config.Simulator.Limit)

		// Wait for the timestep duration before the next step
		time.Sleep(d.timestep)
	}

	// Collect and upload logs
	return d.collectAndUploadLogs(ctx)
}

// executeStep executes a single scenario step
func (d *Driver) executeStep(ctx context.Context, step config.ScenarioStep) error {
	fmt.Printf("Executing step %s with %d actions\n", step.Step.Name, len(step.Step.Actions))

	for _, action := range step.Step.Actions {
		actionType := action.Action.ActionType
		actionData := action.Action.ActionData
		comment := action.Action.Comment

		fmt.Printf("  Action: %s - %s\n", actionType, comment)

		// Handle different action types
		switch actionType {
		case "SCALE", "K8S_SCALE":
			// Both SCALE and K8S_SCALE use the same handler with a slightly different format
			// ParseScaleAction now handles both formats (desiredCount or replicas)
			name, count, err := config.ParseScaleAction(actionData)
			if err != nil {
				return fmt.Errorf("invalid scale action data: %w", err)
			}

			fmt.Printf("  Scaling deployment %s to %d replicas\n", name, count)
			if err := d.deploymentMgr.ScaleDeployment(ctx, name, count); err != nil {
				return err
			}

		// Additional action types can be added here
		default:
			fmt.Printf("  Unsupported action type: %s\n", actionType)
		}
	}

	return nil
}

// waitForStableDeployments waits until all deployments are stable
func (d *Driver) waitForStableDeployments(ctx context.Context) error {
	const checkInterval = 5 * time.Second
	const maxWaitTime = 5 * time.Minute

	deadline := time.Now().Add(maxWaitTime)

	for {
		stable, err := d.deploymentMgr.AreDeploymentsStable(ctx)
		if err != nil {
			return fmt.Errorf("failed to check deployment stability: %w", err)
		}

		if stable {
			fmt.Println("All deployments are stable")
			return nil
		}

		if time.Now().After(deadline) {
			// Get comprehensive diagnostics about the deployments before failing
			diagnostics, diagErr := d.deploymentMgr.GetDeploymentDiagnostics(ctx)
			if diagErr != nil {
				fmt.Printf("WARNING: Failed to get deployment diagnostics: %v\n", diagErr)
			} else {
				// Print diagnostics to the logs
				fmt.Println("\n=== DEPLOYMENT STABILITY TIMEOUT DIAGNOSTICS ===")
				fmt.Println(diagnostics)
				fmt.Println("=== END DIAGNOSTICS ===\n")
			}

			return fmt.Errorf("timed out waiting for deployments to stabilize after %v\n\nDiagnostics:%s",
				maxWaitTime, diagnostics)
		}

		fmt.Println("Deployments not yet stable, waiting...")
		// Print a status update every minute (12 iterations)
		if int(time.Since(d.startTime).Seconds()/checkInterval.Seconds())%12 == 0 {
			fmt.Println("Status update: Still waiting for deployments to stabilize...")
			stable, _ := d.deploymentMgr.AreDeploymentsStable(ctx)
			if !stable {
				fmt.Println("Time remaining before timeout:", time.Until(deadline).Round(time.Second))
			}
		}
		time.Sleep(checkInterval)
	}
}

// collectAndUploadLogs collects the audit logs and uploads them to S3
func (d *Driver) collectAndUploadLogs(ctx context.Context) error {
	fmt.Println("Collecting audit logs...")

	// Collect logs
	if err := d.auditLogger.CollectLogs(ctx); err != nil {
		return fmt.Errorf("failed to collect logs: %w", err)
	}

	// Add tracked resource history to audit logs
	if d.tracker != nil {
		fmt.Printf("Adding resource tracking data to audit logs...\n")
		fmt.Printf("Tracked resources: %d resources, %d events, %d types\n",
			d.tracker.GetResourceCount(),
			d.tracker.GetEventCount(),
			len(d.tracker.GetResourceTypes()))

		// Add the resource history to the audit logger
		d.auditLogger.AddResourceHistory(d.tracker.GetHistory())
	}

	// Save logs locally
	logPath, err := d.auditLogger.SaveLogs(ctx)
	if err != nil {
		return fmt.Errorf("failed to save logs: %w", err)
	}

	fmt.Printf("Logs saved to: %s\n", logPath)

	// Upload logs to S3 if configured
	if d.s3BucketName != "" {
		fmt.Printf("Uploading logs to S3 bucket: %s\n", d.s3BucketName)

		// Create S3 uploader
		uploader, err := s3.NewUploader(d.s3Region, d.s3BucketName)
		if err != nil {
			return fmt.Errorf("failed to create S3 uploader: %w", err)
		}

		// Generate S3 object key
		timestamp := time.Now().UTC().Format("20060102-150405")
		objectKey := fmt.Sprintf("logs/%s/%s.json", d.config.Simulator.RunID, timestamp)

		// Upload logs
		if err := uploader.UploadLogFile(logPath, objectKey); err != nil {
			return fmt.Errorf("failed to upload logs to S3: %w", err)
		}

		fmt.Printf("Logs uploaded to S3: s3://%s/%s\n", d.s3BucketName, objectKey)
	}

	// Display execution summary
	duration := time.Since(d.startTime)
	fmt.Printf("\nScenario execution complete:\n")
	fmt.Printf("Run ID: %s\n", d.config.Simulator.RunID)
	fmt.Printf("Steps executed: %d\n", d.stepsExecuted)
	fmt.Printf("Duration: %s\n", duration.String())

	return nil
}
