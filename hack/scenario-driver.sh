#!/bin/bash
# Scenario driver wrapper script

set -euo pipefail

# Default values
SCENARIO="${SCENARIO:-hack/e2e_driver/kubernetes_scenario_1753908801}"
NAMESPACE="${NAMESPACE:-karpenter-test}"
LOG_DIR="${LOG_DIR:-./scenario-logs}"
KUBECONFIG="${KUBECONFIG:-$HOME/.kube/config}"
S3_BUCKET="${S3_BUCKET:-}"
S3_REGION="${S3_REGION:-us-west-2}"

# Display configuration
echo "Scenario Driver Configuration:"
echo "============================="
echo "SCENARIO:   $SCENARIO"
echo "NAMESPACE:  $NAMESPACE"
echo "LOG_DIR:    $LOG_DIR"
echo "KUBECONFIG: $KUBECONFIG"
echo "S3_BUCKET:  ${S3_BUCKET:-<not set>}"
echo "S3_REGION:  $S3_REGION"
echo "============================="

# Ensure the scenario directory exists
if [[ ! -d "$SCENARIO" ]]; then
  echo "Error: Scenario directory does not exist: $SCENARIO"
  exit 1
fi

# Create bin directory if it doesn't exist
mkdir -p ./bin

# Always rebuild in GitHub Actions environment, or build if doesn't exist
if [[ -n "${GITHUB_ACTIONS:-}" ]] || [[ ! -f "./bin/scenario-driver" ]]; then
  echo "Building scenario driver..."
  cd hack/e2e_driver && make build && cd ../../
fi

# Create log directory if it doesn't exist
mkdir -p "$LOG_DIR"

# Run the scenario driver
echo "Running scenario: $SCENARIO"

# Run the scenario driver but capture its exit status
./bin/scenario-driver \
  -scenario "$SCENARIO" \
  -namespace "$NAMESPACE" \
  -log-dir "$LOG_DIR" \
  -kubeconfig "$KUBECONFIG" \
  ${S3_BUCKET:+-s3-bucket "$S3_BUCKET"} \
  ${S3_REGION:+-s3-region "$S3_REGION"}

SCENARIO_EXIT_CODE=$?

# Check if the scenario driver completed successfully
if [ $SCENARIO_EXIT_CODE -eq 0 ]; then
  # Display results
  echo "Scenario completed successfully!"
  echo "Log files saved to: $LOG_DIR"
  ls -la "$LOG_DIR"
else
  echo -e "\n============ SCENARIO DRIVER FAILED ============"
  echo "Exit code: $SCENARIO_EXIT_CODE"
  
  # Collect diagnostic information
  echo -e "\n============ DIAGNOSTIC INFORMATION ============"
  echo -e "\n=== Namespace Information ==="
  kubectl get namespaces "$NAMESPACE" -o wide || echo "Failed to get namespace information"
  
  echo -e "\n=== Nodes ==="
  kubectl get nodes -o wide || echo "Failed to get node information"
  
  echo -e "\n=== Pods in $NAMESPACE ==="
  kubectl get pods -n "$NAMESPACE" -o wide || echo "Failed to get pod information"
  
  echo -e "\n=== Pod Details ==="
  POD_LIST=$(kubectl get pods -n "$NAMESPACE" -o name 2>/dev/null)
  if [ -n "$POD_LIST" ]; then
    echo "$POD_LIST" | while read -r pod; do
      echo -e "\n--- $pod ---"
      kubectl describe "$pod" -n "$NAMESPACE" || echo "Failed to describe $pod"
      
      # Check if the pod is running before trying to get logs
      POD_STATUS=$(kubectl get "$pod" -n "$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null)
      if [ "$POD_STATUS" == "Running" ] || [ "$POD_STATUS" == "Succeeded" ]; then
        echo -e "\n--- $pod logs ---"
        kubectl logs "$pod" -n "$NAMESPACE" --tail=50 || echo "Failed to get logs for $pod"
      else
        echo -e "\n--- $pod is not running ($POD_STATUS), skipping logs ---"
      fi
    done
  fi
  
  echo -e "\n=== Deployments in $NAMESPACE ==="
  kubectl get deployments -n "$NAMESPACE" -o wide || echo "Failed to get deployment information"
  
  echo -e "\n=== Deployment Details ==="
  DEPLOYMENT_LIST=$(kubectl get deployments -n "$NAMESPACE" -o name 2>/dev/null)
  if [ -n "$DEPLOYMENT_LIST" ]; then
    echo "$DEPLOYMENT_LIST" | while read -r deployment; do
      echo -e "\n--- $deployment ---"
      kubectl describe "$deployment" -n "$NAMESPACE" || echo "Failed to describe $deployment"
    done
  fi
  
  echo -e "\n=== Recent Events in $NAMESPACE ==="
  kubectl get events -n "$NAMESPACE" --sort-by=.metadata.creationTimestamp | tail -n 20 || echo "Failed to get events"
  
  echo -e "\n=== General Cluster Health ==="
  kubectl get componentstatuses || echo "Failed to get component statuses"
  
  echo -e "\n============ END DIAGNOSTIC INFORMATION ============"
  echo "Log files saved to: $LOG_DIR"
  ls -la "$LOG_DIR"
  
  # Exit with the same error code
  exit $SCENARIO_EXIT_CODE
fi
