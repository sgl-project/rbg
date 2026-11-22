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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/time/rate"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
	"sigs.k8s.io/rbgs/api/workloads/constants"
)

const listPageSize = 500

type options struct {
	dryRun        bool
	allNamespaces bool
	qps           float64
	timeout       time.Duration
}

func NewMigrateCmd(cf *genericclioptions.ConfigFlags) *cobra.Command {
	o := &options{}
	root := &cobra.Command{Use: "migrate", Short: "Migrate existing RBG resources", Args: cobra.NoArgs}
	pods := &cobra.Command{Use: "pod-labels", Short: "Add the managed marker to legacy-labeled Pods", Args: cobra.NoArgs}
	pods.PersistentFlags().BoolVar(&o.dryRun, "dry-run", false, "Report candidates without changing Pods")
	pods.PersistentFlags().BoolVarP(&o.allNamespaces, "all-namespaces", "A", false, "Scan all namespaces")
	pods.PersistentFlags().Float64Var(&o.qps, "rate-limit", 10, "Maximum API requests per second")
	pods.PersistentFlags().DurationVar(&o.timeout, "timeout", 2*time.Minute, "Overall migration timeout")
	run := func(cmd *cobra.Command, check bool) error {
		if o.qps <= 0 || math.IsNaN(o.qps) || math.IsInf(o.qps, 0) || o.timeout <= 0 {
			return fmt.Errorf("rate-limit and timeout must be positive and finite")
		}
		config, err := cf.ToRESTConfig()
		if err != nil {
			return err
		}
		ns := ""
		if !o.allNamespaces {
			ns, _, err = cf.ToRawKubeConfigLoader().Namespace()
			if err != nil {
				return err
			}
		}
		// Use the command's shared limiter for both REST clients.
		config = rest.CopyConfig(config)
		config.QPS = -1
		config.RateLimiter = nil
		kube, err := kubernetes.NewForConfig(config)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), o.timeout)
		defer cancel()
		m := migrator{kube: kube, namespace: ns, limiter: rate.NewLimiter(rate.Limit(o.qps), 1), out: cmd.OutOrStdout()}
		podErr := m.run(ctx, check, o.dryRun)
		if check && ctx.Err() == nil {
			if err := m.reportLegacyTemplates(ctx, config); err != nil {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "WARNING: legacy workload template report is incomplete: %v\n", err)
			}
		}
		return podErr
	}
	pods.RunE = func(cmd *cobra.Command, _ []string) error { return run(cmd, false) }
	pods.AddCommand(&cobra.Command{Use: "check", Short: "Report unmarked legacy Pods and workload templates", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return run(cmd, true) }})
	root.AddCommand(pods)
	return root
}

type migrator struct {
	kube      kubernetes.Interface
	namespace string
	limiter   *rate.Limiter
	out       io.Writer
}

type labelPatchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value"`
}

func legacyLabeled(pod *corev1.Pod) bool {
	for _, key := range constants.LegacyPodLabelKeys() {
		if _, ok := pod.Labels[key]; ok {
			return true
		}
	}
	return false
}

func (m *migrator) run(ctx context.Context, check, dryRun bool) error {
	seen := make(map[types.UID]bool)
	candidates, patched, failed := 0, 0, 0
	for _, key := range constants.LegacyPodLabelKeys() {
		opts := metav1.ListOptions{LabelSelector: key + ",!" + constants.ManagedLabelKey, Limit: listPageSize}
		lastRestartCandidates, restarted := 0, false
		for {
			if err := m.limiter.Wait(ctx); err != nil {
				return err
			}
			pods, err := m.kube.CoreV1().Pods(m.namespace).List(ctx, opts)
			if err != nil {
				if opts.Continue != "" && (apierrors.IsResourceExpired(err) || apierrors.IsGone(err)) {
					if restarted && candidates == lastRestartCandidates {
						return fmt.Errorf("list candidates for %s: %w", key, err)
					}
					lastRestartCandidates, restarted = candidates, true
					opts.Continue = ""
					continue
				}
				return fmt.Errorf("list candidates for %s: %w", key, err)
			}
			for i := range pods.Items {
				pod := &pods.Items[i]
				if seen[pod.UID] {
					continue
				}
				seen[pod.UID] = true
				candidates++
				if check || dryRun {
					if _, err := fmt.Fprintf(m.out, "unmarked Pod %s/%s\n", pod.Namespace, pod.Name); err != nil {
						return err
					}
					continue
				}
				changed, err := m.mark(ctx, pod)
				if err != nil {
					failed++
					if _, writeErr := fmt.Fprintf(m.out, "failed Pod %s/%s: %v\n", pod.Namespace, pod.Name, err); writeErr != nil {
						return writeErr
					}
				} else if changed {
					patched++
				}
			}
			if pods.Continue == "" {
				break
			}
			opts.Continue = pods.Continue
		}
	}
	if _, err := fmt.Fprintf(m.out, "Pods: candidates=%d patched=%d failed=%d dry-run=%t check=%t\n", candidates, patched, failed, dryRun, check); err != nil {
		return err
	}
	if check && candidates > 0 {
		return fmt.Errorf("migration incomplete: %d unmarked Pods", candidates)
	}
	if failed > 0 {
		return fmt.Errorf("%d Pod patches failed; re-run pod-labels or check to report remaining work", failed)
	}
	return nil
}

