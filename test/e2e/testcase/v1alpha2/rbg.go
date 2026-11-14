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
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
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
		"rbg controller", ginkgo.Label("lifecycle"), func() {

			ginkgo.It(
				"create & delete rbg with standalone roles", ginkgo.Label("smoke"), func() {
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
									wrappersv2.BuildStandaloneRole("role-1").Obj(),
								},
							).Obj()
					}
					rbg := buildRbg()

					f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectRbgV2Equal(rbg)

					role := rbg.Spec.Roles[0]
					risKey := client.ObjectKey{Name: rbg.GetWorkloadName(&role), Namespace: f.Namespace}
					oldRIS := &workloadsv1alpha2.RoleInstanceSet{}
					gomega.Expect(f.Client.Get(f.Ctx, risKey, oldRIS)).Should(gomega.Succeed())

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
							ris := &workloadsv1alpha2.RoleInstanceSet{}
							if err := f.Client.Get(f.Ctx, risKey, ris); err != nil {
								return false
							}
							return metav1.GetControllerOf(ris) == nil
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
					adopted := &workloadsv1alpha2.RoleInstanceSet{}
					gomega.Expect(f.Client.Get(f.Ctx, risKey, adopted)).Should(gomega.Succeed())
					gomega.Expect(adopted.UID).To(
						gomega.Equal(oldRIS.UID), "the orphaned workload must be adopted, not recreated",
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

			ginkgo.It("schedules exclusive groups together and blocks other groups on the same node", func() {
				topologyKey := corev1.LabelHostname
				group := wrappersv2.BuildBasicRoleBasedGroup("exclusive-a", f.Namespace).
					WithAnnotations(map[string]string{constants.GroupExclusiveTopologyKey: topologyKey}).
					WithRoles([]workloadsv1alpha2.RoleSpec{
						wrappersv2.BuildStandaloneRole("frontend").Obj(),
						wrappersv2.BuildStandaloneRole("backend").Obj(),
					}).Obj()
				f.RegisterDebugFn(func() { dumpDebugInfo(f, group) })
				gomega.Expect(f.Client.Create(f.Ctx, group)).To(gomega.Succeed())

				var nodeName string
				gomega.Eventually(func() error {
					pods := &corev1.PodList{}
					if err := f.Client.List(f.Ctx, pods, client.InNamespace(f.Namespace),
						client.MatchingLabels{constants.GroupNameLabelKey: group.Name}); err != nil {
						return err
					}
					if len(pods.Items) != 2 {
						return fmt.Errorf("expected two pods, got %d", len(pods.Items))
					}
					nodeName = pods.Items[0].Spec.NodeName
					if nodeName == "" || pods.Items[1].Spec.NodeName != nodeName {
						return fmt.Errorf("pods are not scheduled on the same node")
					}
					for _, pod := range pods.Items {
						if pod.Labels[constants.GroupUIDLabelKey] != group.GenGroupUniqueKey() {
							return fmt.Errorf("pod %s does not have the group identity label", pod.Name)
						}
					}
					return nil
				}, utils.Timeout, utils.Interval).Should(gomega.Succeed())

				node := &corev1.Node{}
				gomega.Expect(f.Client.Get(f.Ctx, client.ObjectKey{Name: nodeName}, node)).To(gomega.Succeed())
				hostname := node.Labels[topologyKey]
				gomega.Expect(hostname).NotTo(gomega.BeEmpty())
				for _, tc := range []struct {
					name      string
					exclusive bool
				}{
					{name: "ordinary-b"},
					{name: "exclusive-c", exclusive: true},
				} {
					other := wrappersv2.BuildBasicRoleBasedGroup(tc.name, f.Namespace).Obj()
					if tc.exclusive {
						other.Annotations = map[string]string{constants.GroupExclusiveTopologyKey: topologyKey}
					}
					other.Spec.Roles[0].GetTemplate().Spec.NodeSelector = map[string]string{topologyKey: hostname}
					gomega.Expect(f.Client.Create(f.Ctx, other)).To(gomega.Succeed())
					expectGroupBlockedByAntiAffinity(f, other.Name)
				}
			})

			ginkgo.It("blocks an exclusive group from a node occupied by an ordinary group", func() {
				ordinary := wrappersv2.BuildBasicRoleBasedGroup("ordinary-first", f.Namespace).Obj()
				gomega.Expect(f.Client.Create(f.Ctx, ordinary)).To(gomega.Succeed())

				var nodeName string
				gomega.Eventually(func() error {
					pods := &corev1.PodList{}
					if err := f.Client.List(f.Ctx, pods, client.InNamespace(f.Namespace),
						client.MatchingLabels{constants.GroupNameLabelKey: ordinary.Name}); err != nil {
						return err
					}
					if len(pods.Items) != 1 || pods.Items[0].Spec.NodeName == "" {
						return fmt.Errorf("expected one scheduled pod for %s", ordinary.Name)
					}
					nodeName = pods.Items[0].Spec.NodeName
					return nil
				}, utils.Timeout, utils.Interval).Should(gomega.Succeed())

				node := &corev1.Node{}
				gomega.Expect(f.Client.Get(f.Ctx, client.ObjectKey{Name: nodeName}, node)).To(gomega.Succeed())
				hostname := node.Labels[corev1.LabelHostname]
				gomega.Expect(hostname).NotTo(gomega.BeEmpty())
				exclusive := wrappersv2.BuildBasicRoleBasedGroup("exclusive-second", f.Namespace).
					WithAnnotations(map[string]string{constants.GroupExclusiveTopologyKey: corev1.LabelHostname}).Obj()
				exclusive.Spec.Roles[0].GetTemplate().Spec.NodeSelector = map[string]string{corev1.LabelHostname: hostname}
				gomega.Expect(f.Client.Create(f.Ctx, exclusive)).To(gomega.Succeed())
				expectGroupBlockedByAntiAffinity(f, exclusive.Name)
			})

			ginkgo.It("uses a disabled role as a same-group affinity target", func() {
				disabled := wrappersv2.BuildStandaloneRole("disabled").Obj()
				disabled.GetTemplate().Annotations = map[string]string{constants.DisableExclusiveKeyAnnotationKey: "true"}
				enabled := wrappersv2.BuildStandaloneRole("enabled").WithDependencies([]string{"disabled"}).Obj()
				group := wrappersv2.BuildBasicRoleBasedGroup("disabled-anchor", f.Namespace).
					WithAnnotations(map[string]string{constants.GroupExclusiveTopologyKey: corev1.LabelHostname}).
					WithRoles([]workloadsv1alpha2.RoleSpec{disabled, enabled}).Obj()
				gomega.Expect(f.Client.Create(f.Ctx, group)).To(gomega.Succeed())

				gomega.Eventually(func() error {
					pods := &corev1.PodList{}
					if err := f.Client.List(f.Ctx, pods, client.InNamespace(f.Namespace),
						client.MatchingLabels{constants.GroupNameLabelKey: group.Name}); err != nil {
						return err
					}
					if len(pods.Items) != 2 || pods.Items[0].Spec.NodeName == "" ||
						pods.Items[0].Spec.NodeName != pods.Items[1].Spec.NodeName {
						return fmt.Errorf("expected both roles on the disabled role's node")
					}
					for _, pod := range pods.Items {
						if pod.Labels[constants.GroupUIDLabelKey] != group.GenGroupUniqueKey() {
							return fmt.Errorf("pod %s is missing the group identity label", pod.Name)
						}
						if pod.Labels[constants.RoleNameLabelKey] == disabled.Name && pod.Spec.Affinity != nil {
							return fmt.Errorf("disabled role unexpectedly has injected affinity")
						}
					}
					return nil
				}, utils.Timeout, utils.Interval).Should(gomega.Succeed())
			})

			ginkgo.It("reconciles exclusive topology annotation changes on an existing group", func() {
				group := wrappersv2.BuildBasicRoleBasedGroup("toggle-exclusive", f.Namespace).Obj()
				gomega.Expect(f.Client.Create(f.Ctx, group)).To(gomega.Succeed())
				groupPods := func() (*corev1.PodList, error) {
					pods := &corev1.PodList{}
					err := f.Client.List(f.Ctx, pods, client.InNamespace(f.Namespace),
						client.MatchingLabels{constants.GroupNameLabelKey: group.Name})
					return pods, err
				}
				gomega.Eventually(func() bool {
					pods, err := groupPods()
					return err == nil && len(pods.Items) == 1 && pods.Items[0].Status.Phase == corev1.PodRunning
				}, utils.Timeout, utils.Interval).Should(gomega.BeTrue())

				setTopology := func(value string) {
					updateRbgV2(f, group, func(current *workloadsv1alpha2.RoleBasedGroup) {
						if current.Annotations == nil {
							current.Annotations = map[string]string{}
						}
						if value == "" {
							delete(current.Annotations, constants.GroupExclusiveTopologyKey)
						} else {
							current.Annotations[constants.GroupExclusiveTopologyKey] = value
						}
					})
				}

				setTopology(corev1.LabelHostname)
				gomega.Eventually(func() bool {
					pods, err := groupPods()
					if err != nil || len(pods.Items) != 1 || pods.Items[0].Spec.Affinity == nil ||
						pods.Items[0].Spec.Affinity.PodAffinity == nil {
						return false
					}
					terms := pods.Items[0].Spec.Affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution
					return len(terms) == 1 && terms[0].LabelSelector != nil &&
						len(terms[0].LabelSelector.MatchExpressions) == 1 &&
						terms[0].LabelSelector.MatchExpressions[0].Key == constants.GroupUIDLabelKey
				}, utils.Timeout, utils.Interval).Should(gomega.BeTrue())

				setTopology("")
				gomega.Eventually(func() bool {
					pods, err := groupPods()
					return err == nil && len(pods.Items) == 1 && pods.Items[0].Spec.Affinity == nil
				}, utils.Timeout, utils.Interval).Should(gomega.BeTrue())
			})
		},
	)
}

func expectGroupBlockedByAntiAffinity(f *framework.Framework, groupName string) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func() error {
		pods := &corev1.PodList{}
		if err := f.Client.List(f.Ctx, pods, client.InNamespace(f.Namespace),
			client.MatchingLabels{constants.GroupNameLabelKey: groupName}); err != nil {
			return err
		}
		if len(pods.Items) != 1 || pods.Items[0].Spec.NodeName != "" {
			return fmt.Errorf("expected one unscheduled pod for %s", groupName)
		}
		events := &corev1.EventList{}
		if err := f.Client.List(f.Ctx, events, client.InNamespace(f.Namespace)); err != nil {
			return err
		}
		for _, event := range events.Items {
			if event.InvolvedObject.UID == pods.Items[0].UID && event.Reason == "FailedScheduling" &&
				strings.Contains(event.Message, "anti-affinity") {
				return nil
			}
		}
		return fmt.Errorf("no anti-affinity scheduling event for %s", pods.Items[0].Name)
	}, utils.Timeout, utils.Interval).Should(gomega.Succeed())
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
