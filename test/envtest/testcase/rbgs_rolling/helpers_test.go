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

package rbgs_rolling

import (
	"fmt"
	"slices"
	"strconv"
	"time"

	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

const (
	timeout  = time.Minute
	interval = 250 * time.Millisecond

	updatedImage = "nginx:rbgs-rolling-updated"
)

// The Rolling condition rendered as "<status>=<reason>", so one assertion pins both that the
// rollout is over and why.
const (
	rolloutComplete   = "False=RolloutComplete"
	rolloutInProgress = "True=RolloutInProgress"
)

var descendantKinds = []schema.GroupVersionKind{
	workloadsv1alpha2.GroupVersion.WithKind("RoleInstanceSet"),
	workloadsv1alpha2.GroupVersion.WithKind("RoleInstance"),
	corev1.SchemeGroupVersion.WithKind("Pod"),
	corev1.SchemeGroupVersion.WithKind("Service"),
	corev1.SchemeGroupVersion.WithKind("ConfigMap"),
	appsv1.SchemeGroupVersion.WithKind("ControllerRevision"),
}

// envtest has no garbage collector; dependents must disappear before their owner can be replaced.
func startForegroundDeletionReaper() {
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-testutil.Ctx.Done():
				return
			case <-ticker.C:
				reapTerminatingChildren()
			}
		}
	}()
}

func reapTerminatingChildren() {
	children := &workloadsv1alpha2.RoleBasedGroupList{}
	if err := testutil.K8sClient.List(testutil.Ctx, children); err != nil {
		return
	}
	for i := range children.Items {
		child := &children.Items[i]
		if child.DeletionTimestamp.IsZero() || !slices.Contains(child.Finalizers, metav1.FinalizerDeleteDependents) {
			continue
		}
		if err := deleteDescendants(child.Namespace, map[types.UID]bool{child.UID: true}); err != nil {
			continue
		}
		_ = clearForegroundFinalizer(child)
	}
}

func deleteDescendants(namespace string, owners map[types.UID]bool) error {
	for _, gvk := range descendantKinds {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk)
		if err := testutil.K8sClient.List(testutil.Ctx, list, client.InNamespace(namespace)); err != nil {
			return err
		}
		for i := range list.Items {
			obj := &list.Items[i]
			if !ownedBy(obj, owners) {
				continue
			}
			uid := obj.GetUID()
			if err := testutil.K8sClient.Delete(testutil.Ctx, obj,
				client.PropagationPolicy(metav1.DeletePropagationForeground), client.GracePeriodSeconds(0),
				client.Preconditions{UID: &uid}); client.IgnoreNotFound(err) != nil {
				return err
			}
			if err := deleteDescendants(namespace, map[types.UID]bool{uid: true}); err != nil {
				return err
			}
			if err := clearForegroundFinalizer(obj); err != nil {
				return err
			}
			if err := testutil.K8sClient.Get(testutil.Ctx, client.ObjectKeyFromObject(obj), obj); !apierrors.IsNotFound(err) {
				return fmt.Errorf("waiting for dependent %s/%s to disappear: %v", gvk.Kind, obj.GetName(), err)
			}
		}
	}
	return nil
}

