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

// Package podcacheunion is a design-validation prototype, not a production cache.
// It is deliberately excluded from ordinary builds.
package podcacheunion

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

const (
	groupLabel    = "rbg.workloads.x-k8s.io/group-name"
	instanceLabel = "rbg.workloads.x-k8s.io/role-instance-name"
	warmupLabel   = "workloads.x-k8s.io/warmup-name"
	managedLabel  = "rbg.workloads.x-k8s.io/managed"
)

var selectors = []string{groupLabel, instanceLabel, warmupLabel, managedLabel + "=true"}

func admitted(p *corev1.Pod) bool {
	_, group := p.Labels[groupLabel]
	_, instance := p.Labels[instanceLabel]
	_, warmup := p.Labels[warmupLabel]
	return group || instance || warmup || p.Labels[managedLabel] == "true"
}

// Pod RVs from kube-apiserver are decimal, monotonically increasing values.
// Compare arbitrary-length values rather than converting to uint64. This probe
// rejects nonnumeric versions instead of silently imposing an incorrect order.
func compareRV(a, b string) int {
	for _, value := range []string{a, b} {
		if value == "" || strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			panic("probe requires numeric kube-apiserver Pod resource versions")
		}
	}
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return strings.Compare(a, b)
}

type record struct {
	pod *corev1.Pod // immutable; shared with the business index; nil is a deletion watermark
	rv  string
}

type feedWatch struct {
	state  *unionState
	ch     chan watch.Event
	closed bool // protected by state.mu
}

func (w *feedWatch) ResultChan() <-chan watch.Event { return w.ch }
func (w *feedWatch) Stop() {
	w.state.mu.Lock()
	defer w.state.mu.Unlock()
	w.state.closeFeedLocked(w)
}

type unionState struct {
	mu            sync.Mutex
	records       map[string]record
	members       [4]map[string]string // source identities only; no four full Pod caches
	watermarks    [4]string
	synced        [4]bool
	initialDone   [4]chan struct{}
	ready         chan struct{}
	readyClosed   bool
	feed          *feedWatch
	feedAvailable bool
	buffer        int
	copyPods      bool          // comparison-only mode for the original two-copy prototype
	listGate      chan struct{} // bounds page fetch and merge, including recovery
	pending       map[string]string
	queue         workqueue.TypedRateLimitingInterface[string]
	get           func(context.Context, string, string) (*corev1.Pod, error)
	boundaryGets  int
	feedOverflows int
}

func newState(buffer int, get func(context.Context, string, string) (*corev1.Pod, error)) *unionState {
	u := &unionState{
		records: map[string]record{}, ready: make(chan struct{}), buffer: buffer,
		pending: map[string]string{}, get: get,
		listGate: make(chan struct{}, 1),
		queue:    workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]()),
	}
	for i := range u.members {
		u.members[i] = map[string]string{}
		u.initialDone[i] = make(chan struct{})
	}
	return u
}

func (u *unionState) closeFeedLocked(w *feedWatch) {
	if w == nil || w.closed {
		return
	}
	w.closed = true
	close(w.ch)
	if u.feed == w {
		u.feed = nil
		u.feedAvailable = false
	}
}

func (u *unionState) emitLocked(kind watch.EventType, p *corev1.Pod) {
	if u.feed == nil {
		return
	}
	object := p
	if u.copyPods {
		object = p.DeepCopy()
	}
	select {
	case u.feed.ch <- watch.Event{Type: kind, Object: object}:
	default:
		// A slow consumer causes a relist of the bounded union, never a full API
		// list and never unbounded buffering of full Pod objects.
		u.feedOverflows++
		u.closeFeedLocked(u.feed)
	}
}

func (u *unionState) applyLocked(p *corev1.Pod) {
	key := p.Namespace + "/" + p.Name
	old, exists := u.records[key]
	if exists && compareRV(p.ResourceVersion, old.rv) <= 0 {
		return
	}
	u.records[key] = record{pod: p, rv: p.ResourceVersion}
	kind := watch.Modified
	if old.pod == nil {
		kind = watch.Added
	}
	u.emitLocked(kind, p)
}

