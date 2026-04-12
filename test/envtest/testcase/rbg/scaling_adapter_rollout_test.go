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
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/scale"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

var _ = Describe("ScalingAdapter Rollout-Aware Scaling", func() {
	const (
		timeout  = time.Second * 30
		interval = time.Millisecond * 250
	)

	var testNs string

	BeforeEach(func() {
		testNs = fmt.Sprintf("test-sa-rollout-%d", time.Now().UnixNano())
		testutil.CreateNamespace(testNs)
	})

	AfterEach(func() {
		testutil.DeleteNamespace(testNs)
	})

	// patchSTSStatus patches a StatefulSet's status to simulate rollout state.
	// In envtest, no STS controller runs, so we must manually set status fields.
	// This must be called repeatedly because the RBG controller patches the STS
	// spec on each reconcile, incrementing Generation and invalidating our
	// ObservedGeneration.
	patchSTSStatus := func(ns, stsName string, replicas, readyReplicas, updatedReplicas int32) {
		Eventually(func() error {
			sts := &appsv1.StatefulSet{}
			if err := testutil.K8sClient.Get(testutil.Ctx,
				types.NamespacedName{Name: stsName, Namespace: ns}, sts); err != nil {
				return err
			}
			patch := client.MergeFrom(sts.DeepCopy())
			sts.Status.Replicas = replicas
			sts.Status.ReadyReplicas = readyReplicas
			sts.Status.UpdatedReplicas = updatedReplicas
			sts.Status.ObservedGeneration = sts.Generation
			return testutil.K8sClient.Status().Patch(testutil.Ctx, sts, patch)
		}, timeout, interval).Should(Succeed(), "should patch STS %s status", stsName)
	}

	// scaleRBGSA scales the RBGSA via the scale subresource (simulating HPA).
	scaleRBGSA := func(ns, rbgsaName string, replicas int32) {
		rbgsa := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{}
		Eventually(func() error {
			return testutil.K8sClient.Get(testutil.Ctx,
				types.NamespacedName{Name: rbgsaName, Namespace: ns}, rbgsa)
		}, timeout, interval).Should(Succeed(), "RBGSA %s should exist", rbgsaName)

		// Wait for adapter to be bound
		Eventually(func() bool {
			if err := testutil.K8sClient.Get(testutil.Ctx,
				types.NamespacedName{Name: rbgsaName, Namespace: ns}, rbgsa); err != nil {
				return false
			}
			return rbgsa.Status.Phase == "Bound"
		}, timeout, interval).Should(BeTrue(), "RBGSA should be in Bound phase")

		scaleObj := &autoscalingv1.Scale{}
		Expect(testutil.K8sClient.SubResource("scale").Get(testutil.Ctx, rbgsa, scaleObj)).
			Should(Succeed())
		scaleObj.Spec.Replicas = replicas
		Expect(testutil.K8sClient.SubResource("scale").Update(testutil.Ctx, rbgsa,
			client.WithSubResourceBody(scaleObj))).Should(Succeed())
	}

	// getRBGRoleReplicas returns the current replicas for a role in the RBG spec.
	getRBGRoleReplicas := func(ns, rbgName, roleName string) func() int32 {
		return func() int32 {
			rbg := &workloadsv1alpha2.RoleBasedGroup{}
			if err := testutil.K8sClient.Get(testutil.Ctx,
				types.NamespacedName{Name: rbgName, Namespace: ns}, rbg); err != nil {
				return -1
			}
			role, err := rbg.GetRole(roleName)
			if err != nil {
				return -1
			}
			if role.Replicas == nil {
				return -1
			}
			return *role.Replicas
		}
	}

	// hasScaleDownDeferredCondition checks if the RBGSA has the ScaleDownDeferred condition.
	hasScaleDownDeferredCondition := func(ns, rbgsaName string) func() bool {
		return func() bool {
			rbgsa := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{}
			if err := testutil.K8sClient.Get(testutil.Ctx,
				types.NamespacedName{Name: rbgsaName, Namespace: ns}, rbgsa); err != nil {
				return false
			}
			cond := apimeta.FindStatusCondition(rbgsa.Status.Conditions,
				workloadsv1alpha2.AdapterConditionScaleDownDeferred)
			return cond != nil && cond.Status == metav1.ConditionTrue
		}
	}

	Context("StatefulSet with DeferDuringRollout policy", func() {
		var (
			rbgName, stsName, rbgsaName string
		)

		BeforeEach(func() {
			rbgName = "test-defer-sts"
			stsName = rbgName + "-worker"
			rbgsaName = scale.GenerateScalingAdapterName(rbgName, "worker")

			role := wrappersv2.BuildStandaloneRole("worker").
				WithWorkload("apps/v1", "StatefulSet").
				WithReplicas(3).
				WithScalingAdapter(true).
				Obj()
			role.ScalingAdapter.ScaleDownPolicy = ptr.To(workloadsv1alpha2.ScaleDownPolicyDeferDuringRollout)

			rbg := wrappersv2.BuildBasicRoleBasedGroup(rbgName, testNs).
				WithRoles([]workloadsv1alpha2.RoleSpec{role}).Obj()

			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).Should(Succeed())

			// Wait for STS to be created by the RBG controller
			Eventually(func() error {
				return testutil.K8sClient.Get(testutil.Ctx,
					types.NamespacedName{Name: stsName, Namespace: testNs},
					&appsv1.StatefulSet{})
			}, timeout, interval).Should(Succeed())
		})

		It("should defer scale-down during rollout", func() {
			// Simulate rollout in progress
			patchSTSStatus(testNs, stsName, 3, 3, 1)

			// Wait for RBG status to reflect the rollout
			Eventually(func() int32 {
				rbg := &workloadsv1alpha2.RoleBasedGroup{}
				if err := testutil.K8sClient.Get(testutil.Ctx,
					types.NamespacedName{Name: rbgName, Namespace: testNs}, rbg); err != nil {
					return -1
				}
				status, found := rbg.GetRoleStatus("worker")
				if !found {
					return -1
				}
				return status.UpdatedReplicas
			}, timeout, interval).Should(Equal(int32(1)))

			// Scale down via RBGSA (simulating HPA)
			scaleRBGSA(testNs, rbgsaName, 1)

			// Verify: RBG replicas should NOT change (scale-down deferred)
			Consistently(getRBGRoleReplicas(testNs, rbgName, "worker"),
				5*time.Second, interval).Should(Equal(int32(3)),
				"RBG role replicas should stay at 3 while rollout is in progress")

			// Verify: ScaleDownDeferred condition should be set
			Eventually(hasScaleDownDeferredCondition(testNs, rbgsaName),
				timeout, interval).Should(BeTrue(),
				"RBGSA should have ScaleDownDeferred condition")
		})

		It("should proceed with scale-down after rollout completes", func() {
			// Simulate rollout in progress
			patchSTSStatus(testNs, stsName, 3, 3, 1)

			// Wait for RBG status to reflect rollout
			Eventually(func() int32 {
				rbg := &workloadsv1alpha2.RoleBasedGroup{}
				if err := testutil.K8sClient.Get(testutil.Ctx,
					types.NamespacedName{Name: rbgName, Namespace: testNs}, rbg); err != nil {
					return -1
				}
				status, found := rbg.GetRoleStatus("worker")
				if !found {
					return -1
				}
				return status.UpdatedReplicas
			}, timeout, interval).Should(Equal(int32(1)))

			// Scale down (will be deferred)
			scaleRBGSA(testNs, rbgsaName, 1)

			// Confirm it's deferred
			Eventually(hasScaleDownDeferredCondition(testNs, rbgsaName),
				timeout, interval).Should(BeTrue())

			// Simulate rollout completion by patching the RBG status directly.
			// In envtest, no real STS controller runs to maintain ObservedGeneration,
			// so the RBG controller can't construct RoleStatuses from STS status.
			// Patching RBG status directly triggers the RBGSA controller's RBG watch.
			Eventually(func() error {
				rbg := &workloadsv1alpha2.RoleBasedGroup{}
				if err := testutil.K8sClient.Get(testutil.Ctx,
					types.NamespacedName{Name: rbgName, Namespace: testNs}, rbg); err != nil {
					return err
				}
				patch := client.MergeFrom(rbg.DeepCopy())
				rbg.Status.RoleStatuses = []workloadsv1alpha2.RoleStatus{
					{Name: "worker", Replicas: 3, ReadyReplicas: 3, UpdatedReplicas: 3},
				}
				return testutil.K8sClient.Status().Patch(testutil.Ctx, rbg, patch)
			}, timeout, interval).Should(Succeed())

			// Verify: RBG replicas should eventually change to 1.
			// The RBGSA controller's RBG watch fires on RoleStatuses change,
			// then the gating check passes (UpdatedReplicas == Replicas).
			Eventually(getRBGRoleReplicas(testNs, rbgName, "worker"),
				timeout, interval).Should(Equal(int32(1)),
				"RBG role replicas should change to 1 after rollout completes")
		})

		It("should allow scale-up during rollout", func() {
			// Simulate rollout in progress
			patchSTSStatus(testNs, stsName, 3, 3, 1)

			// Wait for RBG status to reflect rollout
			Eventually(func() int32 {
				rbg := &workloadsv1alpha2.RoleBasedGroup{}
				if err := testutil.K8sClient.Get(testutil.Ctx,
					types.NamespacedName{Name: rbgName, Namespace: testNs}, rbg); err != nil {
					return -1
				}
				status, found := rbg.GetRoleStatus("worker")
				if !found {
					return -1
				}
				return status.UpdatedReplicas
			}, timeout, interval).Should(Equal(int32(1)))

			// Scale UP (should proceed immediately, not gated)
			scaleRBGSA(testNs, rbgsaName, 5)

			Eventually(getRBGRoleReplicas(testNs, rbgName, "worker"),
				timeout, interval).Should(Equal(int32(5)),
				"Scale-up should proceed even during rollout")
		})
	})

	Context("StatefulSet with Unrestricted policy", func() {
		It("should allow scale-down during rollout", func() {
			rbgName := "test-unrestricted"
			stsName := rbgName + "-worker"
			rbgsaName := scale.GenerateScalingAdapterName(rbgName, "worker")

			role := wrappersv2.BuildStandaloneRole("worker").
				WithWorkload("apps/v1", "StatefulSet").
				WithReplicas(3).
				WithScalingAdapter(true).
				Obj()
			role.ScalingAdapter.ScaleDownPolicy = ptr.To(workloadsv1alpha2.ScaleDownPolicyUnrestricted)

			rbg := wrappersv2.BuildBasicRoleBasedGroup(rbgName, testNs).
				WithRoles([]workloadsv1alpha2.RoleSpec{role}).Obj()

			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).Should(Succeed())

			// Wait for STS and simulate rollout in progress
			Eventually(func() error {
				return testutil.K8sClient.Get(testutil.Ctx,
					types.NamespacedName{Name: stsName, Namespace: testNs},
					&appsv1.StatefulSet{})
			}, timeout, interval).Should(Succeed())

			patchSTSStatus(testNs, stsName, 3, 3, 1)

			// Scale down (should proceed — Unrestricted policy)
			scaleRBGSA(testNs, rbgsaName, 1)

			Eventually(getRBGRoleReplicas(testNs, rbgName, "worker"),
				timeout, interval).Should(Equal(int32(1)),
				"Scale-down should proceed with Unrestricted policy")
		})
	})

	Context("Deployment with DeferDuringRollout policy", func() {
		It("should allow scale-down during rollout (Deployments are not partition-based)", func() {
			rbgName := "test-deploy-defer"
			rbgsaName := scale.GenerateScalingAdapterName(rbgName, "worker")

			role := wrappersv2.BuildStandaloneRole("worker").
				WithWorkload("apps/v1", "Deployment").
				WithReplicas(3).
				WithScalingAdapter(true).
				Obj()
			role.ScalingAdapter.ScaleDownPolicy = ptr.To(workloadsv1alpha2.ScaleDownPolicyDeferDuringRollout)

			rbg := wrappersv2.BuildBasicRoleBasedGroup(rbgName, testNs).
				WithRoles([]workloadsv1alpha2.RoleSpec{role}).Obj()

			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).Should(Succeed())

			// Wait for Deployment to be created
			deployName := rbgName + "-worker"
			deploy := &appsv1.Deployment{}
			Eventually(func() error {
				return testutil.K8sClient.Get(testutil.Ctx,
					types.NamespacedName{Name: deployName, Namespace: testNs}, deploy)
			}, timeout, interval).Should(Succeed())

			// Simulate rollout in progress on Deployment
			patch := client.MergeFrom(deploy.DeepCopy())
			deploy.Status.Replicas = 3
			deploy.Status.ReadyReplicas = 3
			deploy.Status.UpdatedReplicas = 1
			deploy.Status.ObservedGeneration = deploy.Generation
			Expect(testutil.K8sClient.Status().Patch(testutil.Ctx, deploy, patch)).Should(Succeed())

			// Scale down (should proceed — Deployments are not partition-based)
			scaleRBGSA(testNs, rbgsaName, 1)

			Eventually(getRBGRoleReplicas(testNs, rbgName, "worker"),
				timeout, interval).Should(Equal(int32(1)),
				"Scale-down should proceed for Deployment even with DeferDuringRollout policy")
		})
	})
})
