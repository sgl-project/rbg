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

package v1alpha2

import (
	"fmt"
	"math"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	"sigs.k8s.io/rbgs/test/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

func RunCoordinatedPolicyTestCases(f *framework.Framework) {
	ginkgo.Describe("coordinated policy", func() {

		ginkgo.It("rolling update maxSkew constrains update progress difference between roles", func() {
			template := buildPodTemplateWithStartupDelay(5, 5)

			rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles(
				[]workloadsv1alpha2.RoleSpec{
					wrappersv2.BuildStandaloneRole("role-a").
						WithReplicas(4).
						WithTemplate(&template).
						WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
							MaxUnavailable: ptr.To(intstr.FromInt32(2)),
						}).Obj(),
					wrappersv2.BuildStandaloneRole("role-b").
						WithReplicas(4).
						WithTemplate(&template).
						WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
							MaxUnavailable: ptr.To(intstr.FromInt32(2)),
						}).Obj(),
				}).Obj()

			// Create CoordinatedPolicy with same name as RBG
			cpolicy := &workloadsv1alpha2.CoordinatedPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbg.Name,
					Namespace: f.Namespace,
				},
				Spec: workloadsv1alpha2.CoordinatedPolicySpec{
					Policies: []workloadsv1alpha2.CoordinatedPolicyRule{
						{
							Name:  "coordinated-update",
							Roles: []string{"role-a", "role-b"},
							Strategy: workloadsv1alpha2.CoordinatedPolicyStrategy{
								RollingUpdate: &workloadsv1alpha2.RollingUpdateCoordinationStrategy{
									MaxSkew: ptr.To(intstr.FromInt32(1)),
								},
							},
						},
					},
				},
			}

			f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

			gomega.Expect(f.Client.Create(f.Ctx, cpolicy)).Should(gomega.Succeed())
			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
			f.ExpectRbgV2Equal(rbg)

			time.Sleep(2 * time.Second)

			// Trigger rolling update on both roles
			updateTemplate := buildPodTemplateWithStartupDelay(3, 3)
			updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
				rbg.Spec.Roles[0].StandalonePattern.Template = &updateTemplate
				rbg.Spec.Roles[1].StandalonePattern.Template = &updateTemplate
			})

			// During the rolling update, verify maxSkew constraint is respected
			gomega.Consistently(func() bool {
				roleAUpdated := countUpdatedRoleInstances(f, rbg, "role-a")
				roleBUpdated := countUpdatedRoleInstances(f, rbg, "role-b")
				skew := int(math.Abs(float64(roleAUpdated - roleBUpdated)))
				// maxSkew=1 means the difference should not exceed 1
				return skew <= 1
			}, 30, 2).Should(gomega.BeTrue(),
				"update progress skew between role-a and role-b should not exceed maxSkew=1")

			// Wait for full update to complete
			f.ExpectRbgV2Equal(rbg)

			// Verify all instances are updated
			gomega.Expect(countUpdatedRoleInstances(f, rbg, "role-a")).Should(gomega.Equal(4))
			gomega.Expect(countUpdatedRoleInstances(f, rbg, "role-b")).Should(gomega.Equal(4))

			// Verify stability after coordinated update
			finalPodUIDs := getPodUIDsForRole(f, rbg, "role-a")
			gomega.Consistently(func() map[string]types.UID {
				return getPodUIDsForRole(f, rbg, "role-a")
			}, 15, 2).Should(gomega.Equal(finalPodUIDs),
				"role-a pods should be stable after coordinated update completes")
		})

		ginkgo.It("partition constrains update boundary across coordinated roles", func() {
			rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles(
				[]workloadsv1alpha2.RoleSpec{
					wrappersv2.BuildStandaloneRole("role-a").
						WithReplicas(4).
						WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
							MaxUnavailable: ptr.To(intstr.FromInt32(2)),
						}).Obj(),
					wrappersv2.BuildStandaloneRole("role-b").
						WithReplicas(4).
						WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
							MaxUnavailable: ptr.To(intstr.FromInt32(2)),
						}).Obj(),
				}).Obj()

			// Create CoordinatedPolicy with partition=2 (only ordinal >= 2 should update)
			cpolicy := &workloadsv1alpha2.CoordinatedPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbg.Name,
					Namespace: f.Namespace,
				},
				Spec: workloadsv1alpha2.CoordinatedPolicySpec{
					Policies: []workloadsv1alpha2.CoordinatedPolicyRule{
						{
							Name:  "coordinated-partition",
							Roles: []string{"role-a", "role-b"},
							Strategy: workloadsv1alpha2.CoordinatedPolicyStrategy{
								RollingUpdate: &workloadsv1alpha2.RollingUpdateCoordinationStrategy{
									Partition: ptr.To(intstr.FromInt32(2)),
								},
							},
						},
					},
				},
			}

			f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

			gomega.Expect(f.Client.Create(f.Ctx, cpolicy)).Should(gomega.Succeed())
			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
			f.ExpectRbgV2Equal(rbg)

			// Record initial revisions
			initialRevisionA := getRoleInstanceRevision(f, rbg, "role-a", 0)
			initialRevisionB := getRoleInstanceRevision(f, rbg, "role-b", 0)
			gomega.Expect(initialRevisionA).ShouldNot(gomega.BeEmpty())
			gomega.Expect(initialRevisionB).ShouldNot(gomega.BeEmpty())

			time.Sleep(2 * time.Second)

			// Trigger template update on both roles
			updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
				rbg.Spec.Roles[0].StandalonePattern.Template.Labels = map[string]string{"version": "v2"}
				rbg.Spec.Roles[1].StandalonePattern.Template.Labels = map[string]string{"version": "v2"}
			})

			// Wait for partial update (ordinals >= 2 should update)
			gomega.Eventually(func() bool {
				revA2 := getRoleInstanceRevision(f, rbg, "role-a", 2)
				revA3 := getRoleInstanceRevision(f, rbg, "role-a", 3)
				revB2 := getRoleInstanceRevision(f, rbg, "role-b", 2)
				revB3 := getRoleInstanceRevision(f, rbg, "role-b", 3)
				return revA2 != initialRevisionA && revA3 != initialRevisionA &&
					revB2 != initialRevisionB && revB3 != initialRevisionB
			}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
				"ordinals 2 and 3 should be updated for both roles")

			// Verify ordinals 0 and 1 are still on old revision (partition=2 blocks them)
			gomega.Consistently(func() bool {
				revA0 := getRoleInstanceRevision(f, rbg, "role-a", 0)
				revA1 := getRoleInstanceRevision(f, rbg, "role-a", 1)
				revB0 := getRoleInstanceRevision(f, rbg, "role-b", 0)
				revB1 := getRoleInstanceRevision(f, rbg, "role-b", 1)
				return revA0 == initialRevisionA && revA1 == initialRevisionA &&
					revB0 == initialRevisionB && revB1 == initialRevisionB
			}, 15, 2).Should(gomega.BeTrue(),
				"ordinals 0 and 1 should remain on old revision while partition=2")

			// Lower partition to 0 to allow full update
			gomega.Eventually(func() error {
				cp := &workloadsv1alpha2.CoordinatedPolicy{}
				if err := f.Client.Get(f.Ctx, client.ObjectKeyFromObject(cpolicy), cp); err != nil {
					return err
				}
				cp.Spec.Policies[0].Strategy.RollingUpdate.Partition = ptr.To(intstr.FromInt32(0))
				return f.Client.Update(f.Ctx, cp)
			}, utils.Timeout, utils.Interval).Should(gomega.Succeed())

			// Touch RBG Spec to trigger reconciliation (controller does not watch
			// CoordinatedPolicy, and RBGPredicate only triggers on Spec changes).
			// Changing MaxUnavailable is a Spec change but does not create a new revision.
			updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
				rbg.Spec.Roles[0].RolloutStrategy.RollingUpdate.MaxUnavailable = ptr.To(intstr.FromInt32(4))
				rbg.Spec.Roles[1].RolloutStrategy.RollingUpdate.MaxUnavailable = ptr.To(intstr.FromInt32(4))
			})

			// Wait for full update to complete
			f.ExpectRbgV2Equal(rbg)

			// Verify all instances now have new revision.
			// Use Eventually because the rolling update may still be in progress.
			var newRevisionA, newRevisionB string
			gomega.Eventually(func() bool {
				newRevisionA = getRoleInstanceRevision(f, rbg, "role-a", 2)
				if newRevisionA == "" {
					return false
				}
				for i := 0; i < 4; i++ {
					if getRoleInstanceRevision(f, rbg, "role-a", i) != newRevisionA {
						return false
					}
				}
				newRevisionB = getRoleInstanceRevision(f, rbg, "role-b", 2)
				if newRevisionB == "" {
					return false
				}
				for i := 0; i < 4; i++ {
					if getRoleInstanceRevision(f, rbg, "role-b", i) != newRevisionB {
						return false
					}
				}
				return true
			}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
				"all instances should converge to the new revision after partition is lowered")

			// Verify stability after full coordinated update
			allPodUIDs := make(map[string]types.UID)
			for k, v := range getPodUIDsForRole(f, rbg, "role-a") {
				allPodUIDs[k] = v
			}
			for k, v := range getPodUIDsForRole(f, rbg, "role-b") {
				allPodUIDs[k] = v
			}
			gomega.Consistently(func() map[string]types.UID {
				result := make(map[string]types.UID)
				for k, v := range getPodUIDsForRole(f, rbg, "role-a") {
					result[k] = v
				}
				for k, v := range getPodUIDsForRole(f, rbg, "role-b") {
					result[k] = v
				}
				return result
			}, 15, 2).Should(gomega.Equal(allPodUIDs),
				"all pods should be stable after coordinated partition update completes")
		})

		ginkgo.It("scaling coordination with OrderReady progression constrains scaling skew", func() {
			template := buildPodTemplateWithStartupDelay(5, 5)

			rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles(
				[]workloadsv1alpha2.RoleSpec{
					wrappersv2.BuildStandaloneRole("role-a").
						WithReplicas(1).
						WithTemplate(&template).
						Obj(),
					wrappersv2.BuildStandaloneRole("role-b").
						WithReplicas(1).
						WithTemplate(&template).
						Obj(),
				}).Obj()

			// Create CoordinatedPolicy with scaling coordination
			cpolicy := &workloadsv1alpha2.CoordinatedPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbg.Name,
					Namespace: f.Namespace,
				},
				Spec: workloadsv1alpha2.CoordinatedPolicySpec{
					Policies: []workloadsv1alpha2.CoordinatedPolicyRule{
						{
							Name:  "coordinated-scaling",
							Roles: []string{"role-a", "role-b"},
							Strategy: workloadsv1alpha2.CoordinatedPolicyStrategy{
								Scaling: &workloadsv1alpha2.ScalingCoordinationStrategy{
									MaxSkew:     ptr.To(intstr.FromString("50%")),
									Progression: workloadsv1alpha2.OrderReadyProgression,
								},
							},
						},
					},
				},
			}

			f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

			gomega.Expect(f.Client.Create(f.Ctx, cpolicy)).Should(gomega.Succeed())
			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
			f.ExpectRbgV2Equal(rbg)

			// Scale both roles from 1 to 3
			updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
				rbg.Spec.Roles[0].Replicas = ptr.To(int32(3))
				rbg.Spec.Roles[1].Replicas = ptr.To(int32(3))
			})

			// During scaling, verify skew constraint is respected
			// Use RoleInstanceSet.Status.ReadyReplicas (the signal the controller uses)
			// instead of counting RoleInstance conditions to avoid reconcile latency flakiness.
			gomega.Consistently(func() bool {
				roleAReady := getRoleInstanceSetReadyReplicas(f, rbg, "role-a")
				roleBReady := getRoleInstanceSetReadyReplicas(f, rbg, "role-b")
				skew := int(math.Abs(float64(roleAReady - roleBReady)))
				return skew <= 1
			}, 30, 2).Should(gomega.BeTrue(),
				"ready replicas skew between role-a and role-b should not exceed maxSkew=1 during scaling")

			// Wait for scaling to complete
			f.ExpectRbgV2Equal(rbg)

			// Verify both roles have 3 ready instances
			gomega.Expect(getRoleInstanceSetReadyReplicas(f, rbg, "role-a")).Should(gomega.Equal(int32(3)))
			gomega.Expect(getRoleInstanceSetReadyReplicas(f, rbg, "role-b")).Should(gomega.Equal(int32(3)))

			// Verify stability after coordinated scaling
			finalPodUIDs := getPodUIDsForRole(f, rbg, "role-a")
			gomega.Consistently(func() map[string]types.UID {
				return getPodUIDsForRole(f, rbg, "role-a")
			}, 15, 2).Should(gomega.Equal(finalPodUIDs),
				"pods should be stable after coordinated scaling completes")
		})
	})
}

// countUpdatedRoleInstances returns the number of RoleInstances that have been updated
// to the latest revision (updateRevision matches the RoleInstanceSet's UpdateRevision).
func countUpdatedRoleInstances(f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup, roleName string) int {
	// Get the RoleInstanceSet to find the target UpdateRevision
	ris := &workloadsv1alpha2.RoleInstanceSet{}
	err := f.Client.Get(f.Ctx, client.ObjectKey{
		Name:      fmt.Sprintf("%s-%s", rbg.Name, roleName),
		Namespace: rbg.Namespace,
	}, ris)
	if err != nil {
		return 0
	}
	targetRevision := ris.Status.UpdateRevision
	if targetRevision == "" {
		return 0
	}

	instances := listRoleInstances(f, rbg, roleName)
	count := 0
	for _, ri := range instances {
		if ri.Labels["controller-revision-hash"] == targetRevision {
			count++
		}
	}
	return count
}
