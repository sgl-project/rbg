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

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v2"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/client-go/clientset/versioned"
	"sigs.k8s.io/rbgs/cmd/cli/util"
)

var rolloutDiffCmd = &cobra.Command{
	Use:   "diff <rbgName>",
	Short: "Show the diff between the current rbg and the specified revision",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateRolloutDiff(args); err != nil {
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
		return runRolloutDiff(context.Background(), rbgClient, k8sClient, args[0], util.GetNamespace(rolloutOpts.cf))
	},
}

func init() {
	rolloutDiffCmd.Flags().Int64Var(&rolloutOpts.revision, "revision", rolloutOpts.revision, "specific revision to compare")
}

func validateRolloutDiff(args []string) error {
	if len(args) == 0 || len(args[0]) == 0 {
		return fmt.Errorf("rbg name is required")
	}
	if rolloutOpts.revision <= 0 {
		return fmt.Errorf("--revision must be positive")
	}
	return nil
}

func runRolloutDiff(ctx context.Context, rbgClient versioned.Interface, k8sClient kubernetes.Interface, rbgName, namespace string) error {
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
	var currentRevision *appsv1.ControllerRevision
	var specificRevision *appsv1.ControllerRevision
	for _, rev := range items {
		if rev.Revision == rolloutOpts.revision {
			specificRevision = rev
		}
		if currentRevision == nil || currentRevision.Revision <= rev.Revision {
			currentRevision = rev
		}
	}

	if specificRevision == nil {
		return fmt.Errorf("--revision=%d not found, please check the revision number", rolloutOpts.revision)
	} else if currentRevision == nil {
		return fmt.Errorf("current revision not found, please try again later")
	}

	res, err := extractDiff(currentRevision, specificRevision)
	if err != nil {
		return err
	}

	fmt.Println(res)
	return nil
}

func extractDiff(currentRevision, specificRevision *appsv1.ControllerRevision) (string, error) {
	spec1, err := extractSpec(currentRevision)
	if err != nil {
		return "", err
	}
	spec2, err := extractSpec(specificRevision)
	if err != nil {
		return "", err
	}

	spec1Yaml, err := yaml.Marshal(spec1)
	if err != nil {
		return "", err
	}
	spec2Yaml, err := yaml.Marshal(spec2)
	if err != nil {
		return "", err
	}

	return cmp.Diff(string(spec2Yaml), string(spec1Yaml)), nil
}

func extractSpec(rev *appsv1.ControllerRevision) (interface{}, error) {
	if len(rev.Data.Raw) == 0 {
		return nil, fmt.Errorf("controller revision has no raw data")
	}
	var raw map[string]interface{}
	if err := yaml.Unmarshal(rev.Data.Raw, &raw); err != nil {
		return nil, err
	}
	return raw["spec"], nil
}
