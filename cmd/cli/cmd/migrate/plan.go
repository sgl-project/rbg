/*
Copyright 2026 The RBG Authors.

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

package migrate

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	rbgclient "sigs.k8s.io/rbgs/client-go/clientset/versioned"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/cmd/cli/util"
)

type migrationAction string

const (
	actionSafeCleanup  migrationAction = "safe-cleanup"
	actionWorkloadType migrationAction = "workload-type"
)

type rolePlan struct {
	ResourceKind string
	ResourceName string
	RoleName     string
	WorkloadType string
	Action       migrationAction
	Reason       string
}

func newPlanCmd(cf *genericclioptions.ConfigFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "plan",
		Short: "Report deprecated workload types without changing resources",
		Long: "Read all RoleBasedGroups and RoleBasedGroupSets and report every role that needs migration.\n" +
			"The report only describes impact. It does not decide or schedule a maintenance window.",
		Args:              cobra.NoArgs,
		SilenceUsage:      true,
		DisableAutoGenTag: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := util.GetRBGClient(cf)
			if err != nil {
				return err
			}
			return runPlan(cmd.OutOrStdout(), client, util.GetNamespace(cf))
		},
	}
}

func runPlan(out io.Writer, client rbgclient.Interface, namespace string) error {
	ctx := context.Background()

	rbgList, err := client.WorkloadsV1alpha2().RoleBasedGroups(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list RoleBasedGroups: %w", err)
	}
	rbgsList, err := client.WorkloadsV1alpha2().RoleBasedGroupSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list RoleBasedGroupSets: %w", err)
	}

	plans := make([]rolePlan, 0)
	for i := range rbgList.Items {
		rbg := &rbgList.Items[i]
		for j := range rbg.Spec.Roles {
			role := &rbg.Spec.Roles[j]
			plans = append(plans, planRole("RoleBasedGroup", rbg.Name, role))
		}
	}
	for i := range rbgsList.Items {
		rbgs := &rbgsList.Items[i]
		for j := range rbgs.Spec.GroupTemplate.Spec.Roles {
			role := &rbgs.Spec.GroupTemplate.Spec.Roles[j]
			plans = append(plans, planRole("RoleBasedGroupSet", rbgs.Name, role))
		}
	}

	sort.Slice(plans, func(i, j int) bool {
		if plans[i].ResourceKind != plans[j].ResourceKind {
			return plans[i].ResourceKind < plans[j].ResourceKind
		}
		if plans[i].ResourceName != plans[j].ResourceName {
			return plans[i].ResourceName < plans[j].ResourceName
		}
		return plans[i].RoleName < plans[j].RoleName
	})

	printPlans(out, plans, namespace)
	return nil
}

func planRole(resourceKind, resourceName string, role *workloadsv1alpha2.RoleSpec) rolePlan {
	workloadType := role.GetWorkloadType()
	plan := rolePlan{
		ResourceKind: resourceKind,
		ResourceName: resourceName,
		RoleName:     role.Name,
		WorkloadType: workloadType,
	}

	switch workloadType {
	case constants.RoleInstanceSetWorkloadType:
		plan.Action = actionSafeCleanup
		plan.Reason = "annotation is the default; deleting it does not change behavior"
	case constants.DeploymentWorkloadType,
		constants.StatefulSetWorkloadType,
		constants.LeaderWorkerSetWorkloadType:
		plan.Action = actionWorkloadType
		plan.Reason = "old workload remains in use; changing to RoleInstanceSet replaces its Kind and recreates all Pods"
	default:
		plan.Action = actionWorkloadType
		plan.Reason = "unknown workload type; review it before migrating"
	}

	return plan
}

func printPlans(out io.Writer, plans []rolePlan, namespace string) {
	if len(plans) == 0 {
		_, _ = fmt.Fprintf(out, "No roles found in namespace %s.\n", namespace)
		return
	}

	_, _ = fmt.Fprintf(out, "Migration plan (namespace %s)\n", namespace)
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintf(out, "%-18s %-24s %-16s %-52s %-16s\n", "RESOURCE", "NAME", "ROLE", "WORKLOAD TYPE", "ACTION")

	for _, plan := range plans {
		_, _ = fmt.Fprintf(out, "%-18s %-24s %-16s %-52s %-16s\n",
			plan.ResourceKind, plan.ResourceName, plan.RoleName, plan.WorkloadType, plan.Action)
	}

	safe, destructive := countActions(plans)
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintf(out, "Summary: %d roles, %d safe-cleanup, %d workload-type migration(s).\n", len(plans), safe, destructive)
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintln(out, "Impact:")
	for _, plan := range plans {
		if plan.Action == actionWorkloadType {
			_, _ = fmt.Fprintf(out, "  %s/%s role %s: %s.\n", plan.ResourceKind, plan.ResourceName, plan.RoleName, plan.Reason)
		}
	}
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintln(out, "Next steps: migrate one RoleBasedGroup role at a time with 'kubectl rbg migrate workload-type',")
	_, _ = fmt.Fprintln(out, "verify it is ready, then run 'kubectl rbg migrate storage-version' after all roles are migrated.")
}

func countActions(plans []rolePlan) (safe, destructive int) {
	for _, plan := range plans {
		switch plan.Action {
		case actionSafeCleanup:
			safe++
		case actionWorkloadType:
			destructive++
		}
	}
	return safe, destructive
}
