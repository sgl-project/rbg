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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
	wrappers "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

var _ = Describe("Excluded ordinal admission", func() {
	var namespace string
	BeforeEach(func() {
		namespace = fmt.Sprintf("excluded-admission-%d", time.Now().UnixNano())
		testutil.CreateNamespace(namespace)
	})
	AfterEach(func() { testutil.DeleteNamespace(namespace) })
	It("rejects malformed values with the field and cause, on both create and update", func() {
		role := wrappers.BuildStandaloneRole("worker").WithReplicas(3).Obj()
		group := wrappers.BuildBasicRoleBasedGroup("group", namespace).WithRoles([]workloadsv1alpha2.RoleSpec{role}).Obj()
		Expect(testutil.K8sClient.Create(testutil.Ctx, group)).To(Succeed())
		for _, value := range []string{"-1", "2-1", "2147483648", "1,,3"} {
			created := group.DeepCopy()
			created.Name = "invalid"
			created.ResourceVersion = ""
			created.UID = ""
			created.Spec.Roles[0].Annotations = map[string]string{constants.ExcludedOrdinalsAnnotationKey: value}
			err := testutil.K8sClient.Create(testutil.Ctx, created)
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "%v", err)
			Expect(err.Error()).To(ContainSubstring("spec.roles[0].annotations"))
			updated := getRBG(group.Name, namespace)
			updated.Spec.Roles[0].Annotations = map[string]string{constants.ExcludedOrdinalsAnnotationKey: value}
			err = testutil.K8sClient.Update(testutil.Ctx, updated)
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "%v", err)
		}
		valid := getRBG(group.Name, namespace)
		valid.Spec.Roles[0].Annotations = map[string]string{constants.ExcludedOrdinalsAnnotationKey: "1,3-5,4"}
		Expect(testutil.K8sClient.Update(testutil.Ctx, valid)).To(Succeed())
	})
	It("rejects unsupported roles, RBGSet templates and managed children", func() {
		for _, scenario := range []string{"stateless", "foreign backend", "managed child", "internal key"} {
			role := wrappers.BuildStandaloneRole("worker").WithReplicas(3).Obj()
			role.Annotations = map[string]string{constants.ExcludedOrdinalsAnnotationKey: "1"}
			switch scenario {
			case "stateless":
				role.Annotations[constants.RoleInstancePatternKey] = string(constants.StatelessPattern)
			case "foreign backend":
				role.Annotations[constants.RoleWorkloadTypeAnnotationKey] = constants.StatefulSetWorkloadType
			case "internal key":
				role.Annotations[constants.CommittedSkipOrdinalsAnnotationKey] = "1"
			}
			group := wrappers.BuildBasicRoleBasedGroup("unsupported", namespace).WithRoles([]workloadsv1alpha2.RoleSpec{role}).Obj()
			if scenario == "managed child" {
				group.OwnerReferences = []metav1.OwnerReference{{APIVersion: workloadsv1alpha2.GroupVersion.String(), Kind: "RoleBasedGroupSet", Name: "set", UID: "set-uid"}}
			}
			err := testutil.K8sClient.Create(testutil.Ctx, group)
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "%s: %v", scenario, err)
		}
		role := wrappers.BuildStandaloneRole("worker").WithReplicas(3).Obj()
		role.Annotations = map[string]string{constants.ExcludedOrdinalsAnnotationKey: "1"}
		set := &workloadsv1alpha2.RoleBasedGroupSet{ObjectMeta: metav1.ObjectMeta{Name: "set", Namespace: namespace}, Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{Spec: workloadsv1alpha2.RoleBasedGroupSpec{Roles: []workloadsv1alpha2.RoleSpec{role}}}}}
		err := testutil.K8sClient.Create(testutil.Ctx, set)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "%v", err)
		Expect(err.Error()).To(ContainSubstring("spec.groupTemplate.spec.roles[0]"))
	})
})
