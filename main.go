package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1" // For metadata API
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	v1 "k8s.io/api/core/v1"
)

type DeploymentInfo struct {
	Name                   string
	Namespace              string
	Replicas               int32
	MinReplicas            int32
	MaxReplicas            int32
	CPURequest             string
	CPULimit               string
	MemoryRequest          string
	MemoryLimit            string
	MaxUnavailable		   string
	MaxSurge			   string
	CPUTargetUtilization   int32
	ScaleUpStabilization   *int32
	ScaleDownStabilization *int32
	UpdateResourceAndHPA   string
	UpdateHPAOnly          string
}

// initializes a Kubernetes client using the default kubeconfig.
func getKubeClient() (*kubernetes.Clientset, string) {
	home := os.Getenv("HOME")
	kubeconfig := filepath.Join(home, ".kube", "config")

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		log.Fatalf("💢 Failed to load kubeconfig: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("💢 Failed to create Kubernetes client: %v", err)
	}

	// Get the current namespace from the context
	namespace := getActiveNamespace(kubeconfig)
	return clientset, namespace
}

// getActiveNamespace fetches the current namespace from kubeconfig.
func getActiveNamespace(kubeconfig string) string {
	config, err := clientcmd.LoadFromFile(kubeconfig)
	if err != nil {
		log.Fatalf("💢 Failed to load kubeconfig: %v", err)
	}

	currentContext := config.CurrentContext
	contextConfig, exists := config.Contexts[currentContext]
	if !exists {
		log.Fatalf("💢 Context %s not found in kubeconfig", currentContext)
	}

	return contextConfig.Namespace
}

func getDeploymentInfo(clientset *kubernetes.Clientset, namespace string) ([]DeploymentInfo, error) {
	var results []DeploymentInfo

	// List all Deployments in the namespace.
	deployments, err := clientset.AppsV1().Deployments(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("💢 failed to list deployments: %w", err)
	}

	// List all HPAs in the namespace.
	hpaList, err := clientset.AutoscalingV2().HorizontalPodAutoscalers(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("💢 failed to list HPAs: %w", err)
	}

	// Iterate over Deployments and collect relevant data.
	for _, deploy := range deployments.Items {
		var info DeploymentInfo
		info.Name = deploy.Name
		info.Namespace = deploy.Namespace
		info.Replicas = *deploy.Spec.Replicas

		var totalCPURequest, totalCPULimit, totalMemoryRequest, totalMemoryLimit int64

		// Aggregate resource requests and limits from all containers in the deployment.
		for _, container := range deploy.Spec.Template.Spec.Containers {
			resources := container.Resources
			totalCPURequest += resources.Requests.Cpu().MilliValue()
			totalCPULimit += resources.Limits.Cpu().MilliValue()
			totalMemoryRequest += resources.Requests.Memory().Value() / (1024 * 1024) // Convert bytes to MiB
			totalMemoryLimit += resources.Limits.Memory().Value() / (1024 * 1024)     // Convert bytes to MiB
		}

		info.CPURequest = fmt.Sprintf("%dm", totalCPURequest)
		info.CPULimit = fmt.Sprintf("%dm", totalCPULimit)
		info.MemoryRequest = fmt.Sprintf("%dMi", totalMemoryRequest)
		info.MemoryLimit = fmt.Sprintf("%dMi", totalMemoryLimit)


		// Get `maxUnavailable` dan `maxSurge` dari RollingUpdate Strategy
		if deploy.Spec.Strategy.Type == "RollingUpdate" {
			if deploy.Spec.Strategy.RollingUpdate != nil {
				if deploy.Spec.Strategy.RollingUpdate.MaxUnavailable != nil {
					info.MaxUnavailable = deploy.Spec.Strategy.RollingUpdate.MaxUnavailable.String()
				}

				if deploy.Spec.Strategy.RollingUpdate.MaxSurge != nil {
					info.MaxSurge = deploy.Spec.Strategy.RollingUpdate.MaxSurge.String()
				} 
			}
		}

		// Match HPA with the deployment (if available).
		for _, hpa := range hpaList.Items {
			if hpa.Spec.ScaleTargetRef.Name == deploy.Name && hpa.Spec.ScaleTargetRef.Kind == "Deployment" {
				if hpa.Spec.MinReplicas != nil {
					info.MinReplicas = *hpa.Spec.MinReplicas
				} else {
					info.MinReplicas = 1 // Default to 1 if MinReplicas is not set.
				}
				info.MaxReplicas = hpa.Spec.MaxReplicas

				// Extract CPU target utilization
				for _, metric := range hpa.Spec.Metrics {
					if metric.Type == autoscalingv2.ResourceMetricSourceType && metric.Resource != nil {
						if metric.Resource.Name == v1.ResourceCPU && metric.Resource.Target.AverageUtilization != nil {
							info.CPUTargetUtilization = *metric.Resource.Target.AverageUtilization
						}
					}
				}

				// Extract ScaleUp and ScaleDown behaviors
				if hpa.Spec.Behavior != nil {
					// ScaleUp
					if hpa.Spec.Behavior.ScaleUp != nil {
						info.ScaleUpStabilization = hpa.Spec.Behavior.ScaleUp.StabilizationWindowSeconds
					}

					// ScaleDown
					if hpa.Spec.Behavior.ScaleDown != nil {
						info.ScaleDownStabilization = hpa.Spec.Behavior.ScaleDown.StabilizationWindowSeconds
					}
				}
				break
			}
		}

		results = append(results, info)
	}
	return results, nil
}

