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
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	kubefake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	rbgfake "sigs.k8s.io/rbgs/client-go/clientset/versioned/fake"
)

func TestPlanRoleClassifiesSafeAndDeprecatedWorkloadTypes(t *testing.T) {
	tests := []struct {
		workloadType string
		action       migrationAction
	}{
		{constants.RoleInstanceSetWorkloadType, actionSafeCleanup},
		{constants.DeploymentWorkloadType, actionWorkloadType},
		{constants.StatefulSetWorkloadType, actionWorkloadType},
		{constants.LeaderWorkerSetWorkloadType, actionWorkloadType},
	}
	for _, tt := range tests {
		role := &workloadsv1alpha2.RoleSpec{
			Name: "role",
			Annotations: map[string]string{
				constants.RoleWorkloadTypeAnnotationKey: tt.workloadType,
			},
		}
		got := planRole("RoleBasedGroup", "rbg", role)
		assert.Equal(t, tt.action, got.Action)
		assert.Equal(t, tt.workloadType, got.WorkloadType)
	}

	role := &workloadsv1alpha2.RoleSpec{Name: "role"}
	got := planRole("RoleBasedGroup", "rbg", role)
	assert.Equal(t, actionSafeCleanup, got.Action)
}

func TestRunPlanReportsBothRoleBasedGroupsAndSets(t *testing.T) {
	rbg := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "default"},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{{
				Name: "old",
				Annotations: map[string]string{
					constants.RoleWorkloadTypeAnnotationKey: constants.StatefulSetWorkloadType,
				},
			}},
		},
	}
	rbgs := &workloadsv1alpha2.RoleBasedGroupSet{
		ObjectMeta: metav1.ObjectMeta{Name: "rbgs", Namespace: "default"},
		Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
			GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{{
						Name: "safe",
						Annotations: map[string]string{
							constants.RoleWorkloadTypeAnnotationKey: constants.RoleInstanceSetWorkloadType,
						},
					}},
				},
			},
		},
	}

	var out bytes.Buffer
	client := rbgfake.NewSimpleClientset(rbg, rbgs)
	require.NoError(t, runPlan(&out, client, "default"))
	assert.Contains(t, out.String(), "RoleBasedGroup     rbg")
	assert.Contains(t, out.String(), "workload-type")
	assert.Contains(t, out.String(), "RoleBasedGroupSet  rbgs")
	assert.Contains(t, out.String(), "safe-cleanup")
}

func TestWorkloadTypeMigrationRequiresConfirmationAndUpdatesOneRole(t *testing.T) {
	rbg := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "default"},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{
				{
					Name: "old",
					Annotations: map[string]string{
						constants.RoleWorkloadTypeAnnotationKey: constants.StatefulSetWorkloadType,
					},
				},
				{
					Name: "keep",
					Annotations: map[string]string{
						constants.RoleWorkloadTypeAnnotationKey: constants.StatefulSetWorkloadType,
					},
				},
			},
		},
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:      "old-0",
		Namespace: "default",
		Labels: map[string]string{
			constants.GroupNameLabelKey: "rbg",
			constants.RoleNameLabelKey:  "old",
		},
	}}

	rbgClient := rbgfake.NewSimpleClientset(rbg)
	k8sClient := kubefake.NewSimpleClientset(pod)
	var out bytes.Buffer

	opts := &workloadTypeOptions{
		rbgClient: rbgClient,
		k8sClient: k8sClient,
		rbg:       "rbg",
		role:      "old",
		assumeYes: true,
		in:        strings.NewReader("no\n"),
		out:       &out,
	}
	require.NoError(t, opts.run(context.Background()))

	assert.Contains(t, out.String(), "old-0")
	updated, err := rbgClient.WorkloadsV1alpha2().RoleBasedGroups("default").
		Get(context.Background(), "rbg", metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, updated.Spec.Roles, 2)
	assert.NotContains(t, updated.Spec.Roles[0].Annotations, constants.RoleWorkloadTypeAnnotationKey)
	assert.NotNil(t, updated.Spec.Roles[0].StandalonePattern)
	assert.Contains(t, updated.Spec.Roles[1].Annotations, constants.RoleWorkloadTypeAnnotationKey)
}

