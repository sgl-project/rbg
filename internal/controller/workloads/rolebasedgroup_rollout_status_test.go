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
	"maps"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
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
	"sigs.k8s.io/rbgs/pkg/scheduler"
	gangcommon "sigs.k8s.io/rbgs/pkg/scheduler/common"
	"sigs.k8s.io/rbgs/pkg/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
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
		revision.UID = "lws-revision-uid"
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
		return rbg, []client.Object{lws, leader, pod, worker, revision}
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
						objects[4].SetName(objects[0].GetName() + "-revision")
						objects[4].GetLabels()[lwsv1.SetNameLabelKey] = objects[0].GetName()
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
				objects = append(objects[:3], objects[4:]...)
			case "leader only":
				lws.Spec.LeaderWorkerTemplate.Size = ptr.To(int32(1))
				revision, err := lwsrevision.NewRevision(context.Background(), fake.NewClientBuilder().WithScheme(rolloutTestScheme(t)).Build(), lws, "")
				require.NoError(t, err)
				for _, obj := range objects[1:3] {
					obj.GetLabels()[lwsv1.RevisionKey] = revision.Labels[lwsv1.RevisionKey]
				}
				revision.UID = objects[4].GetUID()
				objects[4] = revision
				objects = append(objects[:3], objects[4:]...)
			}
			c := fake.NewClientBuilder().WithScheme(rolloutTestScheme(t)).WithObjects(objects...).Build()
			r := &RoleBasedGroupReconciler{client: c, apiReader: c}
			complete, err := r.roleWorkloadRolloutComplete(context.Background(), rbg, &rbg.Spec.Roles[0], "target")
			require.NoError(t, err)
			assert.Equal(t, test == "complete without condition observed generation" || test == "leader only", complete)
			// The completion check reads the persisted revision; it must not create another.
			revisions := &appsv1.ControllerRevisionList{}
			require.NoError(t, c.List(context.Background(), revisions))
			assert.Len(t, revisions.Items, 1)
		})
	}
}

