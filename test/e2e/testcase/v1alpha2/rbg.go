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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	"sigs.k8s.io/rbgs/test/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

func RunRbgControllerTestCases(f *framework.Framework) {
	ginkgo.Describe(
		"rbg controller", func() {

			ginkgo.It(
				"create & delete rbg with standalone roles", func() {
					rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).
						WithRoles(
							[]workloadsv1alpha2.RoleSpec{
								wrappersv2.BuildStandaloneRole("role-1").Obj(),
								wrappersv2.BuildStandaloneRole("role-2").Obj(),
							},
						).Obj()

					f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectRbgV2Equal(rbg)

					// delete rbg
					gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectRbgV2Deleted(rbg)
				},
			)

			ginkgo.It(
				"adopts the workload orphaned by a cascade=orphan deletion on recreate", func() {
					buildRbg := func() *workloadsv1alpha2.RoleBasedGroup {
						return wrappersv2.BuildBasicRoleBasedGroup("e2e-test-orphan", f.Namespace).
							WithRoles(
								[]workloadsv1alpha2.RoleSpec{
									wrappersv2.BuildStandaloneRole("role-1").WithWorkload("apps/v1", "Deployment").Obj(),
								},
							).Obj()
					}
					rbg := buildRbg()

					f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectRbgV2Equal(rbg)

					role := rbg.Spec.Roles[0]
					deployKey := client.ObjectKey{Name: rbg.GetWorkloadName(&role), Namespace: f.Namespace}
					oldDeploy := &appsv1.Deployment{}
					gomega.Expect(f.Client.Get(f.Ctx, deployKey, oldDeploy)).Should(gomega.Succeed())

					// Orphan the workload: the GC strips its owner reference but leaves the
					// healthy, serving workload and its pods running.
					gomega.Expect(
						f.Client.Delete(f.Ctx, rbg, client.PropagationPolicy(metav1.DeletePropagationOrphan)),
					).Should(gomega.Succeed())
					gomega.Eventually(
						func() bool {
							err := f.Client.Get(
								f.Ctx, client.ObjectKey{Name: rbg.Name, Namespace: f.Namespace},
								&workloadsv1alpha2.RoleBasedGroup{},
							)
							return apierrors.IsNotFound(err)
						}, utils.Timeout, utils.Interval,
					).Should(gomega.BeTrue())
					gomega.Eventually(
						func() bool {
							deploy := &appsv1.Deployment{}
							if err := f.Client.Get(f.Ctx, deployKey, deploy); err != nil {
								return false
							}
							return metav1.GetControllerOf(deploy) == nil
						}, utils.Timeout, utils.Interval,
					).Should(gomega.BeTrue(), "timed out waiting for the GC to orphan the workload")

					// Recreate the same manifest: the orphan must be adopted in place and the
					// RBG must become ready off it, without recreating the workload.
					newRbg := buildRbg()
					gomega.Expect(f.Client.Create(f.Ctx, newRbg)).Should(gomega.Succeed())
					f.ExpectRbgV2Equal(newRbg)

					persisted := &workloadsv1alpha2.RoleBasedGroup{}
					gomega.Expect(
						f.Client.Get(f.Ctx, client.ObjectKey{Name: newRbg.Name, Namespace: f.Namespace}, persisted),
					).Should(gomega.Succeed())
					adopted := &appsv1.Deployment{}
					gomega.Expect(f.Client.Get(f.Ctx, deployKey, adopted)).Should(gomega.Succeed())
					gomega.Expect(adopted.UID).To(
						gomega.Equal(oldDeploy.UID), "the orphaned workload must be adopted, not recreated",
					)
					controller := metav1.GetControllerOf(adopted)
					gomega.Expect(controller).ToNot(gomega.BeNil())
					gomega.Expect(controller.UID).To(gomega.Equal(persisted.UID))
				},
			)

			ginkgo.It(
				"rbg with dependency", func() {
					rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).
						WithRoles(
							[]workloadsv1alpha2.RoleSpec{
								wrappersv2.BuildStandaloneRole("role-1").Obj(),
								wrappersv2.BuildStandaloneRole("role-2").
									WithDependencies([]string{"role-1"}).Obj(),
							},
						).Obj()

					f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

					f.ExpectRbgV2Equal(rbg)
				},
			)

			ginkgo.It(
				"rbg with engine runtime existed", func() {
					rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).
						WithRoles(
							[]workloadsv1alpha2.RoleSpec{
								wrappersv2.BuildStandaloneRole("role-1").
									WithEngineRuntime(
										[]workloadsv1alpha2.EngineRuntime{{ProfileName: utils.DefaultEngineRuntimeProfileName}},
									).
									Obj(),
							},
						).Obj()

					f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

					gomega.Expect(utils.CreatePatioRuntime(f.Ctx, f.Client)).Should(gomega.Succeed())

					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

					f.ExpectRbgV2Equal(rbg)
				},
			)

			ginkgo.It(
				"rbg with orphan roles", func() {
					rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles(
						[]workloadsv1alpha2.RoleSpec{
							wrappersv2.BuildStandaloneRole("role-1").Obj(),
							wrappersv2.BuildStandaloneRole("role-2").Obj(),
						},
					).Obj()

					f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectRbgV2Equal(rbg)

					// update role name
					updateRbgV2(
						f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
							rbg.Spec.Roles = []workloadsv1alpha2.RoleSpec{
								wrappersv2.BuildStandaloneRole("sts-1").Obj(),
								wrappersv2.BuildStandaloneRole("sts-2").Obj(),
							}
						},
					)
					f.ExpectRbgV2Equal(rbg)

					f.ExpectWorkloadV2NotExist(rbg, wrappersv2.BuildStandaloneRole("role-1").Obj())
					f.ExpectWorkloadV2NotExist(rbg, wrappersv2.BuildStandaloneRole("role-2").Obj())
				},
			)

			ginkgo.It(
				"coordinated rolling update can start with single-replica roles", func() {
					maxSkew := intstr.FromString("10%")
					rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).
						WithRoles(
							[]workloadsv1alpha2.RoleSpec{
								wrappersv2.BuildStandaloneRole("prefill").Obj(),
								wrappersv2.BuildStandaloneRole("decode").Obj(),
							},
						).Obj()
					policy := &workloadsv1alpha2.CoordinatedPolicy{
						ObjectMeta: metav1.ObjectMeta{
							Name:      rbg.Name,
							Namespace: rbg.Namespace,
						},
						Spec: workloadsv1alpha2.CoordinatedPolicySpec{
							Policies: []workloadsv1alpha2.CoordinatedPolicyRule{
								{
									Name:  "prefill-decode",
									Roles: []string{"prefill", "decode"},
									Strategy: workloadsv1alpha2.CoordinatedPolicyStrategy{
										RollingUpdate: &workloadsv1alpha2.RollingUpdateCoordinationStrategy{
											MaxSkew: &maxSkew,
										},
									},
								},
							},
						},
					}

					f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })
					ginkgo.DeferCleanup(func() {
						gomega.Expect(client.IgnoreNotFound(f.Client.Delete(f.Ctx, policy))).Should(gomega.Succeed())
					})

					gomega.Expect(f.Client.Create(f.Ctx, policy)).Should(gomega.Succeed())
					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
					updateRbgV2(
						f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
							for i := range rbg.Spec.Roles {
								template := rbg.Spec.Roles[i].StandalonePattern.Template
								if template.Labels == nil {
									template.Labels = make(map[string]string)
								}
								template.Labels["rollout-version"] = "v2"
							}
						},
					)

					roleInstanceSets := make([]workloadsv1alpha2.RoleInstanceSet, 0, len(rbg.Spec.Roles))
					gomega.Eventually(
						func() bool {
							roleInstanceSets = roleInstanceSets[:0]
							for i := range rbg.Spec.Roles {
								role := &rbg.Spec.Roles[i]
								ris := &workloadsv1alpha2.RoleInstanceSet{}
								err := f.Client.Get(
									f.Ctx,
									client.ObjectKey{
										Name:      rbg.GetWorkloadName(role),
										Namespace: rbg.Namespace,
									},
									ris,
								)
								if err != nil {
									return false
								}
								roleInstanceSets = append(roleInstanceSets, *ris)
							}
							return true
						}, utils.Timeout, utils.Interval,
					).Should(gomega.BeTrue())

					coordinatedPartitions := 0
					progressAllowed := 0
					for _, ris := range roleInstanceSets {
						if ris.Spec.UpdateStrategy.Partition != nil {
							coordinatedPartitions++
						}
						if roleInstanceSetAllowsRollingProgress(&ris) {
							progressAllowed++
						}
					}
					gomega.Expect(coordinatedPartitions).Should(gomega.Equal(len(rbg.Spec.Roles)))
					gomega.Expect(progressAllowed).Should(gomega.BeNumerically(">", 0))
				},
			)

			ginkgo.It(
				"rbg with exclusive-topology", func() {
					topologyKey := "kubernetes.io/hostname"
					rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).
						WithAnnotations(
							map[string]string{constants.GroupExclusiveTopologyKey: topologyKey},
						).
						WithRoles(
							[]workloadsv1alpha2.RoleSpec{
								wrappersv2.BuildStandaloneRole("router").
									WithReplicas(int32(1)).Obj(),
								wrappersv2.BuildStandaloneRole("prefill").
									WithReplicas(int32(1)).Obj(),
								wrappersv2.BuildLeaderWorkerRole("decode").
									WithWorkload("leaderworkerset.x-k8s.io/v1", "LeaderWorkerSet").
									WithReplicas(int32(1)).Obj(),
							},
						).Obj()

					f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectWorkloadV2ExclusiveTopology(rbg, rbg.Spec.Roles[0], topologyKey)
					f.ExpectWorkloadV2ExclusiveTopology(rbg, rbg.Spec.Roles[1], topologyKey)
					f.ExpectWorkloadV2ExclusiveTopology(rbg, rbg.Spec.Roles[2], topologyKey)
				},
			)
		},
	)
}

