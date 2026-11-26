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
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
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

	// Belt-and-braces: controller-runtime's Start() itself calls
	// PrepWithoutInstalling() before InstallCRDs, so conversion webhooks are
	// enabled either way. The explicit call is harmless and makes the
	// allocation visible at the setup site.
	Expect(TestEnv.WebhookInstallOptions.PrepWithoutInstalling()).To(Succeed())

	Cfg, err = TestEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(Cfg).NotTo(BeNil())

	// Route the suite client through a warning recorder so the specs can
	// assert the on-the-wire CRD-level deprecation warning (the admission
	// webhook warnings are gone; the CRD warning is the sole layer).
	clientCfg := rest.CopyConfig(Cfg)
	clientCfg.WarningHandler = GateWarnings
	K8sClient, err = client.New(clientCfg, client.Options{Scheme: scheme.Scheme})
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

	// The same validators with --enable-deprecated-workload-types=false, served on
	// their own paths so a spec can switch the gate to the strict mode with
	// EnableV1a2StrictGate instead of starting a second manager.
	TestMgr.GetWebhookServer().Register(strictRBGValidatePath, admission.WithCustomValidator(
		scheme.Scheme, &workloadsv1alpha2.RoleBasedGroup{}, &workloadsv1alpha2.RoleBasedGroupValidator{
			Client:                        TestMgr.GetClient(),
			APIReader:                     TestMgr.GetAPIReader(),
			EnableDeprecatedWorkloadTypes: false,
		}))
	TestMgr.GetWebhookServer().Register(strictRBGSetValidatePath, admission.WithCustomValidator(
		scheme.Scheme, &workloadsv1alpha2.RoleBasedGroupSet{}, &workloadsv1alpha2.RoleBasedGroupSetValidator{
			EnableDeprecatedWorkloadTypes: false,
		}))

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
	EnableV1a2Gate()
}

// v1a2GateConfigName is the name of the suite's validating webhook
// configuration. Grandfathering specs toggle this configuration to seed
// objects that predate the gate.
const v1a2GateConfigName = "v1a1-gate-test-validating"

// Paths of the strict-mode validators registered next to the production ones.
const (
	strictRBGValidatePath    = "/validate-strict-workloads-x-k8s-io-v1alpha2-rolebasedgroup"
	strictRBGSetValidatePath = "/validate-strict-workloads-x-k8s-io-v1alpha2-rolebasedgroupset"
)

// EnableV1a2Gate (re)installs the v1alpha2 validating webhook configuration
// pointing at the test webhook server, then waits for the gate to be active.
// matchPolicy is left unset (Equivalent, the default) so v1alpha1 writes are
// routed through conversion to the v1alpha2 gate — the production
// composition. It is idempotent: an existing configuration is replaced
// wholesale so a stale one (wrong host, port, or CA bundle) cannot linger.
// The gate runs in compatibility mode, the production default.
func EnableV1a2Gate() {
	installV1a2Gate(
		"/validate-workloads-x-k8s-io-v1alpha2-rolebasedgroup",
		"/validate-workloads-x-k8s-io-v1alpha2-rolebasedgroupset",
		"new uses are not accepted",
	)
}

// EnableV1a2StrictGate is EnableV1a2Gate with the validators that run with
// --enable-deprecated-workload-types=false. Call EnableV1a2Gate afterwards to
// return the suite to compatibility mode.
func EnableV1a2StrictGate() {
	installV1a2Gate(strictRBGValidatePath, strictRBGSetValidatePath, "is deprecated and not enabled on this cluster")
}

// installV1a2Gate replaces the suite's validating webhook configuration with one
// that routes RoleBasedGroup and RoleBasedGroupSet writes to the given paths, and
// waits until a dry-run deprecated create of each kind is rejected with
// activeMessage, which tells the two modes apart. Probing both kinds makes a wrong
// path or a missing registration for either validator fail the suite.
func installV1a2Gate(rbgPath, rbgSetPath, activeMessage string) {
	config := buildValidatingWebhookConfig(TestEnv.WebhookInstallOptions, []gateWebhookSpec{
		{
			name:     "vrolebasedgroup.kb.io",
			path:     rbgPath,
			resource: "rolebasedgroups",
		},
		{
			name:     "vrolebasedgroupset.kb.io",
			path:     rbgSetPath,
			resource: "rolebasedgroupsets",
		},
	})
	if err := K8sClient.Create(Ctx, config); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			Expect(err).NotTo(HaveOccurred())
		}
		// A configuration left behind by a failed run can point at a stale
		// webhook server (host, port, or CA bundle). Replace it wholesale
		// instead of reusing it.
		Expect(K8sClient.Delete(Ctx, config)).To(Succeed())
		Expect(K8sClient.Create(Ctx, config)).To(Succeed())
	}

	// Wait for the gate to be active: a dry-run deprecated create through
	// the v1alpha2 API must be rejected by the requested mode, for both kinds.
	Eventually(func() bool {
		return deprecatedCreateRejected(gateReadinessProbe(), activeMessage)
	}, 10*time.Second, 250*time.Millisecond).Should(BeTrue(), "the v1alpha2 gate did not become active: %s", rbgPath)
	Eventually(func() bool {
		return deprecatedCreateRejected(gateSetReadinessProbe(), activeMessage)
	}, 10*time.Second, 250*time.Millisecond).Should(BeTrue(), "the v1alpha2 gate did not become active: %s", rbgSetPath)
}