func (u *unionState) removeLocked(key, rv string) {
	old, exists := u.records[key]
	if exists && compareRV(rv, old.rv) < 0 {
		return
	}
	u.records[key] = record{rv: rv}
	if old.pod != nil {
		deleted := old.pod.DeepCopy()
		deleted.ResourceVersion = rv
		u.emitLocked(watch.Deleted, deleted)
	}
}

func (u *unionState) boundaryLocked(key, rv string) {
	if old, ok := u.records[key]; ok && compareRV(rv, old.rv) < 0 {
		return
	}
	if prior, ok := u.pending[key]; !ok || compareRV(rv, prior) > 0 {
		u.pending[key] = rv
	}
	u.queue.Add(key)
}

func (u *unionState) resolve(ctx context.Context, key string) error {
	u.mu.Lock()
	rv, pending := u.pending[key]
	if !pending {
		u.mu.Unlock()
		return nil
	}
	if latest, ok := u.records[key]; ok && compareRV(latest.rv, rv) > 0 {
		delete(u.pending, key)
		u.mu.Unlock()
		return nil
	}
	u.boundaryGets++
	u.mu.Unlock()
	parts := strings.SplitN(key, "/", 2)
	pod, err := u.get(ctx, parts[0], parts[1])
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if current, ok := u.pending[key]; ok && current == rv {
		delete(u.pending, key)
	}
	if apierrors.IsNotFound(err) {
		u.removeLocked(key, rv)
	} else if admitted(pod) {
		u.applyLocked(pod)
	} else {
		u.removeLocked(key, pod.ResourceVersion)
	}
	u.pruneLocked()
	return nil
}

func (u *unionState) runResolver(ctx context.Context) {
	defer u.queue.ShutDown()
	go func() { <-ctx.Done(); u.queue.ShutDown() }()
	for {
		key, shutdown := u.queue.Get()
		if shutdown {
			return
		}
		if err := u.resolve(ctx, key); err != nil && ctx.Err() == nil {
			u.queue.AddRateLimited(key)
		} else {
			u.queue.Forget(key)
		}
		u.queue.Done(key)
	}
}

func (u *unionState) pruneLocked() {
	// Keep only deletion identities until every independent source has passed
	// that deletion. This prevents stale replays from resurrecting a replaced
	// Pod. Watermarks also advance on watch bookmarks, not just matching Pods.
	for key, rec := range u.records {
		if rec.pod != nil {
			continue
		}
		past := true
		for _, rv := range u.watermarks {
			if rv == "" || compareRV(rv, rec.rv) < 0 {
				past = false
				break
			}
		}
		if past {
			delete(u.records, key)
		}
	}
}

