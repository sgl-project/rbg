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

package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	wrappers "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

func selectiveTestObjects(t *testing.T, replicas int32, e string) (*workloadsv1alpha2.RoleBasedGroup, *workloadsv1alpha2.RoleInstanceSet, *runtime.Scheme) {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	role := wrappers.BuildStandaloneRole("worker").WithReplicas(replicas).Obj()
	role.Annotations = map[string]string{constants.ExcludedOrdinalsAnnotationKey: e}
	group := wrappers.BuildBasicRoleBasedGroup("selective", "default").WithRoles([]workloadsv1alpha2.RoleSpec{role}).Obj()
	group.UID = "parent-uid"
	group.Generation = 1
	group.APIVersion = workloadsv1alpha2.GroupVersion.String()
	group.Kind = "RoleBasedGroup"
	child := &workloadsv1alpha2.RoleInstanceSet{ObjectMeta: metav1.ObjectMeta{Name: group.GetWorkloadName(&role), Namespace: group.Namespace, UID: "child-uid", OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(group, workloadsv1alpha2.GroupVersion.WithKind("RoleBasedGroup"))}}, Spec: workloadsv1alpha2.RoleInstanceSetSpec{Replicas: ptr.To[int32](5)}}
	return group, child, scheme
}

// Record real conditional patches, not only their successful fake responses.
// A rejected patch must leave both count and record at their previous values.
type membershipPatchClient struct {
	client.Client
	fail    bool
	patches []map[string]interface{}
}

func (c *membershipPatchClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if _, ok := obj.(*workloadsv1alpha2.RoleInstanceSet); ok {
		raw, err := patch.Data(obj)
		if err != nil {
			return err
		}
		content := map[string]interface{}{}
		if err = json.Unmarshal(raw, &content); err != nil {
			return err
		}
		c.patches = append(c.patches, content)
		if c.fail {
			return apierrors.NewConflict(workloadsv1alpha2.GroupVersion.WithResource("roleinstancesets").GroupResource(), obj.GetName(), fmt.Errorf("concurrent update"))
		}
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}

func TestSelectiveMembershipCommitsCountAndRecordTogether(t *testing.T) {
	ctx := context.Background()
	group, child, scheme := selectiveTestObjects(t, 3, "1,3")
	c := &membershipPatchClient{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(group, child).Build(), fail: true}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(group), group))
	r := NewRoleInstanceSetReconciler(scheme, c)
	// The ordinary public reconcile path must not force-apply replicas first.
	err := r.reconcileRoleInstanceSet(ctx, group, &group.Spec.Roles[0], nil, "stable-hash")
	require.True(t, apierrors.IsConflict(err), "%v", err)
	actual := &workloadsv1alpha2.RoleInstanceSet{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(child), actual))
	require.Equal(t, int32(5), *actual.Spec.Replicas)
	require.Empty(t, actual.Annotations[constants.CommittedSkipOrdinalsAnnotationKey])
	require.Len(t, c.patches, 1)
	patch := c.patches[0]
	require.EqualValues(t, 3, patch["spec"].(map[string]interface{})["replicas"])
	metadata := patch["metadata"].(map[string]interface{})
	require.NotEmpty(t, metadata["resourceVersion"])
	require.Equal(t, "1,3", metadata["annotations"].(map[string]interface{})[constants.CommittedSkipOrdinalsAnnotationKey])
	// Retry must observe both the latest role configuration and committed child.
	latest := &workloadsv1alpha2.RoleBasedGroup{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(group), latest))
	latest.Spec.Roles[0].Annotations[constants.ExcludedOrdinalsAnnotationKey] = "0,2"
	latest.Generation++
	require.NoError(t, c.Update(ctx, latest))
	c.fail = false
	require.Error(t, r.reconcileRoleInstanceSet(ctx, group, &group.Spec.Roles[0], nil, "stable-hash"), "stale parent must not be committed")
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(group), latest))
	require.NoError(t, r.reconcileRoleInstanceSet(ctx, latest, &latest.Spec.Roles[0], nil, "stable-hash"))
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(child), actual))
	members, err := actual.CommittedOrdinals()
	require.NoError(t, err)
	require.Equal(t, []int{1, 3, 4}, members)
	require.Equal(t, "0,2", actual.Annotations[constants.ExcludedOrdinalsAnnotationKey])
	// Clearing input and repeating normal sync cannot clear or reinterpret M.
	delete(latest.Spec.Roles[0].Annotations, constants.ExcludedOrdinalsAnnotationKey)
	require.NoError(t, c.Update(ctx, latest))
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(group), latest))
	for i := 0; i < 3; i++ {
		require.NoError(t, r.reconcileRoleInstanceSet(ctx, latest, &latest.Spec.Roles[0], nil, "stable-hash"))
	}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(child), actual))
	members, err = actual.CommittedOrdinals()
	require.NoError(t, err)
	require.Equal(t, []int{1, 3, 4}, members)
}

