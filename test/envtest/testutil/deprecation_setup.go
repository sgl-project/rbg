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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// SetupDeprecationGateTestEnv boots a webhook-enabled envtest environment whose
// validating webhook configurations are NOT yet installed. Tests can seed the
// pre-upgrade state (for example a RoleBasedGroupSet whose template still uses a
// deprecated workload type) through the plain API server, then call
// InstallDeprecationGateWebhooks to switch the 0.9.0 admission gate on and exercise
// the writes an upgraded controller or user would make.
func SetupDeprecationGateTestEnv() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	Ctx, Cancel = context.WithCancel(context.TODO())

	By("bootstrapping deprecation-gate test environment")

	_, currentFile, _, _ := runtime.Caller(0)
	crdPath := filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "config", "crd", "bases")
	TestEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{crdPath},
		ErrorIfCRDPathMissing: true,
	}

	// WebhookInstallOptions is deliberately left without Paths: Start still
	// allocates the local serving host, port and CA, but installs no webhook
	// configuration until the test has seeded its legacy objects.
	var err error
	Cfg, err = TestEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(Cfg).NotTo(BeNil())

	err = admissionregistrationv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	err = workloadsv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	err = workloadsv1alpha2.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	K8sClient, err = client.New(Cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(K8sClient).NotTo(BeNil())

	By("starting the webhook server without installing webhook configurations")
	TestMgr, err = ctrl.NewManager(Cfg, ctrl.Options{
		Scheme: scheme.Scheme,
		Metrics: server.Options{
			BindAddress: "0", // disable metrics to avoid port conflicts between suites
		},
		WebhookServer: webhook.NewServer(webhook.Options{
			Host:    TestEnv.WebhookInstallOptions.LocalServingHost,
			Port:    TestEnv.WebhookInstallOptions.LocalServingPort,
			CertDir: TestEnv.WebhookInstallOptions.LocalServingCertDir,
		}),
	})
	Expect(err).NotTo(HaveOccurred())

	// Register the production validators with the compatibility flag enabled,
	// exactly as cmd/rbgs/main.go does for a 0.9.0 default upgrade.
	if err = (&workloadsv1alpha2.RoleBasedGroup{}).SetupWebhookWithManager(TestMgr, true); err != nil {
		Fail("unable to create RoleBasedGroup admission webhook: " + err.Error())
	}
	if err = (&workloadsv1alpha2.RoleBasedGroupSet{}).SetupWebhookWithManager(TestMgr, true); err != nil {
		Fail("unable to create RoleBasedGroupSet admission webhook: " + err.Error())
	}

	go func() {
		defer GinkgoRecover()
		err := TestMgr.Start(Ctx)
		Expect(err).NotTo(HaveOccurred())
	}()

	// The webhook server must be serving before its configuration is installed;
	// otherwise the API server fails the first admitted request.
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
}

