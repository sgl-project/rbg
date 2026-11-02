//go:build podcacheunion

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

package podcacheunion

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
)

type fakeWatchSource struct{ watcher watch.Interface }

func (f *fakeWatchSource) ListWithContext(context.Context, metav1.ListOptions) (kruntime.Object, error) {
	return &corev1.PodList{}, nil
}

func (f *fakeWatchSource) WatchWithContext(context.Context, metav1.ListOptions) (watch.Interface, error) {
	return f.watcher, nil
}

// TestWatchStreamWrapperTransformsEvents checks the wrapper in isolation: object
// events reach the consumer transformed, bookmarks and errors pass unchanged,
// and the wrapped watch closes when the inner one does.
func TestWatchStreamWrapperTransformsEvents(t *testing.T) {
	inner := watch.NewFakeWithChanSize(5, false)
	lw := &streamTransformLW{inner: &fakeWatchSource{watcher: inner}, transform: PodCacheTransform(StubIdentityLabelsOwners)}
	w, err := lw.WatchWithContext(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	unrelated := pod("ns", "unrelated", map[string]string{"app": "x"})
	admittedPod := pod("ns", "admitted", map[string]string{groupLabel: "g"})
	admittedPod.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "test"}}
	bookmark := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{ResourceVersion: "9", Annotations: map[string]string{metav1.InitialEventsAnnotationKey: "true"}}}
	inner.Add(unrelated)
	inner.Modify(admittedPod)
	inner.Delete(unrelated)
	inner.Action(watch.Bookmark, bookmark)
	inner.Error(&metav1.Status{Code: 410})
	inner.Stop()

	var got []watch.Event
	for event := range w.ResultChan() {
		got = append(got, event)
	}
	if len(got) != 5 {
		t.Fatalf("got %d events, want 5", len(got))
	}
	if p := got[0].Object.(*corev1.Pod); got[0].Type != watch.Added || !isStub(p) || p.Labels["app"] != "x" {
		t.Fatalf("unrelated Pod not delivered as a stub: %s %+v", got[0].Type, p)
	}
	if p := got[1].Object.(*corev1.Pod); got[1].Type != watch.Modified || isStub(p) || p.ManagedFields != nil {
		t.Fatalf("admitted Pod not delivered whole without managedFields: %s %+v", got[1].Type, p)
	}
	if p := got[2].Object.(*corev1.Pod); got[2].Type != watch.Deleted || !isStub(p) {
		t.Fatalf("delete not transformed: %+v", got[2])
	}
	if got[3].Type != watch.Bookmark || got[3].Object != bookmark {
		t.Fatalf("bookmark changed: %+v", got[3])
	}
	if got[4].Type != watch.Error {
		t.Fatalf("error event changed: %+v", got[4])
	}
}

// TestWatchStreamWrapperStopDoesNotLeak stops wrapped watches the way the
// reflector does on expiry, relist, and shutdown: without draining them, while
// an event is pending. Every forwarding goroutine must exit. watch.Filter fails
// this: its loop blocks forever on the unread result channel.
func TestWatchStreamWrapperStopDoesNotLeak(t *testing.T) {
	for _, pending := range []bool{false, true} {
		inner := watch.NewFakeWithChanSize(1, false)
		transformed := make(chan struct{})
		lw := &streamTransformLW{inner: &fakeWatchSource{watcher: inner}, transform: func(obj interface{}) (interface{}, error) {
			close(transformed)
			return obj, nil
		}}
		w, err := lw.WatchWithContext(context.Background(), metav1.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if pending {
			inner.Add(pod("ns", "pending", nil))
			select {
			case <-transformed:
			case <-time.After(5 * time.Second):
				t.Fatal("watch did not pick up the pending event")
			}
		}
		w.Stop()
		w.Stop() // idempotent
		// Observe loop termination without draining ResultChan, even with a
		// pending send; unrelated goroutines cannot affect this assertion.
		select {
		case <-w.(*transformingWatch).finished:
		case <-time.After(5 * time.Second):
			t.Fatalf("watch loop leaked (pending event: %v)", pending)
		}
		if !inner.IsStopped() {
			t.Fatal("underlying watch was not stopped")
		}
	}
}