func TestSelectiveInitialCreationAndFixedCount(t *testing.T) {
	group, _, scheme := selectiveTestObjects(t, 3, "1,3")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(group).Build()
	ctx := context.Background()
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(group), group))
	r := NewRoleInstanceSetReconciler(scheme, c)
	require.NoError(t, r.reconcileRoleInstanceSet(ctx, group, &group.Spec.Roles[0], nil, "stable"))
	actual := &workloadsv1alpha2.RoleInstanceSet{}
	key := types.NamespacedName{Namespace: group.Namespace, Name: group.GetWorkloadName(&group.Spec.Roles[0])}
	require.NoError(t, c.Get(ctx, key, actual))
	members, err := actual.CommittedOrdinals()
	require.NoError(t, err)
	require.Equal(t, []int{0, 2, 4}, members)
	// Fake client doesn't allocate UIDs. Give this newly created object its API identity.
	actual.UID = "new-child"
	require.NoError(t, c.Update(ctx, actual))
	group.Spec.Roles[0].Annotations[constants.ExcludedOrdinalsAnnotationKey] = "0,2,4"
	require.NoError(t, c.Update(ctx, group))
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(group), group))
	require.NoError(t, r.reconcileRoleInstanceSet(ctx, group, &group.Spec.Roles[0], nil, "stable"))
	require.NoError(t, c.Get(ctx, key, actual))
	members, err = actual.CommittedOrdinals()
	require.NoError(t, err)
	require.Equal(t, []int{0, 2, 4}, members)
	for _, component := range actual.Spec.RoleInstanceTemplate.Components {
		require.NotContains(t, component.Template.Annotations, constants.ExcludedOrdinalsAnnotationKey)
		require.NotContains(t, component.Template.Annotations, constants.CommittedSkipOrdinalsAnnotationKey)
	}
}

func TestSelectiveInvalidRecordAndWrongOwnerBlockCommit(t *testing.T) {
	for _, scenario := range []string{"invalid record", "old owner", "orphan", "invalid input", "stateless", "deleting parent", "deleting child", "stateless child"} {
		t.Run(scenario, func(t *testing.T) {
			group, child, scheme := selectiveTestObjects(t, 3, "1,3")
			child.Annotations = map[string]string{constants.CommittedSkipOrdinalsAnnotationKey: "1"}
			switch scenario {
			case "invalid record":
				child.Annotations[constants.CommittedSkipOrdinalsAnnotationKey] = "3-1"
			case "old owner":
				child.OwnerReferences[0].UID = "previous-parent"
			case "orphan":
				child.OwnerReferences = nil
			case "invalid input":
				group.Spec.Roles[0].Annotations[constants.ExcludedOrdinalsAnnotationKey] = "1,,3"
			case "stateless":
				group.Spec.Roles[0].Annotations[constants.RoleInstancePatternKey] = string(constants.StatelessPattern)
			case "deleting parent":
				now := metav1.Now()
				group.DeletionTimestamp = &now
				group.Finalizers = []string{"example.org/hold"}
			case "deleting child":
				now := metav1.Now()
				child.DeletionTimestamp = &now
				child.Finalizers = []string{"example.org/hold"}
			case "stateless child":
				child.Annotations[constants.RoleInstancePatternKey] = string(constants.StatelessPattern)
			}
			before := child.DeepCopy()
			c := &membershipPatchClient{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(group, child).Build()}
			ctx := context.Background()
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(group), group))
			r := NewRoleInstanceSetReconciler(scheme, c)
			require.Error(t, r.reconcileRoleInstanceSet(ctx, group, &group.Spec.Roles[0], nil, "stable"))
			require.Empty(t, c.patches)
			actual := &workloadsv1alpha2.RoleInstanceSet{}
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(child), actual))
			require.Equal(t, int32(5), *actual.Spec.Replicas)
			require.Equal(t, before.Annotations, actual.Annotations)
			require.Equal(t, before.OwnerReferences, actual.OwnerReferences)
		})
	}
}

