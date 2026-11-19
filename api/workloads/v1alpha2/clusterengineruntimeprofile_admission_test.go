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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"sigs.k8s.io/rbgs/api/workloads/constants"
)

// admissionHarness builds a validator over a fake reader seeded with the
// given objects, mirroring the manager wiring (the uncached API reader).
func admissionHarness(t *testing.T, objs ...client.Object) *ClusterEngineRuntimeProfileValidator {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, AddToScheme(scheme))
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return &ClusterEngineRuntimeProfileValidator{Reader: reader}
}

func profileFor(name string) *ClusterEngineRuntimeProfile {
	return &ClusterEngineRuntimeProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       ClusterEngineRuntimeProfileSpec{UpdateStrategy: "NoUpdate"},
	}
}

func rbgReferencing(name, namespace, profileName string) *RoleBasedGroup {
	rbg := &RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: RoleBasedGroupSpec{Roles: []RoleSpec{{
			Name:           "worker",
			EngineRuntimes: []EngineRuntime{{ProfileName: profileName}},
		}}},
	}
	// Every admitted write carries the label the webhook's scoped lookup
	// selects on (see the defaulters and SetHasEngineRuntimesLabel).
	SetHasEngineRuntimesLabel(rbg)
	return rbg
}

func rbgsReferencing(name, namespace, profileName string, replicas int32) *RoleBasedGroupSet {
	rbgs := &RoleBasedGroupSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: RoleBasedGroupSetSpec{
			// zero replicas is the load-bearing case: a set's group
			// template is a live reference with no child RBG required
			Replicas: &replicas,
			GroupTemplate: RoleBasedGroupTemplateSpec{Spec: RoleBasedGroupSpec{
				Roles: []RoleSpec{{
					Name:           "worker",
					EngineRuntimes: []EngineRuntime{{ProfileName: profileName}},
				}},
			}},
		},
	}
	SetRoleBasedGroupSetHasEngineRuntimesLabel(rbgs)
	return rbgs
}

// TestClusterEngineRuntimeProfileValidator pins the KEP-531 Profile
// Mutability freeze: creation is unrestricted, metadata stays editable, and
// spec edits plus deletes are rejected exactly while a live reference
// exists — an RBG role or a RoleBasedGroupSet group template, the latter
// even at zero replicas. Rejection messages name every reference and point
// at the new-profile migration flow.
func TestClusterEngineRuntimeProfileValidator(t *testing.T) {
	ctx := context.Background()

	t.Run("create is unrestricted even with a same-named reference elsewhere", func(t *testing.T) {
		v := admissionHarness(t)
		_, err := v.ValidateCreate(ctx, profileFor("ik"))
		require.NoError(t, err)
	})

	t.Run("a spec edit while unreferenced is admitted", func(t *testing.T) {
		v := admissionHarness(t)
		old := profileFor("ik")
		edited := profileFor("ik")
		edited.Spec.UpdateStrategy = "RollingUpdate"
		_, err := v.ValidateUpdate(ctx, old, edited)
		require.NoError(t, err)
	})

	t.Run("a metadata-only edit while referenced is admitted", func(t *testing.T) {
		v := admissionHarness(t, rbgReferencing("grp", "ns", "ik"))
		old := profileFor("ik")
		edited := profileFor("ik")
		edited.Labels = map[string]string{"tier": "gold"}
		_, err := v.ValidateUpdate(ctx, old, edited)
		require.NoError(t, err, "labels, annotations and other metadata stay editable under the freeze")
	})

	t.Run("a spec edit while referenced is rejected naming the reference", func(t *testing.T) {
		v := admissionHarness(t,
			rbgReferencing("grp", "ns-a", "ik"),
			rbgReferencing("grp-b", "ns-b", "ik"),
			rbgReferencing("other-grp", "ns-b", "other-profile"),
		)
		old := profileFor("ik")
		edited := profileFor("ik")
		edited.Spec.UpdateStrategy = "RollingUpdate"
		_, err := v.ValidateUpdate(ctx, old, edited)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `"ik" is frozen`)
		assert.Contains(t, err.Error(), "RoleBasedGroup ns-a/grp")
		assert.Contains(t, err.Error(), "RoleBasedGroup ns-b/grp-b",
			"the message lists every referencing object")
		assert.NotContains(t, err.Error(), "ns-b/other-grp",
			"objects referencing a different profile are not named")
		assert.Contains(t, err.Error(), "new profile")
	})

	t.Run("delete while referenced is rejected; delete while unreferenced is admitted", func(t *testing.T) {
		referenced := admissionHarness(t, rbgReferencing("grp", "ns-a", "ik"))
		_, err := referenced.ValidateDelete(ctx, profileFor("ik"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "delete is rejected")

		unreferenced := admissionHarness(t)
		_, err = unreferenced.ValidateDelete(ctx, profileFor("ik"))
		require.NoError(t, err)
	})

	t.Run("a zero-replica RBGSet group template is a live reference", func(t *testing.T) {
		v := admissionHarness(t, rbgsReferencing("set", "ns-a", "ik", 0))
		_, err := v.ValidateDelete(ctx, profileFor("ik"))
		require.Error(t, err, "the group template references the profile with no child RBG at all")
		assert.Contains(t, err.Error(), "RoleBasedGroupSet ns-a/set")
	})

	t.Run("the unconverged fallback sees pre-label references", func(t *testing.T) {
		// The upgrade window: a referencing object written before the label
		// shipped is invisible to the scoped lookup, so admission falls
		// back to the unscoped one until the controller reports
		// convergence (#597).
		unlabeled := rbgReferencing("grp-old", "ns-old", "ik")
		delete(unlabeled.Labels, constants.HasEngineRuntimesLabelKey)

		v := admissionHarness(t, unlabeled)
		_, err := v.ValidateDelete(ctx, profileFor("ik"))
		require.Error(t, err, "the unconverged fallback must not miss a pre-label reference")

		SetEngineRuntimeLabelConverged(true)
		defer SetEngineRuntimeLabelConverged(false)
		v = admissionHarness(t, unlabeled)
		_, err = v.ValidateDelete(ctx, profileFor("ik"))
		require.NoError(t, err,
			"once converged, only labeled references are visible; the window is documented in KEP-531")
	})

	t.Run("a spec edit is admitted again once the last reference leaves", func(t *testing.T) {
		v := admissionHarness(t)
		old := profileFor("ik")
		edited := profileFor("ik")
		edited.Spec.UpdateStrategy = "RollingUpdate"
		_, err := v.ValidateUpdate(ctx, old, edited)
		require.NoError(t, err, "the freeze lifts with the last reference")
	})
}

