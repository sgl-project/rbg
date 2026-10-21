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

package stateful

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	podutil "k8s.io/kubernetes/pkg/api/v1/pod"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/reconciler/roleinstanceset/statefulmode"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
)

var _ = Describe("Stateful RoleInstanceSet rollout recovery", func() {
	const (
		timeout  = 25 * time.Second
		interval = 250 * time.Millisecond
	)

	var testNs string

	BeforeEach(func() {
		testNs = fmt.Sprintf("test-stateful-rollout-%d", time.Now().UnixNano())
		testutil.CreateNamespace(testNs)
	})

	AfterEach(func() {
		testutil.DeleteNamespace(testNs)
	})

	It("replaces a stably unhealthy rollout target through the controller requeue path", func() {
		setName := "stale-rollout"
		setKey := types.NamespacedName{Name: setName, Namespace: testNs}

		set := newRolloutRecoverySet(setName, testNs)
		Expect(testutil.K8sClient.Create(testutil.Ctx, set)).To(Succeed())

		instanceKey := types.NamespacedName{Name: fmt.Sprintf("%s-0", setName), Namespace: testNs}
		initialInstance := &workloadsv1alpha2.RoleInstance{}
		Eventually(func() error {
			return testutil.K8sClient.Get(testutil.Ctx, instanceKey, initialInstance)
		}, timeout, interval).Should(Succeed(), "initial RoleInstance should be created")

		podKey := types.NamespacedName{Name: fmt.Sprintf("%s-0-worker-0", setName), Namespace: testNs}
		Eventually(func() error {
			return setPodReady(podKey, true)
		}, timeout, interval).Should(Succeed(), "initial pod should become ready")
		Eventually(func() (int32, error) {
			created := &workloadsv1alpha2.RoleInstanceSet{}
			if err := testutil.K8sClient.Get(testutil.Ctx, setKey, created); err != nil {
				return 0, err
			}
			return created.Status.ReadyReplicas, nil
		}, timeout, interval).Should(Equal(int32(1)), "initial instance should become ready")

		Eventually(func() error {
			return setPodReady(podKey, false)
		}, timeout, interval).Should(Succeed(), "initial pod should become unhealthy")
		Eventually(func() (int32, error) {
			created := &workloadsv1alpha2.RoleInstanceSet{}
			if err := testutil.K8sClient.Get(testutil.Ctx, setKey, created); err != nil {
				return 0, err
			}
			return created.Status.ReadyReplicas, nil
		}, timeout, interval).Should(Equal(int32(0)), "controller should observe the unhealthy instance")

		current := &workloadsv1alpha2.RoleInstanceSet{}
		Expect(testutil.K8sClient.Get(testutil.Ctx, setKey, current)).To(Succeed())
		current.Spec.RoleInstanceTemplate.Components[0].Template.Spec.Containers[0].Image = "nginx:2.0"
		Expect(testutil.K8sClient.Update(testutil.Ctx, current)).To(Succeed())

		var updateRevision string
		Eventually(func() (string, error) {
			created := &workloadsv1alpha2.RoleInstanceSet{}
			if err := testutil.K8sClient.Get(testutil.Ctx, setKey, created); err != nil {
				return "", err
			}
			if created.Status.UpdateRevision == "" || created.Status.UpdateRevision == created.Status.CurrentRevision {
				return "", fmt.Errorf("update revision %q has not diverged from current revision %q", created.Status.UpdateRevision, created.Status.CurrentRevision)
			}
			updateRevision = created.Status.UpdateRevision
			return updateRevision, nil
		}, timeout, interval).ShouldNot(BeEmpty(), "update revision should be created")

		oldUID := initialInstance.UID
		Consistently(func() (types.UID, error) {
			instance := &workloadsv1alpha2.RoleInstance{}
			if err := testutil.K8sClient.Get(testutil.Ctx, instanceKey, instance); err != nil {
				return "", err
			}
			return instance.UID, nil
		}, 8*time.Second, interval).Should(Equal(oldUID), "replacement should wait for the stable-unhealthy window")

		Eventually(func() (bool, error) {
			instance := &workloadsv1alpha2.RoleInstance{}
			if err := testutil.K8sClient.Get(testutil.Ctx, instanceKey, instance); err != nil {
				return false, err
			}
			return instance.UID != oldUID && instance.Labels["controller-revision-hash"] == updateRevision, nil
		}, timeout, interval).Should(BeTrue(), "replacement RoleInstance should be created at the update revision")

		finalInstance := &workloadsv1alpha2.RoleInstance{}
		Expect(testutil.K8sClient.Get(testutil.Ctx, instanceKey, finalInstance)).To(Succeed())
		Expect(finalInstance.UID).NotTo(Equal(oldUID))
		Expect(finalInstance.Labels["controller-revision-hash"]).To(Equal(updateRevision))
	})

	It("recovers early-rollback surge protection from persisted status", func() {
		set := newRolloutRecoverySet("persisted-rollback", testNs)
		set.Spec.UpdateStrategy.MaxSurge = ptr.To(intstr.FromInt32(1))
		set.Spec.UpdateStrategy.MaxUnavailable = ptr.To(intstr.FromInt32(0))
		set.Spec.UpdateStrategy.Paused = true
		Expect(testutil.K8sClient.Create(testutil.Ctx, set)).To(Succeed())
		setKey := types.NamespacedName{Name: set.Name, Namespace: testNs}
		baseKey := types.NamespacedName{Name: set.Name + "-0", Namespace: testNs}
		base := &workloadsv1alpha2.RoleInstance{}
		Eventually(func() error {
			return testutil.K8sClient.Get(testutil.Ctx, baseKey, base)
		}, timeout, interval).Should(Succeed())

		// Seed only API state, as seen by a controller starting after the stale
		// base was replaced. No process-local replacement signal is involved.
		writer := statefulmode.NewRealStatefulInstanceSetStatusUpdater(testutil.K8sClient, nil)
		Eventually(func() error {
			if err := testutil.K8sClient.Get(testutil.Ctx, setKey, set); err != nil {
				return err
			}
			if set.Status.UpdateRevision == "" || set.Status.CurrentRevision != set.Status.UpdateRevision {
				return fmt.Errorf("initial revisions have not converged")
			}
			return writer.PersistRolloutReplacementState(testutil.Ctx, set, &workloadsv1alpha2.RolloutReplacementState{
				UpdateRevision: set.Status.UpdateRevision,
				Replicas:       1,
			})
		}, timeout, interval).Should(Succeed())

		surge := base.DeepCopy()
		surge.ObjectMeta = metav1.ObjectMeta{
			Name: set.Name + "-1", Namespace: testNs,
			Labels: surge.Labels, Annotations: surge.Annotations, OwnerReferences: surge.OwnerReferences,
		}
		surge.Status = workloadsv1alpha2.RoleInstanceStatus{}
		surge.Labels[constants.RoleInstanceIndexLabelKey] = "1"
		surge.Labels[appsv1.StatefulSetPodNameLabel] = surge.Name
		for i := range surge.Spec.Components {
			labels := surge.Spec.Components[i].Template.Labels
			labels[constants.RoleInstanceIndexLabelKey] = "1"
			labels[constants.RoleInstanceNameLabelKey] = surge.Name
			labels[appsv1.StatefulSetPodNameLabel] = surge.Name
		}
		Expect(testutil.K8sClient.Create(testutil.Ctx, surge)).To(Succeed())
		surgePodKey := types.NamespacedName{Name: surge.Name + "-worker-0", Namespace: testNs}
		Eventually(func() error {
			return setPodReady(surgePodKey, true)
		}, timeout, interval).Should(Succeed())
		assertSurgeProtected := func() {
			Consistently(func() (types.UID, error) {
				current := &workloadsv1alpha2.RoleInstance{}
				if err := testutil.K8sClient.Get(testutil.Ctx, types.NamespacedName{Name: surge.Name, Namespace: testNs}, current); err != nil {
					return "", err
				}
				return current.UID, nil
			}, 2*time.Second, interval).Should(Equal(surge.UID), "surge must survive while the base is not ready")
		}
		assertSurgeProtected()
		Eventually(func() error {
			if err := testutil.K8sClient.Get(testutil.Ctx, setKey, set); err != nil {
				return err
			}
			set.Spec.UpdateStrategy.Paused = false
			return testutil.K8sClient.Update(testutil.Ctx, set)
		}, timeout, interval).Should(Succeed())
		assertSurgeProtected()

		basePodKey := types.NamespacedName{Name: base.Name + "-worker-0", Namespace: testNs}
		Eventually(func() error {
			return setPodReady(basePodKey, true)
		}, timeout, interval).Should(Succeed())
		Eventually(func() bool {
			current := &workloadsv1alpha2.RoleInstance{}
			return apierrors.IsNotFound(testutil.K8sClient.Get(testutil.Ctx, types.NamespacedName{Name: surge.Name, Namespace: testNs}, current))
		}, timeout, interval).Should(BeTrue(), "surge should be released after the base converges")
		Eventually(func() (bool, error) {
			current := &workloadsv1alpha2.RoleInstanceSet{}
			if err := testutil.K8sClient.Get(testutil.Ctx, setKey, current); err != nil {
				return false, err
			}
			return current.Status.RolloutReplacementState == nil, nil
		}, timeout, interval).Should(BeTrue(), "completed replacement intent should be cleared")
	})

	It("retains surge through a complete A to B to A rollback when partition decreases", func() {
		set := newRolloutRecoverySet("complete-rollback", testNs)
		set.Spec.Replicas = ptr.To[int32](2)
		set.Spec.UpdateStrategy.Partition = ptr.To(intstr.FromInt32(1))
		set.Spec.UpdateStrategy.MaxSurge = ptr.To(intstr.FromInt32(1))
		set.Spec.UpdateStrategy.MaxUnavailable = ptr.To(intstr.FromInt32(0))
		set.Spec.RoleInstanceTemplate.Components[0].Template.Spec.TerminationGracePeriodSeconds = ptr.To[int64](0)
		Expect(testutil.K8sClient.Create(testutil.Ctx, set)).To(Succeed())
		setKey := types.NamespacedName{Name: set.Name, Namespace: testNs}

		By("bringing both initial A instances to Ready")
		initialBase := awaitRolloutInstance(setKey, 1, "", "nginx:1.0")
		makeRolloutPodReady(initialBase)
		initialZero := awaitRolloutInstance(setKey, 0, "", "nginx:1.0")
		makeRolloutPodReady(initialZero)
		initialSet := awaitRolloutSet(setKey, func(current *workloadsv1alpha2.RoleInstanceSet) bool {
			return current.Status.ReadyReplicas == 2 && current.Status.CurrentRevision != "" && current.Status.CurrentRevision == current.Status.UpdateRevision
		})
		revisionA := initialSet.Status.CurrentRevision

		By("rolling out B to ordinal 1 while ordinal 0 remains at A")
		updateRolloutSpec(setKey, func(current *workloadsv1alpha2.RoleInstanceSet) {
			current.Spec.RoleInstanceTemplate.Components[0].Template.Spec.Containers[0].Image = "nginx:2.0"
		})
		surgeB := awaitRolloutInstance(setKey, 2, "", "nginx:2.0")
		makeRolloutPodReady(surgeB)
		baseB := awaitRolloutInstance(setKey, 1, initialBase.UID, "nginx:2.0")
		makeRolloutPodReady(baseB)
		awaitRolloutSet(setKey, func(current *workloadsv1alpha2.RoleInstanceSet) bool {
			return current.Status.CurrentRevision == revisionA && current.Status.UpdateRevision != revisionA && current.Status.UpdatedReadyReplicas > 0
		})
		Expect(awaitRolloutInstance(setKey, 0, "", "nginx:1.0").UID).To(Equal(initialZero.UID), "partition should leave ordinal 0 at A")

		By("rolling back to A and holding the replacement base unready")
		updateRolloutSpec(setKey, func(current *workloadsv1alpha2.RoleInstanceSet) {
			current.Spec.RoleInstanceTemplate.Components[0].Template.Spec.Containers[0].Image = "nginx:1.0"
		})
		surgeA := awaitRolloutInstance(setKey, 2, surgeB.UID, "nginx:1.0")
		makeRolloutPodReady(surgeA)
		baseA := awaitRolloutInstance(setKey, 1, baseB.UID, "nginx:1.0")
		awaitRolloutSet(setKey, func(current *workloadsv1alpha2.RoleInstanceSet) bool {
			state := current.Status.RolloutReplacementState
			return current.Status.CurrentRevision == revisionA && current.Status.UpdateRevision == revisionA && state != nil && state.Partition == 1
		})

		By("lowering partition without releasing the Ready surge")
		updateRolloutSpec(setKey, func(current *workloadsv1alpha2.RoleInstanceSet) {
			current.Spec.UpdateStrategy.Partition = ptr.To(intstr.FromInt32(0))
		})
		awaitRolloutSet(setKey, func(current *workloadsv1alpha2.RoleInstanceSet) bool {
			return current.Status.ObservedGeneration == current.Generation && current.Status.RolloutReplacementState != nil
		})
		Consistently(func() error {
			return readyRolloutSurgeUnchanged(surgeA, baseA)
		}, 2*time.Second, interval).Should(Succeed(), "surge must remain Ready while the replacement base is unready")

		By("releasing surge only after the replacement base is Ready")
		makeRolloutPodReady(baseA)
		Eventually(func() (bool, error) {
			return rolloutInstanceAndPodDeleted(surgeA)
		}, timeout, interval).Should(BeTrue(), "surge and its pod should be deleted after rollback completes")
		awaitRolloutSet(setKey, func(current *workloadsv1alpha2.RoleInstanceSet) bool {
			return current.Status.RolloutReplacementState == nil && current.Status.ReadyReplicas == 2
		})
	})
})

