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

package testutil

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// SetupV1alpha1GateTestEnv boots envtest with conversion webhooks enabled on
// the CRDs and the production v1alpha2 validators installed. v1alpha1
// requests traverse the real production chain: v1alpha1 -> conversion
// webhook -> v1alpha2 validator -> conversion back. This is the composition
// that applies after the v1alpha1 admission webhooks were removed.
func SetupV1alpha1GateTestEnv() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	Ctx, Cancel = context.WithCancel(context.TODO())

	By("bootstrapping v1alpha1 gate test environment with conversion webhooks")

	_, currentFile, _, _ := runtime.Caller(0)
	crdPath := filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "config", "crd", "bases")

	// Register both API versions before starting: envtest's CRD installer
	// enables conversion webhooks for GroupKinds whose types are registered
	// in the scheme and implement Convertible/Hub.
	err := workloadsv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	err = workloadsv1alpha2.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	TestEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{crdPath},
		ErrorIfCRDPathMissing: true,
	}

	// Pre-generate the CA and allocate the local serving host/port so that
	// InstallCRDs (called during Start) sees a populated CA and enables
	// conversion webhooks on the CRDs. Without this, the CRDs are installed
	// with conversion strategy None and the v1alpha1 request path never
	// traverses the production conversion chain.
	TestEnv.WebhookInstallOptions.PrepWithoutInstalling()

	Cfg, err = TestEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(Cfg).NotTo(BeNil())

	K8sClient, err = client.New(Cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(K8sClient).NotTo(BeNil())

	By("registering the production v1alpha2 webhooks (validators + conversion)")
	TestMgr, err = ctrl.NewManager(Cfg, ctrl.Options{
		Scheme: scheme.Scheme,
		Metrics: server.Options{
			BindAddress: "0",
		},
		WebhookServer: webhook.NewServer(webhook.Options{
			Host:    TestEnv.WebhookInstallOptions.LocalServingHost,
			Port:    TestEnv.WebhookInstallOptions.LocalServingPort,
			CertDir: TestEnv.WebhookInstallOptions.LocalServingCertDir,
		}),
	})
	Expect(err).NotTo(HaveOccurred())

	// Register the same webhooks cmd/rbgs registers in production: the
	// v1alpha2 validators with the compatibility flag, and the v1alpha2
	// defaulters (which also register the conversion webhooks since both
	// API versions are in the scheme).
	if err = (&workloadsv1alpha2.RoleBasedGroup{}).SetupWebhookWithManager(TestMgr, true); err != nil {
		Fail("unable to create RoleBasedGroup admission webhook: " + err.Error())
	}
	if err = (&workloadsv1alpha2.RoleBasedGroupSet{}).SetupWebhookWithManager(TestMgr, true); err != nil {
		Fail("unable to create RoleBasedGroupSet admission webhook: " + err.Error())
	}
	if err = (&workloadsv1alpha2.RoleInstanceSet{}).SetupWebhookWithManager(TestMgr); err != nil {
		Fail("unable to create RoleInstanceSet admission webhook: " + err.Error())
	}

	go func() {
		defer GinkgoRecover()
		err := TestMgr.Start(Ctx)
		Expect(err).NotTo(HaveOccurred())
	}()

	// Wait for the webhook server to be serving.
	Eventually(func() error {
		conn, err := net.DialTimeout(
			"tcp",
			fmt.Sprintf(
				"%s:%d",
				TestEnv.WebhookInstallOptions.LocalServingHost,
				TestEnv.WebhookInstallOptions.LocalServingPort,
			),
			250*time.Millisecond,
		)
		if err != nil {
			return err
		}
		return conn.Close()
	}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

	By("installing the v1alpha2 validating webhook configuration")
	installV1a2GateWebhooks()
}

// installV1a2GateWebhooks installs the v1alpha2 validating webhook
// configuration pointing at the test webhook server. matchPolicy is left
// unset (Equivalent, the default) so v1alpha1 writes are routed through
// conversion to the v1alpha2 gate — the production composition.
func installV1a2GateWebhooks() {
	opts := TestEnv.WebhookInstallOptions
	config := buildValidatingWebhookConfig(opts, []gateWebhookSpec{
		{
			name:     "vrolebasedgroup.kb.io",
			path:     "/validate-workloads-x-k8s-io-v1alpha2-rolebasedgroup",
			resource: "rolebasedgroups",
		},
		{
			name:     "vrolebasedgroupset.kb.io",
			path:     "/validate-workloads-x-k8s-io-v1alpha2-rolebasedgroupset",
			resource: "rolebasedgroupsets",
		},
	})
	Expect(K8sClient.Create(Ctx, config)).To(Succeed())

	// Wait for the gate to be active: a dry-run deprecated create through
	// the v1alpha2 API must be rejected.
	Eventually(func() bool {
		probe := &workloadsv1alpha2.RoleBasedGroup{
			ObjectMeta: metav1ObjectMeta("gate-readiness-v1a2", "default"),
			Spec: workloadsv1alpha2.RoleBasedGroupSpec{
				Roles: []workloadsv1alpha2.RoleSpec{{
					Name:        "worker",
					Replicas:    ptrToInt32(1),
					Annotations: map[string]string{"rbg.workloads.x-k8s.io/role-workload-type": "apps/v1/StatefulSet"},
				}},
			},
		}
		err := K8sClient.Create(Ctx, probe, &client.CreateOptions{DryRun: []string{"All"}})
		return err != nil && containsStr(err.Error(), "new uses are not accepted")
	}, 10*time.Second, 250*time.Millisecond).Should(BeTrue(), "the v1alpha2 gate did not become active")
}

type gateWebhookSpec struct {
	name     string
	path     string
	resource string
}

type admissionv1ValidatingConfig = admissionregistrationv1.ValidatingWebhookConfiguration
type admissionv1Webhook = admissionregistrationv1.ValidatingWebhook
type admissionv1ClientConfig = admissionregistrationv1.WebhookClientConfig
type admissionv1Rule = admissionregistrationv1.RuleWithOperations

var (
	v1a1NoneSideEffect = admissionregistrationv1.SideEffectClassNone
	v1a1FailPolicy     = admissionregistrationv1.Fail
)

func objectMeta(name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name}
}

