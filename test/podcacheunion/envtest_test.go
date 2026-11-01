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
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	clientfeatures "k8s.io/client-go/features"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	rbgclient "sigs.k8s.io/rbgs/pkg/utils/client"
	"sigs.k8s.io/yaml"
)

var apiConfig *rest.Config
var apiClient client.Client
var probeScheme = kruntime.NewScheme()

// Production imports replace client-go's environment-based feature gate.
// Set the effective gate explicitly in every test process, including diagnostics.
func configureWatchListClient(enabled bool) error {
	if err := utilfeature.DefaultMutableFeatureGate.Set(fmt.Sprintf("WatchListClient=%t", enabled)); err != nil {
		return err
	}
	if got := clientfeatures.FeatureGates().Enabled(clientfeatures.WatchListClient); got != enabled {
		return fmt.Errorf("WatchListClient=%v, want %v", got, enabled)
	}
	return nil
}

func TestMain(m *testing.M) {
	for _, add := range []func(*kruntime.Scheme) error{corev1.AddToScheme, appsv1.AddToScheme, workloadsv1alpha2.AddToScheme} {
		if err := add(probeScheme); err != nil {
			panic(err)
		}
	}
	if err := configureWatchListClient(os.Getenv("KUBE_FEATURE_WatchListClient") == "true"); err != nil {
		panic(err)
	}
	// Cache subprocesses connect to their parent's API server, and the summary
	// only reads result files; neither starts its own control plane.
	if os.Getenv("RBG_POD_UNION_TRANSFORM_MODE") != "" || os.Getenv("RBG_POD_UNION_TRANSFORM_SUMMARY_DIR") != "" {
		os.Exit(m.Run())
	}
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	if assets == "" {
		assets = filepath.Join(root, "bin/k8s/1.31.0-"+runtime.GOOS+"-"+runtime.GOARCH)
	}
	data, err := os.ReadFile(filepath.Join(root, "config/crd/bases/workloads.x-k8s.io_roleinstances.yaml"))
	if err != nil {
		panic(err)
	}
	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := yaml.Unmarshal(data, crd); err != nil {
		panic(err)
	}
	env := &envtest.Environment{BinaryAssetsDirectory: assets, CRDs: []*apiextensionsv1.CustomResourceDefinition{crd}, ControlPlaneStartTimeout: 45 * time.Second, ControlPlaneStopTimeout: 30 * time.Second}
	// A network-isolated container has no default route to auto-detect.
	env.ControlPlane.GetAPIServer().Configure().Set("advertise-address", "127.0.0.1")
	// For example WatchList=true, which v1.31.0 ships as an alpha gate that is
	// off by default; the transform comparison measures both server behaviours.
	if gates := os.Getenv("RBG_POD_UNION_APISERVER_FEATURE_GATES"); gates != "" {
		env.ControlPlane.GetAPIServer().Configure().Set("feature-gates", gates)
	}
	apiConfig, err = env.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, "isolated API server failed:", err)
		os.Exit(1)
	}
	apiConfig.QPS, apiConfig.Burst = 200, 300
	apiClient, err = client.New(apiConfig, client.Options{Scheme: probeScheme})
	if err != nil {
		_ = env.Stop()
		panic(err)
	}
	fmt.Println("UNION_ENVTEST: isolated Kubernetes v1.31.0 API server + etcd; no external kubeconfig")
	code := m.Run()
	if err := env.Stop(); err != nil {
		fmt.Fprintln(os.Stderr, "test control plane shutdown:", err)
		code = 1
	}
	os.Exit(code)
}

func eventually(t *testing.T, message string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out: " + message)
}

func namespace(t *testing.T) string {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "union-probe-"}}
	if err := apiClient.Create(context.Background(), ns); err != nil {
		t.Fatal(err)
	}
	return ns.Name
}

func pod(ns, name string, values map[string]string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: values}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "pause", Image: "registry.k8s.io/pause:3.10"}}}}
}

func create(t *testing.T, object client.Object) {
	t.Helper()
	if err := apiClient.Create(context.Background(), object); err != nil {
		t.Fatal(err)
	}
}

func current(t *testing.T, original *corev1.Pod) *corev1.Pod {
	t.Helper()
	result := &corev1.Pod{}
	if err := apiClient.Get(context.Background(), client.ObjectKeyFromObject(original), result); err != nil {
		t.Fatal(err)
	}
	return result
}

func updateLabels(t *testing.T, original *corev1.Pod, values map[string]string) *corev1.Pod {
	t.Helper()
	p := current(t, original)
	p.Labels = values
	if err := apiClient.Update(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	return p
}

// newCacheWithOptions builds the repository cache wrapper with the production
// owner index and returns the Pod informer the no-deepcopy path relies on.
// Callers bound Pods to their namespace with a server-side field selector
// rather than DefaultNamespaces: that option adds controller-runtime's
// multiNamespaceInformer, which already fails this repository's no-deepcopy
// SharedIndexInformer assertion, independently of the transform.
func newCacheWithOptions(t *testing.T, options cache.Options) (cache.Cache, toolscache.SharedIndexInformer, context.Context, context.CancelFunc) {
	t.Helper()
	c, err := rbgclient.NewCache(apiConfig, options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := c.IndexField(ctx, &corev1.Pod{}, "ownerRefUID", func(obj client.Object) []string {
		var values []string
		for _, owner := range obj.GetOwnerReferences() {
			values = append(values, string(owner.UID))
		}
		return values
	}); err != nil {
		cancel()
		t.Fatal(err)
	}
	inf, err := c.GetInformer(ctx, &corev1.Pod{}, cache.BlockUntilSynced(false))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	shared, ok := inf.(toolscache.SharedIndexInformer)
	if !ok {
		cancel()
		t.Fatalf("informer %T breaks existing no-deepcopy path", inf)
	}
	return c, shared, ctx, cancel
}

func runCache(t *testing.T, c cache.Cache, ctx context.Context, cancel context.CancelFunc) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- c.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("cache shutdown: %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("cache did not shut down")
		}
	})
}

func waitSync(t *testing.T, c cache.Cache, ctx context.Context) {
	t.Helper()
	timeout, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if !c.WaitForCacheSync(timeout) {
		t.Fatal("initial cache sync failed")
	}
}

func probeFor(t *testing.T, ns string) *Probe {
	t.Helper()
	p, err := NewProbe(apiConfig, ns)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func countPods(c cache.Cache, ctx context.Context, options ...client.ListOption) int {
	list := &corev1.PodList{}
	if err := c.List(ctx, list, options...); err != nil {
		return -1
	}
	return len(list.Items)
}

func roleInstance(t *testing.T, ns string) *workloadsv1alpha2.RoleInstance {
	t.Helper()
	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "instance"},
		Spec:       workloadsv1alpha2.RoleInstanceSpec{Components: []workloadsv1alpha2.RoleInstanceComponent{{Name: "main", Size: ptr.To(int32(1)), Template: corev1.PodTemplateSpec{Spec: pod(ns, "unused", nil).Spec}}}},
	}
	create(t, instance)
	return instance
}

func ownedPod(ns, name string, root *workloadsv1alpha2.RoleInstance) *corev1.Pod {
	p := pod(ns, name, map[string]string{instanceLabel: root.Name, "keep": "original"})
	p.OwnerReferences = []metav1.OwnerReference{{APIVersion: workloadsv1alpha2.GroupVersion.String(), Kind: "RoleInstance", Name: root.Name, UID: root.UID, Controller: ptr.To(true)}}
	return p
}
