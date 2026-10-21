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
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	rbgclient "sigs.k8s.io/rbgs/client-go/clientset/versioned"
	"sigs.k8s.io/rbgs/cmd/cli/util"
)

type workloadTypeOptions struct {
	cf        *genericclioptions.ConfigFlags
	rbg       string
	role      string
	assumeYes bool
	k8sClient kubernetes.Interface
	rbgClient rbgclient.Interface
	in        io.Reader
	out       io.Writer
}

func newWorkloadTypeCmd(cf *genericclioptions.ConfigFlags) *cobra.Command {
	opts := &workloadTypeOptions{cf: cf}
	cmd := &cobra.Command{
		Use:   "workload-type --rbg RBG --role ROLE",
		Short: "Migrate one role from a deprecated workload to RoleInstanceSet",
		Long: "Remove the role-workload-type annotation from one RoleBasedGroup role and materialize its Pattern.\n" +
			"The downstream workload Kind changes, so all Pods for that role are recreated. Migrate one role,\n" +
			"wait for it to become ready, then migrate the next role.",
		Args:              cobra.NoArgs,
		SilenceUsage:      true,
		DisableAutoGenTag: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.in = cmd.InOrStdin()
			opts.out = cmd.OutOrStdout()
			if opts.rbgClient == nil {
				client, err := util.GetRBGClient(opts.cf)
				if err != nil {
					return err
				}
				opts.rbgClient = client
			}
			if opts.k8sClient == nil {
				client, err := util.GetK8SClientSet(opts.cf)
				if err != nil {
					return err
				}
				opts.k8sClient = client
			}
			return opts.run(cmd.Context())
		},
	}

	cmd.Flags().StringVar(&opts.rbg, "rbg", "", "RoleBasedGroup name (required)")
	cmd.Flags().StringVar(&opts.role, "role", "", "role name (required)")
	cmd.Flags().BoolVarP(&opts.assumeYes, "yes", "y", false, "skip interactive confirmation (non-interactive use)")
	_ = cmd.MarkFlagRequired("rbg")
	_ = cmd.MarkFlagRequired("role")

	return cmd
}

func (o *workloadTypeOptions) run(ctx context.Context) error {
	if o.rbg == "" || o.role == "" {
		return errors.New("--rbg and --role are required")
	}
	namespace := util.GetNamespace(o.cf)

	rbg, err := o.rbgClient.WorkloadsV1alpha2().RoleBasedGroups(namespace).Get(ctx, o.rbg, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get RoleBasedGroup %s/%s: %w", namespace, o.rbg, err)
	}

	roleIndex := -1
	for i := range rbg.Spec.Roles {
		if rbg.Spec.Roles[i].Name == o.role {
			roleIndex = i
			break
		}
	}
	if roleIndex == -1 {
		return fmt.Errorf("role %q not found in RoleBasedGroup %s/%s", o.role, namespace, o.rbg)
	}

	role := &rbg.Spec.Roles[roleIndex]
	annotation, hasAnnotation := role.Annotations[constants.RoleWorkloadTypeAnnotationKey]
	if !hasAnnotation {
		return fmt.Errorf("role %q already uses the default RoleInstanceSet workload type", o.role)
	}
	if annotation == constants.RoleInstanceSetWorkloadType {
		// A redundant default annotation is a pure cleanup: preserve the existing
		// Pattern exactly so removing the annotation cannot change behavior.
		delete(role.Annotations, constants.RoleWorkloadTypeAnnotationKey)
		if len(role.Annotations) == 0 {
			role.Annotations = nil
		}
		_, err := o.update(ctx, rbg)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(o.out, "Removed the redundant RoleInstanceSet annotation from RoleBasedGroup %s/%s role %q.\n", namespace, o.rbg, o.role)
		return nil
	}

	pods, err := o.listRolePods(ctx, namespace, o.rbg, o.role)
	if err != nil {
		return fmt.Errorf("list Pods for role %q: %w", o.role, err)
	}
	printPodsToRecreate(o.out, namespace, o.rbg, o.role, annotation, pods)

	if !o.assumeYes {
		confirmed, err := confirm(o.in)
		if err != nil {
			return err
		}
		if !confirmed {
			_, _ = fmt.Fprintln(o.out, "Migration cancelled.")
			return nil
		}
	}

	if err := materializeDefaultPattern(role); err != nil {
		return err
	}
	delete(role.Annotations, constants.RoleWorkloadTypeAnnotationKey)
	if len(role.Annotations) == 0 {
		role.Annotations = nil
	}

	if _, err := o.update(ctx, rbg); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(o.out, "Migrated RoleBasedGroup %s/%s role %q to RoleInstanceSet.\n", namespace, o.rbg, o.role)
	_, _ = fmt.Fprintln(o.out, "Wait for the role to become ready before migrating another role.")
	return nil
}