// InstallDeprecationGateWebhooks installs the 0.9.0 compatibility-mode validating
// webhooks against the already-running test webhook server. matchPolicy stays
// unset (Equivalent) so the rules match the production v1alpha2 validators.
func InstallDeprecationGateWebhooks() {
	opts := TestEnv.WebhookInstallOptions
	url := func(path string) string {
		return fmt.Sprintf("https://%s%s", net.JoinHostPort(opts.LocalServingHost, fmt.Sprint(opts.LocalServingPort)), path)
	}

	config := &admissionregistrationv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "rbgs-deprecation-gate-test"},
		Webhooks: []admissionregistrationv1.ValidatingWebhook{
			{
				Name:                    "vrolebasedgroup.kb.io",
				AdmissionReviewVersions: []string{"v1"},
				SideEffects:             &noneSideEffect,
				FailurePolicy:           &failPolicy,
				ClientConfig: admissionregistrationv1.WebhookClientConfig{
					URL:      ptrTo(url("/validate-workloads-x-k8s-io-v1alpha2-rolebasedgroup")),
					CABundle: opts.LocalServingCAData,
				},
				Rules: []admissionregistrationv1.RuleWithOperations{{
					Operations: []admissionregistrationv1.OperationType{
						admissionregistrationv1.Create,
						admissionregistrationv1.Update,
					},
					Rule: admissionregistrationv1.Rule{
						APIGroups:   []string{"workloads.x-k8s.io"},
						APIVersions: []string{"v1alpha2"},
						Resources:   []string{"rolebasedgroups"},
					},
				}},
			},
			{
				Name:                    "vrolebasedgroupset.kb.io",
				AdmissionReviewVersions: []string{"v1"},
				SideEffects:             &noneSideEffect,
				FailurePolicy:           &failPolicy,
				ClientConfig: admissionregistrationv1.WebhookClientConfig{
					URL:      ptrTo(url("/validate-workloads-x-k8s-io-v1alpha2-rolebasedgroupset")),
					CABundle: opts.LocalServingCAData,
				},
				Rules: []admissionregistrationv1.RuleWithOperations{{
					Operations: []admissionregistrationv1.OperationType{
						admissionregistrationv1.Create,
						admissionregistrationv1.Update,
					},
					Rule: admissionregistrationv1.Rule{
						APIGroups:   []string{"workloads.x-k8s.io"},
						APIVersions: []string{"v1alpha2"},
						Resources:   []string{"rolebasedgroupsets"},
					},
				}},
			},
		},
	}

	Expect(K8sClient.Create(Ctx, config)).To(Succeed())
	waitForDeprecationGate()
}

// waitForDeprecationGate blocks until both installed validators reject a new
// deprecated workload use. The API server's admission dispatcher observes new
// webhook configurations asynchronously, so a fixed sleep cannot prove the gate
// is active: the exemption spec could pass before the gate is installed, and the
// rejection specs could accidentally admit their objects on a slow runner. The
// probes use server-side dry-run creates, which traverse admission without
// persisting anything.
func waitForDeprecationGate() {
	gateRejected := func(err error) bool {
		return err != nil && strings.Contains(err.Error(), "new uses are not accepted")
	}
	probeRole := workloadsv1alpha2.RoleSpec{
		Name:        "worker",
		Replicas:    ptr.To(int32(1)),
		Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.StatefulSetWorkloadType},
		Pattern: workloadsv1alpha2.Pattern{
			StandalonePattern: &workloadsv1alpha2.StandalonePattern{
				TemplateSource: workloadsv1alpha2.TemplateSource{
					Template: ptr.To(corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{Name: "nginx", Image: "nginx:latest"}},
						},
					}),
				},
			},
		},
	}

	Eventually(func() bool {
		rbg := &workloadsv1alpha2.RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "gate-readiness-rbg", Namespace: gateProbeNamespace},
			Spec:       workloadsv1alpha2.RoleBasedGroupSpec{Roles: []workloadsv1alpha2.RoleSpec{probeRole}},
		}
		err := K8sClient.Create(Ctx, rbg, &client.CreateOptions{DryRun: []string{metav1.DryRunAll}})
		return gateRejected(err)
	}, 10*time.Second, 250*time.Millisecond).Should(BeTrue(), "RoleBasedGroup gate did not become active")

	Eventually(func() bool {
		rbgset := &workloadsv1alpha2.RoleBasedGroupSet{
			ObjectMeta: metav1.ObjectMeta{Name: "gate-readiness-set", Namespace: gateProbeNamespace},
			Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
				Replicas: ptr.To(int32(1)),
				GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
					Spec: workloadsv1alpha2.RoleBasedGroupSpec{Roles: []workloadsv1alpha2.RoleSpec{probeRole}},
				},
			},
		}
		err := K8sClient.Create(Ctx, rbgset, &client.CreateOptions{DryRun: []string{metav1.DryRunAll}})
		return gateRejected(err)
	}, 10*time.Second, 250*time.Millisecond).Should(BeTrue(), "RoleBasedGroupSet gate did not become active")
}

// gateProbeNamespace is where the dry-run gate probes are submitted. It is
// created by the suite fixture before the webhooks are installed.
const gateProbeNamespace = "deprecation-gate"

var (
	noneSideEffect = admissionregistrationv1.SideEffectClassNone
	failPolicy     = admissionregistrationv1.Fail
)

func ptrTo[T any](v T) *T { return &v }
