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

package statefulmode

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	instancelisters "sigs.k8s.io/rbgs/client-go/listers/workloads/v1alpha2"
)

// fixedRevisionControl keeps the A/B history fixed while exercising the real
// update phases and status writer through the public Reconcile entry point.
type fixedRevisionControl struct {
	core                  *defaultStatefulInstanceSetControl
	current, intermediate *apps.ControllerRevision
	listCalls, syncCalls  int
}

func (c *fixedRevisionControl) ListRevisions(_ *workloadsv1alpha2.RoleInstanceSet) ([]*apps.ControllerRevision, error) {
	c.listCalls++
	return []*apps.ControllerRevision{c.current, c.intermediate}, nil
}

func (c *fixedRevisionControl) AdoptOrphanRevisions(_ *workloadsv1alpha2.RoleInstanceSet, _ []*apps.ControllerRevision) error {
	return fmt.Errorf("unexpected adoption of owned test revisions")
}

func (c *fixedRevisionControl) UpdateStatefulInstanceSet(ctx context.Context, set *workloadsv1alpha2.RoleInstanceSet, instances []*workloadsv1alpha2.RoleInstance) error {
	c.syncCalls++
	status, err := c.core.updateStatefulInstanceSet(ctx, set, c.current, c.current, 0,
		instances, []*apps.ControllerRevision{c.current, c.intermediate})
	if err != nil {
		return err
	}
	return c.core.statusUpdater.UpdateInstanceSetStatus(ctx, set, status)
}

type cachedRollbackReconciler struct {
	set           *workloadsv1alpha2.RoleInstanceSet
	setCache      cache.Indexer
	instanceCache cache.Indexer
	api           client.Client
	control       *fixedRevisionControl
	objects       *fakeInstanceObjectManager
	reconciler    *ReconcileStatefulInstanceSet
	intentVersion string
	request       reconcile.Request
}

func newCachedRollbackReconciler(t *testing.T) *cachedRollbackReconciler {
	t.Helper()
	set := newRevisionTestSet("nginx:1.0")
	set.Name, set.UID = "rollback-cache-lag", types.UID(t.Name())
	set.ResourceVersion = "1"
	set.Spec.Replicas = ptr.To[int32](1)
	set.Spec.PodManagementPolicy = constants.ParallelPodManagement
	set.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": set.Name}}
	set.Spec.UpdateStrategy.Type = workloadsv1alpha2.RecreatePodUpdateStrategyType
	set.Spec.UpdateStrategy.MaxSurge = ptr.To(intstr.FromInt32(1))
	set.Spec.UpdateStrategy.MaxUnavailable = ptr.To(intstr.FromInt32(0))
	current, err := newRevision(set, 1, ptr.To[int32](0))
	require.NoError(t, err)
	set.Spec.RoleInstanceTemplate.Components[0].Template.Spec.Containers[0].Image = "nginx:2.0"
	intermediate, err := newRevision(set, 2, ptr.To[int32](0))
	require.NoError(t, err)
	set.Spec.RoleInstanceTemplate.Components[0].Template.Spec.Containers[0].Image = "nginx:1.0"
	set.Status.CurrentRevision, set.Status.UpdateRevision = current.Name, intermediate.Name

	h := &cachedRollbackReconciler{
		set:           set,
		setCache:      cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc}),
		instanceCache: cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc}),
		objects:       &fakeInstanceObjectManager{},
		request:       reconcile.Request{NamespacedName: types.NamespacedName{Namespace: set.Namespace, Name: set.Name}},
	}
	require.NoError(t, h.setCache.Add(set.DeepCopy()))
	scheme := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	h.api = fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(set).WithObjects(set).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				err := c.SubResource(subresource).Patch(ctx, obj, patch, opts...)
				if err == nil {
					h.intentVersion = obj.GetResourceVersion()
				}
				return err
			},
		}).Build()
	setLister := instancelisters.NewRoleInstanceSetLister(h.setCache)
	recorder := record.NewFakeRecorder(64)
	h.control = &fixedRevisionControl{
		current: current, intermediate: intermediate,
		core: &defaultStatefulInstanceSetControl{
			instanceControl: NewStatefulInstanceControlFromManager(h.objects, recorder),
			inplaceControl:  &fakeInplaceControl{},
			statusUpdater:   NewRealStatefulInstanceSetStatusUpdater(h.api, setLister),
			recorder:        recorder,
		},
	}
	h.reconciler = &ReconcileStatefulInstanceSet{
		control:               h.control,
		instanceControl:       &realInstanceControl{client: h.api, recorder: recorder},
		roleInstanceSetLister: setLister,
		roleInstanceLister:    instancelisters.NewRoleInstanceLister(h.instanceCache),
	}
	t.Cleanup(func() {
		rolloutStatusExpectations.Delete(set)
		updateExpectations.DeleteExpectations(getInstanceSetKey(set))
		durationStore.Pop(getInstanceSetKey(set))
		instanceUnhealthySince.Delete(h.request.NamespacedName)
	})
	return h
}