func (u *unionState) list(ctx context.Context, _ metav1.ListOptions) (runtime.Object, error) {
	select {
	case <-u.ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.closeFeedLocked(u.feed)
	u.feed = &feedWatch{state: u, ch: make(chan watch.Event, u.buffer)}
	u.feedAvailable = true
	if u.copyPods {
		// Preserve the original prototype's snapshot path for an apples-to-apples
		// comparison in separate memory-probe processes.
		list := &corev1.PodList{ListMeta: metav1.ListMeta{ResourceVersion: "1"}}
		for _, rec := range u.records {
			if rec.pod != nil {
				list.Items = append(list.Items, *rec.pod.DeepCopy())
			}
		}
		return list, nil
	}
	// RawExtension.Object lets the reflector extract the original Pod pointers.
	// A PodList's value Items would introduce another Pod struct per item even
	// with a shallow copy. Each published version stays immutable, so queued
	// events and snapshots remain valid while newer versions replace records.
	list := &metav1.List{ListMeta: metav1.ListMeta{ResourceVersion: "1"}}
	for _, rec := range u.records {
		if rec.pod != nil {
			list.Items = append(list.Items, runtime.RawExtension{Object: rec.pod})
		}
	}
	return list, nil
}

func (u *unionState) watch(_ context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if opts.SendInitialEvents != nil && *opts.SendInitialEvents {
		return nil, apierrors.NewBadRequest("prototype union bridge uses list then watch")
	}
	if u.feed == nil || !u.feedAvailable {
		// The bridge has no single API cursor. Its four reflectors retain their
		// own cursors. Force an in-memory union relist after any feed disconnect.
		return nil, apierrors.NewResourceExpired("resnapshot the in-memory union")
	}
	u.feedAvailable = false
	return u.feed, nil
}

type sourceStore struct {
	union  *unionState
	source int
}

func (s *sourceStore) Add(obj interface{}) error { return s.Update(obj) }
func (s *sourceStore) Update(obj interface{}) error {
	pod := obj.(*corev1.Pod)
	u := s.union
	u.mu.Lock()
	defer u.mu.Unlock()
	u.members[s.source][pod.Namespace+"/"+pod.Name] = string(pod.UID)
	u.applyLocked(pod)
	return nil
}
func (s *sourceStore) Delete(obj interface{}) error {
	pod := obj.(*corev1.Pod)
	u := s.union
	u.mu.Lock()
	defer u.mu.Unlock()
	key := pod.Namespace + "/" + pod.Name
	if u.members[s.source][key] == string(pod.UID) {
		delete(u.members[s.source], key)
	}
	// A filtered watch's DELETE can mean selector exit rather than deletion.
	// Resolve at the boundary rather than deleting another source's live Pod.
	u.boundaryLocked(key, pod.ResourceVersion)
	return nil
}
func (s *sourceStore) Replace(items []interface{}, rv string) error {
	u := s.union
	u.mu.Lock()
	defer u.mu.Unlock()
	old := u.members[s.source]
	next := make(map[string]string, len(items))
	for _, item := range items {
		switch object := item.(type) {
		case *corev1.Pod:
			key := object.Namespace + "/" + object.Name
			next[key] = string(object.UID)
			u.applyLocked(object)
		case *metav1.PartialObjectMetadata:
			// Streaming lists merged each full Pod as its page arrived. Only
			// identities survive until Replace commits the complete snapshot.
			next[object.Namespace+"/"+object.Name] = string(object.UID)
		default:
			return fmt.Errorf("unsupported source snapshot item %T", item)
		}
	}
	u.members[s.source] = next
	for key := range old {
		if _, exists := next[key]; !exists {
			u.boundaryLocked(key, rv)
		}
	}
	u.watermarks[s.source] = rv
	first := !u.synced[s.source]
	u.synced[s.source] = true
	if first {
		close(u.initialDone[s.source])
	}
	if !u.readyClosed && u.synced == [4]bool{true, true, true, true} {
		u.readyClosed = true
		close(u.ready)
	}
	u.pruneLocked()
	return nil
}
func (s *sourceStore) Resync() error { return nil }
func (s *sourceStore) UpdateResourceVersion(rv string) {
	u := s.union
	u.mu.Lock()
	defer u.mu.Unlock()
	if prior := u.watermarks[s.source]; prior == "" || compareRV(rv, prior) > 0 {
		u.watermarks[s.source] = rv
	}
	u.pruneLocked()
}

// Probe provides a factory for controller-runtime's real NewInformer hook.
// Its source hooks are fault injection points used only by the validation tests.
type Probe struct {
	client                      kubernetes.Interface
	Namespace                   string
	CopyPods                    bool          // benchmark-only: reproduce the original two-copy bridge
	AggregateLists              bool          // benchmark-only: retain the original parallel full snapshots
	SerialInitialLists          bool          // benchmark-only: serialize first snapshots; relists remain independent
	ListPageTimeout             time.Duration // zero uses the prototype's 10s per-page budget
	BeforeList                  func(context.Context, int) error
	mu                          sync.Mutex
	states                      []*unionState
	watches                     [4][]watch.Interface
	lists                       [4]int
	watchCalls                  [4]int
	requests                    []podRequest
	activeLists, maxActiveLists int
}

type podRequest struct {
	Selector string
	Watch    bool
	Limit    string
}

type observedTransport struct {
	base  http.RoundTripper
	probe *Probe
}

func (o *observedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/pods") {
		query := request.URL.Query()
		o.probe.mu.Lock()
		o.probe.requests = append(o.probe.requests, podRequest{Selector: query.Get("labelSelector"), Watch: query.Get("watch") == "true", Limit: query.Get("limit")})
		o.probe.mu.Unlock()
	}
	return o.base.RoundTrip(request)
}

