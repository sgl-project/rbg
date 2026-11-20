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

package podcache

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
)

func TestTransform(t *testing.T) {
	for _, tc := range []struct {
		name   string
		labels map[string]string
		owners []metav1.OwnerReference
		whole  bool
	}{
		{name: "unrelated"},
		{name: "group", labels: map[string]string{constants.GroupNameLabelKey: ""}, whole: true},
		{name: "instance", labels: map[string]string{constants.RoleInstanceNameLabelKey: "r"}, whole: true},
		{name: "warmup", labels: map[string]string{constants.WarmupNameLabelKey: "w"}, whole: true},
		{name: "managed", labels: map[string]string{constants.ManagedLabelKey: "true"}, whole: true},
		{name: "false", labels: map[string]string{constants.ManagedLabelKey: "false"}},
		{name: "legacy overrides false", labels: map[string]string{constants.ManagedLabelKey: "false", constants.GroupNameLabelKey: "g"}, whole: true},
		{name: "noncontroller owner", owners: []metav1.OwnerReference{{APIVersion: "workloads.x-k8s.io/v1alpha2", Kind: "RoleInstance"}}, whole: true},
		{name: "controller owner", owners: []metav1.OwnerReference{{APIVersion: "workloads.x-k8s.io/v1alpha2", Kind: "RoleInstance", Controller: boolPtr(true)}}, whole: true},
		{name: "foreign owner", owners: []metav1.OwnerReference{{APIVersion: "apps/v1", Controller: boolPtr(true)}}},
		{name: "malformed owner", owners: []metav1.OwnerReference{{APIVersion: "workloads.x-k8s.io/v1/invalid"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := metav1.Now()
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns", UID: "uid", ResourceVersion: "7", Labels: tc.labels, OwnerReferences: tc.owners,
				DeletionTimestamp: &now, Annotations: map[string]string{"large": "annotation"}, ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "test"}}},
				Spec: corev1.PodSpec{NodeName: "node"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
			out, err := Transform(pod)
			require.NoError(t, err)
			got := out.(*corev1.Pod)
			require.Equal(t, tc.whole, Admitted(got))
			require.Nil(t, got.ManagedFields)
			if tc.whole {
				require.Equal(t, "node", got.Spec.NodeName)
			} else {
				require.Empty(t, got.Spec)
				require.Empty(t, got.Status)
				require.Empty(t, got.Annotations)
			}
			require.Equal(t, pod.Labels, got.Labels)
			require.Equal(t, pod.OwnerReferences, got.OwnerReferences)
			require.Equal(t, pod.DeletionTimestamp, got.DeletionTimestamp)
			require.Equal(t, "7", got.ResourceVersion)
			require.Equal(t, pod.UID, got.UID)
			again, err := Transform(got)
			require.NoError(t, err)
			require.Equal(t, got, again)
		})
	}
	nonPod := &corev1.Service{}
	out, err := Transform(nonPod)
	require.NoError(t, err)
	require.Same(t, nonPod, out)
}

func boolPtr(b bool) *bool { return &b }

func TestConfigure(t *testing.T) {
	var all cache.Options
	require.NoError(t, Configure(&all, ScopeAll))
	require.Nil(t, all.NewInformer)
	require.Nil(t, all.ByObject)
	require.Error(t, Configure(&all, "typo"))
	var filtered cache.Options
	require.NoError(t, Configure(&filtered, ScopeFiltered))
	require.NotNil(t, filtered.NewInformer)
	require.Len(t, filtered.ByObject, 1)
}

func TestConfigurePreservesInformerConstructorAndCacheOptions(t *testing.T) {
	service := &corev1.Service{}
	byObject := map[client.Object]cache.ByObject{service: {Transform: cache.TransformStripManagedFields()}}
	var received toolscache.ListerWatcher
	opts := cache.Options{ByObject: byObject, NewInformer: func(lw toolscache.ListerWatcher, obj runtime.Object, resync time.Duration, indexes toolscache.Indexers) toolscache.SharedIndexInformer {
		received = lw
		return toolscache.NewSharedIndexInformer(lw, obj, resync, indexes)
	}}
	require.NoError(t, Configure(&opts, ScopeFiltered))
	require.Len(t, byObject, 1)
	require.NotNil(t, opts.ByObject[service].Transform)
	lw := &toolscache.ListWatch{}
	opts.NewInformer(lw, service, 0, nil)
	require.Same(t, lw, received)
	opts.NewInformer(lw, &corev1.Pod{}, 0, nil)
	require.IsType(t, &transformingListerWatcher{}, received)
}

func TestConfigureRejectsConflictingPodTransforms(t *testing.T) {
	transform := cache.TransformStripManagedFields()
	opts := cache.Options{ByObject: map[client.Object]cache.ByObject{&corev1.Pod{}: {Transform: transform}}}
	require.ErrorContains(t, Configure(&opts, ScopeFiltered), "exclusive use of the Pod transform")
	opts = cache.Options{DefaultNamespaces: map[string]cache.Config{"default": {Transform: transform}}}
	require.ErrorContains(t, Configure(&opts, ScopeFiltered), "transform for namespace")
}

func TestWatchEventsAndStop(t *testing.T) {
	source := watch.NewRaceFreeFake()
	w := newTransformingWatch(context.Background(), source)
	for _, eventType := range []watch.EventType{watch.Added, watch.Modified, watch.Deleted, watch.Bookmark, watch.Error} {
		pod := &corev1.Pod{Spec: corev1.PodSpec{NodeName: "node"}}
		source.Action(eventType, pod)
		select {
		case event := <-w.ResultChan():
			require.Equal(t, eventType, event.Type)
			if eventType == watch.Bookmark || eventType == watch.Error {
				require.Same(t, pod, event.Object)
			} else {
				require.Empty(t, event.Object.(*corev1.Pod).Spec)
			}
		case <-time.After(time.Second):
			t.Fatal("event not forwarded")
		}
	}
	w.Stop()
	w.Stop()
	requireWatchStopped(t, w)

	source = watch.NewRaceFreeFake()
	w = newTransformingWatch(context.Background(), source)
	source.Add(&corev1.Pod{})
	// The source buffer drains only once the wrapper reaches its pending send.
	require.Eventually(t, func() bool { return len(source.ResultChan()) == 0 }, time.Second, time.Millisecond)
	w.Stop()
	requireWatchStopped(t, w)

	ctx, cancel := context.WithCancel(context.Background())
	w = newTransformingWatch(ctx, watch.NewRaceFreeFake())
	cancel()
	requireWatchStopped(t, w)
}

func requireWatchStopped(t *testing.T, w watch.Interface) {
	t.Helper()
	require.Eventually(t, func() bool {
		select {
		case _, open := <-w.ResultChan():
			return !open
		default:
			return false
		}
	}, time.Second, time.Millisecond, "watch result channel did not close")
}
