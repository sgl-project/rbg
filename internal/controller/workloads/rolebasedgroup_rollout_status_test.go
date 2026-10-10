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

package workloads

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
	lwsrevision "sigs.k8s.io/lws/pkg/utils/revision"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/reconciler"
)

func rolloutTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, lwsv1.AddToScheme(scheme))
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	return scheme
}

func rolloutTestFixture(t *testing.T, kind string) (*workloadsv1alpha2.RoleBasedGroup, []client.Object) {
	t.Helper()
	apiVersion := appsv1.SchemeGroupVersion.String()
	switch kind {
	case "RoleInstanceSet":
		apiVersion = workloadsv1alpha2.GroupVersion.String()
	case "LeaderWorkerSet":
		apiVersion = lwsv1.GroupVersion.String()
	}
	rbg := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "group", Namespace: "default", UID: "rbg-uid", Generation: 2},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{Roles: []workloadsv1alpha2.RoleSpec{{
			Name: "role", Replicas: ptr.To(int32(1)),
			Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: apiVersion + "/" + kind},
		}}},
	}
	role := &rbg.Spec.Roles[0]
	meta := metav1.ObjectMeta{
		Name: rbg.GetWorkloadName(role), Namespace: rbg.Namespace, UID: "workload-uid", Generation: 2,
		Labels:          map[string]string{fmt.Sprintf(constants.RoleRevisionLabelKeyFmt, role.Name): "target"},
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(rbg, workloadsv1alpha2.GroupVersion.WithKind("RoleBasedGroup"))},
	}
	switch kind {
	case "Deployment":
		return rbg, []client.Object{&appsv1.Deployment{
			ObjectMeta: meta, Spec: appsv1.DeploymentSpec{Replicas: ptr.To(int32(1))},
			Status: appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1},
		}}
	case "StatefulSet":
		return rbg, []client.Object{rolloutTestStatefulSet(meta)}
	case "RoleInstanceSet":
		ris := &workloadsv1alpha2.RoleInstanceSet{
			ObjectMeta: meta,
			Spec: workloadsv1alpha2.RoleInstanceSetSpec{
				Replicas: ptr.To(int32(1)),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "role"}},
			},
			Status: workloadsv1alpha2.RoleInstanceSetStatus{
				ObservedGeneration: 2, Replicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1, UpdatedReadyReplicas: 1,
				UpdateRevision: "group-role-targetri",
			},
		}
		instance := &workloadsv1alpha2.RoleInstance{
			ObjectMeta: metav1.ObjectMeta{
				Name: "instance", Namespace: rbg.Namespace, UID: "instance-uid", Generation: 2,
				Labels: map[string]string{
					"app":                                 "role",
					appsv1.ControllerRevisionHashLabelKey: "targetri",
					constants.RoleInstanceOwnerLabelKey:   string(ris.UID),
				},
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(ris, workloadsv1alpha2.GroupVersion.WithKind("RoleInstanceSet"))},
			},
			Spec: workloadsv1alpha2.RoleInstanceSpec{Components: []workloadsv1alpha2.RoleInstanceComponent{{Name: "worker", Size: ptr.To(int32(1))}}},
			Status: workloadsv1alpha2.RoleInstanceStatus{
				ObservedGeneration: 2,
				ComponentStatuses:  []workloadsv1alpha2.RoleInstanceComponentStatus{{Name: "worker", Size: 1, ReadyReplicas: 1, UpdatedReplicas: 1}},
				Conditions:         []workloadsv1alpha2.RoleInstanceCondition{{Type: workloadsv1alpha2.RoleInstanceReady, Status: corev1.ConditionTrue}},
			},
		}
		return rbg, []client.Object{ris, instance}
	case "LeaderWorkerSet":
		lws := &lwsv1.LeaderWorkerSet{
			ObjectMeta: meta,
			Spec:       lwsv1.LeaderWorkerSetSpec{Replicas: ptr.To(int32(1)), LeaderWorkerTemplate: lwsv1.LeaderWorkerTemplate{Size: ptr.To(int32(2))}},
			Status:     lwsv1.LeaderWorkerSetStatus{Replicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1},
		}
		revision, err := lwsrevision.NewRevision(context.Background(), fake.NewClientBuilder().WithScheme(rolloutTestScheme(t)).Build(), lws, "")
		require.NoError(t, err)
		hash := revision.Labels[lwsv1.RevisionKey]
		leader := rolloutTestStatefulSet(metav1.ObjectMeta{
			Name: lws.Name, Namespace: lws.Namespace, UID: "leader-uid", Generation: 2,
			Labels:          map[string]string{lwsv1.RevisionKey: hash},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(lws, lwsv1.GroupVersion.WithKind("LeaderWorkerSet"))},
		})
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: lws.Name + "-0", Namespace: lws.Namespace, UID: "pod-uid",
				Labels:          map[string]string{lwsv1.RevisionKey: hash},
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(leader, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
		}
		worker := rolloutTestStatefulSet(metav1.ObjectMeta{
			Name: pod.Name, Namespace: pod.Namespace, UID: "worker-uid", Generation: 2,
			Labels:          map[string]string{lwsv1.RevisionKey: hash},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(pod, corev1.SchemeGroupVersion.WithKind("Pod"))},
		})
		return rbg, []client.Object{lws, leader, pod, worker}
	}
	t.Fatalf("unknown kind %s", kind)
	return nil, nil
}

