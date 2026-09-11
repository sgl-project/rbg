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

// Package testutil provides shared test utilities for envtest-based controller tests.
package testutil

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"runtime"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
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

// SetupWebhookTestEnv initializes a webhook-enabled test environment: the same
// CRDs as SetupTestEnv, plus the three mutating admission webhooks the release
// installs (RoleBasedGroup, RoleBasedGroupSet, RoleInstanceSet defaulters) served
// by the manager's webhook server. No controllers are started: the point of this
// suite is the admission path itself, and the objects it creates must survive
// exactly as the webhooks wrote them.
//
// SetupTestEnv deliberately wires no webhooks, so the existing suites keep running
// without admission interfering with their expectations. The webhook tests live in
// their own task package and must call this function, not SetupTestEnv.
func SetupWebhookTestEnv() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	Ctx, Cancel = context.WithCancel(context.TODO())

	By("bootstrapping webhook-enabled test environment")

	_, currentFile, _, _ := runtime.Caller(0)
	crdPath := filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "config", "crd", "bases")
	webhookPath := filepath.Join(filepath.Dir(currentFile), "webhook")

	TestEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{crdPath},
		ErrorIfCRDPathMissing: true,
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			Paths: []string{webhookPath},
		},
	}

	var err error
	Cfg, err = TestEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(Cfg).NotTo(BeNil())

	err = workloadsv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	err = workloadsv1alpha2.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	K8sClient, err = client.New(Cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(K8sClient).NotTo(BeNil())

	By("setting up the webhook server on the manager")
	TestMgr, err = ctrl.NewManager(Cfg, ctrl.Options{
		Scheme: scheme.Scheme,
		Metrics: server.Options{
			BindAddress: "0", // disable metrics to avoid port conflicts between suites
		},
		// WebhookServer must serve on the host/port envtest reserved for it, with
		// the CA it generated; otherwise the API server cannot validate the TLS
		// handshake and every admitted request fails.
		WebhookServer: webhook.NewServer(webhook.Options{
			Host:    TestEnv.WebhookInstallOptions.LocalServingHost,
			Port:    TestEnv.WebhookInstallOptions.LocalServingPort,
			CertDir: TestEnv.WebhookInstallOptions.LocalServingCertDir,
		}),
	})
	Expect(err).NotTo(HaveOccurred())

	// Register the same custom defaulters the controller registers in production
	// (cmd/rbgs/main.go), so the admission path under test is the shipping one.
	if err = (&workloadsv1alpha2.RoleBasedGroup{}).SetupWebhookWithManager(TestMgr, true); err != nil {
		Fail("unable to create RoleBasedGroup admission webhook: " + err.Error())
	}
	if err = (&workloadsv1alpha2.RoleBasedGroupSet{}).SetupWebhookWithManager(TestMgr, true); err != nil {
		Fail("unable to create RoleBasedGroupSet admission webhook: " + err.Error())
	}
	if err = (&workloadsv1alpha2.RoleInstanceSet{}).SetupWebhookWithManager(TestMgr); err != nil {
		Fail("unable to create RoleInstanceSet admission webhook: " + err.Error())
	}

	// Start the manager so the webhook server begins serving.
	go func() {
		defer GinkgoRecover()
		err := TestMgr.Start(Ctx)
		Expect(err).NotTo(HaveOccurred())
	}()

	// Wait for the webhook server's listener to come up before the first request is
	// admitted. A fixed sleep would race a slow bind on a loaded CI runner, and with
	// failurePolicy=Fail the first Create would fail the whole suite spuriously.
	Eventually(
		func() error {
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
			conn.Close()
			return nil
		}, 10*time.Second, 100*time.Millisecond,
	).Should(Succeed())
}