func TestLeaderWorkerSetRolloutWaitsForPersistedLiveRevision(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*lwsv1.LeaderWorkerSet)
	}{
		{name: "leader template", change: func(lws *lwsv1.LeaderWorkerSet) {
			lws.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec.Containers[0].Image = "test:v2"
		}},
		{name: "worker template", change: func(lws *lwsv1.LeaderWorkerSet) {
			lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0].Image = "test:v2"
		}},
		{name: "network config", change: func(lws *lwsv1.LeaderWorkerSet) {
			lws.Spec.NetworkConfig = &lwsv1.NetworkConfig{SubdomainPolicy: ptr.To(lwsv1.SubdomainUniquePerReplica)}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			rbg, objects := rolloutTestFixture(t, "LeaderWorkerSet")
			lws := objects[0].(*lwsv1.LeaderWorkerSet)
			lws.Spec.LeaderWorkerTemplate.LeaderTemplate = &corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "leader", Image: "test:v1"}},
			}}
			lws.Spec.LeaderWorkerTemplate.WorkerTemplate = corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "worker", Image: "test:v1"}},
			}}
			objects[4].GetLabels()[lwsv1.RevisionKey] = "legacy-revision-key"
			objects = seedUpgradeWorkloadStatus(t, lws, objects)
			rbg.Annotations = map[string]string{constants.RolloutRequestAnnotationKey: "2"}
			rbg.Status.ObservedGeneration = rbg.Generation
			rbg.Status.ObservedRolloutRequest = "1"
			rbg.Status.Conditions = []metav1.Condition{
				{Type: string(workloadsv1alpha2.RoleBasedGroupReady), Status: metav1.ConditionTrue, ObservedGeneration: rbg.Generation},
				{Type: string(workloadsv1alpha2.RoleBasedGroupRollingUpdateInProgress), Status: metav1.ConditionTrue, ObservedGeneration: rbg.Generation},
			}
			c := fake.NewClientBuilder().WithScheme(rolloutTestScheme(t)).WithObjects(append(objects, rbg)...).
				WithStatusSubresource(rbg).Build()
			r := &RoleBasedGroupReconciler{client: c, apiReader: c}
			expected := map[string]string{"role": "target"}
			check := func(wantComplete bool, wantAck string) {
				t.Helper()
				complete, err := r.roleWorkloadRolloutComplete(ctx, rbg, &rbg.Spec.Roles[0], "target")
				require.NoError(t, err)
				assert.Equal(t, wantComplete, complete)
				require.NoError(t, r.finalizeRolloutStatus(ctx, rbg, expected))
				got := &workloadsv1alpha2.RoleBasedGroup{}
				require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(rbg), got))
				assert.Equal(t, wantAck, got.Status.ObservedRolloutRequest)
				assert.Equal(t, wantComplete, apimeta.IsStatusConditionFalse(got.Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupRollingUpdateInProgress)))
				assert.Equal(t, wantComplete, groupSetChildReady(got), "the set must retain the availability slot until the live target completes")
			}

			liveRevision, err := lwsrevision.NewRevision(ctx, c, lws, "")
			require.NoError(t, err)
			require.NotEqual(t, "legacy-revision-key", liveRevision.Labels[lwsv1.RevisionKey])
			require.True(t, lwsrevision.EqualRevision(liveRevision, objects[4].(*appsv1.ControllerRevision)))
			complete, err := r.roleWorkloadRolloutComplete(ctx, rbg, &rbg.Spec.Roles[0], "target")
			require.NoError(t, err)
			require.True(t, complete, "matching persisted data must accept the legacy revision key")

			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(lws), lws))
			test.change(lws)
			lws.Generation++
			require.NoError(t, c.Update(ctx, lws))
			for pass := 0; pass < 3; pass++ {
				check(false, "1")
			}
			revisions := &appsv1.ControllerRevisionList{}
			require.NoError(t, c.List(ctx, revisions))
			require.Len(t, revisions.Items, 1, "the barrier must not persist the live revision itself")

			target, err := lwsrevision.NewRevision(ctx, c, lws, "")
			require.NoError(t, err)
			require.False(t, lwsrevision.EqualRevision(target, objects[4].(*appsv1.ControllerRevision)))
			require.NoError(t, c.Create(ctx, target))
			check(false, "1")
			objects[1].(*appsv1.StatefulSet).Spec.Template = *lws.Spec.LeaderWorkerTemplate.LeaderTemplate.DeepCopy()
			objects[2].(*corev1.Pod).Spec = *lws.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec.DeepCopy()
			objects[3].(*appsv1.StatefulSet).Spec.Template = *lws.Spec.LeaderWorkerTemplate.WorkerTemplate.DeepCopy()
			for _, dependent := range objects[1:4] {
				dependent.GetLabels()[lwsv1.RevisionKey] = target.Labels[lwsv1.RevisionKey]
				require.NoError(t, c.Update(ctx, dependent))
			}
			check(true, "2")
			require.NoError(t, c.List(ctx, revisions))
			assert.Len(t, revisions.Items, 2)
		})
	}
}

func TestLeaderWorkerSetRolloutChecksHighestRevisionData(t *testing.T) {
	for _, test := range []string{"missing revision", "missing key", "foreign revision", "mismatched data with matching key", "newer mismatched revision"} {
		t.Run(test, func(t *testing.T) {
			rbg, objects := rolloutTestFixture(t, "LeaderWorkerSet")
			revision := objects[4].(*appsv1.ControllerRevision)
			switch test {
			case "missing revision":
				objects = objects[:4]
			case "missing key":
				delete(revision.Labels, lwsv1.RevisionKey)
			case "foreign revision":
				revision.OwnerReferences[0].UID = "another-lws"
			case "mismatched data with matching key":
				revision.Data.Raw = []byte(`{"spec":{"leaderWorkerTemplate":{"size":3}}}`)
			case "newer mismatched revision":
				newer := revision.DeepCopy()
				newer.Name = "newer-revision"
				newer.Revision++
				newer.Data.Raw = []byte(`{"spec":{"leaderWorkerTemplate":{"size":3}}}`)
				objects = append(objects, newer)
			}
			c := fake.NewClientBuilder().WithScheme(rolloutTestScheme(t)).WithObjects(objects...).Build()
			r := &RoleBasedGroupReconciler{client: c, apiReader: c}
			complete, err := r.roleWorkloadRolloutComplete(context.Background(), rbg, &rbg.Spec.Roles[0], "target")
			require.NoError(t, err)
			assert.False(t, complete)
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
			rbg.Annotations = map[string]string{constants.RolloutRequestAnnotationKey: "3"}
			rbg.Status.ObservedRolloutRequest = "2"
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
			assert.Equal(t, "2", got.Status.ObservedRolloutRequest, "status construction must not acknowledge a request before workload application")
			require.NoError(t, r.finalizeRolloutStatus(ctx, rbg, map[string]string{"role": "target"}))
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(rbg), got))
			wantAck := "2"
			if test == "complete" {
				wantAck = "3"
			}
			assert.Equal(t, wantAck, got.Status.ObservedRolloutRequest)
			require.NoError(t, r.setGangConfiguredCondition(ctx, rbg, &gangcommon.GangStrategy{}, nil))
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(rbg), got))
			assert.Equal(t, wantAck, got.Status.ObservedRolloutRequest, "the second SSA status writer must preserve acknowledgement")
		})
	}
}