func rolloutTestStatefulSet(meta metav1.ObjectMeta) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: meta, Spec: appsv1.StatefulSetSpec{Replicas: ptr.To(int32(1))},
		Status: appsv1.StatefulSetStatus{ObservedGeneration: 2, Replicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1},
	}
}

func TestRoleWorkloadRolloutComplete(t *testing.T) {
	for _, kind := range []string{"RoleInstanceSet", "Deployment", "StatefulSet", "LeaderWorkerSet"} {
		for _, test := range []string{"complete", "old hash", "missing hash", "unknown target", "foreign owner", "terminating", "replica drift", "old generation", "updating or paused", "missing workload", "wrong name", "long name"} {
			t.Run(kind+"/"+test, func(t *testing.T) {
				rbg, objects := rolloutTestFixture(t, kind)
				role := &rbg.Spec.Roles[0]
				expected := "target"
				switch test {
				case "old hash":
					objects[0].GetLabels()[fmt.Sprintf(constants.RoleRevisionLabelKeyFmt, role.Name)] = "old"
				case "missing hash":
					objects[0].SetLabels(nil)
				case "unknown target":
					expected = ""
				case "foreign owner":
					objects[0].GetOwnerReferences()[0].UID = "another-rbg"
				case "terminating":
					objects[0].SetDeletionTimestamp(ptr.To(metav1.Now()))
					objects[0].SetFinalizers([]string{"test-finalizer"})
				case "replica drift":
					role.Replicas = ptr.To(int32(2))
				case "old generation":
					if kind == "LeaderWorkerSet" {
						objects[1].SetGeneration(3)
					} else {
						objects[0].SetGeneration(3)
					}
				case "updating or paused":
					switch w := objects[0].(type) {
					case *workloadsv1alpha2.RoleInstanceSet:
						w.Spec.UpdateStrategy.Paused = true
						w.Status.UpdatedReplicas = 0
					case *appsv1.Deployment:
						w.Spec.Paused = true
						w.Status.UpdatedReplicas = 0
					case *appsv1.StatefulSet:
						w.Status.UpdatedReplicas = 0
					case *lwsv1.LeaderWorkerSet:
						w.Status.UpdatedReplicas = 0
					}
				case "missing workload":
					objects = objects[1:]
				case "wrong name":
					objects[0].SetName("not-the-target")
				case "long name":
					// Name lookup must use GetWorkloadName's 63-character truncation.
					rbg.Name = strings.Repeat("g", 60)
					objects[0].SetName(rbg.GetWorkloadName(role))
					if kind == "LeaderWorkerSet" {
						objects[1].SetName(objects[0].GetName())
						objects[2].SetName(objects[0].GetName() + "-0")
						objects[3].SetName(objects[2].GetName())
					}
				}
				c := fake.NewClientBuilder().WithScheme(rolloutTestScheme(t)).WithObjects(objects...).Build()
				r := &RoleBasedGroupReconciler{client: c, apiReader: c}
				complete, err := r.roleWorkloadRolloutComplete(context.Background(), rbg, role, expected)
				require.NoError(t, err)
				assert.Equal(t, test == "complete" || test == "long name", complete)
			})
		}
	}
}

