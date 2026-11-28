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

package webhook

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
	wrappers "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

var _ = Describe("Role instance pattern change warnings", func() {
	var (
		namespace string
		warnings  *testutil.GateWarningRecorder
		c         client.Client
	)
	BeforeEach(func() {
		namespace = fmt.Sprintf("pattern-warning-%d", time.Now().UnixNano())
		testutil.CreateNamespace(namespace)
		warnings = &testutil.GateWarningRecorder{}
		cfg := rest.CopyConfig(testutil.Cfg)
		cfg.WarningHandler = warnings
		var err error
		c, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
		Expect(err).NotTo(HaveOccurred())
	})
	AfterEach(func() { testutil.DeleteNamespace(namespace) })

	setPattern := func(role *workloadsv1alpha2.RoleSpec, pattern constants.InstancePatternType) {
		if role.Annotations == nil {
			role.Annotations = map[string]string{}
		}
		role.Annotations[constants.RoleInstancePatternKey] = string(pattern)
	}

	It("warns when an existing role switches to Stateless and stores the switch", func() {
		role := wrappers.BuildStandaloneRole("worker").WithReplicas(2).Obj()
		group := wrappers.BuildBasicRoleBasedGroup("group", namespace).WithRoles([]workloadsv1alpha2.RoleSpec{role}).Obj()
		Expect(c.Create(testutil.Ctx, group)).To(Succeed())

		By("making the default pattern explicit")
		updated := getRBG(group.Name, namespace)
		setPattern(&updated.Spec.Roles[0], constants.StatefulPattern)
		Expect(c.Update(testutil.Ctx, updated)).To(Succeed())
		Expect(warnings.All()).To(BeEmpty())

		By("switching the role to Stateless")
		updated = getRBG(group.Name, namespace)
		setPattern(&updated.Spec.Roles[0], constants.StatelessPattern)
		Expect(c.Update(testutil.Ctx, updated)).To(Succeed())
		Expect(warnings.All()).To(ConsistOf(ContainSubstring(
			`spec.roles[0] (role "worker"): changing ` + constants.RoleInstancePatternKey +
				` from Stateful to Stateless deletes all RoleInstances of this role at once`)))
		Expect(getRBG(group.Name, namespace).Spec.Roles[0].Annotations).
			To(HaveKeyWithValue(constants.RoleInstancePatternKey, string(constants.StatelessPattern)))
	})

	It("warns when a RoleBasedGroupSet template switches a role", func() {
		role := wrappers.BuildStandaloneRole("worker").WithReplicas(2).Obj()
		setPattern(&role, constants.StatelessPattern)
		set := &workloadsv1alpha2.RoleBasedGroupSet{
			ObjectMeta: metav1.ObjectMeta{Name: "set", Namespace: namespace},
			Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
				GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
					Spec: workloadsv1alpha2.RoleBasedGroupSpec{Roles: []workloadsv1alpha2.RoleSpec{role}},
				},
			},
		}
		Expect(c.Create(testutil.Ctx, set)).To(Succeed())
		Expect(warnings.All()).To(BeEmpty())

		updated := &workloadsv1alpha2.RoleBasedGroupSet{}
		Expect(c.Get(testutil.Ctx, client.ObjectKeyFromObject(set), updated)).To(Succeed())
		delete(updated.Spec.GroupTemplate.Spec.Roles[0].Annotations, constants.RoleInstancePatternKey)
		Expect(c.Update(testutil.Ctx, updated)).To(Succeed())
		Expect(warnings.All()).To(ConsistOf(ContainSubstring(
			`spec.groupTemplate.spec.roles[0] (role "worker"): changing ` + constants.RoleInstancePatternKey +
				` from Stateless to Stateful deletes all RoleInstances of this role at once in each child RoleBasedGroup the set updates in place`)))
	})
})