func TestPostApplicationRolloutCompletion(t *testing.T) {
	for _, test := range []struct {
		name     string
		request  string
		observed string
	}{
		{name: "pending request", request: "3", observed: "2"},
		{name: "acknowledged request", request: "3", observed: "3"},
		{name: "standalone"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			rbg, objects := rolloutTestFixture(t, "RoleInstanceSet")
			rbg.Annotations = map[string]string{constants.RolloutRequestAnnotationKey: test.request}
			rbg.Status.ObservedRolloutRequest = test.observed
			ris := objects[0].(*workloadsv1alpha2.RoleInstanceSet)
			hashKey := fmt.Sprintf(constants.RoleRevisionLabelKeyFmt, "role")
			ris.Labels[hashKey] = "old"
			patches := 0
			scheme := rolloutTestScheme(t)
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(objects, rbg)...).
				WithStatusSubresource(rbg).WithInterceptorFuncs(interceptor.Funcs{
				SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
					patches++
					return c.SubResource(subresource).Patch(ctx, obj, patch, opts...)
				},
			}).Build()
			r := &RoleBasedGroupReconciler{client: c, apiReader: c, scheme: scheme, recorder: record.NewFakeRecorder(10),
				workloadReconciler: make(map[string]reconciler.WorkloadReconciler)}
			expected := map[string]string{"role": "target"}
			_, err := r.constructAndUpdateRoleStatuses(ctx, rbg, expected)
			require.NoError(t, err)
			require.True(t, apimeta.IsStatusConditionTrue(rbg.Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupRollingUpdateInProgress)))
			require.NoError(t, r.finalizeRolloutStatus(ctx, rbg, expected))
			assert.Equal(t, 1, patches, "incomplete workloads must retain the barrier and request")
			assert.Equal(t, test.observed, rbg.Status.ObservedRolloutRequest)

			updated := &workloadsv1alpha2.RoleInstanceSet{}
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(ris), updated))
			updated.Labels[hashKey] = "target"
			require.NoError(t, c.Update(ctx, updated))
			assert.Equal(t, ris.Generation, updated.Generation)
			assert.Equal(t, ris.Spec, updated.Spec)
			assert.False(t, WorkloadPredicate().Update(event.UpdateEvent{ObjectOld: ris, ObjectNew: updated}))
			require.NoError(t, r.finalizeRolloutStatus(ctx, rbg, expected))
			got := &workloadsv1alpha2.RoleBasedGroup{}
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(rbg), got))
			condition := apimeta.FindStatusCondition(got.Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupRollingUpdateInProgress))
			require.NotNil(t, condition)
			assert.Equal(t, metav1.ConditionFalse, condition.Status)
			assert.Equal(t, rbg.Generation, condition.ObservedGeneration)
			assert.Equal(t, "AllRolesUpdated", condition.Reason)
			assert.Equal(t, test.request, got.Status.ObservedRolloutRequest)
			assert.Equal(t, 2, patches, "completion and acknowledgement must share one status patch")
			require.NoError(t, r.finalizeRolloutStatus(ctx, rbg, expected))
			assert.Equal(t, 2, patches, "completed rollouts must not be patched again")
		})
	}
}