// Cache lag must not disable the conditional writer after E has been removed.
type staleMembershipClient struct {
	client.Client
	stale *workloadsv1alpha2.RoleInstanceSet
}

func (c *staleMembershipClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if set, ok := obj.(*workloadsv1alpha2.RoleInstanceSet); ok && key.Name == c.stale.Name {
		*set = *c.stale.DeepCopy()
		return nil
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func TestSelectiveWriterUsesUncachedRecordAfterInputRemoval(t *testing.T) {
	group, child, scheme := selectiveTestObjects(t, 4, "")
	delete(group.Spec.Roles[0].Annotations, constants.ExcludedOrdinalsAnnotationKey)
	stale := child.DeepCopy() // old dense count 5 in the informer cache
	child.Spec.Replicas = ptr.To[int32](3)
	child.Annotations = map[string]string{constants.CommittedSkipOrdinalsAnnotationKey: "1,3"}
	backend := fake.NewClientBuilder().WithScheme(scheme).WithObjects(group, child).Build()
	ctx := context.Background()
	require.NoError(t, backend.Get(ctx, client.ObjectKeyFromObject(group), group))
	c := &staleMembershipClient{Client: backend, stale: stale}
	r := NewRoleInstanceSetReconciler(scheme, c)
	r.SetAPIReader(backend)
	require.NoError(t, r.reconcileRoleInstanceSet(ctx, group, &group.Spec.Roles[0], nil, "stable"))
	actual := &workloadsv1alpha2.RoleInstanceSet{}
	require.NoError(t, backend.Get(ctx, client.ObjectKeyFromObject(child), actual))
	members, err := actual.CommittedOrdinals()
	require.NoError(t, err)
	require.Equal(t, []int{0, 1, 2, 4}, members)
}

func TestSelectiveStatusOnlyParentUpdateDoesNotBlockCommit(t *testing.T) {
	ctx := context.Background()
	group, child, scheme := selectiveTestObjects(t, 3, "1,3")
	c := &membershipPatchClient{Client: fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(group).WithObjects(group, child).Build()}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(group), group))
	latest := group.DeepCopy()
	latest.Status.Conditions = []metav1.Condition{{
		Type: "Ready", Status: metav1.ConditionFalse, Reason: "Progressing",
		LastTransitionTime: metav1.Now(),
	}}
	require.NoError(t, c.Status().Update(ctx, latest))
	require.NotEqual(t, group.ResourceVersion, latest.ResourceVersion)
	require.Equal(t, group.Generation, latest.Generation)
	r := NewRoleInstanceSetReconciler(scheme, c)
	require.NoError(t, r.reconcileRoleInstanceSet(ctx, group, &group.Spec.Roles[0], nil, "stable"))
	require.Len(t, c.patches, 1)
	actual := &workloadsv1alpha2.RoleInstanceSet{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(child), actual))
	members, err := actual.CommittedOrdinals()
	require.NoError(t, err)
	require.Equal(t, []int{0, 2, 4}, members)
}