type rolloutInstance struct {
	*workloadsv1alpha2.RoleInstance
	pod *corev1.Pod
}

func awaitRolloutInstance(setKey types.NamespacedName, ordinal int, oldUID types.UID, image string) rolloutInstance {
	var observed rolloutInstance
	key := types.NamespacedName{Name: fmt.Sprintf("%s-%d", setKey.Name, ordinal), Namespace: setKey.Namespace}
	Eventually(func() error {
		instance := &workloadsv1alpha2.RoleInstance{}
		if err := testutil.K8sClient.Get(testutil.Ctx, key, instance); err != nil {
			return err
		}
		if instance.UID == oldUID || instance.DeletionTimestamp != nil || instance.Spec.Components[0].Template.Spec.Containers[0].Image != image {
			return fmt.Errorf("waiting for new %s instance %s", image, key)
		}
		pod := &corev1.Pod{}
		if err := testutil.K8sClient.Get(testutil.Ctx, types.NamespacedName{Name: key.Name + "-worker-0", Namespace: key.Namespace}, pod); err != nil {
			return err
		}
		owner := metav1.GetControllerOf(pod)
		if owner == nil || owner.UID != instance.UID {
			// envtest has no garbage collector. Once the RoleInstance UID has
			// changed, emulate collection of its old pod so the new pod can exist.
			if err := deleteOrphanedRolloutPod(pod); err != nil {
				return err
			}
			return fmt.Errorf("waiting for pod owned by instance %s", instance.UID)
		}
		if pod.DeletionTimestamp != nil || pod.Spec.Containers[0].Image != image {
			return fmt.Errorf("waiting for new %s pod %s", image, pod.Name)
		}
		observed = rolloutInstance{RoleInstance: instance, pod: pod}
		return nil
	}, 25*time.Second, 250*time.Millisecond).Should(Succeed())
	return observed
}

