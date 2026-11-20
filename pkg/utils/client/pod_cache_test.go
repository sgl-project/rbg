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

package client

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	clientfeatures "k8s.io/client-go/features"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	_ "k8s.io/kubernetes/pkg/features"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	"sigs.k8s.io/rbgs/pkg/podcache"
)

func TestPodCacheAPI(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("set KUBEBUILDER_ASSETS to run isolated API integration tests")
	}
	oldGate := clientfeatures.FeatureGates().Enabled(clientfeatures.WatchListClient)
	t.Cleanup(func() {
		require.NoError(t, utilfeature.DefaultMutableFeatureGate.Set(fmt.Sprintf("WatchListClient=%t", oldGate)))
	})
	for _, streamingServer := range []bool{false, true} {
		t.Run(fmt.Sprintf("server-streaming=%t", streamingServer), func(t *testing.T) {
			env := &envtest.Environment{}
			env.ControlPlane.GetAPIServer().Configure().Set("advertise-address", "127.0.0.1")
			env.ControlPlane.GetAPIServer().Configure().Set("feature-gates", fmt.Sprintf("WatchList=%t", streamingServer))
			cfg, err := env.Start()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, env.Stop()) })
			direct, err := client.New(cfg, client.Options{Scheme: clientgoscheme.Scheme})
			require.NoError(t, err)
			ctx := context.Background()
			require.NoError(t, direct.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cache-test"}}))
			for _, name := range []string{"unrelated", "legacy", "owned"} {
				pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "cache-test"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "test/image"}}}}
				if name == "legacy" {
					pod.Labels = map[string]string{constants.GroupNameLabelKey: "group"}
				}
				if name == "owned" {
					pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "workloads.x-k8s.io/v1alpha2", Kind: "RoleInstance", Name: "owner", UID: "owner-uid"}}
				}
				require.NoError(t, direct.Create(ctx, pod))
			}
			for _, tc := range []struct {
				scope  string
				stream bool
			}{{podcache.ScopeAll, false}, {podcache.ScopeFiltered, false}, {podcache.ScopeFiltered, true}, {podcache.ScopeAll, true}} {
				t.Run(fmt.Sprintf("%s-client-streaming=%t", tc.scope, tc.stream), func(t *testing.T) {
					require.NoError(t, utilfeature.DefaultMutableFeatureGate.Set(fmt.Sprintf("WatchListClient=%t", tc.stream)))
					requests := &podCacheRequests{}
					cacheConfig := rest.CopyConfig(cfg)
					cacheConfig.Wrap(requests.wrap)
					c, err := NewCacheForScope(tc.scope)(cacheConfig, cache.Options{Scheme: clientgoscheme.Scheme})
					require.NoError(t, err)
					require.NoError(t, c.IndexField(ctx, &corev1.Pod{}, "ownerUID", func(obj client.Object) []string {
						values := make([]string, 0, len(obj.GetOwnerReferences()))
						for _, owner := range obj.GetOwnerReferences() {
							values = append(values, string(owner.UID))
						}
						return values
					}))
					runCtx, cancel := context.WithCancel(ctx)
					done := make(chan error, 1)
					go func() { done <- c.Start(runCtx) }()
					t.Cleanup(func() {
						cancel()
						select {
						case err := <-done:
							require.NoError(t, err)
						case <-time.After(10 * time.Second):
							t.Error("cache did not stop")
						}
					})
					syncCtx, syncCancel := context.WithTimeout(ctx, 15*time.Second)
					defer syncCancel()
					require.True(t, c.WaitForCacheSync(syncCtx))
					require.Equal(t, tc.stream, requests.watchList.Load(), "streaming list requested")
					require.Equal(t, !tc.stream || !streamingServer, requests.list.Load(), "ordinary Pod List requested")
					read := func(name string) *corev1.Pod {
						p := &corev1.Pod{}
						require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "cache-test", Name: name}, p))
						return p
					}
					for _, name := range []string{"unrelated", "legacy", "owned"} {
						p := read(name)
						if tc.scope == podcache.ScopeFiltered && name == "unrelated" {
							require.Empty(t, p.Spec.Containers)
						} else {
							require.Len(t, p.Spec.Containers, 1)
						}
					}
					for _, noCopy := range []bool{false, true} {
						for _, selector := range []client.ListOption{client.MatchingLabels{constants.GroupNameLabelKey: "group"}, client.MatchingFields{"ownerUID": "owner-uid"}} {
							opts := []client.ListOption{selector, client.InNamespace("cache-test")}
							if noCopy {
								opts = append(opts, DisableDeepCopy)
							}
							var list corev1.PodList
							require.NoError(t, c.List(ctx, &list, opts...))
							require.Len(t, list.Items, 1)
							require.Len(t, list.Items[0].Spec.Containers, 1)
						}
					}
					unrelated := &corev1.Pod{}
					key := types.NamespacedName{Namespace: "cache-test", Name: "unrelated"}
					require.NoError(t, direct.Get(ctx, key, unrelated))
					uid := unrelated.UID
					unrelated.Labels = map[string]string{constants.ManagedLabelKey: "true"}
					require.NoError(t, direct.Update(ctx, unrelated))
					require.Eventually(t, func() bool { return len(read("unrelated").Spec.Containers) == 1 }, 10*time.Second, 20*time.Millisecond)
					unrelated.Labels = nil
					require.NoError(t, direct.Update(ctx, unrelated))
					require.Eventually(t, func() bool { return read("unrelated").ResourceVersion == unrelated.ResourceVersion }, 10*time.Second, 20*time.Millisecond)
					require.Equal(t, uid, read("unrelated").UID)
					if tc.scope == podcache.ScopeFiltered {
						require.Empty(t, read("unrelated").Spec.Containers)
					}
					// Transforms are cache-only: the API object is unchanged when switching scopes.
					require.NoError(t, direct.Get(ctx, key, unrelated))
					require.Len(t, unrelated.Spec.Containers, 1)
					for _, noCopy := range []bool{false, true} {
						opts := []client.ListOption{client.InNamespace("cache-test")}
						if noCopy {
							opts = append(opts, DisableDeepCopy)
						}
						var list corev1.PodList
						require.NoError(t, c.List(ctx, &list, opts...))
						names := make([]string, 0, len(list.Items))
						for i := range list.Items {
							pod := &list.Items[i]
							names = append(names, pod.Name)
							if tc.scope == podcache.ScopeFiltered && pod.Name == "unrelated" {
								require.Empty(t, pod.Spec.Containers)
							} else {
								require.Len(t, pod.Spec.Containers, 1)
							}
						}
						require.ElementsMatch(t, []string{"unrelated", "legacy", "owned"}, names)
					}
					transient := unrelated.DeepCopy()
					transient.Name = "delete-event"
					transient.ResourceVersion = ""
					transient.UID = ""
					require.NoError(t, direct.Create(ctx, transient))
					transientKey := client.ObjectKeyFromObject(transient)
					require.Eventually(t, func() bool { return c.Get(ctx, transientKey, &corev1.Pod{}) == nil }, 10*time.Second, 20*time.Millisecond)
					require.NoError(t, direct.Delete(ctx, transient, client.GracePeriodSeconds(0)))
					require.Eventually(t, func() bool { return apierrors.IsNotFound(c.Get(ctx, transientKey, &corev1.Pod{})) }, 10*time.Second, 20*time.Millisecond)
				})
			}
		})
	}
}

type podCacheRequests struct {
	watchList atomic.Bool
	list      atomic.Bool
}

func (r *podCacheRequests) wrap(next http.RoundTripper) http.RoundTripper {
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodGet && req.URL.Path == "/api/v1/pods" {
			query := req.URL.Query()
			if query.Get("sendInitialEvents") == "true" {
				r.watchList.Store(true)
			} else if query.Get("watch") != "true" {
				r.list.Store(true)
			}
		}
		return next.RoundTrip(req)
	})
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