func TestSelectiveDenseOrphanAdoption(t *testing.T) {
	for _, tc := range []struct {
		name, record    string
		wrongLabel, bad bool
	}{
		{name: "dense orphan"},
		{name: "sparse orphan", record: "1", bad: true},
		{name: "unrelated orphan", wrongLabel: true, bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			group, child, scheme := selectiveTestObjects(t, 3, "1,3")
			otherOwner := metav1.OwnerReference{APIVersion: "v1", Kind: "ConfigMap", Name: "observer", UID: "observer-uid"}
			child.OwnerReferences = []metav1.OwnerReference{otherOwner}
			child.Labels = map[string]string{constants.GroupNameLabelKey: group.Name}
			child.Annotations = map[string]string{constants.CommittedSkipOrdinalsAnnotationKey: tc.record}
			if tc.wrongLabel {
				child.Labels[constants.GroupNameLabelKey] = "unrelated"
			}
			c := &membershipPatchClient{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(group, child).Build()}
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(group), group))
			r := NewRoleInstanceSetReconciler(scheme, c)
			err := r.reconcileRoleInstanceSet(ctx, group, &group.Spec.Roles[0], nil, "stable")
			actual := &workloadsv1alpha2.RoleInstanceSet{}
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(child), actual))
			if tc.bad {
				require.Error(t, err)
				require.Empty(t, c.patches)
				require.Equal(t, child.Spec.Replicas, actual.Spec.Replicas)
				require.Equal(t, child.Annotations, actual.Annotations)
				require.Equal(t, child.OwnerReferences, actual.OwnerReferences)
				return
			}
			require.NoError(t, err)
			require.Len(t, c.patches, 1)
			require.Equal(t, child.UID, actual.UID)
			require.Equal(t, group.UID, metav1.GetControllerOf(actual).UID)
			require.Contains(t, actual.OwnerReferences, otherOwner)
			require.Equal(t, "1,3", actual.Annotations[constants.CommittedSkipOrdinalsAnnotationKey])
			require.Equal(t, int32(3), *actual.Spec.Replicas)
			metadata := c.patches[0]["metadata"].(map[string]interface{})
			require.Contains(t, metadata, "ownerReferences")
			require.NotEmpty(t, metadata["resourceVersion"])
			require.EqualValues(t, 3, c.patches[0]["spec"].(map[string]interface{})["replicas"])
			require.Equal(t, "1,3", metadata["annotations"].(map[string]interface{})[constants.CommittedSkipOrdinalsAnnotationKey])
		})
	}
}

func TestSelectiveRejectsChangedParentIdentityOrGeneration(t *testing.T) {
	for _, changedUID := range []bool{false, true} {
		t.Run(fmt.Sprintf("changedUID=%v", changedUID), func(t *testing.T) {
			ctx := context.Background()
			observed, child, scheme := selectiveTestObjects(t, 3, "1,3")
			latest := observed.DeepCopy()
			if changedUID {
				latest.UID = "replacement-parent"
			} else {
				latest.Generation++
				latest.Spec.Roles[0].Replicas = ptr.To[int32](4)
			}
			c := &membershipPatchClient{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(latest, child).Build()}
			r := NewRoleInstanceSetReconciler(scheme, c)
			err := r.reconcileRoleInstanceSet(ctx, observed, &observed.Spec.Roles[0], nil, "stable")
			require.True(t, apierrors.IsConflict(err), "%v", err)
			require.Empty(t, c.patches)
		})
	}
}

