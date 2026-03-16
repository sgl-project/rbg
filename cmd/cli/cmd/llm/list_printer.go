/*
Copyright 2026.

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

package llm

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func listPrintTable(cmd *cobra.Command, services []serviceInfo, showNamespace bool) {
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
		fmt.Fprintln(w, "NAME\tMODEL\tENGINE\tMODE\tREVISION\tREPLICAS\tSTATUS")
		for _, svc := range services {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
				svc.Name, svc.Model, svc.Engine, svc.Mode, svc.Revision, svc.Replicas, svc.Status)
		}
	}
}

func listPrintJSON(_ *cobra.Command, rbgList *workloadsv1alpha2.RoleBasedGroupList) error {
	out, err := json.MarshalIndent(rbgList, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal to JSON: %w", err)
	}
	_, err = fmt.Fprintf(os.Stdout, "%s\n", out)
	return err
}

func listPrintYAML(_ *cobra.Command, rbgList *workloadsv1alpha2.RoleBasedGroupList) error {
	out, err := yaml.Marshal(rbgList)
	if err != nil {
		return fmt.Errorf("failed to marshal to YAML: %w", err)
	}
	_, err = os.Stdout.Write(out)
	return err
}