func (h *cachedRollbackReconciler) instance(ordinal int, revision string, ready bool) *workloadsv1alpha2.RoleInstance {
	instance := buildInst(h.set.Name, ordinal, revision, ready, true)
	instance.Labels["app"] = h.set.Name
	instance.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(h.set, controllerKind)}
	return instance
}

func (h *cachedRollbackReconciler) reconcile(t *testing.T) reconcile.Result {
	t.Helper()
	h.objects.created, h.objects.deleted = nil, nil
	result, err := h.reconciler.Reconcile(context.Background(), h.request)
	require.NoError(t, err, "instance operations before return: created=%v, deleted=%v", h.objects.created, h.objects.deleted)
	return result
}

func (h *cachedRollbackReconciler) advanceSetCache(t *testing.T) *workloadsv1alpha2.RoleInstanceSet {
	t.Helper()
	stored := &workloadsv1alpha2.RoleInstanceSet{}
	require.NoError(t, h.api.Get(context.Background(), client.ObjectKeyFromObject(h.set), stored))
	require.NoError(t, h.setCache.Update(stored))
	return stored
}

func TestReconcileWaitsForPersistedReplacementStateInSetCache(t *testing.T) {
	h := newCachedRollbackReconciler(t)
	base := h.instance(0, h.control.intermediate.Name, true)
	surge := h.instance(1, h.control.current.Name, true)
	require.NoError(t, h.instanceCache.Add(base))
	require.NoError(t, h.instanceCache.Add(surge))
	h.reconcile(t)
	require.Equal(t, []string{base.Name}, h.objects.deleted)
	require.Equal(t, "2", h.intentVersion, "the real writer must receive the new resource version from the API")
	require.Equal(t, 1, h.control.syncCalls)
	require.Equal(t, 1, h.control.listCalls)

	// The RI informer sees deletion first, while the independent set informer
	// still has RV 1 and no replacement state. No reconcile work may run yet.
	require.NoError(t, h.instanceCache.Delete(base))
	cached, err := h.reconciler.roleInstanceSetLister.RoleInstanceSets(h.set.Namespace).Get(h.set.Name)
	require.NoError(t, err)
	require.Equal(t, "1", cached.ResourceVersion)
	require.Nil(t, cached.Status.RolloutReplacementState)
	result := h.reconcile(t)
	require.Positive(t, result.RequeueAfter)
	require.Equal(t, 1, h.control.syncCalls, "stale set cache must block the core update")
	require.Equal(t, 1, h.control.listCalls, "the gate must precede revision adoption")
	require.Empty(t, h.objects.created)
	require.Empty(t, h.objects.deleted)

	// Delivering the set watch event resumes the real update phases. The base
	// is recreated, and its persisted replacement intent keeps surge alive.
	stored := h.advanceSetCache(t)
	require.NotNil(t, stored.Status.RolloutReplacementState)
	h.reconcile(t)
	require.Equal(t, 2, h.control.syncCalls)
	require.Equal(t, []string{base.Name}, h.objects.created)
	require.Empty(t, h.objects.deleted)

	replacement := h.instance(0, h.control.current.Name, false)
	replacement.Generation = 1
	require.NoError(t, h.instanceCache.Add(replacement))
	h.advanceSetCache(t)
	h.reconcile(t)
	require.Empty(t, h.objects.deleted)

	ready := replacement.DeepCopy()
	ready.Status.Conditions[0].Status = v1.ConditionTrue
	ready.Status.ObservedGeneration = ready.Generation
	require.NoError(t, h.instanceCache.Update(ready))
	h.advanceSetCache(t)
	h.reconcile(t)
	require.Equal(t, []string{surge.Name}, h.objects.deleted)
	stored = h.advanceSetCache(t)
	require.Nil(t, stored.Status.RolloutReplacementState)
}

func TestForgetRolloutStatusExpectationPreservesRecreatedSet(t *testing.T) {
	for _, tombstone := range []bool{false, true} {
		t.Run(fmt.Sprintf("tombstone=%t", tombstone), func(t *testing.T) {
			old := &workloadsv1alpha2.RoleInstanceSet{ObjectMeta: metav1.ObjectMeta{
				Namespace: "default", Name: "recreated-set", UID: types.UID(t.Name() + "/old"), ResourceVersion: "1",
			}}
			newSet := old.DeepCopy()
			newSet.UID = types.UID(t.Name() + "/new")
			for _, set := range []*workloadsv1alpha2.RoleInstanceSet{old, newSet} {
				written := set.DeepCopy()
				written.ResourceVersion = "2"
				rolloutStatusExpectations.Expect(written)
				t.Cleanup(func() { rolloutStatusExpectations.Delete(set) })
			}
			var deleted interface{} = old
			if tombstone {
				deleted = cache.DeletedFinalStateUnknown{Key: "default/recreated-set", Obj: old}
			}
			forgetRolloutStatusExpectation(deleted)
			satisfied, _ := rolloutStatusExpectations.IsSatisfied(old)
			require.True(t, satisfied, "deletion must remove the old UID expectation")
			satisfied, _ = rolloutStatusExpectations.IsSatisfied(newSet)
			require.False(t, satisfied, "deleting the old UID must not release the recreated set")
		})
	}
}
