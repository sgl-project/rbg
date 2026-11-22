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

package migrate

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/rbgs/api/workloads/constants"
)

func TestPodLabelsCommandAPI(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("set KUBEBUILDER_ASSETS to run isolated API integration tests")
	}
	env := &envtest.Environment{}
	env.ControlPlane.GetAPIServer().Configure().Set("advertise-address", "127.0.0.1")
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, env.Stop()) })
	kube, err := kubernetes.NewForConfig(cfg)
	require.NoError(t, err)
	ctx := context.Background()
	_, err = kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "migration"}}, metav1.CreateOptions{})
	require.NoError(t, err)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "migration", Labels: map[string]string{constants.GroupNameLabelKey: "g", "keep": "yes"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "test/image"}}}}
	pod, err = kube.CoreV1().Pods("migration").Create(ctx, pod, metav1.CreateOptions{})
	require.NoError(t, err)
	falsePod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "explicit-false", Namespace: "migration", Labels: map[string]string{
		constants.GroupNameLabelKey: "g", constants.ManagedLabelKey: "false",
	}}, Spec: pod.Spec}
	falsePod, err = kube.CoreV1().Pods("migration").Create(ctx, falsePod, metav1.CreateOptions{})
	require.NoError(t, err)
	legacyLabels := map[string]string{constants.GroupNameLabelKey: "g"}
	selector := &metav1.LabelSelector{MatchLabels: legacyLabels}
	template := corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: legacyLabels}, Spec: pod.Spec}
	deployment, err := kube.AppsV1().Deployments("migration").Create(ctx, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "d", Namespace: "migration", Labels: legacyLabels}, Spec: appsv1.DeploymentSpec{
		Selector: selector, Template: template,
	}}, metav1.CreateOptions{})
	require.NoError(t, err)
	statefulSet, err := kube.AppsV1().StatefulSets("migration").Create(ctx, &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "migration", Labels: legacyLabels}, Spec: appsv1.StatefulSetSpec{
		ServiceName: "s", Selector: selector, Template: template,
	}}, metav1.CreateOptions{})
	require.NoError(t, err)
	markedTemplate := deployment.DeepCopy()
	markedTemplate.ObjectMeta = metav1.ObjectMeta{Name: "marked-template", Namespace: deployment.Namespace, Labels: legacyLabels}
	markedTemplate.Spec.Template.Labels[constants.ManagedLabelKey] = constants.ManagedLabelValue
	_, err = kube.AppsV1().Deployments(deployment.Namespace).Create(ctx, markedTemplate, metav1.CreateOptions{})
	require.NoError(t, err)
	unrelated := deployment.DeepCopy()
	unrelated.ObjectMeta = metav1.ObjectMeta{Name: "unrelated-deployment", Namespace: deployment.Namespace}
	_, err = kube.AppsV1().Deployments(deployment.Namespace).Create(ctx, unrelated, metav1.CreateOptions{})
	require.NoError(t, err)
	run := func(config *rest.Config, args ...string) (string, error) {
		cf := genericclioptions.NewConfigFlags(false)
		// Supply only the isolated API server; never resolve the user's kubeconfig.
		host := cfg.Host
		cf.APIServer = &host
		namespace := "default"
		cf.Namespace = &namespace
		cf.KubeConfig = new(string)
		*cf.KubeConfig = os.DevNull
		cf.WrapConfigFn = func(_ *rest.Config) *rest.Config { return rest.CopyConfig(config) }
		cmd := NewMigrateCmd(cf)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(append([]string{"pod-labels", "-A", "--rate-limit=1000"}, args...))
		err := cmd.ExecuteContext(ctx)
		return out.String(), err
	}
	out, err := run(cfg, "--dry-run")
	require.NoError(t, err)
	require.Contains(t, out, "candidates=1 patched=0")
	require.NotContains(t, out, "unmarked template")
	unchanged, err := kube.CoreV1().Pods("migration").Get(ctx, "p", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, pod.ResourceVersion, unchanged.ResourceVersion)
	out, err = run(cfg, "check")
	require.ErrorContains(t, err, "migration incomplete: 1 unmarked Pods")
	require.Contains(t, out, "unmarked template Deployment migration/d (spec.template)")
	out, err = run(cfg)
	require.NoError(t, err)
	require.Contains(t, out, "patched=1")
	marked, err := kube.CoreV1().Pods("migration").Get(ctx, "p", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, pod.UID, marked.UID)
	require.Equal(t, pod.Spec, marked.Spec)
	require.Equal(t, "yes", marked.Labels["keep"])
	require.Equal(t, "true", marked.Labels[constants.ManagedLabelKey])
	out, err = run(cfg)
	require.NoError(t, err)
	require.Contains(t, out, "candidates=0")
	out, err = run(cfg, "check")
	require.NoError(t, err, "unmarked legacy templates must not prevent a clean check: %s", out)
	require.Contains(t, out, "candidates=0 patched=0 failed=0 dry-run=false check=true")
	require.NotContains(t, out, "explicit-false")
	require.Contains(t, out, "unmarked template Deployment migration/d (spec.template)")
	require.Contains(t, out, "unmarked template StatefulSet migration/s (spec.template)")
	require.Contains(t, out, "Legacy workload templates: unmarked=2 (informational)\nWARNING: The reported workloads can still create Pods without managed=true.")
	require.Contains(t, out, "Plan to migrate the affected roles to RoleInstanceSet using the v1alpha2 API.")
	require.NotContains(t, out, "WARNING: legacy workload template report")
	t.Run("uses the command rate limiter for both clients", func(t *testing.T) {
		limitedConfig := rest.CopyConfig(cfg)
		limitedConfig.RateLimiter = flowcontrol.NewFakeNeverRateLimiter()
		out, err := run(limitedConfig, "check")
		require.NoError(t, err)
		require.Contains(t, out, "candidates=0 patched=0 failed=0 dry-run=false check=true")
		require.Contains(t, out, "Legacy workload templates: unmarked=2 (informational)")
		require.NotContains(t, out, "WARNING: legacy workload template report")
	})
	t.Run("skips templates when the Pod scan times out", func(t *testing.T) {
		out, err := run(cfg, "check", "--timeout=1ns")
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.NotContains(t, out, "legacy workload template report")
		require.NotContains(t, out, "Legacy workload templates:")
	})
	t.Run("no migration advice without unmarked templates", func(t *testing.T) {
		out, err := run(cfg, "check", "--all-namespaces=false")
		require.NoError(t, err)
		require.Contains(t, out, "Legacy workload templates: unmarked=0 (informational)")
		require.NotContains(t, out, "Plan to migrate the affected roles")
	})
	t.Run("template list failure is informational", func(t *testing.T) {
		failingConfig := rest.CopyConfig(cfg)
		failingConfig.Wrap(func(next http.RoundTripper) http.RoundTripper {
			return migrationRoundTripper(func(req *http.Request) (*http.Response, error) {
				if req.Method == http.MethodGet && req.URL.Path == "/apis/apps/v1/deployments" {
					return nil, fmt.Errorf("test deployment list failure")
				}
				return next.RoundTrip(req)
			})
		})
		out, err := run(failingConfig, "check")
		require.NoError(t, err)
		require.Contains(t, out, "WARNING: legacy workload template report is incomplete")
		require.Contains(t, out, "test deployment list failure")
		require.Contains(t, out, "unmarked template StatefulSet migration/s (spec.template)")
	})
	afterFalse, err := kube.CoreV1().Pods("migration").Get(ctx, falsePod.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, falsePod.ResourceVersion, afterFalse.ResourceVersion)
	require.Equal(t, "false", afterFalse.Labels[constants.ManagedLabelKey])
	after, err := kube.AppsV1().Deployments("migration").Get(ctx, "d", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, deployment.Spec, after.Spec)
	require.Equal(t, deployment.ResourceVersion, after.ResourceVersion)
	afterStatefulSet, err := kube.AppsV1().StatefulSets("migration").Get(ctx, "s", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, statefulSet.Spec, afterStatefulSet.Spec)
	require.Equal(t, statefulSet.ResourceVersion, afterStatefulSet.ResourceVersion)
	// A stale candidate must not label a same-name replacement, even on its first patch attempt.
	zero := int64(0)
	require.NoError(t, kube.CoreV1().Pods("migration").Delete(ctx, "p", metav1.DeleteOptions{GracePeriodSeconds: &zero}))
	replacement := pod.DeepCopy()
	replacement.UID = ""
	replacement.ResourceVersion = ""
	replacement, err = kube.CoreV1().Pods("migration").Create(ctx, replacement, metav1.CreateOptions{})
	require.NoError(t, err)
	m := migrator{kube: kube, limiter: rate.NewLimiter(rate.Inf, 1)}
	changed, err := m.mark(ctx, pod)
	require.NoError(t, err)
	require.False(t, changed)
	fresh, err := kube.CoreV1().Pods("migration").Get(ctx, "p", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, replacement.UID, fresh.UID)
	require.NotContains(t, fresh.Labels, constants.ManagedLabelKey)

	t.Run("concurrent status updates", func(t *testing.T) {
		racingPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "status-race", Namespace: "migration", Labels: map[string]string{constants.GroupNameLabelKey: "g"}}, Spec: pod.Spec}
		racingPod, err := kube.CoreV1().Pods("migration").Create(ctx, racingPod, metav1.CreateOptions{})
		require.NoError(t, err)
		attempts := 0
		racingConfig := rest.CopyConfig(cfg)
		racingConfig.Wrap(func(next http.RoundTripper) http.RoundTripper {
			return migrationRoundTripper(func(req *http.Request) (*http.Response, error) {
				if req.Method == http.MethodPatch && strings.HasSuffix(req.URL.Path, "/pods/status-race") {
					attempts++
					current, err := kube.CoreV1().Pods("migration").Get(req.Context(), racingPod.Name, metav1.GetOptions{})
					if err != nil {
						return nil, err
					}
					current.Status.Message = fmt.Sprintf("concurrent status update %d", attempts)
					if _, err := kube.CoreV1().Pods("migration").UpdateStatus(req.Context(), current, metav1.UpdateOptions{}); err != nil {
						return nil, err
					}
				}
				return next.RoundTrip(req)
			})
		})
		racingClient, err := kubernetes.NewForConfig(racingConfig)
		require.NoError(t, err)
		m := migrator{kube: racingClient, limiter: rate.NewLimiter(rate.Inf, 1)}
		changed, err := m.mark(ctx, racingPod)
		require.NoError(t, err)
		require.True(t, changed)
		require.Positive(t, attempts)
		current, err := kube.CoreV1().Pods("migration").Get(ctx, racingPod.Name, metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, constants.ManagedLabelValue, current.Labels[constants.ManagedLabelKey])
		require.Contains(t, current.Status.Message, "concurrent status update")
	})

	for _, tc := range []struct {
		name  string
		patch string
	}{
		{name: "concurrent marker", patch: fmt.Sprintf(`{"metadata":{"labels":{"%s":"false"}}}`, constants.ManagedLabelKey)},
		{name: "removed legacy label", patch: fmt.Sprintf(`{"metadata":{"labels":{"%s":null}}}`, constants.GroupNameLabelKey)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := strings.ReplaceAll(tc.name, " ", "-")
			candidate := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "migration", Labels: map[string]string{constants.GroupNameLabelKey: "g"}}, Spec: pod.Spec}
			candidate, err := kube.CoreV1().Pods("migration").Create(ctx, candidate, metav1.CreateOptions{})
			require.NoError(t, err)
			attempts := 0
			racingConfig := rest.CopyConfig(cfg)
			racingConfig.Wrap(func(next http.RoundTripper) http.RoundTripper {
				return migrationRoundTripper(func(req *http.Request) (*http.Response, error) {
					if req.Method == http.MethodPatch && strings.HasSuffix(req.URL.Path, "/pods/"+name) {
						attempts++
						if attempts == 1 {
							if _, err := kube.CoreV1().Pods("migration").Patch(req.Context(), name, types.MergePatchType, []byte(tc.patch), metav1.PatchOptions{}); err != nil {
								return nil, err
							}
						}
					}
					return next.RoundTrip(req)
				})
			})
			racingClient, err := kubernetes.NewForConfig(racingConfig)
			require.NoError(t, err)
			m := migrator{kube: racingClient, limiter: rate.NewLimiter(rate.Inf, 1)}
			changed, err := m.mark(ctx, candidate)
			require.NoError(t, err)
			require.False(t, changed)
			require.Positive(t, attempts)
			current, err := kube.CoreV1().Pods("migration").Get(ctx, name, metav1.GetOptions{})
			require.NoError(t, err)
			if tc.name == "concurrent marker" {
				require.Equal(t, "false", current.Labels[constants.ManagedLabelKey])
			} else {
				require.NotContains(t, current.Labels, constants.ManagedLabelKey)
				require.NotContains(t, current.Labels, constants.GroupNameLabelKey)
			}
		})
	}

	t.Run("expired pagination resumes migration", func(t *testing.T) {
		const namespace = "pagination"
		_, err := kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}, metav1.CreateOptions{})
		require.NoError(t, err)
		for _, name := range []string{"first", "second", "third"} {
			_, err := kube.CoreV1().Pods(namespace).Create(ctx, &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{constants.GroupNameLabelKey: "g"}},
				Spec:       pod.Spec,
			}, metav1.CreateOptions{})
			require.NoError(t, err)
		}

		expired := 0
		pagedConfig := rest.CopyConfig(cfg)
		pagedConfig.Wrap(func(next http.RoundTripper) http.RoundTripper {
			return migrationRoundTripper(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodGet || req.URL.Path != "/api/v1/namespaces/"+namespace+"/pods" {
					return next.RoundTrip(req)
				}
				query := req.URL.Query()
				if query.Get("continue") != "" {
					expired++
					return &http.Response{
						StatusCode: http.StatusGone,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Expired","message":"continue token expired","code":410}`)),
						Request:    req,
					}, nil
				}
				query.Set("limit", "1")
				pagedRequest := req.Clone(req.Context())
				pagedRequest.URL.RawQuery = query.Encode()
				return next.RoundTrip(pagedRequest)
			})
		})
		pagedClient, err := kubernetes.NewForConfig(pagedConfig)
		require.NoError(t, err)
		var out bytes.Buffer
		m := migrator{kube: pagedClient, namespace: namespace, limiter: rate.NewLimiter(rate.Inf, 1), out: &out}
		require.NoError(t, m.run(ctx, false, false))
		require.GreaterOrEqual(t, expired, 2, "test must exercise repeated token expiry")
		require.Contains(t, out.String(), "candidates=3 patched=3 failed=0")
		pods, err := kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
		require.Len(t, pods.Items, 3)
		for _, item := range pods.Items {
			require.Equal(t, constants.ManagedLabelValue, item.Labels[constants.ManagedLabelKey])
		}

		for _, name := range []string{"fourth", "fifth"} {
			_, err := kube.CoreV1().Pods(namespace).Create(ctx, &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{constants.GroupNameLabelKey: "g"}},
				Spec:       pod.Spec,
			}, metav1.CreateOptions{})
			require.NoError(t, err)
		}
		stalledCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		err = m.run(stalledCtx, false, true)
		require.ErrorContains(t, err, "continue token expired")
	})
}

type migrationRoundTripper func(*http.Request) (*http.Response, error)

func (f migrationRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
