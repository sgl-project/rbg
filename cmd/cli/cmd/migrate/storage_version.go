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

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	rbgclient "sigs.k8s.io/rbgs/client-go/clientset/versioned"
	"sigs.k8s.io/rbgs/cmd/cli/util"
)

func newStorageVersionCmd(cf *genericclioptions.ConfigFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "storage-version",
		Short: "Rewrite RBG resources in the v1alpha2 storage encoding",
		Long: "Perform a no-op read/update pass through the v1alpha2 endpoints for every conversion-enabled RBG custom resource.\n" +
			"This is lossless, repeatable, and ensures objects that have never been updated are stored as v1alpha2.",
		Args:              cobra.NoArgs,
		SilenceUsage:      true,
		DisableAutoGenTag: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			rbgClient, err := util.GetRBGClient(cf)
			if err != nil {
				return err
			}
			return runStorageVersionMigration(cmd.Context(), cmd.OutOrStdout(), rbgClient, util.GetNamespace(cf))
		},
	}
}

func runStorageVersionMigration(ctx context.Context, out io.Writer, typedClient rbgclient.Interface, namespace string) error {
	versioned := typedClient.WorkloadsV1alpha2()

	var errs []error
	counts := map[string]int{}

	rbgList, err := versioned.RoleBasedGroups(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		errs = append(errs, fmt.Errorf("list RoleBasedGroups: %w", err))
	} else {
		counts["RoleBasedGroup"] = 0
		for i := range rbgList.Items {
			obj := &rbgList.Items[i]
			if obj.DeletionTimestamp != nil {
				continue
			}
			if _, err := versioned.RoleBasedGroups(namespace).Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
				errs = append(errs, fmt.Errorf("rewrite RoleBasedGroup %s: %w", obj.Name, err))
			} else {
				counts["RoleBasedGroup"]++
			}
		}
	}

	rbgsList, err := versioned.RoleBasedGroupSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		errs = append(errs, fmt.Errorf("list RoleBasedGroupSets: %w", err))
	} else {
		counts["RoleBasedGroupSet"] = 0
		for i := range rbgsList.Items {
			obj := &rbgsList.Items[i]
			if obj.DeletionTimestamp != nil {
				continue
			}
			if _, err := versioned.RoleBasedGroupSets(namespace).Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
				errs = append(errs, fmt.Errorf("rewrite RoleBasedGroupSet %s: %w", obj.Name, err))
			} else {
				counts["RoleBasedGroupSet"]++
			}
		}
	}

	if len(errs) != 0 {
		return fmt.Errorf("storage-version migration failed: %v", errs)
	}

	updated := 0
	for _, kind := range []string{
		"RoleBasedGroup",
		"RoleBasedGroupSet",
	} {
		_, _ = fmt.Fprintf(out, "Rewrote %d %s object(s) through the v1alpha2 endpoint.\n", counts[kind], kind)
		updated += counts[kind]
	}
	_, _ = fmt.Fprintf(out, "Storage-version migration complete: %d object(s) rewritten in namespace %s.\n", updated, namespace)
	return nil
}
