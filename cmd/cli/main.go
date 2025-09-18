/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	namespace string
	name      string
)

const (
	progressBarWidth = 16
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "kubectl-rbg-status",
		Short: "Display RoleBasedGroup status information",
		Args:  cobra.ExactArgs(1),
		RunE:  run,
	}

	rootCmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "Namespace of the resource")

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(cmd *cobra.Command, args []string) error {
	return runWithClient(cmd, args, nil)
}

func runWithClient(_ *cobra.Command, args []string, dynamicClient dynamic.Interface) error {
	// Retrieve the first argument as the resource name
	name = args[0]

	// Create a dynamic client if not provided
	if dynamicClient == nil {
		// Fetch Kubernetes configuration
		config, err := getConfig()
		if err != nil {
			return fmt.Errorf("failed to get kubeconfig: %w", err)
		}

		// Create a dynamic client
		dynamicClient, err = dynamic.NewForConfig(config)
		if err != nil {
			return fmt.Errorf("failed to create dynamic client: %w", err)
		}
	}

	// Define GVR
	gvr := schema.GroupVersionResource{
		Group:    "workloads.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "rolebasedgroups",
	}

	// Fetch the resource object
	resource, err := dynamicClient.Resource(gvr).Namespace(namespace).Get(
		context.TODO(),
		name,
		metav1.GetOptions{},
	)
	if err != nil {
		return fmt.Errorf("failed to get RoleBasedGroup: %w", err)
	}

	// Parse the status of the resource
	roleStatuses, err := parseStatus(resource)
	if err != nil {
		return fmt.Errorf("failed to parse status: %w", err)
	}

	// Retrieve the creation timestamp
	creationTimestamp, found, err := unstructured.NestedString(resource.Object, "metadata", "creationTimestamp")
	if err != nil {
		return fmt.Errorf("failed to get creation time: %w", err)
	}

	// Calculate the age of the resource
	var ageStr string
	if found {
		createTime, err := time.Parse(time.RFC3339, creationTimestamp)
		if err == nil {
			ageStr = duration.HumanDuration(time.Since(createTime))
		}
	}
	if ageStr == "" {
		ageStr = "<unknown>"
	}

	// Generate and print the report
	printReport(resource, roleStatuses, ageStr)
	return nil
}

func getConfig() (*rest.Config, error) {
	kubeconfig := clientcmd.NewDefaultClientConfigLoadingRules().GetDefaultFilename()
	return clientcmd.BuildConfigFromFlags("", kubeconfig)
}

func parseStatus(resource *unstructured.Unstructured) ([]map[string]interface{}, error) {
	status, found, err := unstructured.NestedMap(resource.Object, "status")
	if err != nil {
		return nil, fmt.Errorf("error accessing status: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("status not found")
	}

	roleStatuses, found, err := unstructured.NestedSlice(status, "roleStatuses")
	if err != nil {
		return nil, fmt.Errorf("error accessing roleStatuses: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("roleStatuses not found")
	}

	var results []map[string]interface{}
	for _, rs := range roleStatuses {
		if roleStatus, ok := rs.(map[string]interface{}); ok {
			results = append(results, roleStatus)
		}
	}
	return results, nil
}

func printReport(resource *unstructured.Unstructured, roleStatuses []map[string]interface{}, ageStr string) {
	fmt.Printf("📊 Resource Overview\n")
	fmt.Printf("  Namespace: %s\n", namespace)
	fmt.Printf("  Name:      %s\n\n", resource.GetName())
	fmt.Printf("  Age:       %s\n\n", ageStr)

	fmt.Println("📦 Role Statuses")

	totalReady := 0
	totalReplicas := 0

	for _, rs := range roleStatuses {
		name := getString(rs, "name")
		ready := getInt64(rs, "readyReplicas")
		replicas := getInt64(rs, "replicas")

		percent := 0.0
		if replicas > 0 {
			percent = float64(ready) / float64(replicas) * 100
		}

		bar := progressBar(percent, progressBarWidth)
		fmt.Printf(
			"%-12s %d/%d\t\t(total: %d)\t[%s] %d%%\n",
			name,
			ready,
			replicas,
			replicas,
			bar,
			int(percent),
		)

		totalReady += int(ready)
		totalReplicas += int(replicas)
	}

	fmt.Printf(
		"\n∑ Summary: %d roles | %d/%d Ready\n",
		len(roleStatuses),
		totalReady,
		totalReplicas,
	)
}

func getString(m map[string]interface{}, key string) string {
	v, found, _ := unstructured.NestedString(m, key)
	if !found {
		return ""
	}
	return v
}

func getInt64(m map[string]interface{}, key string) int64 {
	v, found, _ := unstructured.NestedInt64(m, key)
	if !found {
		return 0
	}
	return v
}

func progressBar(percent float64, width int) string {
	filled := int(percent / 100 * float64(width))
	if filled > width {
		filled = width
	}
	return strings.Repeat("█", filled) + strings.Repeat(" ", width-filled)
}
