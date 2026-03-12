package list

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// printTable prints the service list as a formatted table
func printTable(cmd *cobra.Command, services []ServiceInfo, showNamespace bool) {
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 3, ' ', 0)
	defer w.Flush()

	if len(services) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No resources found.")
		return
	}

	if showNamespace {
		fmt.Fprintln(w, "NAME\tNAMESPACE\tMODEL\tENGINE\tMODE\tREVISION\tREPLICAS\tSTATUS")
		for _, svc := range services {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
				svc.Name, svc.Namespace, svc.Model, svc.Engine, svc.Mode, svc.Revision, svc.Replicas, svc.Status)
		}
	} else {
		fmt.Fprintln(w, "NAME\tNAMESPACE\tMODEL\tENGINE\tMODE\tREVISION\tREPLICAS\tSTATUS")
		for _, svc := range services {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
				svc.Name, svc.Namespace, svc.Model, svc.Engine, svc.Mode, svc.Revision, svc.Replicas, svc.Status)
		}
	}
}

// printJSON prints the RoleBasedGroupList as JSON
func printJSON(_ *cobra.Command, rbgList *workloadsv1alpha2.RoleBasedGroupList) error {
	out, err := json.MarshalIndent(rbgList, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal to JSON: %w", err)
	}
	_, err = fmt.Fprintf(os.Stdout, "%s\n", out)
	return err
}

// printYAML prints the RoleBasedGroupList as YAML
func printYAML(_ *cobra.Command, rbgList *workloadsv1alpha2.RoleBasedGroupList) error {
	out, err := yaml.Marshal(rbgList)
	if err != nil {
		return fmt.Errorf("failed to marshal to YAML: %w", err)
	}
	_, err = os.Stdout.Write(out)
	return err
}
