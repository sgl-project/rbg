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
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	toolscache "k8s.io/client-go/tools/cache"
)

// WatchStreamTransformInformer returns a cache.Options.NewInformer that also
// applies the transform to watch events before the reflector sees them.
//
// client-go v0.34.2 passes a store's transform to the reflector's streaming-list
// temporary store only if the store implements the full Store interface
// (TransformingStore). The informer FIFOs implement ReflectorStore only, so
// without this wrapper every streamed object stays whole until Replace. The
// informer's own transform still runs afterwards; the transform is idempotent.
// A List response is decoded whole before anything can transform it, so the
// wrapper changes nothing without a streaming list.
func WatchStreamTransformInformer(transform toolscache.TransformFunc) func(toolscache.ListerWatcher, runtime.Object, time.Duration, toolscache.Indexers) toolscache.SharedIndexInformer {
	return func(lw toolscache.ListerWatcher, obj runtime.Object, resync time.Duration, indexers toolscache.Indexers) toolscache.SharedIndexInformer {
		return toolscache.NewSharedIndexInformer(&streamTransformLW{inner: toolscache.ToListerWatcherWithContext(lw), transform: transform}, obj, resync, indexers)
	}
}

type streamTransformLW struct {
	inner     toolscache.ListerWatcherWithContext
	transform toolscache.TransformFunc
}

func (l *streamTransformLW) List(options metav1.ListOptions) (runtime.Object, error) {
	return l.ListWithContext(context.Background(), options)
}

func (l *streamTransformLW) Watch(options metav1.ListOptions) (watch.Interface, error) {
	return l.WatchWithContext(context.Background(), options)
}

func (l *streamTransformLW) ListWithContext(ctx context.Context, options metav1.ListOptions) (runtime.Object, error) {
	return l.inner.ListWithContext(ctx, options)
}

func (l *streamTransformLW) WatchWithContext(ctx context.Context, options metav1.ListOptions) (watch.Interface, error) {
	inner, err := l.inner.WatchWithContext(ctx, options)
	if err != nil {
		return nil, err
	}
	w := &transformingWatch{inner: inner, transform: l.transform, result: make(chan watch.Event), done: make(chan struct{}), finished: make(chan struct{})}
	go w.run()
	return w, nil
}

// transformingWatch forwards an inner watch, transforming object events. It is
// not watch.Filter: that helper's loop blocks forever on its unbuffered result
// channel once the consumer stops reading, and the reflector stops a watch
// without draining it on every expiry, relist, and shutdown. Here the loop also
// selects on Stop, so its goroutine always exits.
type transformingWatch struct {
	inner     watch.Interface
	transform toolscache.TransformFunc
	result    chan watch.Event
	done      chan struct{}
	finished  chan struct{}
	stopOnce  sync.Once
}

func (w *transformingWatch) run() {
	defer close(w.finished)
	defer close(w.result)
	defer w.inner.Stop()
	for {
		var event watch.Event
		var ok bool
		select {
		case <-w.done:
			return
		case event, ok = <-w.inner.ResultChan():
			if !ok {
				return
			}
		}
		switch event.Type {
		case watch.Added, watch.Modified, watch.Deleted:
			// The informer transforms the object again, so a failure here only
			// forgoes the early reduction.
			if out, err := w.transform(event.Object); err == nil {
				if object, ok := out.(runtime.Object); ok {
					event.Object = object
				}
			}
		}
		select {
		case <-w.done:
			return
		case w.result <- event:
		}
	}
}

func (w *transformingWatch) Stop() {
	w.stopOnce.Do(func() { close(w.done) })
}

func (w *transformingWatch) ResultChan() <-chan watch.Event {
	return w.result
}

var _ toolscache.ListerWatcherWithContext = (*streamTransformLW)(nil)
var _ watch.Interface = (*transformingWatch)(nil)

// streamTransformRelistInformer composes the comparison's relist probe with the
// watch-stream transform, so the stream-transform mode is measured like the rest.
func streamTransformRelistInformer(probe *relistProbe, transform toolscache.TransformFunc) func(toolscache.ListerWatcher, runtime.Object, time.Duration, toolscache.Indexers) toolscache.SharedIndexInformer {
	return func(lw toolscache.ListerWatcher, obj runtime.Object, resync time.Duration, indexers toolscache.Indexers) toolscache.SharedIndexInformer {
		probed := &relistLW{inner: toolscache.ToListerWatcherWithContext(lw), probe: probe}
		return toolscache.NewSharedIndexInformer(&streamTransformLW{inner: probed, transform: transform}, obj, resync, indexers)
	}
}
