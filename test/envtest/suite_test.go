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

package envtest

import (
	"context"
	"path/filepath"
	"testing"
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

// Global variables for test suite
var (
	cfg       *rest.Config
	k8sClient client.Client
	testEnv   *envtest.Environment
	ctx       context.Context
	cancel    context.CancelFunc
	testMgr   manager.Manager
)

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Controller Envtest Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	ctx, cancel = context.WithCancel(context.TODO())

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "config", "crd", "bases"),
		},
		ErrorIfCRDPathMissing: true,
	}

	var err error
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	// Add schemes
	err = workloadsv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = apiextensionsv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	// Create k8sClient
	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	// Create and start controller manager
	By("setting up controller manager")
	testMgr, err = setupManager(ctx)
	Expect(err).NotTo(HaveOccurred())

	// Register field indexes
	err = fieldindex.RegisterFieldIndexes(testMgr.GetCache())
	Expect(err).NotTo(HaveOccurred())

	// Setup all controllers
	err = setupRBGController(testMgr)
	Expect(err).NotTo(HaveOccurred())

	err = setupInstanceSetController(testMgr)
	Expect(err).NotTo(HaveOccurred())

	err = setupInstanceController(testMgr)
	Expect(err).NotTo(HaveOccurred())

	err = setupPodController(testMgr)
	Expect(err).NotTo(HaveOccurred())

	err = setupRBGScalingAdapterController(testMgr)
	Expect(err).NotTo(HaveOccurred())

	err = setupRBGSController(testMgr)
	Expect(err).NotTo(HaveOccurred())

	// Start the manager
	go func() {
		defer GinkgoRecover()
		err := testMgr.Start(ctx)
		Expect(err).NotTo(HaveOccurred())
	}()

	// Wait for manager to be ready
	time.Sleep(100 * time.Millisecond)
})

var _ = AfterSuite(func() {
	cancel()
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})

// Helper functions for tests

// createNamespace creates a namespace for testing
func createNamespace(name string) *corev1.Namespace {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
	Expect(k8sClient.Create(ctx, ns)).Should(Succeed())
	return ns
}

// deleteNamespace deletes a namespace and waits for deletion to complete
func deleteNamespace(name string) {
	ns := &corev1.Namespace{}
	err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, ns)
	if err != nil {
		// Namespace doesn't exist, nothing to delete
		return
	}

	// Delete the namespace
	err = k8sClient.Delete(ctx, ns)
	if err != nil {
		// Ignore not found errors
		return
	}

	// Wait for namespace to be deleted (with timeout)
	for i := 0; i < 30; i++ {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, ns)
		if err != nil {
			// Namespace is gone
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// waitForReconcile waits for a short period to allow reconciliation
func waitForReconcile() {
	time.Sleep(100 * time.Millisecond)
}

// setupManager creates and starts a controller manager for testing
func setupManager(ctx context.Context) (manager.Manager, error) {
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
	})
	if err != nil {
		return nil, err
	}

	return mgr, nil
}

// setupRBGController sets up the RoleBasedGroup controller
func setupRBGController(mgr manager.Manager) error {
	rbgReconciler := workloadscontroller.NewRoleBasedGroupReconciler(mgr)
	return rbgReconciler.SetupWithManager(mgr, controller.Options{})
}

// setupPodController sets up the Pod controller
func setupPodController(mgr manager.Manager) error {
	podReconciler := workloadscontroller.NewPodReconciler(mgr)
	return podReconciler.SetupWithManager(mgr, controller.Options{})
}

// setupRBGScalingAdapterController sets up the RoleBasedGroupScalingAdapter controller
func setupRBGScalingAdapterController(mgr manager.Manager) error {
	rbgScalingAdapterReconciler := workloadscontroller.NewRoleBasedGroupScalingAdapterReconciler(mgr)
	return rbgScalingAdapterReconciler.SetupWithManager(mgr, controller.Options{})
}

// setupRBGSController sets up the RoleBasedGroupSet controller
func setupRBGSController(mgr manager.Manager) error {
	rbgsReconciler := workloadscontroller.NewRoleBasedGroupSetReconciler(mgr)
	return rbgsReconciler.SetupWithManager(mgr, controller.Options{})
}

// setupInstanceController sets up the Instance controller
func setupInstanceController(mgr manager.Manager) error {
	instanceReconciler := workloadscontroller.NewInstanceReconciler(mgr)
	return instanceReconciler.SetupWithManager(mgr, controller.Options{})
}

// setupInstanceSetController sets up the InstanceSet controller
func setupInstanceSetController(mgr manager.Manager) error {
	instanceSetReconciler := workloadscontroller.NewInstanceSetReconciler(mgr)
	return instanceSetReconciler.SetupWithManager(mgr, controller.Options{})
}

// eventuallyWithTimeout returns an Eventually assertion with default timeout
func eventuallyWithTimeout() AsyncAssertion {
	return Eventually(timeout(10 * time.Second))
}

func timeout(d time.Duration) func() time.Duration {
	return func() time.Duration {
		return d
	}
}