func TestRoleInstanceSetRolloutChecksLiveInstances(t *testing.T) {
	for _, test := range []string{"complete", "full revision name", "stateful without owner label", "old revision", "paused with stale updated counts", "old instance generation", "components still updating", "missing component status", "not ready", "terminating", "missing instance", "foreign instance", "surplus instance", "old updated ready count", "missing update revision", "zero replicas"} {
		t.Run(test, func(t *testing.T) {
			rbg, objects := rolloutTestFixture(t, "RoleInstanceSet")
			ris := objects[0].(*workloadsv1alpha2.RoleInstanceSet)
			instance := objects[1].(*workloadsv1alpha2.RoleInstance)
			switch test {
			case "full revision name":
				instance.Labels[appsv1.ControllerRevisionHashLabelKey] = ris.Status.UpdateRevision
			case "stateful without owner label":
				delete(instance.Labels, constants.RoleInstanceOwnerLabelKey)
			case "old revision":
				instance.Labels[appsv1.ControllerRevisionHashLabelKey] = "old"
			case "paused with stale updated counts":
				// A pause must not let old, fully updated counts acknowledge a new spec.
				ris.Spec.UpdateStrategy.Paused = true
				ris.Generation++
			case "old instance generation":
				instance.Generation++
			case "components still updating":
				instance.Status.ComponentStatuses[0].UpdatedReplicas = 0
			case "missing component status":
				instance.Status.ComponentStatuses = nil
			case "not ready":
				instance.Status.Conditions = nil
			case "terminating":
				instance.DeletionTimestamp = ptr.To(metav1.Now())
				instance.Finalizers = []string{"test-finalizer"}
			case "missing instance":
				objects = objects[:1]
			case "foreign instance":
				instance.OwnerReferences[0].UID = "another-ris"
			case "surplus instance":
				extra := instance.DeepCopy()
				extra.Name = "extra"
				objects = append(objects, extra)
			case "old updated ready count":
				ris.Status.UpdatedReadyReplicas = 0
			case "missing update revision":
				ris.Status.UpdateRevision = ""
			case "zero replicas":
				rbg.Spec.Roles[0].Replicas = ptr.To(int32(0))
				ris.Spec.Replicas = ptr.To(int32(0))
				ris.Status = workloadsv1alpha2.RoleInstanceSetStatus{ObservedGeneration: 2}
				objects = objects[:1]
			}
			c := fake.NewClientBuilder().WithScheme(rolloutTestScheme(t)).WithObjects(objects...).Build()
			r := &RoleBasedGroupReconciler{client: c, apiReader: c}
			complete, err := r.roleWorkloadRolloutComplete(context.Background(), rbg, &rbg.Spec.Roles[0], "target")
			require.NoError(t, err)
			assert.Equal(t, test == "complete" || test == "full revision name" || test == "stateful without owner label" || test == "zero replicas", complete)
		})
	}
}

