package config

import (
	"fmt"
	"io/ioutil"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadSimulatorConfig loads and parses a scenario config.yml file
func LoadSimulatorConfig(configPath string) (*SimulatorConfig, error) {
	data, err := ioutil.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config SimulatorConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	return &config, nil
}

// LoadScenarioSteps loads and parses a scenario steps.yml file
func LoadScenarioSteps(stepsPath string) (*ScenarioConfig, error) {
	data, err := ioutil.ReadFile(stepsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read steps file: %w", err)
	}

	var steps ScenarioConfig
	if err := yaml.Unmarshal(data, &steps); err != nil {
		return nil, fmt.Errorf("failed to parse steps file: %w", err)
	}

	return &steps, nil
}

// LoadScenario loads both the config.yml and steps.yml files from a scenario directory
func LoadScenario(scenarioDir string) (*SimulatorConfig, *ScenarioConfig, error) {
	configPath := filepath.Join(scenarioDir, "config.yml")
	stepsPath := filepath.Join(scenarioDir, "steps.yml")

	config, err := LoadSimulatorConfig(configPath)
	if err != nil {
		return nil, nil, err
	}

	steps, err := LoadScenarioSteps(stepsPath)
	if err != nil {
		return nil, nil, err
	}

	return config, steps, nil
}

// IsKubernetesScenario determines if this is a Kubernetes-style scenario
func IsKubernetesScenario(config *SimulatorConfig) bool {
	// Check if we have Kubernetes specific fields
	hasDeploymentsDir := config.Simulator.DeploymentsDirectory != ""
	hasDeployments := len(config.Simulator.Deployments) > 0

	// Check if any cluster is a KubernetesCluster type
	hasK8sCluster := false
	for _, cluster := range config.Simulator.Clusters {
		if cluster.KubernetesCluster != nil {
			hasK8sCluster = true
			break
		}
	}

	return hasDeploymentsDir && hasDeployments && hasK8sCluster
}

// GetClusterByType returns a cluster by its type (EcsCluster or KubernetesCluster)
func GetClusterByType(config *SimulatorConfig, clusterType string) (interface{}, error) {
	for _, cluster := range config.Simulator.Clusters {
		if cluster.EcsCluster != nil && cluster.EcsCluster.Type == clusterType {
			return cluster.EcsCluster, nil
		}
		if cluster.KubernetesCluster != nil && cluster.KubernetesCluster.Type == clusterType {
			return cluster.KubernetesCluster, nil
		}
	}
	return nil, fmt.Errorf("cluster with type %s not found", clusterType)
}

// ParseScaleAction parses the action data string from a SCALE action
// Format: "name=A1,desiredCount=50"
func ParseScaleAction(actionData string) (name string, count int, err error) {
	var nameFound bool

	// Basic parsing for name=value pairs separated by commas
	parts := make(map[string]string)
	for _, part := range strings.Split(actionData, ",") {
		kv := strings.Split(part, "=")
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		value := strings.TrimSpace(kv[1])
		parts[key] = value
	}

	// Extract required fields
	if name, nameFound = parts["name"]; !nameFound {
		return "", 0, fmt.Errorf("missing 'name' in action data")
	}

	if countStr, countFound := parts["desiredCount"]; countFound {
		if count, err = strconv.Atoi(countStr); err != nil {
			return "", 0, fmt.Errorf("invalid 'desiredCount' value: %w", err)
		}
	} else if replicasStr, replicasFound := parts["replicas"]; replicasFound {
		// K8S_SCALE format uses "replicas" instead of "desiredCount"
		if count, err = strconv.Atoi(replicasStr); err != nil {
			return "", 0, fmt.Errorf("invalid 'replicas' value: %w", err)
		}
	} else {
		return "", 0, fmt.Errorf("missing 'desiredCount' or 'replicas' in action data")
	}

	return name, count, nil
}

// LoadKubernetesManifest loads a Kubernetes YAML manifest file
func LoadKubernetesManifest(filePath string) ([]byte, error) {
	data, err := ioutil.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest file: %w", err)
	}
	return data, nil
}

// LoadAllKubernetesManifests loads all deployment YAML manifests from a directory
func LoadAllKubernetesManifests(scenarioDir string, deploymentsDir string, deploymentNames []string) (map[string][]byte, error) {
	result := make(map[string][]byte)

	// Build the full path to the deployments directory
	deploymentsDirPath := deploymentsDir
	if !filepath.IsAbs(deploymentsDir) {
		deploymentsDirPath = filepath.Join(scenarioDir, deploymentsDir)
	}

	// Load each named deployment
	for _, name := range deploymentNames {
		deploymentPath := filepath.Join(deploymentsDirPath, name+".yaml")
		data, err := LoadKubernetesManifest(deploymentPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load manifest for deployment %s: %w", name, err)
		}
		result[name] = data
	}

	return result, nil
}
