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
	"net/http"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// TestNewReconcilerWithNamespaceScopedCache runs NewReconciler against a real
// controller-runtime cache, offline: a static RESTMapper answers every lookup
// and informers are never started. On main the DefaultNamespaces case panics
// as in #581. It also pins the remedy documented in the setup error: a
// ByObject entry with a nil Namespaces inherits DefaultNamespaces, so the
// per-type opt-out must set an explicit empty Namespaces map.
func TestNewReconcilerWithNamespaceScopedCache(t *testing.T) {
	// newReconciler overwrites this package-level client.
	defer func(c client.Client) { sigsruntimeClient = c }(sigsruntimeClient)

	defaultNamespaces := map[string]cache.Config{"ns-a": {}}
	nsSelector := fields.OneTermEqualSelector("metadata.namespace", "ns-a")
	cases := []struct {
		name    string
		opts    cache.Options
		wantErr string
	}{
		{name: "cluster-wide cache", opts: cache.Options{}},
		{
			name:    "DefaultNamespaces",
			opts:    cache.Options{DefaultNamespaces: defaultNamespaces},
			wantErr: "Kind=RoleInstanceSet",
		},
		{
			// A nil ByObject.Namespaces inherits DefaultNamespaces.
			name: "DefaultNamespaces and field selectors that still inherit it",
			opts: cache.Options{
				DefaultNamespaces: defaultNamespaces,
				ByObject: map[client.Object]cache.ByObject{
					&workloadsv1alpha2.RoleInstanceSet{}: {Field: nsSelector},
					&workloadsv1alpha2.RoleInstance{}:    {Field: nsSelector},
				},
			},
			wantErr: "Kind=RoleInstanceSet",
		},
		{
			// An empty, non-nil ByObject.Namespaces opts out of DefaultNamespaces.
			name: "DefaultNamespaces with both kinds opted out",
			opts: cache.Options{
				DefaultNamespaces: defaultNamespaces,
				ByObject: map[client.Object]cache.ByObject{
					&workloadsv1alpha2.RoleInstanceSet{}: {Namespaces: map[string]cache.Config{}, Field: nsSelector},
					&workloadsv1alpha2.RoleInstance{}:    {Namespaces: map[string]cache.Config{}, Field: nsSelector},
				},
			},
		},
		{
			// Only RoleInstanceSet is opted out; RoleInstance still inherits
			// DefaultNamespaces and exercises the second informer lookup.
			name: "DefaultNamespaces with only RoleInstanceSet opted out",
			opts: cache.Options{
				DefaultNamespaces: defaultNamespaces,
				ByObject: map[client.Object]cache.ByObject{
					&workloadsv1alpha2.RoleInstanceSet{}: {Namespaces: map[string]cache.Config{}, Field: nsSelector},
				},
			},
			wantErr: "Kind=RoleInstance",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := clientgoscheme.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := workloadsv1alpha2.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			mapper := meta.NewDefaultRESTMapper(nil)
			mapper.Add(controllerKind, meta.RESTScopeNamespace)
			mapper.Add(workloadsv1alpha2.GroupVersion.WithKind("RoleInstance"), meta.RESTScopeNamespace)
			mgr, err := manager.New(&rest.Config{Host: "http://127.0.0.1:1"}, manager.Options{
				Scheme:                 scheme,
				MapperProvider:         func(*rest.Config, *http.Client) (meta.RESTMapper, error) { return mapper, nil },
				Cache:                  tc.opts,
				Metrics:                metricsserver.Options{BindAddress: "0"},
				HealthProbeBindAddress: "0",
			})
			if err != nil {
				t.Fatalf("manager.New: %v", err)
			}

			_, err = NewReconciler(mgr)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected success, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected an error mentioning %q, got %v", tc.wantErr, err)
			}
		})
	}
}
