package list

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	llmmeta "sigs.k8s.io/rbgs/cmd/cli/cmd/llm/metadata"
	"sigs.k8s.io/rbgs/cmd/cli/util"
)

// ServiceInfo represents a single LLM service entry for display
type ServiceInfo struct {
	Name      string
	Namespace string
	Model     string
	Engine    string
	Mode      string
	Revision  string
	Replicas  int32
	Status    string
}

// NewListCmd creates the llm list command
func NewListCmd(cf *genericclioptions.ConfigFlags) *cobra.Command {
	var (
		allNamespaces bool
		output        string
	)

	cmd := &cobra.Command{
		Use:   "list [flags]",
		Short: "List LLM inference services created by the CLI",
		Long:  `List RoleBasedGroup resources created by 'kubectl rbg llm run'.`,
		Example: `  # List services in current namespace
  kubectl rbg llm list

  # List services in all namespaces
  kubectl rbg llm list -A

  # List services in a specific namespace
  kubectl rbg llm list -n kubeai

  # Output as JSON
  kubectl rbg llm list -o json

  # Filter by label selector
  kubectl rbg llm list -l app.kubernetes.io/name=my-qwen`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := util.GetRBGClient(cf)
			if err != nil {
				return fmt.Errorf("unable to connect to Kubernetes cluster: %w", err)
			}

			listOpts := metav1.ListOptions{
				LabelSelector: llmmeta.RunCommandSourceLabelKey + "=" + llmmeta.RunCommandSourceLabelValue,
			}

			// Determine the namespace to list from
			var namespace string
			if allNamespaces {
				namespace = ""
			} else {
				namespace = util.GetNamespace(cf)
			}

			ctx := context.Background()
			// rbgList, err := client.WorkloadsV1alpha2().RoleBasedGroups(namespace).List(ctx, listOpts)
			rbgList, err := client.WorkloadsV1alpha1().RoleBasedGroups(namespace).List(ctx, listOpts)
			if err != nil {
				return fmt.Errorf("failed to list RoleBasedGroups: %w", err)
			}

			// Handle output format
			switch output {
			case "json":
				return printJSON(cmd, rbgList)
			case "yaml":
				return printYAML(cmd, rbgList)
			default:
				// table format
				services := extractServiceInfos(rbgList)
				printTable(cmd, services, allNamespaces)
			}

			return nil
		},
	}

	cmd.Flags().BoolVarP(&allNamespaces, "all-namespaces", "A", false, "List services across all namespaces")
	cmd.Flags().StringVarP(&output, "output", "o", "table", "Output format: table, json, yaml")

	return cmd
}

// extractServiceInfos extracts ServiceInfo from a list of RoleBasedGroups
func extractServiceInfos(rbgList *workloadsv1alpha1.RoleBasedGroupList) []ServiceInfo {
	services := make([]ServiceInfo, 0, len(rbgList.Items))
	for i := range rbgList.Items {
		rbg := &rbgList.Items[i]
		info := ServiceInfo{
			Name:      rbg.Name,
			Namespace: rbg.Namespace,
			Status:    extractStatus(rbg),
			Replicas:  0,
		}

		// Parse CLI metadata annotation
		if annotationVal, ok := rbg.Annotations[llmmeta.RunCommandMetadataAnnotationKey]; ok {
			var meta llmmeta.RunMetadata
			if err := json.Unmarshal([]byte(annotationVal), &meta); err == nil {
				info.Model = meta.ModelID
				info.Engine = meta.Engine
				info.Mode = meta.Mode
				info.Revision = meta.Revision
			} else {
				info.Model = "unknown"
				info.Engine = "unknown"
				info.Mode = "unknown"
				info.Revision = "unknown"
			}
		} else {
			info.Model = "unknown"
			info.Engine = "unknown"
			info.Mode = "unknown"
			info.Revision = "unknown"
		}

		// Extract replicas from first role
		if len(rbg.Spec.Roles) > 0 && rbg.Spec.Roles[0].Replicas != nil {
			info.Replicas = *rbg.Spec.Roles[0].Replicas
		}

		services = append(services, info)
	}
	return services
}

// extractStatus returns the human-readable status from an RBG's conditions
func extractStatus(rbg *workloadsv1alpha1.RoleBasedGroup) string {
	if len(rbg.Status.Conditions) == 0 {
		return "Pending"
	}
	// Use the first condition's type as status
	return string(rbg.Status.Conditions[0].Type)
}
