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
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	toolscache "k8s.io/client-go/tools/cache"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// StubFields selects what an inadmissible Pod retains in the transform cache.
type StubFields int

const (
	// StubIdentity keeps name, namespace, UID, resourceVersion, and
	// deletionTimestamp: enough for a named existence check, and nothing a label
	// selector or the owner index can match.
	StubIdentity StubFields = iota
	// StubIdentityLabelsOwners also keeps labels and ownerReferences.
	StubIdentityLabelsOwners
)

// transformAdmitted extends the union's label rule with a condition only a
// single informer can afford: a Pod with any ownerReference into the RBG API
// group stays whole, even after every admission label was removed. The owner
// index therefore finds it, and no stub ever carries an RBG ownerReference that
// an owner-index lookup could match.
func transformAdmitted(p *corev1.Pod) bool {
	if admitted(p) {
		return true
	}
	for _, ref := range p.OwnerReferences {
		if gv, err := schema.ParseGroupVersion(ref.APIVersion); err == nil && gv.Group == workloadsv1alpha2.GroupVersion.Group {
			return true
		}
	}
	return false
}

// PodCacheTransform keeps admitted Pods intact apart from managedFields and
// reduces every other Pod to a stub. It must be idempotent: with the
// watch-stream wrapper (transform_stream.go) a watched object is transformed in
// the wrapper and again when the informer's FIFO queues it, and client-go
// requires an idempotent transform. A stub re-enters as a stub, and an admitted
// Pod has no managedFields left to strip.
func PodCacheTransform(fields StubFields) toolscache.TransformFunc {
	return func(obj interface{}) (interface{}, error) {
		p, ok := obj.(*corev1.Pod)
		if !ok {
			return obj, nil
		}
		if transformAdmitted(p) {
			p.ManagedFields = nil
			return p, nil
		}
		stub := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name:              p.Name,
			Namespace:         p.Namespace,
			UID:               p.UID,
			ResourceVersion:   p.ResourceVersion,
			DeletionTimestamp: p.DeletionTimestamp,
		}}
		if fields == StubIdentityLabelsOwners {
			stub.Labels = p.Labels
			stub.OwnerReferences = p.OwnerReferences
		}
		return stub, nil
	}
}

// isStub reports whether a cached Pod is a transform stub. Every Pod accepted by
// the API server has at least one container, so an empty spec marks a stub.
func isStub(p *corev1.Pod) bool {
	return len(p.Spec.Containers) == 0
}

// responseCounter counts decoded API response bytes, a proxy for apiserver
// egress. Go's transport decompresses gzip before the wrapper sees the body, so
// this is payload size, not wire size.
type responseCounter struct {
	bytes atomic.Int64
}

func (c *responseCounter) wrap(rt http.RoundTripper) http.RoundTripper {
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		resp, err := rt.RoundTrip(req)
		if resp != nil && resp.Body != nil {
			resp.Body = &countingBody{ReadCloser: resp.Body, counter: &c.bytes}
		}
		return resp, err
	})
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type countingBody struct {
	io.ReadCloser
	counter *atomic.Int64
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.counter.Add(int64(n))
	return n, err
}

// relistProbe wraps the ordinary informer's ListerWatcher. It counts List and
// streaming-list requests and can end the open watch with 410 Expired, which
// makes the reflector perform a full relist exactly as after a compacted watch.
type relistProbe struct {
	mu              sync.Mutex
	lists           int
	watches         int
	watchLists      int
	initialEventEnd int
	listDone        []time.Time
	initialEnd      []time.Time
	requestStart    []time.Time
	open            map[*expiringWatch]struct{}
	afterExpiry     func() // test hook: change the API snapshot while disconnected
}

func newRelistProbe() *relistProbe {
	return &relistProbe{open: map[*expiringWatch]struct{}{}}
}

// NewInformer matches cache.Options.NewInformer. controller-runtime applies the
// configured transform to the returned informer afterwards, so the transform
// path is unchanged by this wrapper.
func (r *relistProbe) NewInformer(lw toolscache.ListerWatcher, obj runtime.Object, resync time.Duration, indexers toolscache.Indexers) toolscache.SharedIndexInformer {
	return toolscache.NewSharedIndexInformer(&relistLW{inner: toolscache.ToListerWatcherWithContext(lw), probe: r}, obj, resync, indexers)
}

// expireWatches ends every open watch with 410 Expired and returns how many it
// ended.
func (r *relistProbe) expireWatches() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	for w := range r.open {
		w.expireOnce.Do(func() { close(w.expire) })
	}
	return len(r.open)
}

