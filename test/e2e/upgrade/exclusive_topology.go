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

package upgrade

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	podutil "k8s.io/kubernetes/pkg/api/v1/pod"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

type exclusiveTopologySnapshot struct {
	ris *workloadsv1alpha2.RoleInstanceSet
	pod *corev1.Pod
}

func createExclusiveTopologyFixture(f *framework.Framework, legacyLabel string) *workloadsv1alpha2.RoleBasedGroup {
	// The upgraded anti-affinity would block the rest of the fixtures on a single node.
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "up-exclusive-"}}
	gomega.Expect(f.Client.Create(f.Ctx, ns)).To(gomega.Succeed())
	ginkgo.DeferCleanup(func() error { return f.Client.Delete(f.Ctx, ns) })

	rbg := wrappersv2.BuildBasicRoleBasedGroup("up-exclusive", ns.Name).
		WithAnnotations(map[string]string{constants.GroupExclusiveTopologyKey: corev1.LabelHostname}).Obj()
	// Let the old scheduler rules match the first Pod so the fixture is running before upgrade.
	rbg.Spec.Roles[0].GetTemplate().Labels = map[string]string{legacyLabel: rbg.GenGroupUniqueKey()}
	gomega.Expect(f.Client.Create(f.Ctx, rbg)).To(gomega.Succeed())
	return rbg
}

func captureExclusiveTopology(g gomega.Gomega, f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup, selectorKey string) exclusiveTopologySnapshot {
	role := &rbg.Spec.Roles[0]
	ris := &workloadsv1alpha2.RoleInstanceSet{}
	key := client.ObjectKey{Namespace: rbg.Namespace, Name: rbg.GetWorkloadName(role)}
	g.Expect(f.Client.Get(f.Ctx, key, ris)).To(gomega.Succeed())
	g.Expect(ris.Spec.RoleInstanceTemplate.Components).To(gomega.HaveLen(1))
	wantAffinity := exclusiveTopologyAffinity(rbg, selectorKey)
	g.Expect(ris.Spec.RoleInstanceTemplate.Components[0].Template.Spec.Affinity).To(gomega.Equal(wantAffinity))
	g.Expect(ris.Status.UpdateRevision).NotTo(gomega.BeEmpty())
	g.Expect(ris.Status.CurrentRevision).To(gomega.Equal(ris.Status.UpdateRevision))

	pods := &corev1.PodList{}
	g.Expect(f.Client.List(f.Ctx, pods, client.InNamespace(rbg.Namespace), client.MatchingLabels{
		constants.GroupNameLabelKey: rbg.Name,
	})).To(gomega.Succeed())
	g.Expect(pods.Items).To(gomega.HaveLen(1))
	pod := &pods.Items[0]
	g.Expect(pod.DeletionTimestamp).To(gomega.BeNil())
	g.Expect(podutil.IsPodReady(pod)).To(gomega.BeTrue())
	g.Expect(pod.Spec.Affinity).To(gomega.Equal(wantAffinity))
	g.Expect(pod.Labels).To(gomega.HaveKeyWithValue(selectorKey, rbg.GenGroupUniqueKey()))
	g.Expect(pod.Labels[appsv1.ControllerRevisionHashLabelKey]).NotTo(gomega.BeEmpty())
	return exclusiveTopologySnapshot{ris: ris, pod: pod}
}

func exclusiveTopologyAffinity(rbg *workloadsv1alpha2.RoleBasedGroup, selectorKey string) *corev1.Affinity {
	group := []string{rbg.GenGroupUniqueKey()}
	return &corev1.Affinity{
		PodAffinity: &corev1.PodAffinity{RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
			TopologyKey: corev1.LabelHostname,
			LabelSelector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
				{Key: selectorKey, Operator: metav1.LabelSelectorOpIn, Values: group},
			}},
		}}},
		PodAntiAffinity: &corev1.PodAntiAffinity{RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
			TopologyKey: corev1.LabelHostname,
			LabelSelector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
				{Key: selectorKey, Operator: metav1.LabelSelectorOpExists},
				{Key: selectorKey, Operator: metav1.LabelSelectorOpNotIn, Values: group},
			}},
		}}},
	}
}