// writeCSV saves the DeploymentInfo data into a CSV file with progress animation.
func writeCSV(data []DeploymentInfo) error {
	file, err := os.Create("deployment-info.csv")
	if err != nil {
		return fmt.Errorf("failed to create CSV file: %w", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	writer.Comma = '|'
	defer writer.Flush()

	// Write the CSV header with a new "Number" column.
	if err := writer.Write([]string{
		"No", "Deployment Name", "Namespace", "Replicas",
		"CPU Request", "CPU Limit", "Memory Request", "Memory Limit",
		"MaxUnavailable", "MaxSurge", "Min Replicas", "Max Replicas", "CPU Target Utilization", "ScaleUp Stabilization", 
		"ScaleDown Stabilization", "UpdateResourceAndHPA", "UpdateHPAOnly",
	}); err != nil {
		return fmt.Errorf("failed to write CSV header: %w", err)
	}

	// Write each DeploymentInfo as a row in the CSV with progress messages.
	for i, deploy := range data {
		record := []string{
			strconv.Itoa(i + 1), // Row number (starting from 1)
			deploy.Name,
			deploy.Namespace,
			strconv.Itoa(int(deploy.Replicas)),
			deploy.CPURequest,
			deploy.CPULimit,
			deploy.MemoryRequest,
			deploy.MemoryLimit,
			deploy.MaxUnavailable,
			deploy.MaxSurge,
			strconv.Itoa(int(deploy.MinReplicas)),
			strconv.Itoa(int(deploy.MaxReplicas)),
			strconv.Itoa(int(deploy.CPUTargetUtilization)),
		
			// Check if ScaleUpStabilization is nil before converting it to a string
			func() string {
				if deploy.ScaleUpStabilization != nil {
					return strconv.Itoa(int(*deploy.ScaleUpStabilization))
				}
				return "N/A" // Default value if nil
			}(),

			// Check if ScaleDownStabilization is nil before converting it to a string
			func() string {
				if deploy.ScaleDownStabilization != nil {
					return strconv.Itoa(int(*deploy.ScaleDownStabilization))
				}
				return "N/A"
			}(),

			"false",
			"false",
		}

		if err := writer.Write(record); err != nil {
			return fmt.Errorf("failed to write CSV record: %w", err)
		}

		// Show progress animation with progress bar.
		showSpinner(i+1, len(data), deploy.Name)
	}
	return nil
}

// showSpinner displays an animated progress bar with percentage and progress indicator.
func showSpinner(current, total int, name string) {
	// Spinner frames for smooth animation.
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	frame := frames[current%len(frames)]

	// Calculate progress percentage.
	percentage := (current * 100) / total

	// Create a dynamic progress bar of width 30 characters.
	barWidth := 30
	progress := (current * barWidth) / total
	bar := strings.Repeat("█", progress) + strings.Repeat(" ", barWidth-progress)

	// Print the spinner, progress bar, percentage, and current task.
	fmt.Printf("\r%s [%s] %d%% - Writing %d/%d", frame, bar, percentage, current, total)

	// Flush and wait a bit to slow down the animation (simulate real-time).
	time.Sleep(100 * time.Millisecond)

	// Ensure the output is flushed immediately.
	if current == total {
		fmt.Println() // Move to the next line when done.
	}
}

// confirmPrompt displays a confirmation prompt to the user.
func confirmPrompt() bool {
	fmt.Print("🎯 visit https://github.com/hendralw for the latest version")
	fmt.Print("\n\nDo you want to proceed with running the script? (Y/N): ")
	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(strings.ToUpper(input))
	return input == "Y"
}

func actionPrompt() string {
	fmt.Println("\nSelect an action:")
	fmt.Println("1: Generate Kubernetes Deployment to CSV")
	fmt.Println("2: Patch Kubernetes Spec from CSV")
	fmt.Println("3: Restart All Deployment")
	fmt.Println("4: Exit")
	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	return strings.TrimSpace(input)
}

func generateDeploymentInfo() {
	fmt.Println("\n💥 Running the script...\n")

	clientset, namespace := getKubeClient()
	data, err := getDeploymentInfo(clientset, namespace)
	if err != nil {
		log.Fatalf("💢 Error fetching deployment info: %v", err)
	}

	if err := writeCSV(data); err != nil {
		log.Fatalf("💢 Error writing CSV: %v", err)
	}

	fmt.Println("\n✅ CSV file 'deployment-info.csv' created successfully.")
}

// restarts a specific deployment or all deployments in the specified namespace.
func restartDeployment(deploymentName string) error {
	_, namespace := getKubeClient()
	var cmd *exec.Cmd

	// Restart all deployments in the namespace.
	cmd = exec.Command(
		"kubectl", "rollout", "restart", "deployment", "--all",
		"-n", namespace,
	)

	//if deploymentName == "all" {
	//	// Restart all deployments in the namespace.
	//	cmd = exec.Command(
	//		"kubectl", "rollout", "restart", "deployment", "--all",
	//		"-n", namespace,
	//	)
	//} else {
	//	// Restart a specific deployment.
	//	cmd = exec.Command(
	//		"kubectl", "rollout", "restart", "deployment", deploymentName,
	//		"-n", namespace,
	//	)
	//}

	// Print the command to debug.
	fmt.Println("\n💻 Executing command:", strings.Join(cmd.Args, " "))

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("💢 kubectl rollout restart error: %v\n%s", err, string(output))
	}

	if deploymentName == "all" {
		fmt.Printf("✅ All deployments restarted in namespace %s\n", namespace)
	} else {
		fmt.Printf("✅ Rollout restarted for deployment %s in namespace %s\n", deploymentName, namespace)
	}
	return nil
}

// PATCH: Function for action 2 - Update Kubernetes specs from CSV
func patchKubeResourcesFromCSV() error {
	file, err := os.Open("deployment-info.csv")
	if err != nil {
		return fmt.Errorf("failed to open CSV file: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.Comma = '|'
	_, err = reader.Read() // Skip header row
	if err != nil {
		return fmt.Errorf("failed to read CSV header: %w", err)
	}

	for {
		record, err := reader.Read()
		if err != nil {
			if err.Error() == "EOF" {
				break // End of file reached
			}
			return fmt.Errorf("error reading CSV: %w", err)
		}

		deploymentName := record[1]
		namespace := record[2]
		cpuRequest := record[4]
		//cpuLimit := record[5]
		memoryRequest := record[6]
		memoryLimit := record[7]
		maxUnavailable := record[8]
		maxSurge := record[9]
		minReplicas, _ := strconv.Atoi(record[10])
		maxReplicas, _ := strconv.Atoi(record[11])
		cpuTargetUtilization, _ := strconv.Atoi(record[12])
		scaleUpStabilization, _ := strconv.Atoi(record[13])
		scaleDownStabilization, _ := strconv.Atoi(record[14])

		// Extract data from CSV row
		if strings.ToLower(record[15]) == "true" { // UpdateResourceAndHPA 
			//Run kubectl commands to update deployment resources
			err = setDeploymentResources(namespace, deploymentName, cpuRequest, memoryRequest, memoryLimit, maxUnavailable, maxSurge)
			if err != nil {
				fmt.Printf("\n💢 failed to set resources for deployment %s: %v\n", deploymentName, err)
			}

			// Run kubectl command to patch HPA
			err = patchHPA(deploymentName, namespace, minReplicas, maxReplicas, cpuTargetUtilization, scaleUpStabilization, scaleDownStabilization)
			if err != nil {
				fmt.Printf("\n💢 failed to patch HPA for %s: %v\n", deploymentName, err)
			}
		} else if strings.ToLower(record[16]) == "true" { // UpdateHPAOnly
			// Run kubectl command to patch HPA
			err = patchHPA(deploymentName, namespace, minReplicas, maxReplicas, cpuTargetUtilization, scaleUpStabilization, scaleDownStabilization)
			if err != nil {
				fmt.Printf("\n💢 failed to patch HPA for %s: %v\n", deploymentName, err)
			}
		}

	}

	fmt.Println("✅ Kubernetes specs updated successfully!")
	return nil
}

// Helper function to set deployment resources using kubectl
func setDeploymentResources(namespace, deploymentName, cpuReq, memReq, memLim, maxUnavailable, maxSurge string) error {
	cmd := exec.Command(
		"kubectl", "set", "resources", "deployment", deploymentName,
		"--namespace="+namespace,
		fmt.Sprintf("--requests=cpu=%s,memory=%s", cpuReq, memReq),
		fmt.Sprintf("--limits=memory=%s", memLim),
	)

	fmt.Println("\n💻 Executing command: ", cmd.String())

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("kubectl set resources error: %v\n%s", err, string(output))
	}
	fmt.Printf("✅ Resources updated for deployment %s\n", deploymentName)

	// Update rolling update strategy
	patchData := fmt.Sprintf(`{"spec":{"strategy":{"type":"RollingUpdate","rollingUpdate":{"maxUnavailable":"%s","maxSurge":"%s"}}}}`, maxUnavailable, maxSurge)

	cmd = exec.Command(
		"kubectl", "patch", "deployment", deploymentName, 
		"--namespace="+namespace, 
		"--type=merge", "-p", patchData)

	fmt.Println("\n💻 Executing command: ", cmd.String())	

	output, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("kubectl patch rolling update error: %v\n%s", err, string(output))
	}
	fmt.Printf("✅ Rolling updated for deployment %s\n", deploymentName)

	return nil
}

// Helper function to patch HPA using kubectl
func patchHPA(hpaName, namespace string, minReplicas, maxReplicas, cpuTargetUtilization, scaleUpStabilization, scaleDownStabilization int) error {
	// Create JSON patch data
	patchData := fmt.Sprintf(`{"spec":{"minReplicas":%d,"maxReplicas":%d,"metrics":[{"type":"Resource","resource":{"name":"cpu","target":{"type":"Utilization","averageUtilization":%d}}}],"behavior":{"scaleUp":{"stabilizationWindowSeconds":%d},"scaleDown":{"stabilizationWindowSeconds":%d}}}}`, minReplicas, maxReplicas, cpuTargetUtilization, scaleUpStabilization, scaleDownStabilization)

	cmd := exec.Command(
		"kubectl", "patch", "hpa", hpaName,
		"--namespace="+namespace,
		"--type=merge", "-p", patchData)
	
	fmt.Println("\n💻 Executing command: ", cmd.String())

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("💢 kubectl patch hpa error: %v\n%s", err, string(output))
	}
	fmt.Printf("✅ HPA patched for %s\n", hpaName)

	return nil
}

func main() {
	if !confirmPrompt() {
		fmt.Println("\n💢 Operation cancelled.")
		return
	}

	action := actionPrompt()

	switch action {
	case "1":
		generateDeploymentInfo()
	case "2":
		err := patchKubeResourcesFromCSV()
		if err != nil {
			fmt.Printf("💢 Error updating Kubernetes specs: %v\n", err)
		}
	case "3":
		err := restartDeployment("all")
		if err != nil {
			return
		}
	case "4":
		fmt.Println("\n💢 Exiting the script.")
	default:
		fmt.Println("💢 Invalid choice, please select a valid action.")
	}
	os.Exit(0)
}
