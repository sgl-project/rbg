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

package v1alpha1

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/rbgs/api/workloads/constants"
)

func rbgsV1alpha1ForDeprecationTest(workload WorkloadSpec) *RoleBasedGroup {
	return &RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "default"},
		Spec: RoleBasedGroupSpec{Roles: []RoleSpec{{
			Name:     "worker",
			Workload: workload,
		}}},
	}
}

func TestDeprecationWarningValidatorReturnsAdmissionWarning(t *testing.T) {
	obj := rbgsV1alpha1ForDeprecationTest(WorkloadSpec{})
	warnings, err := DeprecationWarningValidator{}.ValidateCreate(context.Background(), obj)
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "v1alpha1 is deprecated")
	assert.Contains(t, warnings[0], "removed in 0.10.0")
	assert.Contains(t, warnings[0], "migration guide")
	assert.NotContains(t, warnings[0], "kubectl rbg migrate")
}

func TestDeprecationWarningValidatorReturnsAdmissionWarningOnDelete(t *testing.T) {
	obj := rbgsV1alpha1ForDeprecationTest(WorkloadSpec{})
	warnings, err := DeprecationWarningValidator{}.ValidateDelete(context.Background(), obj)
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "v1alpha1 is deprecated")

	deprecatedValidator := &deprecatedWorkloadValidator{EnableDeprecatedWorkloadTypes: true}
	warnings, err = deprecatedValidator.ValidateDelete(context.Background(), obj)
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "v1alpha1 is deprecated")
}

func TestDeprecatedWorkloadValidatorTransitionalAndFinalModes(t *testing.T) {
	oldRole := rbgsV1alpha1ForDeprecationTest(WorkloadSpec{
		APIVersion: "apps/v1",
		Kind:       "StatefulSet",
	})
	sameRole := oldRole.DeepCopy()

	validator := &deprecatedWorkloadValidator{EnableDeprecatedWorkloadTypes: true}
	warnings, err := validator.ValidateCreate(context.Background(), oldRole)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "new uses are not accepted")
	require.Len(t, warnings, 1)

	warnings, err = validator.ValidateUpdate(context.Background(), oldRole, sameRole)
	require.NoError(t, err)
	require.Len(t, warnings, 1)

	newRole := oldRole.DeepCopy()
	newRole.Spec.Roles[0].Workload = WorkloadSpec{
		APIVersion: "apps/v1",
		Kind:       "Deployment",
	}
	_, err = validator.ValidateUpdate(context.Background(), rbgsV1alpha1ForDeprecationTest(WorkloadSpec{
		APIVersion: "workloads.x-k8s.io/v1alpha2",
		Kind:       "RoleInstanceSet",
	}), newRole)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "new uses are not accepted")
}

func TestDeprecatedWorkloadValidatorFinalModeRejectsExistingUse(t *testing.T) {
	oldRole := rbgsV1alpha1ForDeprecationTest(WorkloadSpec{
		APIVersion: "apps/v1",
		Kind:       "StatefulSet",
	})
	sameRole := oldRole.DeepCopy()

	validator := &deprecatedWorkloadValidator{EnableDeprecatedWorkloadTypes: false}
	_, err := validator.ValidateCreate(context.Background(), oldRole)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deprecated and not enabled on this cluster")

	warnings, err := validator.ValidateUpdate(context.Background(), oldRole, sameRole)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deprecated and not enabled on this cluster")
	require.Len(t, warnings, 1)
}