// DisableV1a2Gate deletes the suite's validating webhook configuration and
// waits for the gate to be inactive: a dry-run deprecated create through the
// v1alpha2 API must be accepted. Grandfathering specs use it to seed objects
// that predate the gate.
func DisableV1a2Gate() {
	config := &admissionregistrationv1.ValidatingWebhookConfiguration{
		ObjectMeta: objectMeta(v1a2GateConfigName),
	}
	// Idempotent, symmetric with EnableV1a2Gate's replace-on-exists: a
	// missing configuration is already disabled.
	if err := K8sClient.Delete(Ctx, config); err != nil && !apierrors.IsNotFound(err) {
		Expect(err).NotTo(HaveOccurred())
	}

	// Returning the probe error itself (nil once the dry-run create is
	// accepted) keeps an unrelated probe failure distinguishable from the
	// gate simply staying active: the Eventually failure message then names
	// the actual error instead of reporting a bare inactivity timeout.
	Eventually(func() error {
		return K8sClient.Create(Ctx, gateReadinessProbe(), &client.CreateOptions{DryRun: []string{"All"}})
	}, 10*time.Second, 250*time.Millisecond).Should(Succeed(), "the v1alpha2 gate did not become inactive: the dry-run deprecated create is still failing")
}

// deprecatedCreateRejected reports whether a dry-run create of probe, a v1alpha2
// object that names a deprecated workload type, is rejected by the gate with
// message.
func deprecatedCreateRejected(probe client.Object, message string) bool {
	err := K8sClient.Create(Ctx, probe, &client.CreateOptions{DryRun: []string{"All"}})
	return err != nil && containsStr(err.Error(), message)
}

func gateReadinessProbe() *workloadsv1alpha2.RoleBasedGroup {
	return &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1ObjectMeta("gate-readiness-v1a2", "default"),
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{{
				Name:        "worker",
				Replicas:    ptrToInt32(1),
				Annotations: map[string]string{"rbg.workloads.x-k8s.io/role-workload-type": "apps/v1/StatefulSet"},
			}},
		},
	}
}

// gateSetReadinessProbe is gateReadinessProbe for the RoleBasedGroupSet validator:
// a set whose group template names a deprecated workload type.
func gateSetReadinessProbe() *workloadsv1alpha2.RoleBasedGroupSet {
	return &workloadsv1alpha2.RoleBasedGroupSet{
		ObjectMeta: metav1ObjectMeta("gate-readiness-v1a2-set", "default"),
		Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
			Replicas: ptrToInt32(1),
			GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
				Spec: gateReadinessProbe().Spec,
			},
		},
	}
}

type gateWebhookSpec struct {
	name     string
	path     string
	resource string
}

type admissionv1ValidatingConfig = admissionregistrationv1.ValidatingWebhookConfiguration
type admissionv1Webhook = admissionregistrationv1.ValidatingWebhook
type admissionv1ClientConfig = admissionregistrationv1.WebhookClientConfig

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
		ObjectMeta: objectMeta(v1a2GateConfigName),
		Webhooks:   webhooks,
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && strings.Contains(s, substr)
}

// GateWarningRecorder collects Warning headers returned by the API server.
type GateWarningRecorder struct {
	mu       sync.Mutex
	warnings []string
}

// Reset drops all recorded warnings. Call it at the start of every spec.
func (r *GateWarningRecorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.warnings = nil
}

// HandleWarningHeader implements rest.WarningHandler.
func (r *GateWarningRecorder) HandleWarningHeader(code int, agent string, text string) {
	if code < 200 || code > 299 || text == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.warnings = append(r.warnings, text)
}

// All returns a copy of every recorded warning.
func (r *GateWarningRecorder) All() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.warnings...)
}

// GateWarnings is shared by the v1a1gate suite's client.
var GateWarnings = &GateWarningRecorder{}
