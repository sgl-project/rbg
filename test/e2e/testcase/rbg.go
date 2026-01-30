package testcase

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/utils/ptr"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/scheduler"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	"sigs.k8s.io/rbgs/test/utils"
	"sigs.k8s.io/rbgs/test/wrappers"
)

func RunRbgControllerTestCases(f *framework.Framework) {
	ginkgo.Describe(
		"rbg controller", func() {

			ginkgo.It(
				"create & delete rbg with multi workloads", func() {
					rbg := wrappers.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).
						WithRoles(
							[]workloadsv1alpha1.RoleSpec{
								wrappers.BuildBasicRole("role-deploy").WithWorkload(workloadsv1alpha1.DeploymentWorkloadType).Obj(),
								wrappers.BuildBasicRole("role-sts").WithWorkload(workloadsv1alpha1.StatefulSetWorkloadType).Obj(),
								wrappers.BuildLwsRole("role-lws").Obj(),
							},
						).Obj()

					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectRbgEqual(rbg)

					// delete rbg
					gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectRbgDeleted(rbg)
				},
			)

			ginkgo.It(
				"rbg with dependency", func() {
					rbg := wrappers.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).
						WithRoles(
							[]workloadsv1alpha1.RoleSpec{
								wrappers.BuildBasicRole("role-1").WithWorkload(workloadsv1alpha1.StatefulSetWorkloadType).Obj(),
								wrappers.BuildBasicRole("role-2").WithWorkload(workloadsv1alpha1.StatefulSetWorkloadType).
									WithDependencies([]string{"role-1"}).Obj(),
							},
						).Obj()

					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

					f.ExpectRbgEqual(rbg)
				},
			)

			ginkgo.It(
				"rbg with engine runtime existed", func() {
					rbg := wrappers.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).
						WithRoles(
							[]workloadsv1alpha1.RoleSpec{
								wrappers.BuildBasicRole("role-1").
									WithWorkload(workloadsv1alpha1.StatefulSetWorkloadType).
									WithEngineRuntime(
										[]workloadsv1alpha1.EngineRuntime{{ProfileName: utils.DefaultEngineRuntimeProfileName}},
									).
									Obj(),
							},
						).Obj()

					gomega.Expect(utils.CreatePatioRuntime(f.Ctx, f.Client)).Should(gomega.Succeed())

					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

					f.ExpectRbgEqual(rbg)
				},
			)

			ginkgo.It(
				"rbg with orphan roles", func() {
					rbg := wrappers.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles(
						[]workloadsv1alpha1.RoleSpec{
							wrappers.BuildBasicRole("role-1").Obj(),
							wrappers.BuildBasicRole("role-2").Obj(),
						},
					).Obj()
					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectRbgEqual(rbg)

					// update role name
					utils.UpdateRbg(
						f.Ctx, f.Client, rbg, func(rbg *workloadsv1alpha1.RoleBasedGroup) {
							rbg.Spec.Roles = []workloadsv1alpha1.RoleSpec{
								wrappers.BuildBasicRole("sts-1").Obj(),
								wrappers.BuildBasicRole("sts-2").Obj(),
							}
						},
					)
					f.ExpectRbgEqual(rbg)

					f.ExpectWorkloadNotExist(rbg, wrappers.BuildBasicRole("role-1").Obj())
					f.ExpectWorkloadNotExist(rbg, wrappers.BuildBasicRole("role-2").Obj())
				},
			)

			ginkgo.It(
				"rbg with kube gang scheduling", func() {
					rbg := wrappers.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).
						WithKubeGangScheduling(true).
						WithRoles(
							[]workloadsv1alpha1.RoleSpec{
								{

									Name:     "prefill",
									Replicas: ptr.To(int32(1)),
									RolloutStrategy: &workloadsv1alpha1.RolloutStrategy{
										Type: workloadsv1alpha1.RollingUpdateStrategyType,
									},
									Workload: workloadsv1alpha1.WorkloadSpec{
										APIVersion: "apps/v1",
										Kind:       "StatefulSet",
									},
									TemplateSource: workloadsv1alpha1.TemplateSource{
										Template: ptr.To(wrappers.BuildBasicPodTemplateSpec().Obj()),
									},
								},
								{

									Name:     "decode",
									Replicas: ptr.To(int32(1)),
									RolloutStrategy: &workloadsv1alpha1.RolloutStrategy{
										Type: workloadsv1alpha1.RollingUpdateStrategyType,
									},
									Workload: workloadsv1alpha1.WorkloadSpec{
										APIVersion: "apps/v1",
										Kind:       "StatefulSet",
									},
									TemplateSource: workloadsv1alpha1.TemplateSource{
										Template: ptr.To(wrappers.BuildBasicPodTemplateSpec().Obj()),
									},
								},
							},
						).Obj()

					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

					podGroupLabel := map[string]string{
						scheduler.KubePodGroupLabelKey: rbg.Name,
					}
					f.ExpectWorkloadPodTemplateLabelContains(rbg, rbg.Spec.Roles[0], podGroupLabel)
				},
			)
			ginkgo.It(
				"rbg with volcano gang scheduling", func() {
					template := wrappers.BuildBasicPodTemplateSpec().Obj()
					template.Spec.SchedulerName = "volcano"
					rbg := wrappers.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).
						WithVolcanoGangScheduling("", "default").
						WithRoles(
							[]workloadsv1alpha1.RoleSpec{
								{

									Name:     "prefill",
									Replicas: ptr.To(int32(1)),
									RolloutStrategy: &workloadsv1alpha1.RolloutStrategy{
										Type: workloadsv1alpha1.RollingUpdateStrategyType,
									},
									Workload: workloadsv1alpha1.WorkloadSpec{
										APIVersion: "apps/v1",
										Kind:       "StatefulSet",
									},
									TemplateSource: workloadsv1alpha1.TemplateSource{
										Template: &template,
									},
								},
								{

									Name:     "decode",
									Replicas: ptr.To(int32(1)),
									RolloutStrategy: &workloadsv1alpha1.RolloutStrategy{
										Type: workloadsv1alpha1.RollingUpdateStrategyType,
									},
									Workload: workloadsv1alpha1.WorkloadSpec{
										APIVersion: "apps/v1",
										Kind:       "StatefulSet",
									},
									TemplateSource: workloadsv1alpha1.TemplateSource{
										Template: &template,
									},
								},
							},
						).Obj()

					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

					podGroupAnnotation := map[string]string{
						scheduler.VolcanoPodGroupAnnotationKey: rbg.Name,
					}
					f.ExpectWorkloadPodTemplateAnnotationContains(rbg, rbg.Spec.Roles[0], podGroupAnnotation)
				},
			)

			ginkgo.It(
				"rbg with exclusive-topology", func() {
					topologyKey := "kubernetes.io/hostname"
					rbg := wrappers.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).
						WithAnnotations(
							map[string]string{workloadsv1alpha1.ExclusiveKeyAnnotationKey: topologyKey},
						).
						WithRoles(
							[]workloadsv1alpha1.RoleSpec{
								{
									Name:     "router",
									Replicas: ptr.To(int32(1)),
									RolloutStrategy: &workloadsv1alpha1.RolloutStrategy{
										Type: workloadsv1alpha1.RollingUpdateStrategyType,
									},
									Workload: workloadsv1alpha1.WorkloadSpec{
										APIVersion: "apps/v1",
										Kind:       "Deployment",
									},
									TemplateSource: workloadsv1alpha1.TemplateSource{
										Template: ptr.To(wrappers.BuildBasicPodTemplateSpec().Obj()),
									},
								},
								{
									Name:     "prefill",
									Replicas: ptr.To(int32(1)),
									RolloutStrategy: &workloadsv1alpha1.RolloutStrategy{
										Type: workloadsv1alpha1.RollingUpdateStrategyType,
									},
									Workload: workloadsv1alpha1.WorkloadSpec{
										APIVersion: "apps/v1",
										Kind:       "StatefulSet",
									},
									TemplateSource: workloadsv1alpha1.TemplateSource{
										Template: ptr.To(wrappers.BuildBasicPodTemplateSpec().Obj()),
									},
								},
								{
									Name:     "decode",
									Replicas: ptr.To(int32(1)),
									RolloutStrategy: &workloadsv1alpha1.RolloutStrategy{
										Type: workloadsv1alpha1.RollingUpdateStrategyType,
									},
									Workload: workloadsv1alpha1.WorkloadSpec{
										APIVersion: "leaderworkerset.x-k8s.io/v1",
										Kind:       "LeaderWorkerSet",
									},
									TemplateSource: workloadsv1alpha1.TemplateSource{
										Template: ptr.To(wrappers.BuildBasicPodTemplateSpec().Obj()),
									},
								},
							},
						).Obj()

					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectWorkloadExclusiveTopology(rbg, rbg.Spec.Roles[0], topologyKey)
					f.ExpectWorkloadExclusiveTopology(rbg, rbg.Spec.Roles[1], topologyKey)
					f.ExpectWorkloadExclusiveTopology(rbg, rbg.Spec.Roles[2], topologyKey)
				},
			)
		},
	)

}