func makeRolloutPodReady(instance rolloutInstance) {
	Eventually(func() error {
		return setPodReady(types.NamespacedName{Name: instance.pod.Name, Namespace: instance.Namespace}, true)
	}, 25*time.Second, 250*time.Millisecond).Should(Succeed())
	Eventually(func() (bool, error) {
		current := &workloadsv1alpha2.RoleInstance{}
		if err := testutil.K8sClient.Get(testutil.Ctx, types.NamespacedName{Name: instance.Name, Namespace: instance.Namespace}, current); err != nil {
			return false, err
		}
		return current.UID == instance.UID && rolloutInstanceReady(current), nil
	}, 25*time.Second, 250*time.Millisecond).Should(BeTrue())
}

func awaitRolloutSet(key types.NamespacedName, check func(*workloadsv1alpha2.RoleInstanceSet) bool) *workloadsv1alpha2.RoleInstanceSet {
	current := &workloadsv1alpha2.RoleInstanceSet{}
	Eventually(func() (bool, error) {
		if err := testutil.K8sClient.Get(testutil.Ctx, key, current); err != nil {
			return false, err
		}
		return check(current), nil
	}, 25*time.Second, 250*time.Millisecond).Should(BeTrue())
	return current
}

func updateRolloutSpec(key types.NamespacedName, change func(*workloadsv1alpha2.RoleInstanceSet)) {
	Eventually(func() error {
		current := &workloadsv1alpha2.RoleInstanceSet{}
		if err := testutil.K8sClient.Get(testutil.Ctx, key, current); err != nil {
			return err
		}
		change(current)
		return testutil.K8sClient.Update(testutil.Ctx, current)
	}, 25*time.Second, 250*time.Millisecond).Should(Succeed())
}

