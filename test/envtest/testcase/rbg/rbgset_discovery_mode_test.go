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
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/discovery"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
	wrappers "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
	"sigs.k8s.io/yaml"
)

// Regression tests for #603: the RoleBasedGroupSet controller of v0.7.0 and v0.8.0
// removed the discovery config mode the RoleBasedGroup controller pins on a child,
// the child was then derived as legacy, and its discovery ConfigMap went stale.
var _ = Describe("RoleBasedGroupSet child discovery config", func() {
	const (
		timeout  = 30 * time.Second
		interval = 200 * time.Millisecond
	)
	var (
		namespace string
		setKey    client.ObjectKey
		childKey  client.ObjectKey
		risKey    client.ObjectKey
	)

	BeforeEach(func() {
		namespace = fmt.Sprintf("set-discovery-%d", time.Now().UnixNano())
		testutil.CreateNamespace(namespace)

		role := wrappers.BuildStandaloneRole("worker").WithReplicas(1).Obj()
		set := wrappers.BuildBasicRoleBasedGroupSet("set", namespace).Obj()
		set.Spec.GroupTemplate.Annotations = map[string]string{"team": "a"}
		set.Spec.GroupTemplate.Spec.Roles = []workloadsv1alpha2.RoleSpec{role}
		Expect(testutil.K8sClient.Create(testutil.Ctx, set)).To(Succeed())

		setKey = client.ObjectKeyFromObject(set)
		childKey = client.ObjectKey{Namespace: namespace, Name: "set-0"}
		child := &workloadsv1alpha2.RoleBasedGroup{}
		child.Name = childKey.Name
		risKey = client.ObjectKey{Namespace: namespace, Name: child.GetWorkloadName(&role)}
	})

	AfterEach(func() { testutil.DeleteNamespace(namespace) })

	getChild := func(g Gomega) *workloadsv1alpha2.RoleBasedGroup {
		child := &workloadsv1alpha2.RoleBasedGroup{}
		g.Expect(testutil.K8sClient.Get(testutil.Ctx, childKey, child)).To(Succeed())
		return child
	}
	workerSize := func(g Gomega) int {
		cm := &corev1.ConfigMap{}
		g.Expect(testutil.K8sClient.Get(testutil.Ctx, childKey, cm)).To(Succeed())
		var config discovery.ClusterConfig
		g.Expect(yaml.UnmarshalStrict([]byte(cm.Data["config.yaml"]), &config)).To(Succeed())
		return config.Roles["worker"].Size
	}
	// waitForReconciledChild waits until the child has status, which is what
	// made the RoleBasedGroup controller classify it as a legacy object once
	// its mode annotation was gone.
	waitForReconciledChild := func() {
		Eventually(func(g Gomega) {
			child := getChild(g)
			g.Expect(child.Status.ObservedGeneration).To(BeNumerically(">", 0))
			g.Expect(child.Annotations).To(HaveKeyWithValue("team", "a"))
			g.Expect(child.GetDiscoveryConfigMode()).To(Equal(constants.RefineDiscoveryConfigMode))
			g.Expect(workerSize(g)).To(Equal(1))
		}, timeout, interval).Should(Succeed())
	}
	scaleWorker := func(replicas int32) {
		Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
			set := &workloadsv1alpha2.RoleBasedGroupSet{}
			if err := testutil.K8sClient.Get(testutil.Ctx, setKey, set); err != nil {
				return err
			}
			set.Spec.GroupTemplate.Spec.Roles[0].Replicas = ptr.To(replicas)
			return testutil.K8sClient.Update(testutil.Ctx, set)
		})).To(Succeed())
	}
	getRIS := func(g Gomega) *workloadsv1alpha2.RoleInstanceSet {
		ris := &workloadsv1alpha2.RoleInstanceSet{}
		g.Expect(testutil.K8sClient.Get(testutil.Ctx, risKey, ris)).To(Succeed())
		g.Expect(ris.Status.ObservedGeneration).To(Equal(ris.Generation))
		g.Expect(ris.Status.UpdateRevision).NotTo(BeEmpty())
		return ris
	}

	It("keeps the child in refine mode and its ConfigMap current when the template changes", func() {
		waitForReconciledChild()
		var before *workloadsv1alpha2.RoleInstanceSet
		Eventually(func(g Gomega) { before = getRIS(g) }, timeout, interval).Should(Succeed())

		scaleWorker(2)

		Eventually(func(g Gomega) {
			child := getChild(g)
			g.Expect(child.Spec.Roles[0].Replicas).To(Equal(ptr.To[int32](2)))
			g.Expect(child.GetDiscoveryConfigMode()).To(Equal(constants.RefineDiscoveryConfigMode))
			g.Expect(workerSize(g)).To(Equal(2))
		}, timeout, interval).Should(Succeed())
		Consistently(func(g Gomega) {
			child := getChild(g)
			g.Expect(child.GetDiscoveryConfigMode()).To(Equal(constants.RefineDiscoveryConfigMode))
			g.Expect(child.Annotations).To(HaveKeyWithValue("team", "a"))
		}, 3*time.Second, interval).Should(Succeed())

		// Scaling is not a rollout: the instance template and revision stay as they were.
		Eventually(func(g Gomega) {
			after := getRIS(g)
			g.Expect(after.Spec.Replicas).To(Equal(ptr.To[int32](2)))
			g.Expect(after.Status.UpdateRevision).To(Equal(before.Status.UpdateRevision))
			g.Expect(after.Spec.RoleInstanceTemplate).To(Equal(before.Spec.RoleInstanceTemplate))
		}, timeout, interval).Should(Succeed())
	})

	It("pins a child that has status and no mode to refine", func() {
		waitForReconciledChild()
		var before *workloadsv1alpha2.RoleInstanceSet
		Eventually(func(g Gomega) { before = getRIS(g) }, timeout, interval).Should(Succeed())

		// The state a child is in after v0.7.0 or v0.8.0: reconciled, and without
		// the annotation their set controller removed.
		child := getChild(Default)
		old := child.DeepCopy()
		delete(child.Annotations, constants.DiscoveryConfigModeAnnotationKey)
		Expect(testutil.K8sClient.Patch(testutil.Ctx, child, client.MergeFrom(old))).To(Succeed())

		scaleWorker(2)

		Eventually(func(g Gomega) {
			child := getChild(g)
			g.Expect(child.Spec.Roles[0].Replicas).To(Equal(ptr.To[int32](2)))
			g.Expect(child.GetDiscoveryConfigMode()).To(Equal(constants.RefineDiscoveryConfigMode))
			g.Expect(workerSize(g)).To(Equal(2))
		}, timeout, interval).Should(Succeed())
		Consistently(func(g Gomega) {
			g.Expect(getChild(g).GetDiscoveryConfigMode()).To(Equal(constants.RefineDiscoveryConfigMode))
		}, 3*time.Second, interval).Should(Succeed())

		Eventually(func(g Gomega) {
			after := getRIS(g)
			g.Expect(after.Status.UpdateRevision).To(Equal(before.Status.UpdateRevision))
			g.Expect(after.Spec.RoleInstanceTemplate).To(Equal(before.Spec.RoleInstanceTemplate))
		}, timeout, interval).Should(Succeed())
	})

	It("pins an orphan that derived legacy mode once a set adopts it", func() {
		waitForReconciledChild()

		// A child of v0.7.0 or v0.8.0 whose set was deleted with cascade=orphan:
		// reconciled, without a controller and without the mode annotation.
		set := &workloadsv1alpha2.RoleBasedGroupSet{}
		Expect(testutil.K8sClient.Get(testutil.Ctx, setKey, set)).To(Succeed())
		Expect(testutil.K8sClient.Delete(testutil.Ctx, set)).To(Succeed())
		Eventually(func(g Gomega) {
			err := testutil.K8sClient.Get(testutil.Ctx, setKey, &workloadsv1alpha2.RoleBasedGroupSet{})
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
		}, timeout, interval).Should(Succeed())

		child := getChild(Default)
		old := child.DeepCopy()
		child.OwnerReferences = nil
		delete(child.Annotations, constants.DiscoveryConfigModeAnnotationKey)
		Expect(testutil.K8sClient.Patch(testutil.Ctx, child, client.MergeFrom(old))).To(Succeed())

		// Without a controller it is derived like any RoleBasedGroup that has status.
		Eventually(func(g Gomega) {
			child := getChild(g)
			g.Expect(child.OwnerReferences).To(BeEmpty())
			g.Expect(child.GetDiscoveryConfigMode()).To(Equal(constants.LegacyDiscoveryConfigMode))
		}, timeout, interval).Should(Succeed())

		// A set of the same name adopts the orphan and scales its role.
		role := wrappers.BuildStandaloneRole("worker").WithReplicas(2).Obj()
		adopter := wrappers.BuildBasicRoleBasedGroupSet(setKey.Name, namespace).Obj()
		adopter.Spec.GroupTemplate.Annotations = map[string]string{"team": "a"}
		adopter.Spec.GroupTemplate.Spec.Roles = []workloadsv1alpha2.RoleSpec{role}
		Expect(testutil.K8sClient.Create(testutil.Ctx, adopter)).To(Succeed())

		Eventually(func(g Gomega) {
			child := getChild(g)
			g.Expect(metav1.IsControlledBy(child, adopter)).To(BeTrue())
			g.Expect(child.UID).To(Equal(old.UID))
			g.Expect(child.Spec.Roles[0].Replicas).To(Equal(ptr.To[int32](2)))
			g.Expect(child.GetDiscoveryConfigMode()).To(Equal(constants.RefineDiscoveryConfigMode))
			g.Expect(workerSize(g)).To(Equal(2))
		}, timeout, interval).Should(Succeed())
		Consistently(func(g Gomega) {
			g.Expect(getChild(g).GetDiscoveryConfigMode()).To(Equal(constants.RefineDiscoveryConfigMode))
		}, 3*time.Second, interval).Should(Succeed())
	})
})
