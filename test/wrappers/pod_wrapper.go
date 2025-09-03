package wrappers

import (
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/rbgs/test/utils"
)

type PodWrapper struct {
	corev1.Pod
}

func (podWrapper *PodWrapper) Obj() corev1.Pod {
	return podWrapper.Pod
}

func (podWrapper *PodWrapper) WithName(name string) *PodWrapper {
	podWrapper.Name = name
	return podWrapper
}

func (podWrapper *PodWrapper) WithPrefixName(prefixName string) *PodWrapper {
	podWrapper.Name = fmt.Sprintf("%s-%s", prefixName, string(uuid.NewUUID())[:10])
	return podWrapper
}

func (podWrapper *PodWrapper) WithLabels(labels map[string]string) *PodWrapper {
	podWrapper.Labels = labels
	return podWrapper
}

func (podWrapper *PodWrapper) WithReadyCondition(ready bool) *PodWrapper {
	var conditionStatus corev1.ConditionStatus
	if ready {
		conditionStatus = corev1.ConditionTrue
	} else {
		conditionStatus = corev1.ConditionFalse
	}

	podWrapper.Status = corev1.PodStatus{
		Phase: corev1.PodRunning,
		Conditions: []corev1.PodCondition{
			{
				Type:   corev1.PodReady,
				Status: conditionStatus,
			},
		},
	}
	return podWrapper
}

func BuildBasicPod() *PodWrapper {
	return &PodWrapper{
		corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-pod",
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "nginx",
						Image: utils.DefaultImage,
					},
				},
			},
		},
	}
}

func BuildDeletingPod() *PodWrapper {
	return &PodWrapper{
		corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "test-pod",
				Namespace:         "default",
				DeletionTimestamp: &metav1.Time{Time: time.Now()},
				Finalizers:        []string{"kubernetes.io/rolebasedgroup-controller"},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "nginx",
						Image: utils.DefaultImage,
					},
				},
			},
		},
	}
}

type PodTemplateSpecWrapper struct {
	corev1.PodTemplateSpec
}

func (podTemplateWrapper *PodTemplateSpecWrapper) Obj() corev1.PodTemplateSpec {
	return podTemplateWrapper.PodTemplateSpec
}

func (podTemplateWrapper *PodTemplateSpecWrapper) WithContainers(
	containers []corev1.Container,
) *PodTemplateSpecWrapper {
	podTemplateWrapper.Spec.Containers = containers
	return podTemplateWrapper
}

func (podTemplateWrapper *PodTemplateSpecWrapper) WithResources(
	resources corev1.ResourceRequirements,
	containerIndex int,
) *PodTemplateSpecWrapper {
	if containerIndex < 0 || containerIndex > len(podTemplateWrapper.Spec.Containers)-1 {
		containerIndex = 0
	}
	podTemplateWrapper.Spec.Containers[containerIndex].Resources = resources
	return podTemplateWrapper
}

func BuildBasicPodTemplateSpec() *PodTemplateSpecWrapper {
	return &PodTemplateSpecWrapper{
		corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "nginx",
						Image: utils.DefaultImage,
					},
				},
			},
		},
	}
}
