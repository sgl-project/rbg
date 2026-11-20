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
	"fmt"
	"maps"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Configure sets the Pod transform for filtered scope, including watch events.
func Configure(opts *cache.Options, scope string) error {
	if err := ValidateScope(scope); err != nil {
		return err
	}
	if scope == ScopeAll {
		return nil
	}
	if err := configureTransform(opts); err != nil {
		return err
	}
	newInformer := opts.NewInformer
	if newInformer == nil {
		newInformer = toolscache.NewSharedIndexInformer
	}
	opts.NewInformer = func(lw toolscache.ListerWatcher, obj runtime.Object, resync time.Duration, indexes toolscache.Indexers) toolscache.SharedIndexInformer {
		if _, ok := obj.(*corev1.Pod); ok {
			lw = &transformingListerWatcher{ListerWatcher: lw}
		}
		return newInformer(lw, obj, resync, indexes)
	}
	return nil
}

func configureTransform(opts *cache.Options) error {
	var podKey client.Object = &corev1.Pod{}
	for key := range opts.ByObject {
		if _, ok := key.(*corev1.Pod); ok {
			podKey = key
			break
		}
	}
	podOptions := opts.ByObject[podKey]
	if podOptions.Transform != nil {
		return fmt.Errorf("filtered Pod cache requires exclusive use of the Pod transform")
	}
	namespaces := podOptions.Namespaces
	if namespaces == nil {
		namespaces = opts.DefaultNamespaces
	}
	for namespace, config := range namespaces {
		if config.Transform != nil || opts.DefaultNamespaces[namespace].Transform != nil {
			return fmt.Errorf("filtered Pod cache conflicts with the transform for namespace %q", namespace)
		}
	}
	podOptions.Transform = Transform
	opts.ByObject = maps.Clone(opts.ByObject)
	if opts.ByObject == nil {
		opts.ByObject = make(map[client.Object]cache.ByObject)
	}
	opts.ByObject[podKey] = podOptions
	return nil
}

type transformingListerWatcher struct {
	toolscache.ListerWatcher
}

func (lw *transformingListerWatcher) ListWithContext(ctx context.Context, opts metav1.ListOptions) (runtime.Object, error) {
	return toolscache.ToListerWithContext(lw.ListerWatcher).ListWithContext(ctx, opts)
}

func (lw *transformingListerWatcher) Watch(opts metav1.ListOptions) (watch.Interface, error) {
	return lw.WatchWithContext(context.Background(), opts)
}

func (lw *transformingListerWatcher) WatchWithContext(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	source, err := toolscache.ToWatcherWithContext(lw.ListerWatcher).WatchWithContext(ctx, opts)
	if err != nil {
		return nil, err
	}
	return newTransformingWatch(ctx, source), nil
}

type transformingWatch struct {
	source watch.Interface
	result chan watch.Event
	stop   chan struct{}
	once   sync.Once
}

func newTransformingWatch(ctx context.Context, source watch.Interface) *transformingWatch {
	w := &transformingWatch{source: source, result: make(chan watch.Event), stop: make(chan struct{})}
	go w.run(ctx)
	return w
}

func (w *transformingWatch) Stop() {
	w.once.Do(func() { close(w.stop); w.source.Stop() })
}

func (w *transformingWatch) ResultChan() <-chan watch.Event { return w.result }

func (w *transformingWatch) run(ctx context.Context) {
	defer close(w.result)
	defer w.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stop:
			return
		case event, ok := <-w.source.ResultChan():
			if !ok {
				return
			}
			switch event.Type {
			case watch.Added, watch.Modified, watch.Deleted:
				if event.Object != nil {
					obj, _ := Transform(event.Object)
					event.Object = obj.(runtime.Object)
				}
			}
			select {
			case w.result <- event:
			case <-ctx.Done():
				return
			case <-w.stop:
				return
			}
		}
	}
}
