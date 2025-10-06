package deployment

import (
	"context"
	"fmt"

	"sigs.k8s.io/karpenter/hack/e2e_driver/pkg/config"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Manager handles Kubernetes deployments for scenario workloads
type Manager struct {
	client        *kubernetes.Clientset
	dynamicClient dynamic.Interface
	namespace     string
	labels        map[string]string
	manifests     map[string][]byte // Stores loaded Kubernetes manifests by name
}

// NewManager creates a new deployment manager
func NewManager(namespace string, kubeconfigPath string) (*Manager, error) {
	// Try to load from specified kubeconfig location first
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		// Fall back to in-cluster config
		config, err = rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to create Kubernetes config: %w", err)
		}
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	// Create dynamic client for handling arbitrary resources
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	return &Manager{
		client:        clientset,
		dynamicClient: dynamicClient,
		namespace:     namespace,
		labels: map[string]string{
			"managed-by": "k8s-sim-driver",
		},
		manifests: make(map[string][]byte),
	}, nil
}

// NewManagerWithConfig creates a new deployment manager with explicit config
func NewManagerWithConfig(config *rest.Config, namespace string) (*Manager, error) {
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	// Create dynamic client for handling arbitrary resources
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	return &Manager{
		client:        clientset,
		dynamicClient: dynamicClient,
		namespace:     namespace,
		labels: map[string]string{
			"managed-by": "k8s-sim-driver",
		},
		manifests: make(map[string][]byte),
	}, nil
}

// LoadKubernetesManifests loads all Kubernetes manifest files from the scenario directory
func (m *Manager) LoadKubernetesManifests(scenarioDir string, deploymentsDir string, deploymentNames []string) error {
	// Load all manifests
	manifests, err := config.LoadAllKubernetesManifests(scenarioDir, deploymentsDir, deploymentNames)
	if err != nil {
		return fmt.Errorf("failed to load Kubernetes manifests: %w", err)
	}

	// Store them in the manager
	m.manifests = manifests

	return nil
}

// ApplyKubernetesManifests applies loaded Kubernetes manifests to the cluster
func (m *Manager) ApplyKubernetesManifests(ctx context.Context) error {
	// For now, we'll focus on applying just the deployment manifests using the existing API
	for name, _ := range m.manifests {
		// In a real implementation, we would parse the YAML and use server-side apply
		// But for now, we'll just create a simple deployment with the name
		replicas := int32(1)

		deployment := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: m.namespace,
				Labels: map[string]string{
					"app":        name,
					"managed-by": "k8s-sim-driver",
				},
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"app": name,
					},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app":        name,
							"managed-by": "k8s-sim-driver",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  name,
								Image: "nginx:latest", // Using nginx as a simple placeholder
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("0.5"),
										corev1.ResourceMemory: resource.MustParse("512Mi"),
									},
									Limits: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("1.0"),
										corev1.ResourceMemory: resource.MustParse("1Gi"),
									},
								},
							},
						},
					},
				},
			},
		}

		// Create the deployment
		_, err := m.client.AppsV1().Deployments(m.namespace).Create(ctx, deployment, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("failed to create deployment %s: %w", name, err)
		}

		fmt.Printf("Created deployment from manifest: %s\n", name)
	}

	return nil
}

// CreateDeployment creates a Kubernetes deployment for a workload
func (m *Manager) CreateDeployment(ctx context.Context, workload config.Workload) error {
	name := workload.ServiceOwnedWorkload.Name
	replicas := int32(workload.ServiceOwnedWorkload.StartingWorkloads)

	// Set resource requirements based on task definition
	cpuRequest := fmt.Sprintf("%g", workload.ServiceOwnedWorkload.TaskDefinition.CPU)
	memoryRequest := fmt.Sprintf("%dMi", workload.ServiceOwnedWorkload.TaskDefinition.Memory)

	// Create labels for this specific workload
	workloadLabels := map[string]string{
		"app":        name,
		"managed-by": "k8s-sim-driver",
	}

	// Create the deployment object
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: m.namespace,
			Labels:    workloadLabels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: workloadLabels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  name,
							Image: "nginx:latest", // Using nginx as a simple placeholder
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse(cpuRequest),
									corev1.ResourceMemory: resource.MustParse(memoryRequest),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse(cpuRequest),
									corev1.ResourceMemory: resource.MustParse(memoryRequest),
								},
							},
						},
					},
				},
			},
		},
	}

	// Create the deployment in Kubernetes
	_, err := m.client.AppsV1().Deployments(m.namespace).Create(ctx, deployment, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create deployment %s: %w", name, err)
	}

	return nil
}

// ScaleDeployment scales a deployment to the specified replica count
func (m *Manager) ScaleDeployment(ctx context.Context, name string, replicas int) error {
	// Get the current deployment
	deployment, err := m.client.AppsV1().Deployments(m.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get deployment %s: %w", name, err)
	}

	// Update the replica count
	replicaCount := int32(replicas)
	deployment.Spec.Replicas = &replicaCount

	// Update the deployment
	_, err = m.client.AppsV1().Deployments(m.namespace).Update(ctx, deployment, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to scale deployment %s to %d replicas: %w", name, replicas, err)
	}

	return nil
}

// AreDeploymentsStable checks if all managed deployments are in a stable state
func (m *Manager) AreDeploymentsStable(ctx context.Context) (bool, error) {
	// Get all deployments with our managed-by label
	deployments, err := m.client.AppsV1().Deployments(m.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "managed-by=k8s-sim-driver",
	})
	if err != nil {
		return false, fmt.Errorf("failed to list deployments: %w", err)
	}

	// Check if each deployment is stable
	for _, deployment := range deployments.Items {
		if deployment.Status.ReadyReplicas != *deployment.Spec.Replicas {
			return false, nil
		}
	}

	return true, nil
}

// GetClientset returns the Kubernetes clientset
func (m *Manager) GetClientset() *kubernetes.Clientset {
	return m.client
}

// DeleteAllDeployments deletes all deployments managed by this driver
func (m *Manager) DeleteAllDeployments(ctx context.Context) error {
	// Get all deployments with our managed-by label
	deployments, err := m.client.AppsV1().Deployments(m.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "managed-by=k8s-sim-driver",
	})
	if err != nil {
		return fmt.Errorf("failed to list deployments: %w", err)
	}

	// Delete each deployment
	for _, deployment := range deployments.Items {
		err := m.client.AppsV1().Deployments(m.namespace).Delete(ctx, deployment.Name, metav1.DeleteOptions{})
		if err != nil {
			return fmt.Errorf("failed to delete deployment %s: %w", deployment.Name, err)
		}
	}

	return nil
}
