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

package v1alpha2

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/rbgs/api/workloads/constants"
)

func templateWithoutContainers(name string) RoleTemplate {
	return RoleTemplate{Name: name}
}

func TestRatchetAllowsUpdatesToInvalidDiscoveryConfigVersions(t *testing.T) {
	base := &RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "group"},
		Spec:       RoleBasedGroupSpec{Roles: []RoleSpec{{Name: "worker"}}},
	}
	oldRBG := base.DeepCopy()
	oldRBG.SetDiscoveryConfigVersion(constants.DiscoveryConfigVersion("v2"))

	updated := oldRBG.DeepCopy()
	updated.Labels = map[string]string{"team": "a"}
	validator := &RoleBasedGroupValidator{EnableDeprecatedWorkloadTypes: true, Client: fake.NewClientBuilder().Build()}
	_, err := validator.ValidateUpdate(context.Background(), oldRBG, updated)
	require.NoError(t, err, "a persisted invalid version must not block unrelated updates: %v", err)

	newlyInvalid := base.DeepCopy()
	newlyInvalid.SetDiscoveryConfigVersion(constants.DiscoveryConfigVersion("v2"))
	_, err = validator.ValidateUpdate(context.Background(), base, newlyInvalid)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported value")

	deleting := oldRBG.DeepCopy()
	deleting.DeletionTimestamp = ptr.To(metav1.Now())
	_, err = validator.ValidateUpdate(context.Background(), oldRBG, deleting)
	require.NoError(t, err, "deletion-carried updates must not be blocked by the version ratchet: %v", err)
}

func TestRatchetAllowsRBGSetUpdatesToInvalidDiscoveryConfigVersions(t *testing.T) {
	base := &RoleBasedGroupSet{
		ObjectMeta: metav1.ObjectMeta{Name: "set"},
		Spec: RoleBasedGroupSetSpec{
			Replicas: ptr.To(int32(1)),
		},
	}
	oldRBGS := base.DeepCopy()
	oldRBGS.Spec.GroupTemplate.Annotations = map[string]string{
		constants.DiscoveryConfigVersionAnnotationKey: "v2",
	}

	updated := oldRBGS.DeepCopy()
	updated.Labels = map[string]string{"team": "a"}
	validator := &RoleBasedGroupSetValidator{EnableDeprecatedWorkloadTypes: true}
	_, err := validator.ValidateUpdate(context.Background(), oldRBGS, updated)
	require.NoError(t, err, "a persisted invalid set version must not block unrelated updates: %v", err)

	newlyInvalid := base.DeepCopy()
	newlyInvalid.Spec.GroupTemplate.Annotations = map[string]string{
		constants.DiscoveryConfigVersionAnnotationKey: "v2",
	}
	_, err = validator.ValidateUpdate(context.Background(), base, newlyInvalid)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported value")

	deleting := oldRBGS.DeepCopy()
	deleting.DeletionTimestamp = ptr.To(metav1.Now())
	_, err = validator.ValidateUpdate(context.Background(), oldRBGS, deleting)
	require.NoError(t, err, "deletion-carried set updates must not be blocked by the version ratchet: %v", err)
}

// A persisted object with two invalid templates must stay partially fixable:
// repairing one template leaves the other violation pre-existing, so the
// update is admitted even though the aggregate error string changed.
func TestRatchetAllowsPartialTemplateRepairs(t *testing.T) {
	oldRBG := &RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "group"},
		Spec:       RoleBasedGroupSpec{RoleTemplates: []RoleTemplate{templateWithoutContainers("a"), templateWithoutContainers("b")}},
	}
	newRBG := oldRBG.DeepCopy()
	newRBG.Spec.RoleTemplates[0].Template.Spec.Containers = []corev1.Container{{Name: "app"}}

	validator := &RoleBasedGroupValidator{EnableDeprecatedWorkloadTypes: true, Client: fake.NewClientBuilder().Build()}
	_, err := validator.ValidateUpdate(context.Background(), oldRBG, newRBG)
	require.NoError(t, err, "remaining pre-existing violations must be ratcheted: %v", err)

	// Introducing a fresh violation is still rejected.
	freshRBG := oldRBG.DeepCopy()
	freshRBG.Spec.RoleTemplates = append(freshRBG.Spec.RoleTemplates, templateWithoutContainers("c"))
	_, err = validator.ValidateUpdate(context.Background(), oldRBG, freshRBG)
	require.Error(t, err)
	require.Contains(t, err.Error(), `spec.roleTemplates[2].template.spec.containers`)
}

// ratchetUpdateableSpec must compare the old snapshot under its own ownership:
// otherwise a standalone object with excluded ordinals produces the same
// managed-placement violation in both snapshots, and the newly forbidden
// configuration is admitted as pre-existing.
func TestRatchetValidatesOldSnapshotWithItsOwnOwnership(t *testing.T) {
	controller := true
	owner := metav1.OwnerReference{APIVersion: GroupVersion.String(), Kind: "RoleBasedGroupSet", Name: "set", UID: "set-uid", Controller: &controller}

	standaloneWithExclusions := func() *RoleBasedGroup {
		return &RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "group"},
			Spec: RoleBasedGroupSpec{Roles: []RoleSpec{{
				Name:        "worker",
				Annotations: map[string]string{constants.ExcludedOrdinalsAnnotationKey: "1"},
			}}},
		}
	}

	// Standalone with exclusions is valid on its own.
	validator := &RoleBasedGroupValidator{EnableDeprecatedWorkloadTypes: true, Client: fake.NewClientBuilder().Build()}
	_, err := validator.ValidateCreate(context.Background(), standaloneWithExclusions())
	require.NoError(t, err)

	// Adding the owner introduces the violation; it must not be ratcheted.
	managed := standaloneWithExclusions()
	managed.OwnerReferences = []metav1.OwnerReference{owner}
	_, err = validator.ValidateUpdate(context.Background(), standaloneWithExclusions(), managed)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not supported on RBGSet templates or RBGSet-managed RBGs")

	// A persisted managed violation stays updatable (ratcheted) and fixable.
	fixed := managed.DeepCopy()
	delete(fixed.Spec.Roles[0].Annotations, constants.ExcludedOrdinalsAnnotationKey)
	_, err = validator.ValidateUpdate(context.Background(), managed, fixed)
	require.NoError(t, err, "%v", err)
}
