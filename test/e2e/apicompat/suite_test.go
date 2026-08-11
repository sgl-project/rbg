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

// Package apicompat is a standalone e2e suite for a cluster where the rbgs chart was
// installed with controller.deprecatedWorkloadTypes.enabled=false.
//
// It cannot live in the main test/e2e suite: most of those specs create Deployment /
// StatefulSet / LeaderWorkerSet workloads, which this configuration deliberately
// rejects. So this is its own go-test entrypoint with its own Kind cluster in CI.
//
// Because the toggle is a process-wide controller flag plus a cluster-scoped
// validating webhook (both un-namespaced), the assertions only make sense against a
// release actually installed with the toggle off. A BeforeSuite preflight fails fast
// with setup instructions when that precondition does not hold, rather than letting
// every spec fail with a confusing "webhook accepted a Deployment" error.
package apicompat

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/rbgs/test/e2e/framework"
)

const (
	managerDeploymentName = "rbgs-controller-manager"
	// disabledFlag is the arg the chart renders when the toggle is off.
	disabledFlag = "--enable-deprecated-workload-types=false"
	// deprecatedRejectionSubstring is the stable part of the validator's error for a
	// role that uses a deprecated workload type (see
	// api/workloads/v1alpha2/rolebasedgroup_validation.go).
	deprecatedRejectionSubstring = "is deprecated and not enabled on this cluster"
)

// f is the shared framework for the suite, wired up in TestDeprecatedWorkloadTypesDisabled.
var f *framework.Framework

// controllerNamespace is where the chart installed the controller. Defaults to the
// chart's rbgs-system; override with RBGS_NAMESPACE.
func controllerNamespace() string {
	if ns := os.Getenv("RBGS_NAMESPACE"); ns != "" {
		return ns
	}
	return "rbgs-system"
}

func TestDeprecatedWorkloadTypesDisabled(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)

	f = framework.NewFramework(true)

	ginkgo.BeforeSuite(func() {
		preflightRequireToggleDisabled(f)
		f.BeforeAll()
	})
	ginkgo.AfterSuite(func() {
		f.AfterAll()
	})
	ginkgo.AfterEach(func() {
		f.AfterEach()
	})

	ginkgo.Describe(
		"[deprecated-disabled] deprecated workload types disabled via Helm toggle",
		func() {
			RunDeprecatedDisabledTestCases(f)
		},
	)

	ginkgo.RunSpecs(t, "run rbg deprecated-workload-types-disabled e2e test")
}

// preflightRequireToggleDisabled verifies the release under test really runs with the
// toggle off. On any mismatch it aborts the whole suite with instructions for
// preparing a conforming environment, so a run against an ordinary (enabled) cluster
// produces one actionable message instead of a wall of misleading spec failures.
func preflightRequireToggleDisabled(f *framework.Framework) {
	ns := controllerNamespace()
	deploy, err := f.Clientset.AppsV1().Deployments(ns).Get(f.Ctx, managerDeploymentName, metav1.GetOptions{})
	if err != nil {
		ginkgo.Fail(prepInstructions(fmt.Sprintf(
			"could not read Deployment %s/%s: %v", ns, managerDeploymentName, err)))
		return
	}

	if !managerHasDisabledFlag(deploy) {
		ginkgo.Fail(prepInstructions(fmt.Sprintf(
			"Deployment %s/%s does not run with %q; the release appears to have the deprecated "+
				"workload types ENABLED", ns, managerDeploymentName, disabledFlag)))
		return
	}
}

func managerHasDisabledFlag(deploy *appsv1.Deployment) bool {
	for _, c := range deploy.Spec.Template.Spec.Containers {
		for _, arg := range c.Args {
			if arg == disabledFlag {
				return true
			}
		}
	}
	return false
}

// prepInstructions wraps a diagnosis with the steps to prepare a conforming cluster.
func prepInstructions(reason string) string {
	return fmt.Sprintf(`this suite requires a cluster where the rbgs chart is installed with the
deprecated workload types DISABLED, but the precondition was not met:

  %s

Prepare the environment before running this suite (adjust names/tags as needed):

  # 1. a cluster with the RBG CRDs + LeaderWorkerSet CRDs installed
  # 2. install the chart with the toggle OFF:
  helm upgrade --install rbgs deploy/helm/rbgs \
    --create-namespace --namespace %s \
    --set controller.deprecatedWorkloadTypes.enabled=false \
    --wait

  # 3. point KUBECONFIG at that cluster and run:
  go test ./test/e2e/apicompat/ -v -ginkgo.v

If the controller runs in a different namespace, set RBGS_NAMESPACE to it.

NOTE: this is a fresh-install-only configuration. Do NOT flip an existing enabled
release to disabled just to run this suite — use a throwaway cluster.`,
		reason, controllerNamespace())
}

// ctx is a convenience accessor used by the spec file.
func ctx() context.Context { return f.Ctx }

// containsRejection reports whether err is a validator rejection for a deprecated
// workload type.
func containsRejection(err error) bool {
	return err != nil && strings.Contains(err.Error(), deprecatedRejectionSubstring)
}
