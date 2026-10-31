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
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
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

// ContractWarningRecorder collects the Warning headers the API server returned
// to the suite's client. The rest client is configured with it so the specs can
// assert the on-the-wire admission warnings instead of re-implementing them.
type ContractWarningRecorder struct {
	mu       sync.Mutex
	warnings []string
}

// Reset drops all recorded warnings. Call it at the start of every spec.
func (r *ContractWarningRecorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.warnings = nil
}

// HandleWarningHeader implements rest.WarningHandler.
func (r *ContractWarningRecorder) HandleWarningHeader(code int, agent string, text string) {
	if code < 200 || code > 299 || text == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.warnings = append(r.warnings, text)
}

// Containing returns every recorded warning containing substr.
func (r *ContractWarningRecorder) Containing(substr string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, warning := range r.warnings {
		if strings.Contains(warning, substr) {
			out = append(out, warning)
		}
	}
	return out
}

// All returns a copy of every recorded warning.
func (r *ContractWarningRecorder) All() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.warnings...)
}

// ContractWarnings is shared by the contract suite's client.
var ContractWarnings = &ContractWarningRecorder{}

// SetupV1alpha1ContractTestEnv boots envtest, registers the production webhook
// handlers on the manager, and installs the shipped
// config/webhook/manifests.yaml (rewritten to the local test server). Unlike the
// v1alpha2 deprecation suite, nothing here is hand-written: the webhook rules,
// paths, failurePolicy, and matchPolicy values under test are the bytes release
// rendering consumes, and the handlers are the ones cmd/rbgs registers.
func SetupV1alpha1ContractTestEnv() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	Ctx, Cancel = context.WithCancel(context.TODO())

	By("bootstrapping the v1alpha1 contract test environment")

	_, currentFile, _, _ := runtime.Caller(0)
	crdPath := filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "config", "crd", "bases")
	webhookManifestPath := filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "config", "webhook", "manifests.yaml")

	TestEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{crdPath},
		ErrorIfCRDPathMissing: true,
	}

	var err error
	Cfg, err = TestEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(Cfg).NotTo(BeNil())

	err = workloadsv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	err = workloadsv1alpha2.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	err = admissionregistrationv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	// Route the suite client through the warning recorder so the specs can
	// assert the warnings the API server actually returned.
	clientCfg := rest.CopyConfig(Cfg)
	clientCfg.WarningHandler = ContractWarnings
	K8sClient, err = client.New(clientCfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(K8sClient).NotTo(BeNil())

	By("registering the production webhook handlers")
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

	registerProductionWebhooks(TestMgr)

	go func() {
		defer GinkgoRecover()
		err := TestMgr.Start(Ctx)
		Expect(err).NotTo(HaveOccurred())
	}()

	// The webhook server must be serving before the shipped configurations that
	// point at it are installed.
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

	By("installing the shipped webhook manifests")
	installShippedWebhookConfigurations(webhookManifestPath)

	// The readiness probe (and every spec) submits objects into the contract
	// namespace, so it must exist before the gate is probed.
	CreateNamespace(contractNamespace)
	waitForV1alpha1Gate()
}

// registerProductionWebhooks mirrors the registration order cmd/rbgs uses.
func registerProductionWebhooks(mgr ctrl.Manager) {
	Expect((&workloadsv1alpha2.RoleBasedGroup{}).SetupWebhookWithManager(mgr, true)).To(Succeed())
	Expect((&workloadsv1alpha2.RoleBasedGroupSet{}).SetupWebhookWithManager(mgr, true)).To(Succeed())
	Expect((&workloadsv1alpha2.CoordinatedPolicy{}).SetupWebhookWithManager(mgr, false)).To(Succeed())
	Expect((&workloadsv1alpha2.RoleInstanceSet{}).SetupWebhookWithManager(mgr)).To(Succeed())

	Expect((&workloadsv1alpha1.RoleBasedGroup{}).SetupDeprecationWebhookWithManager(mgr, true)).To(Succeed())
	Expect((&workloadsv1alpha1.RoleBasedGroupSet{}).SetupDeprecationWebhookWithManager(mgr, true)).To(Succeed())
	Expect((&workloadsv1alpha1.InstanceSet{}).SetupDeprecationWebhookWithManager(mgr)).To(Succeed())
	Expect((&workloadsv1alpha1.Instance{}).SetupDeprecationWebhookWithManager(mgr)).To(Succeed())
	Expect((&workloadsv1alpha1.RoleBasedGroupScalingAdapter{}).SetupDeprecationWebhookWithManager(mgr)).To(Succeed())
	Expect((&workloadsv1alpha1.ClusterEngineRuntimeProfile{}).SetupDeprecationWebhookWithManager(mgr)).To(Succeed())
}

