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

package rbg

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/discovery"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
	wrappers "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
	"sigs.k8s.io/yaml"
)

// Stateful roles always mount the RBG-level discovery ConfigMap, so the controller must
// create it for every RBG with a stateful role, whatever discovery-config-mode says (#595).
var _ = Describe("Discovery ConfigMap", func() {
	const (
		timeout  = 30 * time.Second
		interval = 250 * time.Millisecond
	)

	var namespace string

	BeforeEach(func() {
		namespace = fmt.Sprintf("test-discovery-cm-%d", time.Now().UnixNano())
		testutil.CreateNamespace(namespace)
	})

	AfterEach(func() {
		testutil.DeleteNamespace(namespace)
	})

	expectDiscoveryConfigMap := func(key types.NamespacedName, role string) {
		Eventually(func(g Gomega) {
			group := &workloadsv1alpha2.RoleBasedGroup{}
			g.Expect(testutil.K8sClient.Get(testutil.Ctx, key, group)).To(Succeed())
			cm := &corev1.ConfigMap{}
			g.Expect(testutil.K8sClient.Get(testutil.Ctx, key, cm)).To(Succeed())
			owner := metav1.GetControllerOf(cm)
			g.Expect(owner).NotTo(BeNil())
			g.Expect(owner.UID).To(Equal(group.UID))
			var config discovery.ClusterConfig
			g.Expect(yaml.UnmarshalStrict([]byte(cm.Data["config.yaml"]), &config)).To(Succeed())
			g.Expect(config.Roles).To(HaveKey(role))
		}, timeout, interval).Should(Succeed())
	}

	It("creates it when an RBGSet child gains its first stateful role", func() {
		router := wrappers.BuildStandaloneRole("router").WithReplicas(0).
			WithAnnotations(map[string]string{constants.RoleInstancePatternKey: string(constants.StatelessPattern)}).Obj()
		set := wrappers.BuildBasicRoleBasedGroupSet("discovery-set", namespace).Obj()
		set.Spec.GroupTemplate.Spec.Roles = []workloadsv1alpha2.RoleSpec{router}
		Expect(testutil.K8sClient.Create(testutil.Ctx, set)).To(Succeed())

		// Wait until the child has status: controllers before #595 classified a child
		// with status but without a mode annotation as legacy. The RBGSet controller
		// strips that annotation because it replaces child annotations with the template's.
		childKey := types.NamespacedName{Namespace: namespace, Name: set.Name + "-0"}
		Eventually(func(g Gomega) {
			child := &workloadsv1alpha2.RoleBasedGroup{}
			g.Expect(testutil.K8sClient.Get(testutil.Ctx, childKey, child)).To(Succeed())
			g.Expect(child.Status.RoleStatuses).NotTo(BeEmpty())
		}, timeout, interval).Should(Succeed())
		err := testutil.K8sClient.Get(testutil.Ctx, childKey, &corev1.ConfigMap{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "a stateless-only group mounts no discovery ConfigMap, got %v", err)

		worker := wrappers.BuildStandaloneRole("worker").WithReplicas(0).Obj()
		Eventually(func() error {
			latest := &workloadsv1alpha2.RoleBasedGroupSet{}
			if err := testutil.K8sClient.Get(testutil.Ctx, client.ObjectKeyFromObject(set), latest); err != nil {
				return err
			}
			latest.Spec.GroupTemplate.Spec.Roles = append(latest.Spec.GroupTemplate.Spec.Roles, worker)
			return testutil.K8sClient.Update(testutil.Ctx, latest)
		}, timeout, interval).Should(Succeed())

		expectDiscoveryConfigMap(childKey, "worker")
	})

	It("creates it for a stateful role of an RBG annotated with the legacy mode", func() {
		group := wrappers.BuildBasicRoleBasedGroup("discovery-legacy", namespace).
			// The wire value that controllers before #595 wrote; it is ignored now.
			WithAnnotations(map[string]string{"rbg.workloads.x-k8s.io/discovery-config-mode": "legacy"}).
			WithRoles([]workloadsv1alpha2.RoleSpec{wrappers.BuildStandaloneRole("worker").WithReplicas(0).Obj()}).Obj()
		Expect(testutil.K8sClient.Create(testutil.Ctx, group)).To(Succeed())

		expectDiscoveryConfigMap(client.ObjectKeyFromObject(group), "worker")
	})

	It("does not stamp a discovery-config-mode annotation on new groups", func() {
		group := wrappers.BuildBasicRoleBasedGroup("discovery-unannotated", namespace).
			WithRoles([]workloadsv1alpha2.RoleSpec{wrappers.BuildStandaloneRole("worker").WithReplicas(0).Obj()}).Obj()
		Expect(testutil.K8sClient.Create(testutil.Ctx, group)).To(Succeed())

		key := client.ObjectKeyFromObject(group)
		expectDiscoveryConfigMap(key, "worker")
		// Nothing reads the annotation any more, and the RBGSet controller strips child
		// annotations that its template lacks, so writing it would only add churn.
		latest := &workloadsv1alpha2.RoleBasedGroup{}
		Expect(testutil.K8sClient.Get(testutil.Ctx, key, latest)).To(Succeed())
		Expect(latest.Annotations).NotTo(HaveKey("rbg.workloads.x-k8s.io/discovery-config-mode"))
	})
})