func (o *workloadTypeOptions) update(ctx context.Context, rbg *workloadsv1alpha2.RoleBasedGroup) (*workloadsv1alpha2.RoleBasedGroup, error) {
	updated, err := o.rbgClient.WorkloadsV1alpha2().
		RoleBasedGroups(rbg.Namespace).
		Update(ctx, rbg, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("update RoleBasedGroup %s/%s: %w", rbg.Namespace, rbg.Name, err)
	}
	return updated, nil
}

func (o *workloadTypeOptions) listRolePods(ctx context.Context, namespace, rbgName, roleName string) ([]string, error) {
	selector := fmt.Sprintf("%s=%s,%s=%s",
		constants.GroupNameLabelKey, rbgName,
		constants.RoleNameLabelKey, roleName,
	)
	pods, err := o.k8sClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(pods.Items))
	for _, pod := range pods.Items {
		names = append(names, pod.Name)
	}
	return names, nil
}

func printPodsToRecreate(out io.Writer, namespace, rbgName, roleName, workloadType string, pods []string) {
	_, _ = fmt.Fprintf(out, "\nMigration will change workload type %q to RoleInstanceSet.\n", workloadType)
	_, _ = fmt.Fprintf(out, "RoleBasedGroup %s/%s role %q will have all %d Pod(s) recreated:\n", namespace, rbgName, roleName, len(pods))
	if len(pods) == 0 {
		_, _ = fmt.Fprintln(out, "  (none found)")
		return
	}
	for _, pod := range pods {
		_, _ = fmt.Fprintf(out, "  %s\n", pod)
	}
	_, _ = fmt.Fprintln(out)
}

func confirm(in io.Reader) (bool, error) {
	if in == nil {
		return false, errors.New("no input stream available for confirmation; use --yes in non-interactive mode")
	}
	fmt.Print("Proceed and recreate these Pods? Enter 'yes' to continue: ")
	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && answer == "" {
		return false, fmt.Errorf("read confirmation: %w", err)
	}
	return strings.TrimSpace(strings.ToLower(answer)) == "yes", nil
}

func materializeDefaultPattern(role *workloadsv1alpha2.RoleSpec) error {
	switch role.GetWorkloadType() {
	case "", constants.RoleInstanceSetWorkloadType:
		if role.StandalonePattern == nil && role.LeaderWorkerPattern == nil && role.CustomComponentsPattern == nil {
			role.StandalonePattern = &workloadsv1alpha2.StandalonePattern{}
		}
	case constants.DeploymentWorkloadType, constants.StatefulSetWorkloadType:
		if role.LeaderWorkerPattern != nil || role.CustomComponentsPattern != nil {
			return fmt.Errorf("role %q uses an incompatible Pattern; review it manually before migration", role.Name)
		}
		if role.StandalonePattern == nil {
			role.StandalonePattern = &workloadsv1alpha2.StandalonePattern{}
		}
	case constants.LeaderWorkerSetWorkloadType:
		if role.StandalonePattern != nil || role.CustomComponentsPattern != nil {
			return fmt.Errorf("role %q uses an incompatible Pattern; review it manually before migration", role.Name)
		}
		if role.LeaderWorkerPattern == nil {
			role.LeaderWorkerPattern = &workloadsv1alpha2.LeaderWorkerPattern{}
		}
	default:
		return fmt.Errorf("role %q uses unknown workload type %q", role.Name, role.GetWorkloadType())
	}
	return nil
}
