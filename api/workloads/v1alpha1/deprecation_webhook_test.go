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

func TestWorkloadType(t *testing.T) {
	assert.Equal(t, constants.StatefulSetWorkloadType, workloadType(WorkloadSpec{
		APIVersion: "apps/v1",
		Kind:       "StatefulSet",
	}))
	assert.Empty(t, workloadType(WorkloadSpec{}))
}

func TestObjectKey(t *testing.T) {
	key, ok := objectKey(rbgsV1alpha1ForDeprecationTest(WorkloadSpec{}))
	require.True(t, ok)
	assert.Equal(t, "default", key.Namespace)
	assert.Equal(t, "rbg", key.Name)

	_, ok = objectKey(&runtimeUnknown{})
	assert.False(t, ok)
}

type runtimeUnknown struct {
	runtime.Object
}

var _ runtime.Object = runtimeUnknown{}

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

	t.Run("switching between deprecated types stays grandfathered by role name", func(t *testing.T) {
		// Pin the compatibility semantics: an existing role may switch between
		// deprecated workload types while migration is enabled. Renaming the role
		// (or introducing a new one) is the new use that gets rejected.
		switchedRole := oldRole.DeepCopy()
		switchedRole.Spec.Roles[0].Workload = WorkloadSpec{APIVersion: "apps/v1", Kind: "Deployment"}
		_, err := validator.ValidateUpdate(context.Background(), oldRole, switchedRole)
		require.NoError(t, err)
	})

	t.Run("a renamed role is a new use", func(t *testing.T) {
		renamedRole := oldRole.DeepCopy()
		renamedRole.Spec.Roles[0].Name = "renamed-worker"
		_, err := validator.ValidateUpdate(context.Background(), oldRole, renamedRole)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "new uses are not accepted")
	})
}
