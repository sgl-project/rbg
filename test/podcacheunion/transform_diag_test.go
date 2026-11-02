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
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestAPITransformSyncHeapTimeline is a diagnostic, not a measurement: it shows
// where the transform runs during the initial sync and how large the Go heap
// grows meanwhile, to explain the startup peaks the comparison records. Run it
// with and without KUBE_FEATURE_WatchListClient and the API server's WatchList
// gate.
func TestAPITransformSyncHeapTimeline(t *testing.T) {
	if os.Getenv("RBG_POD_UNION_TRANSFORM_DIAG") != "1" {
		t.Skip("opt-in diagnostic")
	}
	related := envInt(t, "RBG_POD_UNION_RELATED_PODS", 48)
	unrelated := envInt(t, "RBG_POD_UNION_UNRELATED_PODS", 1000)
	ns := namespace(t)
	for i := range related + unrelated {
		values := map[string]string{"app": "unrelated"}
		if i < related {
			values = map[string]string{groupLabel: "g", instanceLabel: "i", warmupLabel: "w", managedLabel: "true"}
		}
		p := pod(ns, fmt.Sprintf("diag-%05d", i), values)
		for index := range 64 {
			p.Spec.Containers[0].Env = append(p.Spec.Containers[0].Env, corev1.EnvVar{Name: fmt.Sprintf("DATA_%03d", index), Value: strings.Repeat("x", 512)})
		}
		create(t, p)
	}
	for _, mode := range []string{"full", "transform-labels-owners", "transform-labels-owners-watch"} {
		var mu sync.Mutex
		var calls []time.Duration
		podOptions := cache.ByObject{Field: fields.OneTermEqualSelector("metadata.namespace", ns)}
		options := cache.Options{Scheme: probeScheme}
		start := time.Now()
		if mode != "full" {
			transform := PodCacheTransform(StubIdentityLabelsOwners)
			counted := func(obj interface{}) (interface{}, error) {
				mu.Lock()
				calls = append(calls, time.Since(start))
				mu.Unlock()
				return transform(obj)
			}
			podOptions.Transform = counted
			if mode == "transform-labels-owners-watch" {
				options.NewInformer = WatchStreamTransformInformer(counted)
			}
		}
		options.ByObject = map[client.Object]cache.ByObject{&corev1.Pod{}: podOptions}
		runtime.GC()
		runtime.GC()
		var base runtime.MemStats
		runtime.ReadMemStats(&base)
		c, informer, ctx, cancel := newCacheWithOptions(t, options)
		var peakAlloc, peakInuse, peakSys uint64
		stop, done := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(done)
			for {
				var stats runtime.MemStats
				runtime.ReadMemStats(&stats)
				peakAlloc, peakInuse, peakSys = max(peakAlloc, stats.HeapAlloc), max(peakInuse, stats.HeapInuse), max(peakSys, stats.HeapSys)
				select {
				case <-stop:
					return
				case <-time.After(2 * time.Millisecond):
				}
			}
		}()
		start = time.Now()
		done2 := make(chan error, 1)
		go func() { done2 <- c.Start(ctx) }()
		for !informer.HasSynced() {
			if time.Since(start) > time.Minute {
				cancel()
				close(stop)
				<-done
				t.Fatal("initial cache sync timed out")
			}
			time.Sleep(time.Millisecond)
		}
		synced := time.Since(start)
		close(stop)
		<-done
		var end runtime.MemStats
		runtime.ReadMemStats(&end)
		mu.Lock()
		var before, after int
		for _, at := range calls {
			if at <= synced {
				before++
			} else {
				after++
			}
		}
		first, last := time.Duration(0), time.Duration(0)
		if len(calls) > 0 {
			first, last = calls[0], calls[len(calls)-1]
		}
		mu.Unlock()
		t.Logf("%s streaming=%s: sync %v; transform calls %d (first %v, last %v, after sync %d); peak HeapAlloc %.1f MiB, HeapInuse %.1f MiB, HeapSys %.1f MiB above a %.1f MiB baseline; GCs during sync %d",
			mode, os.Getenv("KUBE_FEATURE_WatchListClient"), synced.Round(time.Millisecond), before+after, first.Round(time.Millisecond), last.Round(time.Millisecond), after,
			mib(peakAlloc), mib(peakInuse), mib(peakSys), mib(base.HeapAlloc), end.NumGC-base.NumGC)
		cancel()
		if err := <-done2; err != nil {
			t.Fatal(err)
		}
	}
}
