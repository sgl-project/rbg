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

package rollout

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/client-go/clientset/versioned"
	"sigs.k8s.io/rbgs/cmd/cli/util"
	"sigs.k8s.io/rbgs/pkg/utils"
)

var rolloutUndoCmd = &cobra.Command{
	Use:   "undo <rbgName>",
	Short: "Undo a previous rollout",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateRolloutUndo(args); err != nil {
			return err
		}
		rbgClient, err := util.GetRBGClient(rolloutOpts.cf)
		if err != nil {
			return err
		}
		k8sClient, err := util.GetK8SClientSet(rolloutOpts.cf)
		if err != nil {
			return err
		}
		return runRolloutUndo(context.Background(), rbgClient, k8sClient, args[0], util.GetNamespace(rolloutOpts.cf))
	},
}

func init() {
	rolloutUndoCmd.Flags().Int64Var(&rolloutOpts.revision, "revision", rolloutOpts.revision, "rollback to specific revision")
}

func validateRolloutUndo(args []string) error {
	if len(args) == 0 || len(args[0]) == 0 {
		return fmt.Errorf("rbg name is required")
	}
	if rolloutOpts.revision < 0 {
		return fmt.Errorf("--revision cannot be negative")
	}
	return nil
}

func runRolloutUndo(ctx context.Context, rbgClient versioned.Interface, k8sClient kubernetes.Interface, rbgName, namespace string) error {
	rbgObject, err := rbgClient.WorkloadsV1alpha1().RoleBasedGroups(namespace).Get(ctx, rbgName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if rbgObject == nil {
		return fmt.Errorf("RoleBasedGroup %s not found", rbgName)
	}

	// List ControllerRevision
	revisions, err := k8sClient.AppsV1().
		ControllerRevisions(namespace).
		List(context.TODO(), metav1.ListOptions{
			LabelSelector: fmt.Sprintf("%s=%s", workloadsv1alpha1.SetNameLabelKey, rbgObject.Name),
		})
	if err != nil {
		return err
	}

	history := revisions.Items
	var items []*appsv1.ControllerRevision
	for i := range history {
		ref := metav1.GetControllerOfNoCopy(&history[i])
		if ref == nil || ref.UID == rbgObject.GetUID() {
			items = append(items, &history[i])
		}
	}
	items = sortRevisionsStable(items)

	if rolloutOpts.revision == 0 {
		if len(items) <= 1 {
			return fmt.Errorf("no enough revision found, current revision is the latest one")
		}
		return rollback(ctx, rbgClient, rbgObject, items[len(items)-2])
	} else {
		if len(items) > 1 && rolloutOpts.revision == items[len(items)-1].Revision {
			klog.Info("Specified revision is current rbg's revision, no need to rollback")
			return nil
		}
		for _, rev := range items {
			if rev.Revision == rolloutOpts.revision {
				return rollback(ctx, rbgClient, rbgObject, rev)
			}
		}
		return fmt.Errorf("revision %d not found", rolloutOpts.revision)
	}
}

func rollback(ctx context.Context, rbgClient versioned.Interface, rbg *workloadsv1alpha1.RoleBasedGroup, specificRevision *appsv1.ControllerRevision) error {
	newRbg, err := utils.ApplyRevision(rbg, specificRevision)
	if err != nil {
		return err
	}
	// todo: use ssa to update
	_, err = rbgClient.WorkloadsV1alpha1().RoleBasedGroups(rbg.Namespace).Update(ctx, newRbg, metav1.UpdateOptions{})
	if err == nil {
		fmt.Printf("rbg %s rollback to revision %d successfully\n", rbg.Name, specificRevision.Revision)
	}
	return err
}
