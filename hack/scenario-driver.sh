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

# Build the driver if it doesn't exist
if [[ ! -f "./bin/scenario-driver" ]]; then
  echo "Building scenario driver..."
  cd hack/e2e_driver && make build && cd ../../
fi

# Create log directory if it doesn't exist
mkdir -p "$LOG_DIR"

# Run the scenario driver
echo "Running scenario: $SCENARIO"
./bin/scenario-driver \
  -scenario "$SCENARIO" \
  -namespace "$NAMESPACE" \
  -log-dir "$LOG_DIR" \
  -kubeconfig "$KUBECONFIG" \
  ${S3_BUCKET:+-s3-bucket "$S3_BUCKET"} \
  ${S3_REGION:+-s3-region "$S3_REGION"}

# Display results
echo "Scenario completed!"
echo "Log files saved to: $LOG_DIR"
ls -la "$LOG_DIR"