func TestRoleHashUpgradeConvergesWithoutChangingWorkloadTemplates(t *testing.T) {
	for _, kind := range []string{"RoleInstanceSet", "Deployment", "StatefulSet", "LeaderWorkerSet"} {
		for _, annotation := range []string{constants.GroupExclusiveTopologyKey, constants.GangSchedulingAnnotationKey} {
			t.Run(kind+"/"+annotation, func(t *testing.T) {
				ctx := context.Background()
				rbg, objects := rolloutTestFixture(t, kind)
				rbg.APIVersion, rbg.Kind = workloadsv1alpha2.GroupVersion.String(), "RoleBasedGroup"
				rbg.Annotations = map[string]string{constants.DiscoveryConfigModeAnnotationKey: "refine", annotation: "true"}
				if annotation == constants.GroupExclusiveTopologyKey {
					rbg.Annotations[annotation] = "kubernetes.io/hostname"
				}
				role := &rbg.Spec.Roles[0]
				role.Pattern = wrappersv2.BuildStandaloneRole(role.Name).Obj().Pattern
				if kind == "LeaderWorkerSet" {
					role.Pattern = wrappersv2.BuildLeaderWorkerRole(role.Name).WithSize(2).Obj().Pattern
				}
				rolling := wrappersv2.BuildRollingUpdate(1, 0)
				rolling.Type = workloadsv1alpha2.RecreatePodUpdateStrategyType
				rolling.Partition = ptr.To(intstr.FromInt32(0))
				role.RolloutStrategy = &workloadsv1alpha2.RolloutStrategy{
					Type: workloadsv1alpha2.RollingUpdateStrategyType, RollingUpdate: &rolling,
				}
				scheme := rolloutTestScheme(t)
				c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(rbg).WithStatusSubresource(rbg, objects[0]).Build()
				revision, err := utils.NewRevision(ctx, c, rbg, nil)
				require.NoError(t, err)
				revision.UID = "pre-upgrade-revision"
				require.NoError(t, c.Create(ctx, revision))
				// Before the annotation fold, workloads used the persisted revision's role hash directly.
				legacyHashes, err := utils.GetRolesRevisionHash(revision)
				require.NoError(t, err)
				hashKey := fmt.Sprintf(constants.RoleRevisionLabelKeyFmt, role.Name)
				rec, err := reconciler.NewWorkloadReconciler(role.GetWorkloadSpec(), scheme, c)
				require.NoError(t, err)
				gang, err := scheduler.NewGangScheduler(scheduler.KubeSchedulerPlugin, c, "")
				require.NoError(t, err)
				rec.(reconciler.GangSchedulerSetter).SetGangScheduler(gang)
				require.NoError(t, rec.Reconciler(ctx, rbg, role.DeepCopy(), nil, legacyHashes[role.Name]))
				workload := objects[0].DeepCopyObject().(client.Object)
				require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(objects[0]), workload))
				workload.SetUID(objects[0].GetUID())
				workload.SetGeneration(2)
				require.NoError(t, c.Update(ctx, workload))
				objects = seedUpgradeWorkloadStatus(t, workload, objects)
				require.NoError(t, c.Status().Update(ctx, workload))
				for _, dependent := range objects[1:] {
					require.NoError(t, c.Create(ctx, dependent))
				}
				initialRevisions := &appsv1.ControllerRevisionList{}
				require.NoError(t, c.List(ctx, initialRevisions))
				before := workload.DeepCopyObject().(client.Object)
				require.Equal(t, legacyHashes[role.Name], before.GetLabels()[hashKey])
				r := &RoleBasedGroupReconciler{client: c, apiReader: c, scheme: scheme, recorder: record.NewFakeRecorder(20),
					workloadReconciler: map[string]reconciler.WorkloadReconciler{role.GetWorkloadType(): rec}}
				complete, err := r.roleWorkloadRolloutComplete(ctx, rbg, role, legacyHashes[role.Name])
				require.NoError(t, err)
				require.True(t, complete, "the fixture must already be serving before the upgrade")

				expected, err := r.handleRevisions(ctx, rbg)
				require.NoError(t, err)
				require.NotEqual(t, legacyHashes[role.Name], expected[role.Name], "the old fixture must exercise the changed hash recipe")
				_, err = r.constructAndUpdateRoleStatuses(ctx, rbg, expected)
				require.NoError(t, err)
				require.True(t, apimeta.IsStatusConditionTrue(rbg.Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupRollingUpdateInProgress)))
				require.NoError(t, rec.Reconciler(ctx, rbg, role.DeepCopy(), nil, expected[role.Name]))
				require.NoError(t, r.finalizeRolloutStatus(ctx, rbg, expected))
				require.True(t, apimeta.IsStatusConditionFalse(rbg.Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupRollingUpdateInProgress)))
				require.Empty(t, rbg.Status.ObservedRolloutRequest, "standalone upgrades must not invent a rollout request")
				require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(before), workload))
				assert.Equal(t, expected[role.Name], workload.GetLabels()[hashKey])
				assert.Equal(t, before.GetUID(), workload.GetUID())
				assert.Equal(t, before.GetOwnerReferences(), workload.GetOwnerReferences())
				assert.Equal(t, upgradeWorkloadTemplate(before), upgradeWorkloadTemplate(workload))

				for pass := 0; pass < 5; pass++ {
					hashes, err := r.handleRevisions(ctx, rbg)
					require.NoError(t, err)
					require.Equal(t, expected, hashes)
					require.NoError(t, rec.Reconciler(ctx, rbg, role.DeepCopy(), nil, hashes[role.Name]))
					require.NoError(t, r.finalizeRolloutStatus(ctx, rbg, hashes))
					require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(before), workload))
					assert.Equal(t, before.GetUID(), workload.GetUID())
					assert.Equal(t, expected[role.Name], workload.GetLabels()[hashKey])
					assert.Equal(t, upgradeWorkloadTemplate(before), upgradeWorkloadTemplate(workload))
				}
				settled := workload.DeepCopyObject().(client.Object)
				require.NoError(t, rec.Reconciler(ctx, rbg, role.DeepCopy(), nil, expected[role.Name]))
				require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(before), workload))
				assert.Equal(t, settled, workload, "repeated reconciliation must preserve the entire settled workload")
				for _, dependent := range objects[1:] {
					got := dependent.DeepCopyObject().(client.Object)
					require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(dependent), got))
					assert.Equal(t, dependent, got, "the existing instance or LWS leader/worker chain must stay untouched")
				}
				revisions := &appsv1.ControllerRevisionList{}
				require.NoError(t, c.List(ctx, revisions))
				assert.ElementsMatch(t, initialRevisions.Items, revisions.Items, "hash migration must reuse persisted revisions")
			})
		}
	}
}

