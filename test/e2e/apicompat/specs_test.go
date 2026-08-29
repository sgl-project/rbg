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

package apicompat

import (
	"context"
	"fmt"
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	wrappersv1 "sigs.k8s.io/rbgs/test/wrappers/v1alpha1"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

const (
	controllerClusterRoleName    = "rbgs-controller-role"
	controllerServiceAccountName = "rbgs-controller-sa"
	controllerPodSelector        = "control-plane=rbgs-controller"
)

// workloadType is a deprecated workload type keyed by the annotation form the role
// wrapper takes.
type workloadType struct {
	apiVersion string
	kind       string
}

// deprecatedWorkloads are the three workload types the toggle turns off.
var deprecatedWorkloads = []workloadType{
	{"apps/v1", "Deployment"},
	{"apps/v1", "StatefulSet"},
	{"leaderworkerset.x-k8s.io/v1", "LeaderWorkerSet"},
}

// deprecatedResourceAttr is an RBAC (apiGroup, resource) pair that must not be granted
// to the controller when the toggle is off.
type deprecatedResourceAttr struct {
	group    string
	resource string
}

var deprecatedResourceAttrs = []deprecatedResourceAttr{
	{"apps", "deployments"},
	{"apps", "statefulsets"},
	{"leaderworkerset.x-k8s.io", "leaderworkersets"},
}

// RunDeprecatedDisabledTestCases asserts the behaviour of a release installed with
// controller.deprecatedWorkloadTypes.enabled=false: the manager renders the disabled
// flag and stays healthy, the RBAC for deprecated types is gone (both as written and
// as enforced by SubjectAccessReview), the validating webhook rejects every create and
// update that carries a deprecated workload type across v1alpha2 RBG/RBGSet and the
// v1alpha1 default-StatefulSet path, and the default RoleInstanceSet path still works.
func RunDeprecatedDisabledTestCases(f *framework.Framework) {
	ns := controllerNamespace()

	ginkgo.It("keeps the manager healthy with the disabled flag and no RBAC-forbidden crashes", func() {
		deploy, err := f.Clientset.AppsV1().Deployments(ns).Get(ctx(), managerDeploymentName, metav1.GetOptions{})
		gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
		gomega.Expect(managerHasDisabledFlag(deploy)).Should(gomega.BeTrue(),
			"manager must run with %q", disabledFlag)

		available := false
		for _, c := range deploy.Status.Conditions {
			if c.Type == appsAvailable && c.Status == corev1.ConditionTrue {
				available = true
			}
		}
		gomega.Expect(available).Should(gomega.BeTrue(), "manager Deployment must be Available")

		pods, err := f.Clientset.CoreV1().Pods(ns).List(ctx(), metav1.ListOptions{LabelSelector: controllerPodSelector})
		gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
		gomega.Expect(pods.Items).ShouldNot(gomega.BeEmpty(), "expected at least one controller pod")

		for i := range pods.Items {
			pod := &pods.Items[i]
			for _, cs := range pod.Status.ContainerStatuses {
				// A non-zero restart count here is the signature of the failure this
				// toggle must avoid: RBAC removed but a watch left in place, which
				// crashloops the controller on an RBAC-forbidden list/watch.
				gomega.Expect(cs.RestartCount).Should(gomega.BeNumerically("==", 0),
					"controller container %q in pod %s restarted; check logs for RBAC-forbidden watches", cs.Name, pod.Name)
			}

			logs, err := f.GetPodLogs(ns, pod.Name)
			gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
			for _, forbidden := range []string{
				"deployments.apps is forbidden",
				"statefulsets.apps is forbidden",
				"leaderworkersets.leaderworkerset.x-k8s.io is forbidden",
			} {
				gomega.Expect(logs).ShouldNot(gomega.ContainSubstring(forbidden),
					"controller must not attempt to watch a deprecated type it has no RBAC for (pod %s)", pod.Name)
			}
		}
	})

	ginkgo.It("omits RBAC for the deprecated workload types from the controller ClusterRole", func() {
		role, err := f.Clientset.RbacV1().ClusterRoles().Get(ctx(), controllerClusterRoleName, metav1.GetOptions{})
		gomega.Expect(err).ShouldNot(gomega.HaveOccurred())

		granted := map[string]bool{}
		for _, rule := range role.Rules {
			for _, g := range rule.APIGroups {
				for _, r := range rule.Resources {
					granted[g+"/"+r] = true
				}
			}
		}
		for _, a := range deprecatedResourceAttrs {
			gomega.Expect(granted[a.group+"/"+a.resource]).Should(gomega.BeFalse(),
				"ClusterRole must not grant %s/%s when the toggle is off", a.group, a.resource)
			gomega.Expect(granted[a.group+"/"+a.resource+"/status"]).Should(gomega.BeFalse(),
				"ClusterRole must not grant %s/%s/status when the toggle is off", a.group, a.resource)
		}
	})

	ginkgo.It("denies the controller ServiceAccount create access to deprecated types (SubjectAccessReview)", func() {
		user := fmt.Sprintf("system:serviceaccount:%s:%s", ns, controllerServiceAccountName)

		for _, a := range deprecatedResourceAttrs {
			sar := &authorizationv1.SubjectAccessReview{
				Spec: authorizationv1.SubjectAccessReviewSpec{
					User: user,
					ResourceAttributes: &authorizationv1.ResourceAttributes{
						Verb:     "create",
						Group:    a.group,
						Resource: a.resource,
					},
				},
			}
			got, err := f.Clientset.AuthorizationV1().SubjectAccessReviews().Create(ctx(), sar, metav1.CreateOptions{})
			gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
			gomega.Expect(got.Status.Allowed).Should(gomega.BeFalse(),
				"controller SA must be denied create on %s/%s", a.group, a.resource)
		}

		// Positive control: the SAR itself is meaningful only if a type the controller
		// does own is allowed. RoleInstanceSet is the non-deprecated default.
		sar := &authorizationv1.SubjectAccessReview{
			Spec: authorizationv1.SubjectAccessReviewSpec{
				User: user,
				ResourceAttributes: &authorizationv1.ResourceAttributes{
					Verb:     "create",
					Group:    "workloads.x-k8s.io",
					Resource: "roleinstancesets",
				},
			},
		}
		got, err := f.Clientset.AuthorizationV1().SubjectAccessReviews().Create(ctx(), sar, metav1.CreateOptions{})
		gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
		gomega.Expect(got.Status.Allowed).Should(gomega.BeTrue(),
			"controller SA must still be allowed create on roleinstancesets")
	})

	ginkgo.It("rejects RoleBasedGroup creation for every deprecated workload type", func() {
		for _, w := range deprecatedWorkloads {
			name := fmt.Sprintf("dep-rbg-create-%s", strings.ToLower(w.kind))
			rbg := wrappersv2.BuildBasicRoleBasedGroup(name, f.Namespace).
				WithRoles([]workloadsv1alpha2.RoleSpec{
					wrappersv2.BuildStandaloneRole("role-1").WithWorkload(w.apiVersion, w.kind).Obj(),
				}).Obj()

			err := f.Client.Create(ctx(), rbg)
			gomega.Expect(containsRejection(err)).Should(gomega.BeTrue(),
				"create of RBG with %s/%s should be rejected, got: %v", w.apiVersion, w.kind, err)
		}
	})

	ginkgo.It("rejects a RoleBasedGroup update that introduces a deprecated workload type", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("dep-rbg-update", f.Namespace).
			WithRoles([]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildStandaloneRole("role-1").Obj(), // RoleInstanceSet default
			}).Obj()
		gomega.Expect(f.Client.Create(ctx(), rbg)).Should(gomega.Succeed(),
			"a RoleInstanceSet RBG must be admitted")

		fetched := &workloadsv1alpha2.RoleBasedGroup{}
		err := updateWithConflictRetry(ctx(), f.Client, client.ObjectKeyFromObject(rbg), fetched, func() {
			setRoleWorkload(&fetched.Spec.Roles[0], constants.DeploymentWorkloadType)
		})
		gomega.Expect(containsRejection(err)).Should(gomega.BeTrue(),
			"update introducing a Deployment workload should be rejected, got: %v", err)
	})

	ginkgo.It("rejects RoleBasedGroupSet creation for every deprecated workload type", func() {
		for _, w := range deprecatedWorkloads {
			name := fmt.Sprintf("dep-rbgset-create-%s", strings.ToLower(w.kind))
			rbgset := wrappersv2.BuildBasicRoleBasedGroupSet(name, f.Namespace).Obj()
			rbgset.Spec.GroupTemplate.Spec.Roles = []workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildStandaloneRole("role-1").WithWorkload(w.apiVersion, w.kind).Obj(),
			}

			err := f.Client.Create(ctx(), rbgset)
			gomega.Expect(containsRejection(err)).Should(gomega.BeTrue(),
				"create of RBGSet with %s/%s should be rejected, got: %v", w.apiVersion, w.kind, err)
		}
	})

	ginkgo.It("rejects a RoleBasedGroupSet update that introduces a deprecated workload type", func() {
		rbgset := wrappersv2.BuildBasicRoleBasedGroupSet("dep-rbgset-update", f.Namespace).Obj()
		gomega.Expect(f.Client.Create(ctx(), rbgset)).Should(gomega.Succeed(),
			"a RoleInstanceSet RBGSet must be admitted")

		fetched := &workloadsv1alpha2.RoleBasedGroupSet{}
		err := updateWithConflictRetry(ctx(), f.Client, client.ObjectKeyFromObject(rbgset), fetched, func() {
			setRoleWorkload(&fetched.Spec.GroupTemplate.Spec.Roles[0], constants.StatefulSetWorkloadType)
		})
		gomega.Expect(containsRejection(err)).Should(gomega.BeTrue(),
			"update introducing a StatefulSet workload should be rejected, got: %v", err)
	})

	ginkgo.It("rejects a v1alpha1 RoleBasedGroup that relies on the default StatefulSet workload", func() {
		// v1alpha1 defaults spec.roles[].workload to apps/v1 StatefulSet, and the
		// conversion webhook records that in the role-workload-type annotation the
		// validator reads. So a v1alpha1 object carrying that default is refused here.
		rbg := wrappersv1.BuildBasicRoleBasedGroup("dep-v1alpha1-default", f.Namespace).Obj()
		err := f.Client.Create(ctx(), rbg)
		gomega.Expect(containsRejection(err)).Should(gomega.BeTrue(),
			"v1alpha1 RBG defaulting to StatefulSet should be rejected, got: %v", err)
	})

	ginkgo.It("still admits a RoleBasedGroup that uses the default RoleInstanceSet workload", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("dep-roleinstanceset-ok", f.Namespace).
			WithRoles([]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildStandaloneRole("role-1").Obj(),
			}).Obj()
		gomega.Expect(f.Client.Create(ctx(), rbg)).Should(gomega.Succeed(),
			"a RoleInstanceSet RBG must be admitted when deprecated types are disabled")
	})
}