func readyRolloutSurgeUnchanged(surge, replacement rolloutInstance) error {
	current := &workloadsv1alpha2.RoleInstance{}
	if err := testutil.K8sClient.Get(testutil.Ctx, types.NamespacedName{Name: surge.Name, Namespace: surge.Namespace}, current); err != nil {
		return err
	}
	if current.UID != surge.UID || current.DeletionTimestamp != nil || !rolloutInstanceReady(current) {
		return fmt.Errorf("surge instance was replaced, terminated, or became unready")
	}
	pod := &corev1.Pod{}
	if err := testutil.K8sClient.Get(testutil.Ctx, types.NamespacedName{Name: surge.pod.Name, Namespace: surge.Namespace}, pod); err != nil {
		return err
	}
	if pod.UID != surge.pod.UID || pod.DeletionTimestamp != nil || !podutil.IsPodReady(pod) {
		return fmt.Errorf("surge pod was replaced, terminated, or became unready")
	}
	if err := testutil.K8sClient.Get(testutil.Ctx, types.NamespacedName{Name: replacement.Name, Namespace: replacement.Namespace}, current); err != nil {
		return err
	}
	if current.UID != replacement.UID || rolloutInstanceReady(current) {
		return fmt.Errorf("replacement base was replaced again or unexpectedly became ready")
	}
	return nil
}