func NewProbe(cfg *rest.Config, namespace string) (*Probe, error) {
	p := &Probe{Namespace: namespace}
	config := rest.CopyConfig(cfg)
	previous := config.WrapTransport
	config.WrapTransport = func(base http.RoundTripper) http.RoundTripper {
		if previous != nil {
			base = previous(base)
		}
		return &observedTransport{base: base, probe: p}
	}
	c, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	p.client = c
	return p, nil
}

func (p *Probe) listPage(ctx context.Context, source int, selector string, opts metav1.ListOptions) (*corev1.PodList, error) {
	p.mu.Lock()
	p.activeLists++
	if p.activeLists > p.maxActiveLists {
		p.maxActiveLists = p.activeLists
	}
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.activeLists--; p.mu.Unlock() }()
	if p.BeforeList != nil {
		if err := p.BeforeList(ctx, source); err != nil {
			return nil, err
		}
	}
	opts.LabelSelector = selector
	// Kubernetes 1.31 may ignore a limit with RV=0. Use a recent consistent
	// snapshot so every initial page is bounded on the validated server.
	if opts.ResourceVersion == "0" {
		opts.ResourceVersion = ""
	}
	p.mu.Lock()
	p.lists[source]++
	p.mu.Unlock()
	return p.client.CoreV1().Pods(p.Namespace).List(ctx, opts)
}

