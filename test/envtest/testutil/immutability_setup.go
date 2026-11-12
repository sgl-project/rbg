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
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	workloadscontroller "sigs.k8s.io/rbgs/internal/controller/workloads"
)

// SetupImmutabilityTestEnv boots the envtest form the KEP-531 profile
// mutability suite needs: the mutating defaulters, the RBG/RBGSet
// validating webhooks, the ClusterEngineRuntimeProfile freeze webhook and
// the reference-tracking controller are all served by the manager, so
// admission enforcement and status tracking are observable together. The
// other suites are unaffected: this shape lives in its own task package.
func SetupImmutabilityTestEnv() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	Ctx, Cancel = context.WithCancel(context.TODO())

	By("bootstrapping immutability test environment")

	_, currentFile, _, _ := runtime.Caller(0)
	crdPath := filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "config", "crd", "bases")
	webhookPath := filepath.Join(filepath.Dir(currentFile), "immutability")

	TestEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{crdPath},
		ErrorIfCRDPathMissing: true,
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			Paths: []string{webhookPath},
		},
	}

	// Register the schemes BEFORE TestEnv.Start(): envtest inspects the
	// scheme's convertible GroupKinds to decide which installed CRDs get a
	// conversion stanza pointing at the manager's /convert endpoint. The
	// v1alpha1 presence specs below ride that conversion — matchPolicy
	// Equivalent hands the webhook a converted v1alpha2 object, where the
	// conversion's pattern inference decides the standalone exemption.
	err := workloadsv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	err = workloadsv1alpha2.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	Cfg, err = TestEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(Cfg).NotTo(BeNil())

	K8sClient, err = client.New(Cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(K8sClient).NotTo(BeNil())

	// The manager stays in the suite's hands: StartImmutabilityManager wires
	// the mutating defaulters the installed configuration requires, the CERP
	// freeze validator under test (all registered exactly as cmd/rbgs/main.go
	// registers them in production) and the reference-tracking controller, so
	// admission enforcement and status tracking are observable together.
	// The suite may stop and start the manager to observe tracking across
	// a restart — the baseline lives in status, not in memory.
}

var (
	immutabilityMgrDone   = make(chan struct{})
	immutabilityMgrCancel context.CancelFunc
)

// StartImmutabilityManager brings up a manager serving the defaulters, the
// RBG/RBGSet/CERP validating webhooks and the reference-tracking controller,
// and blocks until the webhook listener is up. Safe to call again after
// StopImmutabilityManager — a restarted manager re-reads status, which is
// exactly what the restart scenario observes.
func StartImmutabilityManager() {
	By("starting the immutability manager")
	mgrCtx, cancel := context.WithCancel(Ctx)
	immutabilityMgrCancel = cancel
	done := make(chan struct{})
	immutabilityMgrDone = done

	var err error
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

	// Register the mutating defaulters the installed configuration requires,
	// plus the CERP freeze validator under test — all wired exactly as
	// cmd/rbgs/main.go wires them in production.
	if err = (&workloadsv1alpha2.RoleBasedGroup{}).SetupWebhookWithManager(TestMgr, true); err != nil {
		Fail("unable to create RoleBasedGroup admission webhook: " + err.Error())
	}
	if err = (&workloadsv1alpha2.RoleBasedGroupSet{}).SetupWebhookWithManager(TestMgr, true); err != nil {
		Fail("unable to create RoleBasedGroupSet admission webhook: " + err.Error())
	}
	if err = (&workloadsv1alpha2.RoleInstanceSet{}).SetupWebhookWithManager(TestMgr); err != nil {
		Fail("unable to create RoleInstanceSet admission webhook: " + err.Error())
	}
	if err = (&workloadsv1alpha2.ClusterEngineRuntimeProfile{}).SetupWebhookWithManager(TestMgr); err != nil {
		Fail("unable to create ClusterEngineRuntimeProfile admission webhook: " + err.Error())
	}
	if err = workloadscontroller.NewClusterEngineRuntimeProfileReconciler(TestMgr).
		SetupWithManager(TestMgr, controller.Options{SkipNameValidation: ptr.To(true)}); err != nil {
		Fail("unable to create ClusterEngineRuntimeProfile controller: " + err.Error())
	}

	go func() {
		defer GinkgoRecover()
		defer close(done)
		err := TestMgr.Start(mgrCtx)
		Expect(err).NotTo(HaveOccurred())
	}()

	// Wait for the webhook server's listener before the first admitted request.
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
			if err := conn.Close(); err != nil {
				return err
			}
			return nil
		}, 10*time.Second, 100*time.Millisecond,
	).Should(Succeed())
}

// StopImmutabilityManager shuts the manager down and waits for the stop to
// finish, so a subsequent StartImmutabilityManager rebinds the webhook port
// instead of racing it.
func StopImmutabilityManager() {
	By("stopping the immutability manager")
	immutabilityMgrCancel()
	Eventually(immutabilityMgrDone, 10*time.Second, 100*time.Millisecond).Should(BeClosed())
}