func rolloutInstanceReady(instance *workloadsv1alpha2.RoleInstance) bool {
	for _, condition := range instance.Status.Conditions {
		if condition.Type == workloadsv1alpha2.RoleInstanceReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func rolloutInstanceAndPodDeleted(surge rolloutInstance) (bool, error) {
	instance := &workloadsv1alpha2.RoleInstance{}
	err := testutil.K8sClient.Get(testutil.Ctx, types.NamespacedName{Name: surge.Name, Namespace: surge.Namespace}, instance)
	if !apierrors.IsNotFound(err) {
		return false, err
	}
	pod := &corev1.Pod{}
	err = testutil.K8sClient.Get(testutil.Ctx, types.NamespacedName{Name: surge.pod.Name, Namespace: surge.Namespace}, pod)
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, deleteOrphanedRolloutPod(pod)
}

func deleteOrphanedRolloutPod(pod *corev1.Pod) error {
	owner := metav1.GetControllerOf(pod)
	if owner == nil || owner.Kind != "RoleInstance" {
		return fmt.Errorf("pod %s has no RoleInstance owner", pod.Name)
	}
	instance := &workloadsv1alpha2.RoleInstance{}
	err := testutil.K8sClient.Get(testutil.Ctx, types.NamespacedName{Name: owner.Name, Namespace: pod.Namespace}, instance)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil && instance.UID == owner.UID {
		return fmt.Errorf("pod %s still has a live owner", pod.Name)
	}
	return client.IgnoreNotFound(testutil.K8sClient.Delete(testutil.Ctx, pod, client.GracePeriodSeconds(0), client.Preconditions{UID: &pod.UID}))
}

func setPodReady(key types.NamespacedName, ready bool) error {
	pod := &corev1.Pod{}
	if err := testutil.K8sClient.Get(testutil.Ctx, key, pod); err != nil {
		return err
	}
	if ready {
		testutil.SetPodRunningAndReady(pod)
	} else {
		pod.Status.Phase = corev1.PodRunning
		pod.Status.Conditions = []corev1.PodCondition{
			{
				Type:               corev1.PodReady,
				Status:             corev1.ConditionFalse,
				LastTransitionTime: metav1.Now(),
			},
			{
				Type:               corev1.ContainersReady,
				Status:             corev1.ConditionFalse,
				LastTransitionTime: metav1.Now(),
			},
		}
		for i := range pod.Status.ContainerStatuses {
			pod.Status.ContainerStatuses[i].Ready = false
		}
	}
	return testutil.K8sClient.Status().Update(testutil.Ctx, pod)
}

func newRolloutRecoverySet(name, namespace string) *workloadsv1alpha2.RoleInstanceSet {
	return &workloadsv1alpha2.RoleInstanceSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Annotations: map[string]string{
				constants.RoleInstancePatternKey: string(constants.StatefulPattern),
			},
		},
		Spec: workloadsv1alpha2.RoleInstanceSetSpec{
			Replicas:            ptr.To[int32](1),
			PodManagementPolicy: constants.ParallelPodManagement,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": name},
			},
			RoleInstanceTemplate: workloadsv1alpha2.RoleInstanceTemplate{
				RoleInstanceSpec: workloadsv1alpha2.RoleInstanceSpec{
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{
							Name: "worker",
							Size: ptr.To[int32](1),
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{Name: "nginx", Image: "nginx:1.0"},
									},
								},
							},
						},
					},
				},
			},
			UpdateStrategy: workloadsv1alpha2.RoleInstanceSetUpdateStrategy{
				Type:           workloadsv1alpha2.RecreatePodUpdateStrategyType,
				MaxUnavailable: ptr.To(intstr.FromInt32(1)),
				MaxSurge:       ptr.To(intstr.FromInt32(0)),
			},
		},
	}
}