func clearForegroundFinalizer(obj client.Object) error {
	uid := obj.GetUID()
	if err := testutil.K8sClient.Get(testutil.Ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
		return client.IgnoreNotFound(err)
	}
	if obj.GetUID() != uid || !slices.Contains(obj.GetFinalizers(), metav1.FinalizerDeleteDependents) {
		return nil
	}
	base := obj.DeepCopyObject().(client.Object)
	obj.SetFinalizers(slices.DeleteFunc(obj.GetFinalizers(), func(finalizer string) bool {
		return finalizer == metav1.FinalizerDeleteDependents
	}))
	return testutil.K8sClient.Patch(testutil.Ctx, obj, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
}

func ownedBy(obj client.Object, owners map[types.UID]bool) bool {
	for _, ref := range obj.GetOwnerReferences() {
		if owners[ref.UID] {
			return true
		}
	}
	return false
}

// buildSet returns a RoleBasedGroupSet whose group template runs one standalone role with
// roleReplicas groups per role. Zero role replicas make a child Ready without any Pod, which
// is what the specs that only assert group level pacing use.
func buildSet(
	name, ns string, replicas, roleReplicas int32, strategy *workloadsv1alpha2.GroupSetRolloutStrategy,
) *workloadsv1alpha2.RoleBasedGroupSet {
	set := wrappersv2.BuildBasicRoleBasedGroupSet(name, ns).
		WithReplicas(replicas).
		WithRolloutStrategy(strategy).
		Obj()
	for i := range set.Spec.GroupTemplate.Spec.Roles {
		set.Spec.GroupTemplate.Spec.Roles[i].Replicas = ptr.To(roleReplicas)
	}
	return set
}

func buildRollingSet(
	name, ns string, replicas, roleReplicas, maxUnavailable, maxSurge, partition int32,
) *workloadsv1alpha2.RoleBasedGroupSet {
	return buildSet(name, ns, replicas, roleReplicas,
		wrappersv2.BuildRecreateRolloutStrategy(maxUnavailable, maxSurge, partition))
}

func childName(setName string, ordinal int) string {
	return fmt.Sprintf("%s-%d", setName, ordinal)
}

// updateSetTemplate applies mutate to the stored group template.
func updateSetTemplate(ns, name string, mutate func(*workloadsv1alpha2.RoleBasedGroupSet)) {
	EventuallyWithOffset(1, func() error {
		set, err := fetchSet(ns, name)
		if err != nil {
			return err
		}
		mutate(set)
		return testutil.K8sClient.Update(testutil.Ctx, set)
	}, timeout, interval).Should(Succeed())
}

func setTemplateImage(set *workloadsv1alpha2.RoleBasedGroupSet, image string) {
	for i := range set.Spec.GroupTemplate.Spec.Roles {
		set.Spec.GroupTemplate.Spec.Roles[i].Pattern.StandalonePattern.Template.Spec.Containers[0].Image = image
	}
}

func childImage(child *workloadsv1alpha2.RoleBasedGroup) string {
	return child.Spec.Roles[0].Pattern.StandalonePattern.Template.Spec.Containers[0].Image
}

func fetchSet(ns, name string) (*workloadsv1alpha2.RoleBasedGroupSet, error) {
	set := &workloadsv1alpha2.RoleBasedGroupSet{}
	err := testutil.K8sClient.Get(testutil.Ctx, client.ObjectKey{Namespace: ns, Name: name}, set)
	return set, err
}

func mustFetchSet(ns, name string) *workloadsv1alpha2.RoleBasedGroupSet {
	set, err := fetchSet(ns, name)
	ExpectWithOffset(1, err).ShouldNot(HaveOccurred())
	return set
}

// fetchChildren returns the live children of a set keyed by ordinal, skipping the ones that
// are already being deleted.
func fetchChildren(ns, setName string) map[int]*workloadsv1alpha2.RoleBasedGroup {
	list := &workloadsv1alpha2.RoleBasedGroupList{}
	if err := testutil.K8sClient.List(testutil.Ctx, list, client.InNamespace(ns),
		client.MatchingLabels{constants.GroupSetNameLabelKey: setName}); err != nil {
		return nil
	}
	children := make(map[int]*workloadsv1alpha2.RoleBasedGroup, len(list.Items))
	for i := range list.Items {
		child := &list.Items[i]
		if !child.DeletionTimestamp.IsZero() {
			continue
		}
		ordinal, err := strconv.Atoi(child.Labels[constants.GroupSetIndexLabelKey])
		if err != nil {
			continue
		}
		children[ordinal] = child
	}
	return children
}

func rollingStateOf(ns, setName string) (string, error) {
	set, err := fetchSet(ns, setName)
	if err != nil {
		return "", err
	}
	condition := meta.FindStatusCondition(set.Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupSetRolling))
	if condition == nil {
		return "", nil
	}
	return fmt.Sprintf("%s=%s", condition.Status, condition.Reason), nil
}

// markChildPodsReady reports every pending Pod of a child RoleBasedGroup as Running and
// Ready, standing in for the kubelet that envtest does not run.
func markChildPodsReady(ns, name string) error {
	pods := &corev1.PodList{}
	if err := testutil.K8sClient.List(testutil.Ctx, pods, client.InNamespace(ns),
		client.MatchingLabels{constants.GroupNameLabelKey: name}); err != nil {
		return err
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if !pod.DeletionTimestamp.IsZero() || pod.Status.Phase == corev1.PodRunning {
			continue
		}
		fresh := &corev1.Pod{}
		if err := testutil.K8sClient.Get(testutil.Ctx, client.ObjectKeyFromObject(pod), fresh); err != nil {
			return err
		}
		testutil.SetPodRunningAndReady(fresh)
		if err := testutil.K8sClient.Status().Update(testutil.Ctx, fresh); err != nil {
			return err
		}
	}
	return nil
}