func TestDeprecatedWorkloadValidatorRestartPolicyCompatibility(t *testing.T) {
	workloads := []struct {
		name       string
		spec       WorkloadSpec
		native     bool
		deprecated bool
	}{
		{name: "implicit RoleInstanceSet", native: true},
		{name: "RoleInstanceSet", spec: WorkloadSpec{APIVersion: "workloads.x-k8s.io/v1alpha2", Kind: "RoleInstanceSet"}, native: true},
		{name: "LeaderWorkerSet", spec: WorkloadSpec{APIVersion: "leaderworkerset.x-k8s.io/v1", Kind: "LeaderWorkerSet"}, deprecated: true},
		{name: "StatefulSet", spec: WorkloadSpec{APIVersion: "apps/v1", Kind: "StatefulSet"}, deprecated: true},
		{name: "Deployment", spec: WorkloadSpec{APIVersion: "apps/v1", Kind: "Deployment"}, deprecated: true},
		{name: "unsupported workload", spec: WorkloadSpec{APIVersion: "example.com/v1", Kind: "CustomWorkload"}},
	}
	for _, workload := range workloads {
		for _, pattern := range []string{"leaderWorker", "components", "standalone"} {
			for _, policy := range []RestartPolicyType{"", NoneRestartPolicy, RecreateRoleInstanceOnPodRestart, RecreateRoleInstanceOnPodRestartFromCreation} {
				for _, allowExisting := range []bool{true, false} {
					name := workload.name + "/" + pattern + "/" + string(policy)
					if !allowExisting {
						name += "/deprecated-disabled"
					}
					t.Run(name, func(t *testing.T) {
						role := RoleSpec{Name: "worker", Workload: workload.spec, RestartPolicy: policy}
						switch pattern {
						case "leaderWorker":
							role.LeaderWorkerSet = &LeaderWorkerTemplate{Size: ptr.To(int32(2))}
							role.TemplateSource = TemplateSource{Template: podTemplate("app")}
						case "components":
							role.Components = []InstanceComponent{{Name: "worker", Template: *podTemplate("app")}}
						case "standalone":
							role.TemplateSource = TemplateSource{Template: podTemplate("app")}
						}
						group := &RoleBasedGroup{Spec: RoleBasedGroupSpec{Roles: []RoleSpec{role}}}
						set := &RoleBasedGroupSet{Spec: RoleBasedGroupSetSpec{Template: group.Spec}}
						objects := []struct {
							name      string
							obj       runtime.Object
							fieldPath string
						}{
							{name: "RBG", obj: group, fieldPath: "spec.roles[0]"},
							{name: "RBGSet", obj: set, fieldPath: "spec.template.roles[0]"},
						}
						validator := &deprecatedWorkloadValidator{EnableDeprecatedWorkloadTypes: allowExisting}
						for _, object := range objects {
							t.Run(object.name, func(t *testing.T) {
								before := object.obj.DeepCopyObject()
								old := object.obj.DeepCopyObject()
								oldRoles, _, err := deprecatedWorkloadRoles(old)
								require.NoError(t, err)
								oldRoles[0].RestartPolicy = ""
								for _, update := range []bool{false, true} {
									t.Run(map[bool]string{false: "create", true: "update"}[update], func(t *testing.T) {
										var warnings []string
										var err error
										if update {
											warnings, err = validator.ValidateUpdate(context.Background(), old, object.obj)
										} else {
											warnings, err = validator.ValidateCreate(context.Background(), object.obj)
										}
										require.Len(t, warnings, 1)
										assert.Contains(t, warnings[0], "v1alpha1 is deprecated")
										switch {
										case policy == RecreateRoleInstanceOnPodRestartFromCreation && !workload.native:
											require.Error(t, err)
											assert.Contains(t, err.Error(), string(policy))
											assert.Contains(t, err.Error(), "RoleInstanceSet")
											assert.Contains(t, err.Error(), object.fieldPath)
											assert.NotContains(t, err.Error(), "deprecated", "compatibility must be checked before grandfathering")
										case workload.deprecated && (!update || !allowExisting):
											require.Error(t, err)
											assert.Contains(t, err.Error(), "deprecated")
										default:
											require.NoError(t, err, "native and grandfathered legacy policies must retain their behavior")
										}
									})
								}
								assert.Equal(t, before, object.obj, "validation must not mutate admission objects")
							})
						}
					})
				}
			}
		}
	}
}

func TestWorkloadType(t *testing.T) {
	assert.Equal(t, constants.StatefulSetWorkloadType, workloadType(WorkloadSpec{
		APIVersion: "apps/v1",
		Kind:       "StatefulSet",
	}))
	assert.Empty(t, workloadType(WorkloadSpec{}))
}

func TestDeprecatedWorkloadValidatorValidateUpdateTrustsAdmissionOldObject(t *testing.T) {
	// The validator deliberately performs no cache lookup: the admission request's
	// oldObj is the authoritative persisted state, already defaulted by the
	// v1alpha1 schema. A validator without a client therefore still honors the
	// grandfathered role.
	oldRole := rbgsV1alpha1ForDeprecationTest(WorkloadSpec{
		APIVersion: "apps/v1",
		Kind:       "StatefulSet",
	})
	validator := &deprecatedWorkloadValidator{EnableDeprecatedWorkloadTypes: true}

	t.Run("grandfathered role remains accepted without a persisted-object read", func(t *testing.T) {
		sameRole := oldRole.DeepCopy()
		sameRole.Spec.Roles[0].Replicas = ptr.To(int32(3))
		_, err := validator.ValidateUpdate(context.Background(), oldRole, sameRole)
		require.NoError(t, err)
	})

	t.Run("switching between deprecated types is a new use", func(t *testing.T) {
		// Pin the type-exact compatibility semantics: an existing role keeps its
		// own deprecated workload type, but switching to another deprecated type
		// provisions a new deprecated workload and is rejected. Migrate the role
		// to RoleInstanceSet instead.
		switchedRole := oldRole.DeepCopy()
		switchedRole.Spec.Roles[0].Workload = WorkloadSpec{APIVersion: "apps/v1", Kind: "Deployment"}
		_, err := validator.ValidateUpdate(context.Background(), oldRole, switchedRole)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "new uses are not accepted")
	})

	t.Run("a renamed role is a new use", func(t *testing.T) {
		renamedRole := oldRole.DeepCopy()
		renamedRole.Spec.Roles[0].Name = "renamed-worker"
		_, err := validator.ValidateUpdate(context.Background(), oldRole, renamedRole)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "new uses are not accepted")
	})
}
