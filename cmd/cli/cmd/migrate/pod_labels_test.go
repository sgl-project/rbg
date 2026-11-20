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
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	"sigs.k8s.io/rbgs/api/workloads/constants"
)

func candidate(name string, labels map[string]string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", UID: types.UID(name), ResourceVersion: "10", Labels: labels}}
}

func TestCandidatesDryRunAndRerun(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		t.Run(map[bool]string{true: "dry-run", false: "patch"}[dryRun], func(t *testing.T) {
			kube := fake.NewSimpleClientset(
				candidate("group", map[string]string{constants.GroupNameLabelKey: "g", "keep": "yes"}),
				candidate("instance", map[string]string{constants.RoleInstanceNameLabelKey: "r"}),
				candidate("warmup", map[string]string{constants.WarmupNameLabelKey: "w"}),
				candidate("overlap", map[string]string{constants.GroupNameLabelKey: "g", constants.WarmupNameLabelKey: "w"}),
				candidate("false", map[string]string{constants.GroupNameLabelKey: "g", constants.ManagedLabelKey: "false"}),
				candidate("unrelated", nil),
			)
			var out bytes.Buffer
			m := migrator{kube: kube, namespace: "ns", limiter: rate.NewLimiter(rate.Inf, 1), out: &out}
			require.NoError(t, m.run(context.Background(), false, dryRun))
			require.Contains(t, out.String(), "candidates=4")
			patches, lists := 0, 0
			for _, action := range kube.Actions() {
				if action.GetVerb() == "list" {
					lists++
					require.Contains(t, action.(clienttesting.ListAction).GetListRestrictions().Labels.String(), "!"+constants.ManagedLabelKey)
				}
				if action.GetVerb() == "patch" {
					patches++
					patch := action.(clienttesting.PatchAction)
					require.Equal(t, types.JSONPatchType, patch.GetPatchType())
					require.NotContains(t, string(patch.GetPatch()), "resourceVersion")
				}
			}
			require.Equal(t, 3, lists)
			if dryRun {
				require.Zero(t, patches)
			} else {
				require.Equal(t, 4, patches)
				out.Reset()
				require.NoError(t, m.run(context.Background(), false, false))
				require.Contains(t, out.String(), "candidates=0")
				pod, err := kube.CoreV1().Pods("ns").Get(context.Background(), "group", metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, "yes", pod.Labels["keep"])
			}
		})
	}
}

func TestConflictRetryAndReplacement(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		pod := candidate("p", map[string]string{constants.GroupNameLabelKey: "g"})
		kube := fake.NewSimpleClientset(pod)
		attempts := 0
		kube.PrependReactor("patch", "pods", func(action clienttesting.Action) (bool, runtime.Object, error) {
			attempts++
			if attempts == 1 {
				return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "pods"}, "p", io.EOF)
			}
			return false, nil, nil
		})
		kube.PrependReactor("get", "pods", func(action clienttesting.Action) (bool, runtime.Object, error) {
			fresh := pod.DeepCopy()
			fresh.ResourceVersion = "11"
			if replaced {
				fresh.UID = "replacement"
			}
			return true, fresh, nil
		})
		m := migrator{kube: kube, limiter: rate.NewLimiter(rate.Inf, 1), out: io.Discard}
		changed, err := m.mark(context.Background(), pod)
		require.NoError(t, err)
		require.Equal(t, !replaced, changed)
		if replaced {
			require.Equal(t, 1, attempts)
		} else {
			require.Equal(t, 2, attempts)
		}
	}
}

func TestRateLimitCancellation(t *testing.T) {
	m := migrator{kube: fake.NewSimpleClientset(), limiter: rate.NewLimiter(0.001, 1), out: io.Discard}
	require.True(t, m.limiter.Allow())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	require.Error(t, m.run(ctx, false, false))
	require.Empty(t, m.kube.(*fake.Clientset).Actions())
}

func TestCheckReportsUnmarkedPodsWithoutWrites(t *testing.T) {
	kube := fake.NewSimpleClientset(
		candidate("p", map[string]string{constants.GroupNameLabelKey: "g"}),
		candidate("explicit-false", map[string]string{constants.GroupNameLabelKey: "g", constants.ManagedLabelKey: "false"}),
	)
	var out bytes.Buffer
	m := migrator{kube: kube, namespace: "ns", limiter: rate.NewLimiter(rate.Inf, 1), out: &out}
	require.ErrorContains(t, m.run(context.Background(), true, false), "migration incomplete: 1 unmarked Pods")
	require.Contains(t, out.String(), "unmarked Pod ns/p")
	require.NotContains(t, out.String(), "explicit-false")
	for _, action := range kube.Actions() {
		require.Equal(t, "list", action.GetVerb())
		require.Equal(t, "pods", action.GetResource().Resource)
	}
}