// appsAvailable is the Deployment Available condition type.
const appsAvailable = "Available"

// setRoleWorkload sets the role-workload-type annotation used by GetWorkloadType.
func setRoleWorkload(role *workloadsv1alpha2.RoleSpec, workloadType string) {
	if role.Annotations == nil {
		role.Annotations = map[string]string{}
	}
	role.Annotations[constants.RoleWorkloadTypeAnnotationKey] = workloadType
}

// updateWithConflictRetry re-Gets obj, applies mutate, and Updates it, retrying on
// optimistic-concurrency (409 conflict) errors. The controller writes status on
// freshly created RBG/RBGSet objects, so a plain Get->Update races it and surfaces a
// conflict instead of reaching the validating webhook. A conflict is transient and
// unrelated to whether the update is admissible, so we re-Get and retry; the final
// non-conflict Update error (typically the validator's rejection, or nil on success)
// is returned for the caller to assert on.
func updateWithConflictRetry(
	ctx context.Context,
	c client.Client,
	key client.ObjectKey,
	obj client.Object,
	mutate func(),
) error {
	const maxAttempts = 10
	var lastErr error
	for i := 0; i < maxAttempts; i++ {
		if err := c.Get(ctx, key, obj); err != nil {
			return err
		}
		mutate()
		lastErr = c.Update(ctx, obj)
		if lastErr == nil || !apierrors.IsConflict(lastErr) {
			return lastErr
		}
	}
	return fmt.Errorf("update %s kept hitting optimistic-concurrency conflicts after %d attempts: %w",
		key, maxAttempts, lastErr)
}