func TestLeaderWorkerSetRolloutChecksTargetRevisionChain(t *testing.T) {
	for _, test := range []string{"complete without condition observed generation", "old leader hash", "old pod hash", "old worker hash", "old worker generation", "worker updating", "worker replicas drift", "pod not ready", "foreign worker", "missing leader", "missing pod", "missing worker", "leader only"} {
		t.Run(test, func(t *testing.T) {
			rbg, objects := rolloutTestFixture(t, "LeaderWorkerSet")
			lws := objects[0].(*lwsv1.LeaderWorkerSet)
			worker := objects[3].(*appsv1.StatefulSet)
			switch test {
			case "complete without condition observed generation":
				lws.Status.Conditions = []metav1.Condition{{Type: string(lwsv1.LeaderWorkerSetAvailable), Status: metav1.ConditionTrue}}
			case "old leader hash":
				objects[1].GetLabels()[lwsv1.RevisionKey] = "old"
			case "old pod hash":
				objects[2].GetLabels()[lwsv1.RevisionKey] = "old"
			case "old worker hash":
				worker.Labels[lwsv1.RevisionKey] = "old"
			case "old worker generation":
				worker.Generation++
			case "worker updating":
				worker.Status.UpdatedReplicas = 0
			case "worker replicas drift":
				worker.Spec.Replicas = ptr.To(int32(2))
			case "pod not ready":
				objects[2].(*corev1.Pod).Status.Conditions = nil
			case "foreign worker":
				worker.OwnerReferences[0].UID = "another-pod"
			case "missing leader":
				objects = append(objects[:1], objects[2:]...)
			case "missing pod":
				objects = append(objects[:2], objects[3:]...)
			case "missing worker":
				objects = objects[:3]
			case "leader only":
				lws.Spec.LeaderWorkerTemplate.Size = ptr.To(int32(1))
				revision, err := lwsrevision.NewRevision(context.Background(), fake.NewClientBuilder().WithScheme(rolloutTestScheme(t)).Build(), lws, "")
				require.NoError(t, err)
				for _, obj := range objects[1:3] {
					obj.GetLabels()[lwsv1.RevisionKey] = revision.Labels[lwsv1.RevisionKey]
				}
				objects = objects[:3]
			}
			c := fake.NewClientBuilder().WithScheme(rolloutTestScheme(t)).WithObjects(objects...).Build()
			r := &RoleBasedGroupReconciler{client: c, apiReader: c}
			complete, err := r.roleWorkloadRolloutComplete(context.Background(), rbg, &rbg.Spec.Roles[0], "target")
			require.NoError(t, err)
			assert.Equal(t, test == "complete without condition observed generation" || test == "leader only", complete)
			// Hash computation must not create a ControllerRevision.
			revisions := &appsv1.ControllerRevisionList{}
			require.NoError(t, c.List(context.Background(), revisions))
			assert.Empty(t, revisions.Items)
		})
	}
}

func TestRolloutStatusAPIError(t *testing.T) {
	apiErr := errors.New("apiserver unavailable")
	for _, kind := range []string{"RoleInstanceSet", "LeaderWorkerSet"} {
		for _, operation := range []string{"get", "list"} {
			t.Run(kind+"/"+operation, func(t *testing.T) {
				rbg, objects := rolloutTestFixture(t, kind)
				c := fake.NewClientBuilder().WithScheme(rolloutTestScheme(t)).WithObjects(objects...).Build()
				reader := interceptor.NewClient(c, interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if operation == "get" {
							return apiErr
						}
						return c.Get(ctx, key, obj, opts...)
					},
					List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error { return apiErr },
				})
				r := &RoleBasedGroupReconciler{client: c, apiReader: reader}
				complete, err := r.roleWorkloadRolloutComplete(context.Background(), rbg, &rbg.Spec.Roles[0], "target")
				assert.False(t, complete)
				require.ErrorIs(t, err, apiErr)
			})
		}
	}
}