func (m *migrator) reportLegacyTemplates(ctx context.Context, config *rest.Config) error {
	workloads, err := dynamic.NewForConfig(config)
	if err != nil {
		return err
	}
	const specField = "spec"
	appTemplate := [][]string{{specField, "template"}}
	leaderWorkerSets := lwsv1.GroupVersion.WithResource("leaderworkersets")
	workloadTypes := []struct {
		kind     string
		resource schema.GroupVersionResource
		paths    [][]string
	}{
		{"Deployment", appsv1.SchemeGroupVersion.WithResource("deployments"), appTemplate},
		{"StatefulSet", appsv1.SchemeGroupVersion.WithResource("statefulsets"), appTemplate},
		{"LeaderWorkerSet", leaderWorkerSets, [][]string{
			{specField, "leaderWorkerTemplate", "leaderTemplate"},
			{specField, "leaderWorkerTemplate", "workerTemplate"},
		}},
	}
	unmarked := 0
	var failures []error
	for _, workload := range workloadTypes {
		resource := workloads.Resource(workload.resource).Namespace(m.namespace)
		count, err := m.reportResourceTemplates(ctx, resource, workload.kind, workload.paths)
		unmarked += count
		if err == nil || (workload.resource == leaderWorkerSets && apierrors.IsNotFound(err)) {
			continue
		}
		failures = append(failures, fmt.Errorf("%s: %w", workload.kind, err))
	}
	summary := fmt.Sprintf("Legacy workload templates: unmarked=%d (informational)\n", unmarked)
	if unmarked > 0 {
		summary += "WARNING: The reported workloads can still create Pods without managed=true. " +
			"Plan to migrate the affected roles to RoleInstanceSet using the v1alpha2 API.\n"
	}
	if _, err := fmt.Fprint(m.out, summary); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func (m *migrator) reportResourceTemplates(ctx context.Context, resource dynamic.ResourceInterface, kind string, paths [][]string) (int, error) {
	unmarked := 0
	opts := metav1.ListOptions{LabelSelector: constants.GroupNameLabelKey, Limit: listPageSize}
	for {
		if err := m.limiter.Wait(ctx); err != nil {
			return unmarked, err
		}
		list, err := resource.List(ctx, opts)
		if err != nil {
			return unmarked, err
		}
		for _, obj := range list.Items {
			for _, path := range paths {
				template, found, err := unstructured.NestedFieldNoCopy(obj.Object, path...)
				if err != nil {
					return unmarked, err
				}
				if !found || template == nil {
					continue
				}
				marker, _, err := unstructured.NestedString(obj.Object, append(path, "metadata", "labels", constants.ManagedLabelKey)...)
				if err != nil {
					return unmarked, err
				}
				if marker == constants.ManagedLabelValue {
					continue
				}
				if _, err := fmt.Fprintf(m.out, "unmarked template %s %s/%s (%s)\n",
					kind, obj.GetNamespace(), obj.GetName(), strings.Join(path, ".")); err != nil {
					return unmarked, err
				}
				unmarked++
			}
		}
		opts.Continue = list.GetContinue()
		if opts.Continue == "" {
			return unmarked, nil
		}
	}
}

func (m *migrator) mark(ctx context.Context, candidate *corev1.Pod) (bool, error) {
	current := candidate
	changed := false
	err := retry.OnError(retry.DefaultBackoff, func(err error) bool {
		return apierrors.IsConflict(err) || apierrors.IsInvalid(err)
	}, func() error {
		if current == nil {
			if err := m.limiter.Wait(ctx); err != nil {
				return err
			}
			pod, err := m.kube.CoreV1().Pods(candidate.Namespace).Get(ctx, candidate.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return nil
			}
			if err != nil {
				return err
			}
			current = pod
		}
		if current.UID != candidate.UID || !legacyLabeled(current) {
			return nil
		}
		if _, present := current.Labels[constants.ManagedLabelKey]; present {
			return nil
		}
		markerPath := strings.ReplaceAll(strings.ReplaceAll(constants.ManagedLabelKey, "~", "~0"), "/", "~1")
		patch, err := json.Marshal([]labelPatchOperation{
			{Op: "test", Path: "/metadata/uid", Value: candidate.UID},
			{Op: "test", Path: "/metadata/labels", Value: current.Labels},
			{Op: "add", Path: "/metadata/labels/" + markerPath, Value: constants.ManagedLabelValue},
		})
		if err != nil {
			return err
		}
		if err := m.limiter.Wait(ctx); err != nil {
			return err
		}
		_, err = m.kube.CoreV1().Pods(candidate.Namespace).Patch(ctx, candidate.Name, types.JSONPatchType, patch, metav1.PatchOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if apierrors.IsConflict(err) || apierrors.IsInvalid(err) {
			current = nil
		}
		if err == nil {
			changed = true
		}
		return err
	})
	return changed, err
}