// TestListEngineRuntimeProfileReferences pins the reference scan both
// admission and the reference-tracking controller share: all namespaces, both
// kinds, and profileName only — a role naming any other profile never
// freezes this one.
func TestListEngineRuntimeProfileReferences(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	require.NoError(t, AddToScheme(scheme))
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		rbgReferencing("grp-a", "ns-a", "ik"),
		rbgReferencing("grp-b", "ns-b", "ik"),
		rbgReferencing("grp-c", "ns-c", "someone-else"),
		rbgsReferencing("set-a", "ns-a", "ik", 0),
		rbgsReferencing("set-b", "ns-b", "someone-else", 3),
	).Build()

	refs, err := ListEngineRuntimeProfileReferences(ctx, reader, "ik")
	require.NoError(t, err)
	require.Len(t, refs, 3)
	strings := make([]string, len(refs))
	for i := range refs {
		strings[i] = refs[i].String()
	}
	assert.ElementsMatch(t, []string{
		"RoleBasedGroup ns-a/grp-a",
		"RoleBasedGroup ns-b/grp-b",
		"RoleBasedGroupSet ns-a/set-a",
	}, strings)

	t.Run("the unscoped variant also finds pre-label objects", func(t *testing.T) {
		// The upgrade path: an object written before the label shipped (or
		// past the admission defaulters) is invisible to the scoped lookup
		// but remains a live reference for the controller's authority scan.
		unlabeled := rbgReferencing("grp-old", "ns-old", "ik")
		delete(unlabeled.Labels, constants.HasEngineRuntimesLabelKey)
		unscoped := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			unlabeled,
			rbgsReferencing("set-old", "ns-old", "ik", 0),
		).Build()

		scoped, err := ListEngineRuntimeProfileReferences(ctx, unscoped, "ik")
		require.NoError(t, err)
		assert.Len(t, scoped, 1, "only the labeled set is visible to the scoped lookup")

		all, err := ListAllEngineRuntimeProfileReferences(ctx, unscoped, "ik")
		require.NoError(t, err)
		allStrings := make([]string, len(all))
		for i := range all {
			allStrings[i] = all[i].String()
		}
		assert.ElementsMatch(t, []string{
			"RoleBasedGroup ns-old/grp-old",
			"RoleBasedGroupSet ns-old/set-old",
		}, allStrings)
	})
}
