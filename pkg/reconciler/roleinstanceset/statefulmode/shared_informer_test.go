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

package statefulmode

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	toolscache "k8s.io/client-go/tools/cache"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// stubCache lets a test pin the Informer returned for one GVK. The embedded
// interface keeps the rest of cache.Cache unimplemented; nothing in the code
// under test calls it.
type stubCache struct {
	ctrlcache.Cache
	informer ctrlcache.Informer
	err      error
}

func (c *stubCache) GetInformerForKind(ctx context.Context, gvk schema.GroupVersionKind, opts ...ctrlcache.InformerGetOption) (ctrlcache.Informer, error) {
	if c.err != nil {
		return nil, c.err
	}
	return c.informer, nil
}

// informerWithoutIndexer models controller-runtime's multiNamespaceInformer:
// it satisfies cache.Informer but has no single indexer to build a lister on.
type informerWithoutIndexer struct {
	ctrlcache.Informer
}

func TestSharedIndexInformerForKindReturnsIndexInformer(t *testing.T) {
	shared := toolscache.NewSharedIndexInformer(nil, nil, time.Hour, nil)
	cacher := &stubCache{informer: shared}

	got, err := sharedIndexInformerForKind(cacher, workloadsv1alpha2.GroupVersion.WithKind("RoleInstanceSet"))
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got != shared {
		t.Fatalf("expected the shared informer to be returned unchanged")
	}
}

func TestSharedIndexInformerForKindErrorsOnNonIndexInformer(t *testing.T) {
	cacher := &stubCache{informer: &informerWithoutIndexer{}}

	got, err := sharedIndexInformerForKind(cacher, controllerKind)
	if err == nil {
		t.Fatalf("expected an error when the informer has no indexer, got informer %v", got)
	}
	if got != nil {
		t.Fatalf("expected no informer on error, got %v", got)
	}
	for _, want := range []string{
		"does not expose a single SharedIndexInformer",
		"DefaultNamespaces",
		"ByObject",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
}

func TestSharedIndexInformerForKindPropagatesCacheError(t *testing.T) {
	cacheErr := errors.New("boom")
	cacher := &stubCache{err: cacheErr}

	got, err := sharedIndexInformerForKind(cacher, controllerKind)
	if !errors.Is(err, cacheErr) {
		t.Fatalf("expected the cache error to be wrapped, got %v", err)
	}
	if got != nil {
		t.Fatalf("expected no informer on error, got %v", got)
	}
}
