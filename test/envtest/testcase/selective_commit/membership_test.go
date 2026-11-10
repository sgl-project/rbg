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

// Package selective_commit exercises API persistence and legacy field ownership
// with no competing controllers, so failed writes can be observed precisely.
package selective_commit

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/reconciler"
	wrappers "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

type membershipWrite struct {
	patchType types.PatchType
	body      map[string]interface{}
	force     bool
}

type guardedClient struct {
	client.Client
	fail        bool
	sawApply    bool
	beforePatch func()
	writes      []membershipWrite
}

// Match informer Get behavior for the ordinary workload/service builders.
func (c *guardedClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if err := c.Client.Get(ctx, key, obj, opts...); err != nil {
		return err
	}
	kinds, _, err := c.Scheme().ObjectKinds(obj)
	if err != nil {
		return err
	}
	if len(kinds) > 0 {
		obj.GetObjectKind().SetGroupVersionKind(kinds[0])
	}
	return nil
}
func (c *guardedClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	_, typed := obj.(*workloadsv1alpha2.RoleInstanceSet)
	if typed || obj.GetObjectKind().GroupVersionKind().Kind == "RoleInstanceSet" {
		raw, err := patch.Data(obj)
		if err != nil {
			return err
		}
		var body map[string]interface{}
		if err = json.Unmarshal(raw, &body); err != nil {
			return err
		}
		options := &client.PatchOptions{}
		for _, opt := range opts {
			opt.ApplyToPatch(options)
		}
		c.writes = append(c.writes, membershipWrite{patchType: patch.Type(), body: body, force: ptr.Deref(options.Force, false)})
		if patch.Type() == types.ApplyPatchType {
			c.sawApply = true
		}
		if c.beforePatch != nil {
			f := c.beforePatch
			c.beforePatch = nil
			f()
		}
		if c.fail {
			return apierrors.NewConflict(workloadsv1alpha2.GroupVersion.WithResource("roleinstancesets").GroupResource(), obj.GetName(), fmt.Errorf("injected concurrent update"))
		}
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}

func assertConditionalMembershipWrite(t *testing.T, writes []membershipWrite, resourceVersion string, replicas int32, record interface{}) {
	t.Helper()
	require.Len(t, writes, 1, "the complete child target must use one patch")
	write := writes[0]
	require.Equal(t, types.MergePatchType, write.patchType)
	require.False(t, write.force, "feature synchronization must not force apply")
	metadata, ok := write.body["metadata"].(map[string]interface{})
	require.True(t, ok)
	require.NotEmpty(t, resourceVersion)
	require.Equal(t, resourceVersion, metadata["resourceVersion"])
	spec, ok := write.body["spec"].(map[string]interface{})
	require.True(t, ok)
	require.Contains(t, spec, "replicas")
	require.EqualValues(t, replicas, spec["replicas"])
	annotations, ok := metadata["annotations"].(map[string]interface{})
	require.True(t, ok)
	require.Contains(t, annotations, constants.CommittedSkipOrdinalsAnnotationKey, "record clearing must be explicit null")
	require.Equal(t, record, annotations[constants.CommittedSkipOrdinalsAnnotationKey])
}

func assertOrdinaryApplyWrite(t *testing.T, writes []membershipWrite, resourceVersion string) {
	t.Helper()
	require.Len(t, writes, 1)
	require.Equal(t, types.ApplyPatchType, writes[0].patchType)
	require.True(t, writes[0].force)
	metadata, ok := writes[0].body["metadata"].(map[string]interface{})
	require.True(t, ok)
	require.NotEmpty(t, resourceVersion)
	require.Equal(t, resourceVersion, metadata["resourceVersion"])
}

func TestConditionalMembershipWithLegacyApplyOwnership(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	environment := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join(filepath.Dir(file), "../../../../config/crd/bases")}, ErrorIfCRDPathMissing: true}
	cfg, err := environment.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, environment.Stop()) })
	scheme := apiruntime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "selective-commit"}}))
	role := wrappers.BuildStandaloneRole("worker").WithReplicas(5).Obj()
	role.Annotations = map[string]string{"example.org/role-setting": "old"}
	parent := wrappers.BuildBasicRoleBasedGroup("group", "selective-commit").WithRoles([]workloadsv1alpha2.RoleSpec{role}).Obj()
	require.NoError(t, c.Create(ctx, parent))
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(parent), parent))
	wrapped := &guardedClient{Client: c}
	r := reconciler.NewRoleInstanceSetReconciler(scheme, wrapped)
	r.SetAPIReader(c)
	sync := func() error {
		// Standalone typed clients clear GVK; normal controller informer objects
		// carry it for the legacy workload/service owner-reference builders.
		parent.SetGroupVersionKind(workloadsv1alpha2.GroupVersion.WithKind("RoleBasedGroup"))
		return r.Reconciler(ctx, parent, &parent.Spec.Roles[0], nil, "stable-hash")
	}
	// Establish the exact legacy ownership that would make omitting fields from
	// a subsequent apply dangerous. The next feature reconcile must avoid apply.
	require.NoError(t, sync())
	require.NoError(t, sync())
	require.True(t, wrapped.sawApply)
	key := client.ObjectKey{Name: parent.GetWorkloadName(&parent.Spec.Roles[0]), Namespace: parent.Namespace}
	get := func() *workloadsv1alpha2.RoleInstanceSet {
		s := &workloadsv1alpha2.RoleInstanceSet{}
		require.NoError(t, c.Get(ctx, key, s))
		return s
	}
	initial := get()
	require.Equal(t, int32(5), *initial.Spec.Replicas)
	require.NotEmpty(t, initial.ManagedFields)
	parent.Spec.Roles[0].Replicas = ptr.To[int32](3)
	parent.Spec.Roles[0].Annotations[constants.ExcludedOrdinalsAnnotationKey] = "1,3"
	require.NoError(t, c.Update(ctx, parent))
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(parent), parent))
	wrapped.fail = true
	wrapped.sawApply = false
	wrapped.writes = nil
	require.True(t, apierrors.IsConflict(sync()))
	assertConditionalMembershipWrite(t, wrapped.writes, initial.ResourceVersion, 3, "1,3")
	require.False(t, wrapped.sawApply, "ordinary sync must not run before or after failed commitment")
	failed := get()
	require.Equal(t, int32(5), *failed.Spec.Replicas)
	require.Empty(t, failed.Annotations[constants.CommittedSkipOrdinalsAnnotationKey])
	// A real API conflict, caused by another writer changing the child between
	// read and patch, must also preserve the old pair.
	wrapped.fail = false
	wrapped.writes = nil
	wrapped.beforePatch = func() {
		latest := get()
		if latest.Annotations == nil {
			latest.Annotations = map[string]string{}
		}
		latest.Annotations["example.org/external"] = "keep"
		require.NoError(t, c.Update(ctx, latest, client.FieldOwner("external")))
	}
	require.True(t, apierrors.IsConflict(sync()))
	assertConditionalMembershipWrite(t, wrapped.writes, failed.ResourceVersion, 3, "1,3")
	failed = get()
	require.Equal(t, int32(5), *failed.Spec.Replicas)
	require.Empty(t, failed.Annotations[constants.CommittedSkipOrdinalsAnnotationKey])
	// A fresh reconciler instance models restart: only stored records are used.
	r = reconciler.NewRoleInstanceSetReconciler(scheme, wrapped)
	r.SetAPIReader(c)
	expectedVersion := get().ResourceVersion
	wrapped.writes = nil
	require.NoError(t, sync())
	assertConditionalMembershipWrite(t, wrapped.writes, expectedVersion, 3, "1,3")
	committed := get()
	require.Equal(t, int32(3), *committed.Spec.Replicas)
	require.Equal(t, "1,3", committed.Annotations[constants.CommittedSkipOrdinalsAnnotationKey])
	members, err := committed.CommittedOrdinals()
	require.NoError(t, err)
	require.Equal(t, []int{0, 2, 4}, members)
	require.Equal(t, "keep", committed.Annotations["example.org/external"])
	require.NotContains(t, committed.Annotations, constants.ExcludedOrdinalsAnnotationKey)
	// Editing or emptying E at fixed replicas must not reinterpret stored M,
	// nor re-enter forced SSA, even when the computed child target is unchanged.
	for _, value := range []string{"0,2,4", ""} {
		parent.Spec.Roles[0].Annotations[constants.ExcludedOrdinalsAnnotationKey] = value
		require.NoError(t, c.Update(ctx, parent))
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(parent), parent))
		wrapped.writes = nil
		wrapped.sawApply = false
		require.NoError(t, sync())
		require.False(t, wrapped.sawApply)
		require.Empty(t, wrapped.writes, "unchanged child target must not be rewritten")
		current := get()
		currentMembers, err := current.CommittedOrdinals()
		require.NoError(t, err)
		require.Equal(t, []int{0, 2, 4}, currentMembers)
		require.Equal(t, int32(3), *current.Spec.Replicas)
		require.Equal(t, committed.ResourceVersion, current.ResourceVersion)
	}
	// Unrelated child-level lifecycle controls must survive parent synchronization.
	committed.Spec.Lifecycle = &workloadsv1alpha2.RoleInstanceSetLifecycle{PreDelete: &workloadsv1alpha2.RoleInstanceSetLifecycleHook{LabelsHandler: map[string]string{"example.org/drain": "hold"}}}
	require.NoError(t, c.Update(ctx, committed, client.FieldOwner("lifecycle-operator")))
	delete(parent.Spec.Roles[0].Annotations, constants.ExcludedOrdinalsAnnotationKey)
	delete(parent.Spec.Roles[0].Annotations, "example.org/role-setting")
	require.NoError(t, c.Update(ctx, parent))
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(parent), parent))
	expectedVersion = get().ResourceVersion
	wrapped.writes = nil
	wrapped.sawApply = false
	for i := 0; i < 3; i++ {
		require.NoError(t, sync())
	}
	require.False(t, wrapped.sawApply, "a stored sparse record keeps the conditional writer after E removal")
	assertConditionalMembershipWrite(t, wrapped.writes, expectedVersion, 3, "1,3")
	kept := get()
	require.Equal(t, "1,3", kept.Annotations[constants.CommittedSkipOrdinalsAnnotationKey])
	require.NotNil(t, kept.Spec.Lifecycle)
	require.NotContains(t, kept.Annotations, "example.org/role-setting")
	// Repeated ordinary sync must settle rather than writing defaulted fields forever.
	before := kept.ResourceVersion
	require.NoError(t, sync())
	require.Equal(t, before, get().ResourceVersion)
	// Filling the gaps makes the record empty, after which legacy sync is safe.
	parent.Spec.Roles[0].Replicas = ptr.To[int32](5)
	require.NoError(t, c.Update(ctx, parent))
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(parent), parent))
	expectedVersion = get().ResourceVersion
	wrapped.writes = nil
	require.NoError(t, sync())
	assertConditionalMembershipWrite(t, wrapped.writes, expectedVersion, 5, nil)
	dense := get()
	require.Empty(t, dense.Annotations[constants.CommittedSkipOrdinalsAnnotationKey])
	require.Equal(t, int32(5), *dense.Spec.Replicas)
	wrapped.writes = nil
	wrapped.sawApply = false
	require.NoError(t, sync())
	require.True(t, wrapped.sawApply, "dense roles without E return to ordinary SSA")
	assertOrdinaryApplyWrite(t, wrapped.writes, dense.ResourceVersion)
	require.Empty(t, get().Annotations[constants.CommittedSkipOrdinalsAnnotationKey])
	require.Equal(t, int32(5), *get().Spec.Replicas)
	// Ordinary synchronization that read a dense target cannot overwrite a
	// sparse target committed before its apply reaches the API server.
	parent.Spec.Roles[0].Replicas = ptr.To[int32](4)
	require.NoError(t, c.Update(ctx, parent))
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(parent), parent))
	expectedVersion = get().ResourceVersion
	wrapped.writes = nil
	wrapped.beforePatch = func() {
		latest := get()
		latest.Spec.Replicas = ptr.To[int32](3)
		latest.Annotations[constants.CommittedSkipOrdinalsAnnotationKey] = "1,3"
		require.NoError(t, c.Update(ctx, latest, client.FieldOwner("membership-race")))
	}
	require.True(t, apierrors.IsConflict(sync()), "stale ordinary apply must fail its resourceVersion precondition")
	assertOrdinaryApplyWrite(t, wrapped.writes, expectedVersion)
	raced := get()
	require.Equal(t, int32(3), *raced.Spec.Replicas)
	require.Equal(t, "1,3", raced.Annotations[constants.CommittedSkipOrdinalsAnnotationKey])
	wrapped.writes = nil
	require.NoError(t, sync())
	assertConditionalMembershipWrite(t, wrapped.writes, raced.ResourceVersion, 4, "3")
	raced = get()
	members, err = raced.CommittedOrdinals()
	require.NoError(t, err)
	require.Equal(t, []int{0, 1, 2, 4}, members)

	// A same-name child with a new UID must start from its own record, never
	// inherit the target calculated for the deleted child.
	wrapped.beforePatch = func() {
		old := get()
		require.NoError(t, c.Delete(ctx, old, client.Preconditions{UID: &old.UID}))
		replacement := old.DeepCopy()
		replacement.ResourceVersion = ""
		replacement.UID = ""
		replacement.CreationTimestamp = metav1.Time{}
		replacement.ManagedFields = nil
		replacement.Spec.Replicas = ptr.To[int32](1)
		delete(replacement.Annotations, constants.CommittedSkipOrdinalsAnnotationKey)
		require.NoError(t, c.Create(ctx, replacement))
	}
	// Force an ordinary template change so the conditional writer has work.
	parent.Spec.Roles[0].MinReadySeconds = 1
	require.NoError(t, c.Update(ctx, parent))
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(parent), parent))
	require.True(t, apierrors.IsConflict(sync()))
	replacement := get()
	require.NotEqual(t, initial.UID, replacement.UID)
	require.Equal(t, int32(1), *replacement.Spec.Replicas)
	require.NoError(t, sync())
	replacement = get()
	members, err = replacement.CommittedOrdinals()
	require.NoError(t, err)
	require.Equal(t, []int{0, 1, 2, 3}, members)

}