// updateRbgV2 updates a v1alpha2 RoleBasedGroup with the given update function.
func updateRbgV2(
	f *framework.Framework,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	updateFunc func(rbg *workloadsv1alpha2.RoleBasedGroup),
) {
	gomega.Eventually(
		func() bool {
			err := f.Client.Get(
				f.Ctx,
				client.ObjectKeyFromObject(rbg),
				rbg,
			)
			if err != nil {
				return false
			}
			updateFunc(rbg)
			return f.Client.Update(f.Ctx, rbg) == nil
		}, utils.Timeout, utils.Interval,
	).Should(gomega.BeTrue())
}

func roleInstanceSetAllowsRollingProgress(ris *workloadsv1alpha2.RoleInstanceSet) bool {
	if ris.Spec.Replicas == nil {
		return false
	}
	if ris.Spec.UpdateStrategy.Partition == nil {
		return true
	}

	partition, err := intstr.GetScaledValueFromIntOrPercent(
		ris.Spec.UpdateStrategy.Partition,
		int(*ris.Spec.Replicas),
		true,
	)
	if err != nil {
		return false
	}
	return int32(partition) < *ris.Spec.Replicas
}

// updateRbgSetV2 updates a v1alpha2 RoleBasedGroupSet with the given update function.
func updateRbgSetV2(
	f *framework.Framework,
	rbgset *workloadsv1alpha2.RoleBasedGroupSet,
	updateFunc func(rs *workloadsv1alpha2.RoleBasedGroupSet),
) {
	gomega.Eventually(
		func() bool {
			err := f.Client.Get(
				f.Ctx,
				client.ObjectKeyFromObject(rbgset),
				rbgset,
			)
			if err != nil {
				return false
			}
			updateFunc(rbgset)
			return f.Client.Update(f.Ctx, rbgset) == nil
		}, utils.Timeout, utils.Interval,
	).Should(gomega.BeTrue())
}
