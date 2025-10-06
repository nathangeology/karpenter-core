package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/karpenter/hack/e2e_driver/pkg/driver"
)

func main() {
	// Parse command-line arguments
	scenarioDir := flag.String("scenario", "", "Path to scenario directory containing config.yml and steps.yml")
	namespace := flag.String("namespace", "default", "Kubernetes namespace to deploy workloads")
	auditLogDir := flag.String("log-dir", "./logs", "Directory to store audit logs")
	s3Bucket := flag.String("s3-bucket", "", "S3 bucket to upload logs (optional)")
	s3Region := flag.String("s3-region", "us-west-2", "AWS region for S3 bucket")
	logResults := flag.Bool("log-results", true, "Whether to log execution results")
	kubeconfigPath := flag.String("kubeconfig", "", "Path to the kubeconfig file (defaults to ~/.kube/config if empty)")

	flag.Parse()

	// Validate required arguments
	if *scenarioDir == "" {
		log.Fatal("Scenario directory is required. Use -scenario flag to specify.")
	}

	// Create absolute path for the scenario directory
	absScenarioDir, err := filepath.Abs(*scenarioDir)
	if err != nil {
		log.Fatalf("Failed to resolve absolute path for scenario directory: %v", err)
	}

	// Verify scenario directory exists
	if _, err := os.Stat(absScenarioDir); os.IsNotExist(err) {
		log.Fatalf("Scenario directory does not exist: %s", absScenarioDir)
	}

	// Verify config.yml exists
	configPath := filepath.Join(absScenarioDir, "config.yml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		log.Fatalf("Config file not found: %s", configPath)
	}

	// Verify steps.yml exists
	stepsPath := filepath.Join(absScenarioDir, "steps.yml")
	if _, err := os.Stat(stepsPath); os.IsNotExist(err) {
		log.Fatalf("Steps file not found: %s", stepsPath)
	}

	// Create absolute path for the log directory
	absLogDir, err := filepath.Abs(*auditLogDir)
	if err != nil {
		log.Fatalf("Failed to resolve absolute path for log directory: %v", err)
	}

	// Create log directory if it doesn't exist
	if err := os.MkdirAll(absLogDir, 0755); err != nil {
		log.Fatalf("Failed to create log directory: %v", err)
	}

	// Configure and run the scenario driver
	driverCfg := driver.DriverConfig{
		ScenarioDir:    absScenarioDir,
		Namespace:      *namespace,
		AuditLogDir:    absLogDir,
		S3BucketName:   *s3Bucket,
		S3Region:       *s3Region,
		LogResults:     *logResults,
		KubeconfigPath: *kubeconfigPath,
	}

	// Create and run the driver
	drv, err := driver.NewDriver(driverCfg)
	if err != nil {
		log.Fatalf("Failed to create driver: %v", err)
	}

	// Run the scenario
	ctx := context.Background()
	if err := drv.Run(ctx); err != nil {
		// Format the error for better readability
		errorMsg := fmt.Sprintf("Failed to run scenario: %v", err)

		// Log the error with line breaks to make it more readable in logs
		errorLines := strings.Split(errorMsg, "\n")
		for _, line := range errorLines {
			log.Println(line)
		}

		// Exit with error
		os.Exit(1)
	}

	log.Println("Scenario completed successfully!")
}