func TestConstructRoleStatusesPublishesRolloutBarrier(t *testing.T) {
	for _, test := range []string{"before workload update", "unobserved workload", "complete"} {
		t.Run(test, func(t *testing.T) {
			ctx := context.Background()
			rbg, objects := rolloutTestFixture(t, "RoleInstanceSet")
			rbg.Status.RoleStatuses = []workloadsv1alpha2.RoleStatus{{Name: "role", Replicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1}}
			rbg.Status.Conditions = []metav1.Condition{{
				Type: string(workloadsv1alpha2.RoleBasedGroupRollingUpdateInProgress), Status: metav1.ConditionFalse,
				ObservedGeneration: 1, Reason: "AllRolesUpdated", LastTransitionTime: metav1.Now(),
			}}
			if test == "unobserved workload" {
				ris := objects[0].(*workloadsv1alpha2.RoleInstanceSet)
				ris.Generation++
				ris.Status.Replicas, ris.Status.ReadyReplicas, ris.Status.UpdatedReplicas = 0, 0, 0
			}
			scheme := rolloutTestScheme(t)
			patches := 0
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(objects, rbg)...).
				WithStatusSubresource(rbg).WithInterceptorFuncs(interceptor.Funcs{
				SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
					patches++
					return c.SubResource(subresource).Patch(ctx, obj, patch, opts...)
				},
			}).Build()
			// Before Step 8, only apiReader sees that the target was not applied.
			// An unobserved workload instead exercises preserved old capacity counts.
			ris := objects[0].(*workloadsv1alpha2.RoleInstanceSet)
			if test == "before workload update" {
				ris.Labels[fmt.Sprintf(constants.RoleRevisionLabelKeyFmt, "role")] = "old"
			}
			reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
			r := &RoleBasedGroupReconciler{client: c, apiReader: reader, scheme: scheme, recorder: record.NewFakeRecorder(10),
				workloadReconciler: make(map[string]reconciler.WorkloadReconciler)}
			_, err := r.constructAndUpdateRoleStatuses(ctx, rbg, map[string]string{"role": "target"})
			require.NoError(t, err)
			assert.Equal(t, 1, patches)
			got := &workloadsv1alpha2.RoleBasedGroup{}
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(rbg), got))
			assert.Equal(t, rbg.Generation, got.Status.ObservedGeneration)
			assert.True(t, apimeta.IsStatusConditionTrue(got.Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupReady)))
			condition := apimeta.FindStatusCondition(got.Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupRollingUpdateInProgress))
			require.NotNil(t, condition)
			assert.Equal(t, rbg.Generation, condition.ObservedGeneration)
			assert.Equal(t, test != "complete", condition.Status == metav1.ConditionTrue)
		})
	}
}

func TestConstructRoleStatusesWaitsForEveryRole(t *testing.T) {
	ctx := context.Background()
	rbg, objects := rolloutTestFixture(t, "Deployment")
	first := objects[0].(*appsv1.Deployment)
	first.Labels[fmt.Sprintf(constants.RoleRevisionLabelKeyFmt, "role")] = "old"
	secondRole := *rbg.Spec.Roles[0].DeepCopy()
	secondRole.Name = "other"
	rbg.Spec.Roles = append(rbg.Spec.Roles, secondRole)
	second := first.DeepCopy()
	second.Name = rbg.GetWorkloadName(&secondRole)
	second.UID = "other-workload"
	second.Labels = map[string]string{fmt.Sprintf(constants.RoleRevisionLabelKeyFmt, secondRole.Name): "other-target"}
	scheme := rolloutTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(rbg, first, second).WithStatusSubresource(rbg).Build()
	r := &RoleBasedGroupReconciler{client: c, apiReader: c, scheme: scheme, recorder: record.NewFakeRecorder(10),
		workloadReconciler: make(map[string]reconciler.WorkloadReconciler)}
	expected := map[string]string{"role": "target", "other": "other-target"}
	_, err := r.constructAndUpdateRoleStatuses(ctx, rbg, expected)
	require.NoError(t, err)
	assert.True(t, apimeta.IsStatusConditionTrue(rbg.Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupReady)))
	assert.True(t, apimeta.IsStatusConditionTrue(rbg.Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupRollingUpdateInProgress)))

	// The last role being complete must not hide the first role still updating.
	current := &appsv1.Deployment{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(first), current))
	current.Labels[fmt.Sprintf(constants.RoleRevisionLabelKeyFmt, "role")] = "target"
	require.NoError(t, c.Update(ctx, current))
	_, err = r.constructAndUpdateRoleStatuses(ctx, rbg, expected)
	require.NoError(t, err)
	assert.True(t, apimeta.IsStatusConditionFalse(rbg.Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupRollingUpdateInProgress)))
}