func TestWorkloadTypeMigrationRedundantDefaultIsPureCleanup(t *testing.T) {
	rbg := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "default"},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{{
				Name: "role",
				Pattern: workloadsv1alpha2.Pattern{
					LeaderWorkerPattern: &workloadsv1alpha2.LeaderWorkerPattern{},
				},
				Annotations: map[string]string{
					constants.RoleWorkloadTypeAnnotationKey: constants.RoleInstanceSetWorkloadType,
				},
			}},
		},
	}
	rbgClient := rbgfake.NewSimpleClientset(rbg)
	var out bytes.Buffer
	opts := &workloadTypeOptions{
		rbgClient: rbgClient,
		k8sClient: kubefake.NewSimpleClientset(),
		rbg:       "rbg",
		role:      "role",
		assumeYes: true,
		out:       &out,
	}
	require.NoError(t, opts.run(context.Background()))
	updated, err := rbgClient.WorkloadsV1alpha2().RoleBasedGroups("default").
		Get(context.Background(), "rbg", metav1.GetOptions{})
	require.NoError(t, err)
	assert.NotContains(t, updated.Spec.Roles[0].Annotations, constants.RoleWorkloadTypeAnnotationKey)
	assert.NotNil(t, updated.Spec.Roles[0].LeaderWorkerPattern)
	assert.Nil(t, updated.Spec.Roles[0].StandalonePattern)
}

func TestWorkloadTypeMigrationCanCancel(t *testing.T) {
	rbg := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "default"},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{{
				Name: "old",
				Annotations: map[string]string{
					constants.RoleWorkloadTypeAnnotationKey: constants.DeploymentWorkloadType,
				},
			}},
		},
	}
	rbgClient := rbgfake.NewSimpleClientset(rbg)
	var out bytes.Buffer
	opts := &workloadTypeOptions{
		rbgClient: rbgClient,
		k8sClient: kubefake.NewSimpleClientset(),
		rbg:       "rbg",
		role:      "old",
		in:        strings.NewReader("no\n"),
		out:       &out,
	}
	require.NoError(t, opts.run(context.Background()))

	assert.Contains(t, out.String(), "Migration cancelled.")
	updated, err := rbgClient.WorkloadsV1alpha2().RoleBasedGroups("default").
		Get(context.Background(), "rbg", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Contains(t, updated.Spec.Roles[0].Annotations, constants.RoleWorkloadTypeAnnotationKey)
}

func TestMaterializeDefaultPattern(t *testing.T) {
	statefulRole := &workloadsv1alpha2.RoleSpec{
		Name: "stateful",
		Annotations: map[string]string{
			constants.RoleWorkloadTypeAnnotationKey: constants.StatefulSetWorkloadType,
		},
	}
	require.NoError(t, materializeDefaultPattern(statefulRole))
	assert.NotNil(t, statefulRole.StandalonePattern)

	lwsRole := &workloadsv1alpha2.RoleSpec{
		Name: "lws",
		Annotations: map[string]string{
			constants.RoleWorkloadTypeAnnotationKey: constants.LeaderWorkerSetWorkloadType,
		},
	}
	require.NoError(t, materializeDefaultPattern(lwsRole))
	assert.NotNil(t, lwsRole.LeaderWorkerPattern)

	incompatible := &workloadsv1alpha2.RoleSpec{
		Name: "incompatible",
		Pattern: workloadsv1alpha2.Pattern{
			LeaderWorkerPattern: &workloadsv1alpha2.LeaderWorkerPattern{},
		},
		Annotations: map[string]string{
			constants.RoleWorkloadTypeAnnotationKey: constants.StatefulSetWorkloadType,
		},
	}
	err := materializeDefaultPattern(incompatible)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "incompatible Pattern")
}

func TestStorageVersionMigrationRewritesAllObjectsThroughV1alpha2(t *testing.T) {
	rbg := &workloadsv1alpha2.RoleBasedGroup{ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "default"}}
	rbgs := &workloadsv1alpha2.RoleBasedGroupSet{ObjectMeta: metav1.ObjectMeta{Name: "rbgs", Namespace: "default"}}
	now := metav1.Now()
	deleting := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "deleting",
			Namespace:         "default",
			DeletionTimestamp: &now,
		},
	}
	client := rbgfake.NewSimpleClientset(rbg, rbgs, deleting)
	updateCount := 0
	client.PrependReactor("update", "*", func(action clienttesting.Action) (bool, runtime.Object, error) {
		updateCount++
		return false, nil, nil
	})

	var out bytes.Buffer
	require.NoError(t, runStorageVersionMigration(context.Background(), &out, client, "default"))
	assert.Equal(t, 2, updateCount)
	assert.Contains(t, out.String(), "Storage-version migration complete: 2 object(s)")
	assert.Contains(t, out.String(), "RoleBasedGroup")
}

func TestNewMigrateCommandRegistersSubcommandsAndHelpOrder(t *testing.T) {
	cmd := NewMigrateCmd(genericclioptions.NewConfigFlags(true))
	require.Len(t, cmd.Commands(), 3)
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, cmd.RunE(cmd, nil))
	assert.Contains(t, out.String(), "migrate plan")
	assert.Contains(t, out.String(), "migrate workload-type --rbg RBG --role ROLE")
	assert.Contains(t, out.String(), "migrate storage-version")
}