func (p *Probe) streamPage(ctx context.Context, source int, selector string, store *sourceStore, opts metav1.ListOptions, identities *metav1.PartialObjectMetadataList) (string, error) {
	select {
	case store.union.listGate <- struct{}{}:
		defer func() { <-store.union.listGate }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	timeout := p.ListPageTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	pageContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	page, err := p.listPage(pageContext, source, selector, opts)
	if err != nil {
		return "", err
	}
	if identities.ResourceVersion == "" {
		identities.ResourceVersion = page.ResourceVersion
	} else if identities.ResourceVersion != page.ResourceVersion {
		return "", fmt.Errorf("source %d changed snapshot resourceVersion across pages", source)
	}
	for i := range page.Items {
		// Detach the Pod struct from the page's backing array. Keep its
		// immutable fields, without deep-copying the payload; retaining one
		// element must not keep every sibling's old version alive later.
		pod := new(corev1.Pod)
		*pod = page.Items[i]
		if err := store.Update(pod); err != nil {
			return "", err
		}
		identities.Items = append(identities.Items, metav1.PartialObjectMetadata{
			ObjectMeta: metav1.ObjectMeta{Namespace: pod.Namespace, Name: pod.Name, UID: pod.UID},
		})
	}
	// Returning before the next acquisition drops the full page; only the
	// canonical payloads and metadata-only identities remain. Other sources
	// waiting for the page budget can make progress between these requests.
	return page.Continue, nil
}

func (p *Probe) streamList(ctx context.Context, source int, selector string, store *sourceStore, opts metav1.ListOptions) (runtime.Object, error) {
	opts.Limit = 31
	identities := &metav1.PartialObjectMetadataList{}
	for {
		continuation, err := p.streamPage(ctx, source, selector, store, opts, identities)
		if err != nil {
			// Do not publish snapshot completion or advance the source cursor.
			// Membership already includes partial observations, so a later
			// successful Replace can resolve objects that disappeared on retry.
			return nil, err
		}
		if continuation == "" {
			return identities, nil
		}
		opts.Continue = continuation
		opts.ResourceVersion = ""
		opts.ResourceVersionMatch = ""
	}
}

func (p *Probe) NewInformer(lw toolscache.ListerWatcher, obj runtime.Object, period time.Duration, indexes toolscache.Indexers) toolscache.SharedIndexInformer {
	if _, isPod := obj.(*corev1.Pod); !isPod {
		return toolscache.NewSharedIndexInformer(lw, obj, period, indexes)
	}
	u := newState(256, func(ctx context.Context, namespace, name string) (*corev1.Pod, error) {
		return p.client.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	})
	u.copyPods = p.CopyPods
	p.mu.Lock()
	p.states = append(p.states, u)
	p.mu.Unlock()
	bridge := &toolscache.ListWatch{ListWithContextFunc: u.list, WatchFuncWithContext: u.watch, DisableChunking: true}
	base := toolscache.NewSharedIndexInformer(bridge, obj, period, indexes)
	combined := &unionInformer{SharedIndexInformer: base, state: u, serialInitialLists: p.SerialInitialLists}
	for source, selector := range selectors {
		source, selector := source, selector
		store := &sourceStore{union: u, source: source}
		filtered := &toolscache.ListWatch{
			ListWithContextFunc: func(ctx context.Context, opts metav1.ListOptions) (runtime.Object, error) {
				if p.AggregateLists {
					return p.listPage(ctx, source, selector, opts)
				}
				return p.streamList(ctx, source, selector, store, opts)
			},
			WatchFuncWithContext: func(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
				opts.LabelSelector = selector
				opts.AllowWatchBookmarks = true
				w, err := p.client.CoreV1().Pods(p.Namespace).Watch(ctx, opts)
				if err == nil {
					p.mu.Lock()
					p.watches[source] = append(p.watches[source], w)
					p.watchCalls[source]++
					p.mu.Unlock()
				}
				return w, err
			},
		}
		r := toolscache.NewReflectorWithOptions(filtered, &corev1.Pod{}, store, toolscache.ReflectorOptions{Name: fmt.Sprintf("pod-union-%d", source)})
		r.WatchListPageSize = 31
		combined.reflectors = append(combined.reflectors, r)
	}
	return combined
}

type unionInformer struct {
	toolscache.SharedIndexInformer
	state              *unionState
	reflectors         []*toolscache.Reflector
	serialInitialLists bool
}

func (u *unionInformer) SetTransform(transform toolscache.TransformFunc) error {
	if transform != nil && !u.state.copyPods {
		// A transform may mutate its input. Isolate it from the canonical object;
		// transformed configurations intentionally do not claim shared retention.
		original := transform
		transform = func(obj interface{}) (interface{}, error) {
			if object, ok := obj.(runtime.Object); ok {
				obj = object.DeepCopyObject()
			}
			return original(obj)
		}
	}
	return u.SharedIndexInformer.SetTransform(transform)
}

func (u *unionInformer) Run(stop <-chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	u.RunWithContext(ctx)
}

func (u *unionInformer) RunWithContext(ctx context.Context) {
	var wg sync.WaitGroup
	for source, reflector := range u.reflectors {
		wg.Add(1)
		go func(source int, r *toolscache.Reflector) {
			defer wg.Done()
			if u.serialInitialLists && source > 0 {
				select {
				case <-u.state.initialDone[source-1]:
				case <-ctx.Done():
					return
				}
			}
			r.RunWithContext(ctx)
		}(source, reflector)
	}
	wg.Add(1)
	go func() { defer wg.Done(); u.state.runResolver(ctx) }()
	u.SharedIndexInformer.RunWithContext(ctx)
	wg.Wait()
}

func (u *unionInformer) HasSynced() bool {
	select {
	case <-u.state.ready:
		return u.SharedIndexInformer.HasSynced()
	default:
		return false
	}
}

var _ toolscache.SharedIndexInformer = (*unionInformer)(nil)
var _ toolscache.ReflectorStore = (*sourceStore)(nil)
var _ toolscache.ResourceVersionUpdater = (*sourceStore)(nil)