func upgradeWorkloadTemplate(workload client.Object) interface{} {
	switch obj := workload.(type) {
	case *appsv1.Deployment:
		return obj.Spec.Template
	case *appsv1.StatefulSet:
		return obj.Spec.Template
	case *workloadsv1alpha2.RoleInstanceSet:
		return obj.Spec.RoleInstanceTemplate
	case *lwsv1.LeaderWorkerSet:
		return obj.Spec.LeaderWorkerTemplate
	}
	return nil
}

// Workload statuses and LWS descendants model already-running controllers; this is not a kubelet or LWS E2E.
func seedUpgradeWorkloadStatus(t *testing.T, workload client.Object, objects []client.Object) []client.Object {
	t.Helper()
	switch obj := workload.(type) {
	case *appsv1.Deployment:
		obj.Status = objects[0].(*appsv1.Deployment).Status
	case *appsv1.StatefulSet:
		obj.Status = objects[0].(*appsv1.StatefulSet).Status
		revision := &appsv1.ControllerRevision{ObjectMeta: metav1.ObjectMeta{
			Name: obj.Name + "-old", Namespace: obj.Namespace, UID: "sts-revision",
			Labels:          obj.Spec.Selector.MatchLabels,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(obj, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))},
		}, Revision: 1}
		obj.Status.CurrentRevision, obj.Status.UpdateRevision = revision.Name, revision.Name
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name: obj.Name + "-0", Namespace: obj.Namespace, UID: "sts-pod", Labels: maps.Clone(obj.Spec.Selector.MatchLabels),
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(obj, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))},
		}, Spec: *obj.Spec.Template.Spec.DeepCopy(), Status: corev1.PodStatus{
			Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		}}
		pod.Labels[appsv1.ControllerRevisionHashLabelKey] = revision.Name
		objects = append(objects, revision, pod)
	case *workloadsv1alpha2.RoleInstanceSet:
		obj.Status = objects[0].(*workloadsv1alpha2.RoleInstanceSet).Status
		instance := objects[1].(*workloadsv1alpha2.RoleInstance)
		for key, value := range obj.Spec.Selector.MatchLabels {
			instance.Labels[key] = value
		}
	case *lwsv1.LeaderWorkerSet:
		obj.Status = objects[0].(*lwsv1.LeaderWorkerSet).Status
		revision, err := lwsrevision.NewRevision(context.Background(), fake.NewClientBuilder().WithScheme(rolloutTestScheme(t)).Build(), obj, objects[4].GetLabels()[lwsv1.RevisionKey])
		require.NoError(t, err)
		revision.UID = objects[4].GetUID()
		objects[4] = revision
		for _, dependent := range objects[1:] {
			dependent.GetLabels()[lwsv1.RevisionKey] = revision.Labels[lwsv1.RevisionKey]
		}
		objects[1].(*appsv1.StatefulSet).Spec.Template = *obj.Spec.LeaderWorkerTemplate.LeaderTemplate.DeepCopy()
		objects[2].(*corev1.Pod).Spec = *obj.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec.DeepCopy()
		objects[3].(*appsv1.StatefulSet).Spec.Template = *obj.Spec.LeaderWorkerTemplate.WorkerTemplate.DeepCopy()
	}
	return objects
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