func TestRemovePreviouslyOwnedMetadata(t *testing.T) {
	entry := func(manager string, operation metav1.ManagedFieldsOperationType, subresource, kind, key string) metav1.ManagedFieldsEntry {
		raw, err := json.Marshal(map[string]interface{}{
			"f:metadata": map[string]interface{}{"f:" + kind: map[string]interface{}{"f:" + key: map[string]interface{}{}}},
		})
		require.NoError(t, err)
		return metav1.ManagedFieldsEntry{Manager: manager, Operation: operation, Subresource: subresource, FieldsType: "FieldsV1", FieldsV1: &metav1.FieldsV1{Raw: raw}}
	}
	for _, kind := range []string{"annotations", "labels"} {
		t.Run(kind, func(t *testing.T) {
			own := entry("rbg", metav1.ManagedFieldsOperationApply, "", kind, "example.org/key")
			external := entry("operator", metav1.ManagedFieldsOperationApply, "", kind, "example.org/key")
			otherKind := "labels"
			if kind == otherKind {
				otherKind = "annotations"
			}
			for _, tc := range []struct {
				name    string
				entries []metav1.ManagedFieldsEntry
				desired bool
				keep    bool
			}{
				{name: "exclusive-apply", entries: []metav1.ManagedFieldsEntry{own}},
				{name: "exclusive-update", entries: []metav1.ManagedFieldsEntry{entry("rbg", metav1.ManagedFieldsOperationUpdate, "", kind, "example.org/key")}},
				{name: "shared-apply", entries: []metav1.ManagedFieldsEntry{own, external}, keep: true},
				{name: "shared-update-reversed", entries: []metav1.ManagedFieldsEntry{entry("operator", metav1.ManagedFieldsOperationUpdate, "", kind, "example.org/key"), own}, keep: true},
				{name: "foreign-only", entries: []metav1.ManagedFieldsEntry{external}, keep: true},
				{name: "still-desired", entries: []metav1.ManagedFieldsEntry{own}, desired: true, keep: true},
				{name: "separate-kind", entries: []metav1.ManagedFieldsEntry{own, entry("operator", metav1.ManagedFieldsOperationApply, "", otherKind, "example.org/key")}},
				{name: "ignore-status", entries: []metav1.ManagedFieldsEntry{own, entry("operator", metav1.ManagedFieldsOperationUpdate, "status", kind, "example.org/key")}},
				{name: "unreadable-ownership", entries: []metav1.ManagedFieldsEntry{own, {Manager: "operator", FieldsV1: &metav1.FieldsV1{Raw: []byte("{")}}}, keep: true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					old := &workloadsv1alpha2.RoleInstanceSet{ObjectMeta: metav1.ObjectMeta{ManagedFields: tc.entries}}
					old.Labels = map[string]string{"example.org/key": "value"}
					old.Annotations = map[string]string{"example.org/key": "value"}
					desired := &workloadsv1alpha2.RoleInstanceSet{}
					if tc.desired {
						desired.Labels = map[string]string{"example.org/key": "value"}
						desired.Annotations = map[string]string{"example.org/key": "value"}
					}
					updated := old.DeepCopy()
					removePreviouslyOwnedMetadata(old, updated, desired)
					result, untouched := updated.Annotations, updated.Labels
					if kind == "labels" {
						result, untouched = updated.Labels, updated.Annotations
					}
					_, present := result["example.org/key"]
					require.Equal(t, tc.keep, present)
					require.Equal(t, "value", untouched["example.org/key"], "ownership of another metadata kind must not affect this key")
				})
			}
		})
	}
	old := &workloadsv1alpha2.RoleInstanceSet{ObjectMeta: metav1.ObjectMeta{
		Annotations:   map[string]string{constants.CommittedSkipOrdinalsAnnotationKey: "1,3"},
		ManagedFields: []metav1.ManagedFieldsEntry{entry("rbg", metav1.ManagedFieldsOperationUpdate, "", "annotations", constants.CommittedSkipOrdinalsAnnotationKey)},
	}}
	updated := old.DeepCopy()
	removePreviouslyOwnedMetadata(old, updated, &workloadsv1alpha2.RoleInstanceSet{})
	require.Equal(t, "1,3", updated.Annotations[constants.CommittedSkipOrdinalsAnnotationKey])
}
