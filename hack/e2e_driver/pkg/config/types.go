package config

// SimulatorConfig represents the main configuration for the simulator
type SimulatorConfig struct {
	Simulator struct {
		RunID              string     `yaml:"run_id"`
		RunDescription     string     `yaml:"run_description"`
		AssociatedScenario string     `yaml:"associated_scenario"`
		Timestep           int        `yaml:"timestep"`
		StartStep          int        `yaml:"start_step"`
		Limit              int        `yaml:"limit"`
		InstancePoolSize   int        `yaml:"instance_pool_size"`
		Logging            int        `yaml:"logging"`
		Clusters           []Cluster  `yaml:"clusters"`
		Workloads          []Workload `yaml:"workloads,omitempty"`
		// Kubernetes specific fields
		DeploymentsDirectory string   `yaml:"deployments_directory,omitempty"`
		Deployments          []string `yaml:"deployments,omitempty"`
	} `yaml:"simulator"`
}

// Cluster represents a cluster configuration (ECS or Kubernetes)
type Cluster struct {
	// ECS style cluster
	EcsCluster *EcsClusterConfig `yaml:"EcsCluster,omitempty"`
	// Kubernetes style cluster
	KubernetesCluster *KubernetesClusterConfig `yaml:"KubernetesCluster,omitempty"`
}

// EcsClusterConfig represents an ECS cluster configuration
type EcsClusterConfig struct {
	Type              string             `yaml:"type"`
	Name              string             `yaml:"name"`
	AZCount           int                `yaml:"az_count"`
	CapacityProviders []CapacityProvider `yaml:"capacity_providers"`
}

// KubernetesClusterConfig represents a Kubernetes cluster configuration
type KubernetesClusterConfig struct {
	Type              string `yaml:"type"`
	Name              string `yaml:"name"`
	NodeCount         int    `yaml:"node_count"`
	NodeType          string `yaml:"node_type"`
	Autoscaling       bool   `yaml:"autoscaling"`
	NodepoolDirectory string `yaml:"nodepool_directory"`
}

// CapacityProvider defines a provider for cluster capacity
type CapacityProvider struct {
	Omakase struct {
		Type                   string `yaml:"type"`
		Name                   string `yaml:"name"`
		StartingInstanceType   string `yaml:"starting_instance_type"`
		StartingInstanceCount  int    `yaml:"starting_instance_count"`
		ActivePlacementEnabled bool   `yaml:"active_placement_enabled"`
		Rebalancer             struct {
			Name string `yaml:"name"`
		} `yaml:"rebalancer,omitempty"`
	} `yaml:"Omakase"`
}

// Workload defines a Kubernetes workload to be deployed
type Workload struct {
	ServiceOwnedWorkload struct {
		Type                string         `yaml:"type"`
		PlacementStrategies []string       `yaml:"placement_strategies"`
		CapacityProvider    string         `yaml:"capacity_provider"`
		ClusterName         string         `yaml:"cluster_name"`
		Name                string         `yaml:"name"`
		TaskDefinition      TaskDefinition `yaml:"task_definition"`
		StartingWorkloads   int            `yaml:"starting_workloads"`
		ScaleDownDelay      int            `yaml:"scale_down_delay"`
	} `yaml:"ServiceOwnedWorkload"`
}

// TaskDefinition defines the resource requirements for a task
type TaskDefinition struct {
	Type        string  `yaml:"type"`
	UsageType   string  `yaml:"usage_type"`
	CPU         float64 `yaml:"cpu"`
	Memory      int     `yaml:"memory"`
	CleanupTime float64 `yaml:"cleanup_time"`
}

// ScenarioConfig represents the steps configuration
type ScenarioConfig struct {
	Scenario []ScenarioStep `yaml:"scenario"`
}

// ScenarioStep represents a single step in the scenario
type ScenarioStep struct {
	Step struct {
		Name    string   `yaml:"name"`
		Actions []Action `yaml:"actions"`
	} `yaml:"step"`
}

// Action represents an action to perform during a scenario step
type Action struct {
	Action struct {
		Comment    string `yaml:"comment"`
		ActionType string `yaml:"action_type"`
		ActionData string `yaml:"action_data"`
	} `yaml:"action"`
}
