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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/rbgs/api/workloads/constants"
)

func TestInformerRelistAfterExpiredWatch(t *testing.T) {
	var mu sync.Mutex
	snapshot := &corev1.PodList{ListMeta: metav1.ListMeta{ResourceVersion: "10"}, Items: []corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "gain", ResourceVersion: "10"}, Spec: corev1.PodSpec{NodeName: "node"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "lose", ResourceVersion: "10", Labels: map[string]string{constants.GroupNameLabelKey: "g"}}, Spec: corev1.PodSpec{NodeName: "node"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "deleted", ResourceVersion: "10"}},
	}}
	watches := make(chan *watch.RaceFreeFakeWatcher, 10)
	lw := &toolscache.ListWatch{
		ListWithContextFunc: func(_ context.Context, _ metav1.ListOptions) (runtime.Object, error) {
			mu.Lock()
			defer mu.Unlock()
			return snapshot.DeepCopy(), nil
		},
		WatchFuncWithContext: func(_ context.Context, opts metav1.ListOptions) (watch.Interface, error) {
			if opts.SendInitialEvents != nil && *opts.SendInitialEvents {
				return nil, apierrors.NewBadRequest("streaming unsupported")
			}
			w := watch.NewRaceFreeFake()
			watches <- w
			return w, nil
		},
	}
	inf := toolscache.NewSharedIndexInformer(&transformingListerWatcher{ListerWatcher: lw}, &corev1.Pod{}, 0, nil)
	require.NoError(t, inf.SetTransform(Transform))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); inf.RunWithContext(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("informer did not stop")
		}
	})
	require.Eventually(t, inf.HasSynced, 5*time.Second, time.Millisecond)
	get := func(name string) *corev1.Pod {
		obj, exists, err := inf.GetStore().GetByKey(name)
		if err != nil || !exists {
			return nil
		}
		return obj.(*corev1.Pod)
	}
	require.Empty(t, get("gain").Spec.NodeName)
	require.Equal(t, "node", get("lose").Spec.NodeName)
	var oldWatch *watch.RaceFreeFakeWatcher
	select {
	case oldWatch = <-watches:
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not start")
	}
	mu.Lock()
	snapshot = snapshot.DeepCopy()
	snapshot.ResourceVersion = "20"
	snapshot.Items = snapshot.Items[:2]
	for i := range snapshot.Items {
		snapshot.Items[i].ResourceVersion = "20"
	}
	snapshot.Items[0].Labels = map[string]string{constants.ManagedLabelKey: "true"}
	snapshot.Items[1].Labels = nil
	mu.Unlock()
	oldWatch.Error(&metav1.Status{Status: metav1.StatusFailure, Reason: metav1.StatusReasonExpired, Code: 410})
	require.Eventually(t, func() bool {
		gain, lose := get("gain"), get("lose")
		return gain != nil && lose != nil && gain.ResourceVersion == "20" && lose.ResourceVersion == "20" && get("deleted") == nil
	}, 10*time.Second, 10*time.Millisecond)
	require.Equal(t, "node", get("gain").Spec.NodeName)
	require.Empty(t, get("lose").Spec.NodeName)
}
