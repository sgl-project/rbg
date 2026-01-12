package workloads

import (
	"context"
	"fmt"

	"github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/test/utils"
)

type InstanceSetChecker struct {
	ctx    context.Context
	client client.Client
}

var _ WorkloadEqualChecker = &InstanceSetChecker{}

func NewInstanceSetChecker(ctx context.Context, client client.Client) *InstanceSetChecker {
	return &InstanceSetChecker{
		ctx:    ctx,
		client: client,
	}
}

func (s *InstanceSetChecker) ExpectWorkloadEqual(rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec) error {
	// 1. check instanceset exists
	instanceSet := &v1alpha1.InstanceSet{}
	err := s.client.Get(
		s.ctx, client.ObjectKey{
			Name:      rbg.GetWorkloadName(&role),
			Namespace: rbg.Namespace,
		}, instanceSet,
	)
	if err != nil {
		return fmt.Errorf("failed to get existing InstanceSet: %w", err)
	}

	// check instanceset ready
	if instanceSet.Status.ReadyReplicas != instanceSet.Status.Replicas {
		return fmt.Errorf(
			"instanceset not all ready, status.ready: %d, status.replicas: %d",
			instanceSet.Status.ReadyReplicas, instanceSet.Status.Replicas,
		)
	}

	// 2. check engine runtime container exist
	if role.EngineRuntimes != nil && len(instanceSet.Spec.InstanceTemplate.Components) > 0 {
		for _, component := range instanceSet.Spec.InstanceTemplate.Components {
			for _, container := range component.Template.Spec.Containers {
				if container.Name == role.EngineRuntimes[0].ProfileName {
					return nil
				}
			}
		}
		return fmt.Errorf("not found engine runtime container")
	}

	return nil
}

func (s *InstanceSetChecker) ExpectLabelContains(
	rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec, labels ...map[string]string,
) error {
	// 1. check instanceset exists
	instanceSet := &v1alpha1.InstanceSet{}
	err := s.client.Get(
		s.ctx, client.ObjectKey{
			Name:      rbg.GetWorkloadName(&role),
			Namespace: rbg.Namespace,
		}, instanceSet,
	)
	if err != nil {
		return fmt.Errorf("failed to get existing InstanceSet: %w", err)
	}

	if len(labels) == 0 {
		return fmt.Errorf("labels is empty")
	}

	for _, labelMap := range labels {
		for key, value := range labelMap {
			if !utils.MapContains(instanceSet.Labels, key, value) {
				return fmt.Errorf("instanceset labels do not have key %s, value: %s", key, value)
			}
		}
	}

	return nil
}

func (s *InstanceSetChecker) ExpectPodTemplateLabelContains(
	rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec, labels ...map[string]string,
) error {
	// 1. check instanceset exists
	instanceSet := &v1alpha1.InstanceSet{}
	err := s.client.Get(
		s.ctx, client.ObjectKey{
			Name:      rbg.GetWorkloadName(&role),
			Namespace: rbg.Namespace,
		}, instanceSet,
	)
	if err != nil {
		return fmt.Errorf("failed to get existing InstanceSet: %w", err)
	}

	if len(labels) == 0 {
		return fmt.Errorf("labels is empty")
	}

	// Check labels in all components' pod templates
	if len(instanceSet.Spec.InstanceTemplate.Components) == 0 {
		return fmt.Errorf("instanceset has no components")
	}

	// For each label map provided, check if it exists in at least one component
	for _, labelMap := range labels {
		found := false
		for _, component := range instanceSet.Spec.InstanceTemplate.Components {
			allMatch := true
			for key, value := range labelMap {
				if !utils.MapContains(component.Template.Labels, key, value) {
					allMatch = false
					break
				}
			}
			if allMatch {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("pod template labels do not contain expected labels: %v", labelMap)
		}
	}

	return nil
}

func (s *InstanceSetChecker) ExpectPodTemplateAnnotationContains(
	rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec,
	annotations ...map[string]string,
) error {
	// 1. check instanceset exists
	instanceSet := &v1alpha1.InstanceSet{}
	err := s.client.Get(
		s.ctx, client.ObjectKey{
			Name:      rbg.GetWorkloadName(&role),
			Namespace: rbg.Namespace,
		}, instanceSet,
	)
	if err != nil {
		return fmt.Errorf("failed to get existing InstanceSet: %w", err)
	}

	if len(annotations) == 0 {
		return fmt.Errorf("annotations is empty")
	}

	// Check annotations in all components' pod templates
	if len(instanceSet.Spec.InstanceTemplate.Components) == 0 {
		return fmt.Errorf("instanceset has no components")
	}

	// For each annotation map provided, check if it exists in at least one component
	for _, annotationMap := range annotations {
		found := false
		for _, component := range instanceSet.Spec.InstanceTemplate.Components {
			allMatch := true
			for key, value := range annotationMap {
				if !utils.MapContains(component.Template.Annotations, key, value) {
					allMatch = false
					break
				}
			}
			if allMatch {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("pod template annotations do not contain expected annotations: %v", annotationMap)
		}
	}

	return nil
}

func (s *InstanceSetChecker) ExpectWorkloadNotExist(rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec) error {
	instanceSet := &v1alpha1.InstanceSet{}
	err := s.client.Get(
		s.ctx, client.ObjectKey{
			Name:      rbg.GetWorkloadName(&role),
			Namespace: rbg.Namespace,
		}, instanceSet,
	)
	if err == nil {
		return fmt.Errorf("workload still exists")
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func (s *InstanceSetChecker) ExpectTopologyAffinity(
	rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec, topologyKey string,
) error {
	instanceSet := &v1alpha1.InstanceSet{}
	gomega.Eventually(
		func() error {
			return s.client.Get(
				s.ctx, client.ObjectKey{
					Name:      rbg.GetWorkloadName(&role),
					Namespace: rbg.Namespace,
				}, instanceSet,
			)
		}, utils.Timeout, utils.Interval,
	).ToNot(gomega.HaveOccurred())

	if len(instanceSet.Spec.InstanceTemplate.Components) == 0 {
		return fmt.Errorf("instanceset has no components")
	}

	rbgTopologyKey, found := rbg.GetExclusiveKey()
	if !found {
		return fmt.Errorf("exclusive key not found")
	}
	if rbgTopologyKey != topologyKey {
		return fmt.Errorf("topologyKey not equal, got: %s, expect: %s", rbgTopologyKey, topologyKey)
	}

	// Check affinity in all components' pod templates
	for _, component := range instanceSet.Spec.InstanceTemplate.Components {
		if component.Template.Spec.Affinity == nil {
			return fmt.Errorf("component %s affinity is nil", component.Name)
		}

		err := semanticallyEqualAffinity(
			component.Template.Spec.Affinity, topologyKey, rbg.GenGroupUniqueKey(),
		)
		if err != nil {
			return fmt.Errorf("component %s semanticallyEqualAffinity error: %s", component.Name, err.Error())
		}
	}

	return nil
}
