# Karpenter Scenario Driver

The Karpenter Scenario Driver is a tool for running simulated Kubernetes scenarios to test Karpenter's behavior in various situations. It allows you to define and run scenarios that create and scale deployments, observe Karpenter's reactions, and collect audit logs for analysis.

## Overview

The scenario driver reads configuration from YAML files that define:
1. Kubernetes cluster configuration
2. Deployments to create
3. A sequence of steps to execute, such as scaling deployments
4. Timing and execution parameters

After running a scenario, the driver collects audit logs (including node events) and can upload them to S3 for further analysis.

## Directory Structure

```
hack/e2e_driver/
├── cmd/                      # Command-line interface
│   └── run_scenario/         # Main entry point
├── pkg/                      # Package code
│   ├── audit/                # Audit log collection
│   ├── config/               # Configuration parsing
│   ├── deployment/           # Deployment management
│   ├── driver/               # Scenario execution
│   └── s3/                   # S3 upload functionality
└── kubernetes_scenario_1753908801/  # Example scenario
    ├── config.yml           # Scenario configuration
    ├── steps.yml            # Steps to execute
    ├── deployments/         # Kubernetes deployment manifests
    └── nodepools/           # Nodepool configuration
```

## Building and Running

### Building the Driver

The scenario driver can be built using the Makefile:

```bash
# Using the main Makefile
make scenario-driver-build

# Or directly from the e2e_driver directory
cd hack/e2e_driver && make build
```

This will create a binary at `bin/scenario-driver`.

### Running a Scenario

You can run a scenario using the wrapper script:

```bash
# Using the main Makefile
make scenario-driver-run

# Or using the wrapper script directly
./hack/scenario-driver.sh
```

### Environment Variables

The following environment variables control the scenario driver:

| Variable      | Default                                 | Description                               |
|---------------|----------------------------------------|-------------------------------------------|
| SCENARIO      | hack/e2e_driver/kubernetes_scenario_1753908801 | Path to scenario directory               |
| NAMESPACE     | karpenter-test                         | Kubernetes namespace                      |
| LOG_DIR       | ./scenario-logs                        | Directory to store logs                   |
| KUBECONFIG    | $HOME/.kube/config                     | Path to kubeconfig file                   |
| S3_BUCKET     | (none)                                 | S3 bucket for log upload (optional)       |
| S3_REGION     | us-west-2                              | AWS region for S3                         |

If S3_BUCKET is not set, logs will only be stored locally.

### GitHub Actions Integration

To run the scenario driver in GitHub Actions, you can use the following workflow step:

```yaml
- name: Run Scenario Driver
  env:
    SCENARIO: hack/e2e_driver/kubernetes_scenario_1753908801
    NAMESPACE: karpenter-test
    LOG_DIR: ./scenario-logs
    S3_BUCKET: ${{ secrets.S3_BUCKET }}
    S3_REGION: us-west-2
    AWS_ACCESS_KEY_ID: ${{ secrets.AWS_ACCESS_KEY_ID }}
    AWS_SECRET_ACCESS_KEY: ${{ secrets.AWS_SECRET_ACCESS_KEY }}
  run: |
    ./hack/scenario-driver.sh
```

## Creating New Scenarios

A scenario consists of the following components:

1. A `config.yml` file that defines the cluster configuration and deployments
2. A `steps.yml` file that defines the sequence of steps to execute
3. Kubernetes manifests in a `deployments` directory (for Kubernetes-style scenarios)

### Example Scenario Structure

```yaml
# config.yml
simulator:
  run_id: my-scenario
  run_description: Test scenario for Karpenter
  timestep: 60
  start_step: 1
  limit: 10
  clusters:
    - KubernetesCluster:
        type: KubernetesCluster
        name: default
        # ...more cluster configuration...
  deployments_directory: deployments
  deployments:
    - my-deployment-1
    - my-deployment-2
```

```yaml
# steps.yml
scenario:
  - step:
      name: 1
      actions:
        - action:
            comment: Scale deployment to 5 replicas
            action_type: K8S_SCALE
            action_data: name=my-deployment-1,replicas=5
  # ...more steps...
```

## Analyzing Results

After a scenario runs, logs are stored in the specified LOG_DIR (default: ./scenario-logs). These logs include:

1. Kubernetes API server audit logs
2. Node events
3. Pod information
4. Deployment status

If S3 upload is configured, the logs are also uploaded to the specified S3 bucket.

## Troubleshooting

If you encounter issues:

1. Check that your Kubernetes cluster is running and accessible
2. Verify that the scenario directory exists and contains valid configuration
3. Check the scenario driver logs for errors
4. Examine the collected audit logs for any issues with deployments or scaling operations

## License

This project is licensed under the Apache License 2.0 - see the LICENSE file for details.
