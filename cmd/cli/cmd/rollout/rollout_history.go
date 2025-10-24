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
	"os"

	"github.com/spf13/cobra"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/client-go/kubernetes"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/client-go/clientset/versioned"
	"sigs.k8s.io/rbgs/cmd/cli/util"
)

var rolloutHistoryCmd = &cobra.Command{
	Use:                "history <rbgName>",
	Short:              "View rollout history",
	Args:               cobra.ExactArgs(1),
	DisableAutoGenTag:  true,
	SilenceUsage:       true,
	FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateRolloutHistory(args); err != nil {
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
		return runRolloutHistory(context.Background(), rbgClient, k8sClient, args[0], util.GetNamespace(rolloutOpts.cf))
	},
}

func init() {
	rolloutHistoryCmd.Flags().Int64Var(&rolloutOpts.revision, "revision", rolloutOpts.revision, "See the details, including roles of the revision specified")
}

func validateRolloutHistory(args []string) error {
	if len(args) == 0 || len(args[0]) == 0 {
		return fmt.Errorf("rbg name is required")
	}
	if rolloutOpts.revision < 0 {
		return fmt.Errorf("--revision cannot be negative")
	}
	return nil
}

func runRolloutHistory(ctx context.Context, rbgClient versioned.Interface, k8sClient kubernetes.Interface, rbgName, namespace string) error {
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

	if rolloutOpts.revision > 0 {
		for _, rev := range items {
			if rev.Revision == rolloutOpts.revision {
				scheme := runtime.NewScheme()
				_ = workloadsv1alpha1.AddToScheme(scheme)

				serializer := json.NewSerializerWithOptions(
					json.DefaultMetaFactory, scheme, scheme,
					json.SerializerOptions{Yaml: true, Pretty: true},
				)

				if err := serializer.Encode(rev, os.Stdout); err != nil {
					return err
				}
				return nil
			}
		}
		return fmt.Errorf("revision %d not found", rolloutOpts.revision)
	} else {
		items = sortRevisionsStable(items)

		fmt.Printf(
			"%-36s %s\n", "Name", "Revision")
		for _, rev := range items {
			fmt.Printf(
				"%-36s %d\n", rev.Name, rev.Revision)
		}
		return nil
	}
}