// relistCompletion returns when the first relist request after since started
// and when its data was complete: a List response, or the initial-events-end
// bookmark of a streaming list.
func (r *relistProbe) relistCompletion(since time.Time) (started, done time.Time, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, at := range r.requestStart {
		if at.After(since) {
			started = at
			break
		}
	}
	if started.IsZero() {
		return started, done, false
	}
	for _, list := range [][]time.Time{r.listDone, r.initialEnd} {
		for _, at := range list {
			if at.After(started) && (done.IsZero() || at.Before(done)) {
				done = at
			}
		}
	}
	return started, done, !done.IsZero()
}

func (r *relistProbe) counts() (lists, watches, watchLists, initialEventEnd int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lists, r.watches, r.watchLists, r.initialEventEnd
}

type relistLW struct {
	inner toolscache.ListerWatcherWithContext
	probe *relistProbe
}

func (l *relistLW) List(options metav1.ListOptions) (runtime.Object, error) {
	return l.ListWithContext(context.Background(), options)
}

func (l *relistLW) Watch(options metav1.ListOptions) (watch.Interface, error) {
	return l.WatchWithContext(context.Background(), options)
}

func (l *relistLW) ListWithContext(ctx context.Context, options metav1.ListOptions) (runtime.Object, error) {
	l.probe.mu.Lock()
	l.probe.lists++
	l.probe.requestStart = append(l.probe.requestStart, time.Now())
	l.probe.mu.Unlock()
	obj, err := l.inner.ListWithContext(ctx, options)
	if err == nil {
		l.probe.mu.Lock()
		l.probe.listDone = append(l.probe.listDone, time.Now())
		l.probe.mu.Unlock()
	}
	return obj, err
}

func (l *relistLW) WatchWithContext(ctx context.Context, options metav1.ListOptions) (watch.Interface, error) {
	streaming := options.SendInitialEvents != nil && *options.SendInitialEvents
	l.probe.mu.Lock()
	l.probe.watches++
	if streaming {
		l.probe.watchLists++
		l.probe.requestStart = append(l.probe.requestStart, time.Now())
	}
	l.probe.mu.Unlock()
	inner, err := l.inner.WatchWithContext(ctx, options)
	if err != nil {
		return nil, err
	}
	w := &expiringWatch{inner: inner, result: make(chan watch.Event), expire: make(chan struct{}), done: make(chan struct{}), probe: l.probe}
	l.probe.mu.Lock()
	l.probe.open[w] = struct{}{}
	l.probe.mu.Unlock()
	go w.run()
	return w, nil
}

// expiringWatch forwards an underlying watch and, once expired, delivers a
// single 410 Expired error event instead of further events.
type expiringWatch struct {
	inner      watch.Interface
	result     chan watch.Event
	expire     chan struct{}
	expireOnce sync.Once
	done       chan struct{}
	stopOnce   sync.Once
	probe      *relistProbe
}

func (w *expiringWatch) run() {
	defer func() {
		w.probe.mu.Lock()
		delete(w.probe.open, w)
		w.probe.mu.Unlock()
		close(w.result)
	}()
	for {
		select {
		case <-w.done:
			return
		case <-w.expire:
			w.inner.Stop()
			if w.probe.afterExpiry != nil {
				w.probe.afterExpiry()
			}
			status := &metav1.Status{Status: metav1.StatusFailure, Code: http.StatusGone, Reason: metav1.StatusReasonExpired, Message: "injected watch expiry"}
			select {
			case w.result <- watch.Event{Type: watch.Error, Object: status}:
			case <-w.done:
			}
			return
		case event, ok := <-w.inner.ResultChan():
			if !ok {
				return
			}
			if event.Type == watch.Bookmark {
				if accessor, err := meta.Accessor(event.Object); err == nil && accessor.GetAnnotations()[metav1.InitialEventsAnnotationKey] == "true" {
					w.probe.mu.Lock()
					w.probe.initialEventEnd++
					w.probe.initialEnd = append(w.probe.initialEnd, time.Now())
					w.probe.mu.Unlock()
				}
			}
			select {
			case w.result <- event:
			case <-w.done:
				return
			}
		}
	}
}

func (w *expiringWatch) Stop() {
	w.stopOnce.Do(func() {
		close(w.done)
		w.inner.Stop()
	})
}

func (w *expiringWatch) ResultChan() <-chan watch.Event {
	return w.result
}

var _ toolscache.ListerWatcherWithContext = (*relistLW)(nil)
var _ watch.Interface = (*expiringWatch)(nil)
