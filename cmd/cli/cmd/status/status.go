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

package status

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/rbgs/cmd/cli/util"
)

const (
	progressBarWidth = 16
)

type StatusOptions struct {
	cf *genericclioptions.ConfigFlags
}

var statusOpts StatusOptions

func NewStatusCmd(cf *genericclioptions.ConfigFlags) *cobra.Command {
	statusCmd := &cobra.Command{
		Use:                "status <rbgName>",
		Short:              "Display rbg status information",
		Args:               cobra.ExactArgs(1),
		FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(context.TODO(), args[0])
		},
	}
	statusOpts.cf = cf

	return statusCmd
}

func runStatus(_ context.Context, rbg string) error {
	return runWithClient(nil, rbg, nil)
}

func runWithClient(_ *cobra.Command, name string, dynamicClient dynamic.Interface) error {
	var err error
	// Create a dynamic client if not provided
	if dynamicClient == nil {
		dynamicClient, err = util.GetDefaultDynamicClient(statusOpts.cf)
		if err != nil {
			return fmt.Errorf("failed to create dynamic client: %w", err)
		}
	}

	// Fetch the resource object
	resource, err := util.GetRBGObjectByDynamicClient(context.TODO(), name, util.GetNamespace(statusOpts.cf), dynamicClient)
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
	fmt.Printf("  Namespace: %s\n", util.GetNamespace(statusOpts.cf))
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