func metav1ObjectMeta(name, namespace string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: namespace}
}

func ptrToInt32(v int32) *int32 { return &v }

func buildValidatingWebhookConfig(opts envtest.WebhookInstallOptions, specs []gateWebhookSpec) *admissionv1ValidatingConfig {
	hostPort := net.JoinHostPort(opts.LocalServingHost, fmt.Sprintf("%d", opts.LocalServingPort))
	webhooks := make([]admissionv1Webhook, 0, len(specs))
	for _, s := range specs {
		url := fmt.Sprintf("https://%s%s", hostPort, s.path)
		webhooks = append(webhooks, admissionv1Webhook{
			Name:                    s.name,
			AdmissionReviewVersions: []string{"v1"},
			SideEffects:             &v1a1NoneSideEffect,
			FailurePolicy:           &v1a1FailPolicy,
			ClientConfig: admissionv1ClientConfig{
				URL:      &url,
				CABundle: opts.LocalServingCAData,
			},
			Rules: []admissionregistrationv1.RuleWithOperations{{
				Operations: []admissionregistrationv1.OperationType{"CREATE", "UPDATE"},
				Rule: admissionregistrationv1.Rule{
					APIGroups:   []string{"workloads.x-k8s.io"},
					APIVersions: []string{"v1alpha2"},
					Resources:   []string{s.resource},
				},
			}},
		})
	}
	return &admissionv1ValidatingConfig{
		ObjectMeta: objectMeta("v1a1-gate-test-validating"),
		Webhooks:   webhooks,
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && strings.Contains(s, substr)
}
