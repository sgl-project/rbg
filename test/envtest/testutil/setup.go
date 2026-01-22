/*
Copyright 2025.

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
	"path/filepath"
	"runtime"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	workloadscontroller "sigs.k8s.io/rbgs/internal/controller/workloads"
	"sigs.k8s.io/rbgs/pkg/utils/fieldindex"
)

// Global variables for test suite - exported for use by test packages
var (
	Cfg       *rest.Config
	K8sClient client.Client
	TestEnv   *envtest.Environment
	Ctx       context.Context
	Cancel    context.CancelFunc
	TestMgr   manager.Manager
)

// SetupTestEnv initializes the test environment. Call this in BeforeSuite.
func SetupTestEnv() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	Ctx, Cancel = context.WithCancel(context.TODO())

	By("bootstrapping test environment")

	// Get the path to CRD directory relative to this file
	_, currentFile, _, _ := runtime.Caller(0)
	crdPath := filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "config", "crd", "bases")

	TestEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{crdPath},
		ErrorIfCRDPathMissing: true,
	}

	var err error
	Cfg, err = TestEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(Cfg).NotTo(BeNil())

	// Add schemes
	err = workloadsv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = apiextensionsv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	// Create K8sClient
	K8sClient, err = client.New(Cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(K8sClient).NotTo(BeNil())

	// Create and start controller manager
	By("setting up controller manager")
	TestMgr, err = SetupManager(Ctx)
	Expect(err).NotTo(HaveOccurred())

	// Register field indexes
	err = fieldindex.RegisterFieldIndexes(TestMgr.GetCache())
	Expect(err).NotTo(HaveOccurred())

	// Setup all controllers
	err = SetupRBGController(TestMgr)
	Expect(err).NotTo(HaveOccurred())

	err = SetupInstanceSetController(TestMgr)
	Expect(err).NotTo(HaveOccurred())

	err = SetupInstanceController(TestMgr)
	Expect(err).NotTo(HaveOccurred())

	err = SetupPodController(TestMgr)
	Expect(err).NotTo(HaveOccurred())

	err = SetupRBGScalingAdapterController(TestMgr)
	Expect(err).NotTo(HaveOccurred())

	err = SetupRBGSController(TestMgr)
	Expect(err).NotTo(HaveOccurred())

	// Start the manager
	go func() {
		defer GinkgoRecover()
		err := TestMgr.Start(Ctx)
		Expect(err).NotTo(HaveOccurred())
	}()

	// Wait for manager to be ready
	time.Sleep(100 * time.Millisecond)
}

// TeardownTestEnv cleans up the test environment. Call this in AfterSuite.
func TeardownTestEnv() {
	Cancel()
	By("tearing down the test environment")
	err := TestEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
}

// CreateNamespace creates a namespace for testing
func CreateNamespace(name string) *corev1.Namespace {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
	Expect(K8sClient.Create(Ctx, ns)).Should(Succeed())
	return ns
}

// DeleteNamespace deletes a namespace and waits for deletion to complete
func DeleteNamespace(name string) {
	ns := &corev1.Namespace{}
	err := K8sClient.Get(Ctx, types.NamespacedName{Name: name}, ns)
	if err != nil {
		// Namespace doesn't exist, nothing to delete
		return
	}

	// Delete the namespace
	err = K8sClient.Delete(Ctx, ns)
	if err != nil {
		// Ignore not found errors
		return
	}

	// Wait for namespace to be deleted (with timeout)
	for i := 0; i < 30; i++ {
		err := K8sClient.Get(Ctx, types.NamespacedName{Name: name}, ns)
		if err != nil {
			// Namespace is gone
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// WaitForReconcile waits for a short period to allow reconciliation
func WaitForReconcile() {
	time.Sleep(100 * time.Millisecond)
}

// SetupManager creates and starts a controller manager for testing
func SetupManager(ctx context.Context) (manager.Manager, error) {
	mgr, err := ctrl.NewManager(Cfg, ctrl.Options{
		Scheme: scheme.Scheme,
	})
	if err != nil {
		return nil, err
	}

	return mgr, nil
}

// SetupRBGController sets up the RoleBasedGroup controller
func SetupRBGController(mgr manager.Manager) error {
	rbgReconciler := workloadscontroller.NewRoleBasedGroupReconciler(mgr)
	return rbgReconciler.SetupWithManager(mgr, controller.Options{})
}

// SetupPodController sets up the Pod controller
func SetupPodController(mgr manager.Manager) error {
	podReconciler := workloadscontroller.NewPodReconciler(mgr)
	return podReconciler.SetupWithManager(mgr, controller.Options{})
}

// SetupRBGScalingAdapterController sets up the RoleBasedGroupScalingAdapter controller
func SetupRBGScalingAdapterController(mgr manager.Manager) error {
	rbgScalingAdapterReconciler := workloadscontroller.NewRoleBasedGroupScalingAdapterReconciler(mgr)
	return rbgScalingAdapterReconciler.SetupWithManager(mgr, controller.Options{})
}

// SetupRBGSController sets up the RoleBasedGroupSet controller
func SetupRBGSController(mgr manager.Manager) error {
	rbgsReconciler := workloadscontroller.NewRoleBasedGroupSetReconciler(mgr)
	return rbgsReconciler.SetupWithManager(mgr, controller.Options{})
}

// SetupInstanceController sets up the Instance controller
func SetupInstanceController(mgr manager.Manager) error {
	instanceReconciler := workloadscontroller.NewInstanceReconciler(mgr)
	return instanceReconciler.SetupWithManager(mgr, controller.Options{})
}

// SetupInstanceSetController sets up the InstanceSet controller
func SetupInstanceSetController(mgr manager.Manager) error {
	instanceSetReconciler := workloadscontroller.NewInstanceSetReconciler(mgr)
	return instanceSetReconciler.SetupWithManager(mgr, controller.Options{})
}

// EventuallyWithTimeout returns an Eventually assertion with default timeout
func EventuallyWithTimeout() AsyncAssertion {
	return Eventually(Timeout(10 * time.Second))
}

// Timeout returns a timeout function for Eventually
func Timeout(d time.Duration) func() time.Duration {
	return func() time.Duration {
		return d
	}
}