func TestUpdateRBGStatusUpdatedReplicasOnly(t *testing.T) {
	ctx := context.Background()
	rbg, _ := rolloutTestFixture(t, "RoleInstanceSet")
	patches := 0
	c := fake.NewClientBuilder().WithScheme(rolloutTestScheme(t)).WithObjects(rbg).WithStatusSubresource(rbg).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				patches++
				return c.SubResource(subresource).Patch(ctx, obj, patch, opts...)
			},
		}).Build()
	r := &RoleBasedGroupReconciler{client: c, apiReader: c}
	statuses := []workloadsv1alpha2.RoleStatus{{Name: "role", Replicas: 1, ReadyReplicas: 1}}
	require.NoError(t, r.updateRBGStatus(ctx, rbg, statuses, false))
	statuses[0].UpdatedReplicas = 1
	require.NoError(t, r.updateRBGStatus(ctx, rbg, statuses, false))
	assert.Equal(t, 2, patches)
	got := &workloadsv1alpha2.RoleBasedGroup{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(rbg), got))
	assert.Equal(t, int32(1), got.Status.RoleStatuses[0].UpdatedReplicas)
	require.NoError(t, r.updateRBGStatus(ctx, rbg, statuses, false))
	assert.Equal(t, 2, patches, "unchanged status should not be patched")
	// Same condition status, new generation must still publish a fresh barrier.
	rbg.Generation++
	require.NoError(t, r.updateRBGStatus(ctx, rbg, statuses, false))
	assert.Equal(t, 3, patches)
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(rbg), got))
	condition := apimeta.FindStatusCondition(got.Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupRollingUpdateInProgress))
	require.NotNil(t, condition)
	assert.Equal(t, rbg.Generation, condition.ObservedGeneration)
}

func TestRBGPredicateMetadataOnlyUpdates(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*workloadsv1alpha2.RoleBasedGroup)
		want   bool
	}{
		{"annotation", func(rbg *workloadsv1alpha2.RoleBasedGroup) { rbg.Annotations = map[string]string{"strategy": "new"} }, true},
		{"label", func(rbg *workloadsv1alpha2.RoleBasedGroup) { rbg.Labels = map[string]string{"strategy": "new"} }, true},
		{"remove annotation", func(rbg *workloadsv1alpha2.RoleBasedGroup) { rbg.Annotations = nil }, true},
		{"remove label", func(rbg *workloadsv1alpha2.RoleBasedGroup) { rbg.Labels = nil }, true},
		{"spec", func(rbg *workloadsv1alpha2.RoleBasedGroup) { rbg.Spec.Roles = nil }, true},
		{"status only", func(rbg *workloadsv1alpha2.RoleBasedGroup) { rbg.Status.ObservedGeneration++ }, false},
		{"resource version only", func(rbg *workloadsv1alpha2.RoleBasedGroup) { rbg.ResourceVersion = "2" }, false},
		{"unchanged", func(*workloadsv1alpha2.RoleBasedGroup) {}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			old, _ := rolloutTestFixture(t, "RoleInstanceSet")
			old.Annotations = map[string]string{"strategy": "old"}
			old.Labels = map[string]string{"strategy": "old"}
			updated := old.DeepCopy()
			test.mutate(updated)
			assert.Equal(t, test.want, RBGPredicate().Update(event.UpdateEvent{ObjectOld: old, ObjectNew: updated}))
			assert.Equal(t, old.Generation, updated.Generation)
		})
	}
}