// installShippedWebhookConfigurations decodes the shipped webhook manifests and
// creates them against the test server, rewriting the Kustomize placeholder
// clientConfig (service name/namespace + empty caBundle) exactly the way envtest
// rewrites its own webhook manifests.
func installShippedWebhookConfigurations(path string) {
	data, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())

	opts := TestEnv.WebhookInstallOptions
	hostPort := net.JoinHostPort(opts.LocalServingHost, fmt.Sprint(opts.LocalServingPort))

	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	installed := 0
	for {
		var raw map[string]interface{}
		err := decoder.Decode(&raw)
		if err == io.EOF {
			break
		}
		Expect(err).NotTo(HaveOccurred())
		if raw == nil {
			continue
		}

		switch raw["kind"].(string) {
		case "ValidatingWebhookConfiguration":
			config := &admissionregistrationv1.ValidatingWebhookConfiguration{}
			Expect(k8sruntime.DefaultUnstructuredConverter.FromUnstructured(raw, config)).To(Succeed())
			for i := range config.Webhooks {
				rewriteClientConfigV1(&config.Webhooks[i].ClientConfig, hostPort, opts.LocalServingCAData)
			}
			Expect(K8sClient.Create(Ctx, config)).To(Succeed())
			installed++
		case "MutatingWebhookConfiguration":
			config := &admissionregistrationv1.MutatingWebhookConfiguration{}
			Expect(k8sruntime.DefaultUnstructuredConverter.FromUnstructured(raw, config)).To(Succeed())
			for i := range config.Webhooks {
				rewriteClientConfigV1(&config.Webhooks[i].ClientConfig, hostPort, opts.LocalServingCAData)
			}
			Expect(K8sClient.Create(Ctx, config)).To(Succeed())
			installed++
		default:
			Fail(fmt.Sprintf("unexpected kind in shipped webhook manifests: %v", raw["kind"]))
		}
	}
	Expect(installed).To(Equal(2), "the shipped manifests must contain exactly two webhook configurations")
}

func rewriteClientConfigV1(cc *admissionregistrationv1.WebhookClientConfig, hostPort string, caData []byte) {
	if cc.Service != nil && cc.Service.Path != nil {
		url := fmt.Sprintf("https://%s%s", hostPort, *cc.Service.Path)
		cc.URL = &url
		cc.Service = nil
	}
	cc.CABundle = caData
}

// waitForV1alpha1Gate blocks until the shipped v1alpha1 webhook actually
// rejects a new deprecated workload use through the real admission chain. A
// server-side dry-run create traverses admission without persisting anything.
func waitForV1alpha1Gate() {
	Eventually(func() bool {
		rbg := &workloadsv1alpha1.RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "gate-readiness-v1a1", Namespace: contractNamespace},
			Spec: workloadsv1alpha1.RoleBasedGroupSpec{
				Roles: []workloadsv1alpha1.RoleSpec{{
					Name:     "worker",
					Replicas: ptr.To(int32(1)),
					Workload: workloadsv1alpha1.WorkloadSpec{
						APIVersion: "apps/v1",
						Kind:       "StatefulSet",
					},
					TemplateSource: workloadsv1alpha1.TemplateSource{
						Template: &corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{Name: "nginx", Image: "nginx:latest"}},
							},
						},
					},
				}},
			},
		}
		err := K8sClient.Create(Ctx, rbg, &client.CreateOptions{DryRun: []string{metav1.DryRunAll}})
		return err != nil && strings.Contains(err.Error(), "new uses are not accepted")
	}, 10*time.Second, 250*time.Millisecond).Should(BeTrue(), "the shipped v1alpha1 gate did not become active")
}

const contractNamespace = "deprecation-v1a1-contract"