// waitChildReady drives one child to the Ready condition its controller computes, which is
// what the rollout budget measures.
func waitChildReady(ns, name string) {
	EventuallyWithOffset(1, func() error {
		if err := markChildPodsReady(ns, name); err != nil {
			return err
		}
		child := &workloadsv1alpha2.RoleBasedGroup{}
		if err := testutil.K8sClient.Get(testutil.Ctx, client.ObjectKey{Namespace: ns, Name: name}, child); err != nil {
			return err
		}
		if child.Status.ObservedGeneration < child.Generation {
			return fmt.Errorf("RoleBasedGroup %s is at observedGeneration %d, want %d",
				name, child.Status.ObservedGeneration, child.Generation)
		}
		if !meta.IsStatusConditionTrue(child.Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupReady)) {
			return fmt.Errorf("RoleBasedGroup %s is not Ready", name)
		}
		return nil
	}, timeout, interval).Should(Succeed())
}

// waitRollout keeps every child's Pods ready until the Rolling condition reports wantState,
// and returns the set as it looks at that point.
func waitRollout(ns, setName, wantState string) *workloadsv1alpha2.RoleBasedGroupSet {
	var final *workloadsv1alpha2.RoleBasedGroupSet
	EventuallyWithOffset(1, func() error {
		for _, child := range fetchChildren(ns, setName) {
			if err := markChildPodsReady(ns, child.Name); err != nil {
				return err
			}
		}
		set, err := fetchSet(ns, setName)
		if err != nil {
			return err
		}
		state, err := rollingStateOf(ns, setName)
		if err != nil {
			return err
		}
		if state != wantState {
			return fmt.Errorf("Rolling condition is %q, want %q", state, wantState)
		}
		if set.Status.CurrentRevision == "" {
			return fmt.Errorf("currentRevision is not recorded yet")
		}
		if set.Status.ReadyReplicas != *set.Spec.Replicas {
			return fmt.Errorf("readyReplicas is %d, want %d", set.Status.ReadyReplicas, *set.Spec.Replicas)
		}
		final = set
		return nil
	}, timeout, interval).Should(Succeed())
	return final
}

// waitUpdateRevision waits for the set to record an update revision other than oldRevision
// and returns it.
func waitUpdateRevision(ns, setName, oldRevision string) string {
	EventuallyWithOffset(1, func() (string, error) {
		set, err := fetchSet(ns, setName)
		if err != nil {
			return "", err
		}
		return set.Status.UpdateRevision, nil
	}, timeout, interval).Should(And(Not(BeEmpty()), Not(Equal(oldRevision))))
	return mustFetchSet(ns, setName).Status.UpdateRevision
}

// waitChildRevision waits until an ordinal is served by a child carrying revision.
func waitChildRevision(ns, setName string, ordinal int, revision string) *workloadsv1alpha2.RoleBasedGroup {
	var child *workloadsv1alpha2.RoleBasedGroup
	EventuallyWithOffset(1, func() error {
		found := fetchChildren(ns, setName)[ordinal]
		if found == nil {
			return fmt.Errorf("ordinal %d does not exist", ordinal)
		}
		if got := found.Labels[constants.GroupSetRevisionLabelKey]; got != revision {
			return fmt.Errorf("ordinal %d is at revision %q, want %q", ordinal, got, revision)
		}
		child = found
		return nil
	}, timeout, interval).Should(Succeed())
	return child
}

// deleteChild removes a child the way the rollout does, so the reaper collects it together
// with its dependents, and returns the UID it had.
func deleteChild(ns, setName string, ordinal int) types.UID {
	child := fetchChildren(ns, setName)[ordinal]
	ExpectWithOffset(1, child).ShouldNot(BeNil())
	ExpectWithOffset(1, testutil.K8sClient.Delete(testutil.Ctx, child,
		client.PropagationPolicy(metav1.DeletePropagationForeground))).Should(Succeed())
	return child.UID
}
