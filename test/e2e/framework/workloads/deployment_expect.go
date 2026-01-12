package workloads

import (
	"context"
	"fmt"

	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/test/utils"
)

type DeploymentEqualChecker struct {
	ctx    context.Context
	client client.Client
}

var _ WorkloadEqualChecker = &DeploymentEqualChecker{}

func NewDeploymentEqualChecker(ctx context.Context, client client.Client) *DeploymentEqualChecker {
	return &DeploymentEqualChecker{
		ctx:    ctx,
		client: client,
	}
}

func (d *DeploymentEqualChecker) ExpectWorkloadEqual(rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec) error {
	// check deployment exist
	deployment := &appsv1.Deployment{}
	err := d.client.Get(
		d.ctx, client.ObjectKey{
			Name:      rbg.GetWorkloadName(&role),
			Namespace: rbg.Namespace,
		}, deployment,
	)
	if err != nil {
		return fmt.Errorf("failed to get existing Deployment: %w", err)
	}

	// check deployment ready
	if deployment.Status.ReadyReplicas != *deployment.Spec.Replicas {
		return fmt.Errorf(
			"deployment not all ready, status.ready: %d, status.replicas: %d",
			deployment.Status.ReadyReplicas, *deployment.Spec.Replicas,
		)
	}

	// check engine runtime container exist
	if role.EngineRuntimes != nil {
		for _, container := range deployment.Spec.Template.Spec.Containers {
			if container.Name == role.EngineRuntimes[0].ProfileName {
				return nil
			}
		}
		return fmt.Errorf("not found engine runtime container")
	}
	return nil
}

func (d *DeploymentEqualChecker) ExpectLabelContains(rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec, labels ...map[string]string) error {
	// check deployment exist
	deployment := &appsv1.Deployment{}
	err := d.client.Get(
		d.ctx, client.ObjectKey{
			Name:      rbg.GetWorkloadName(&role),
			Namespace: rbg.Namespace,
		}, deployment,
	)
	if err != nil {
		return fmt.Errorf("failed to get existing deployment: %w", err)
	}

	if len(labels) == 0 {
		return fmt.Errorf("labels is empty")
	}

	for _, labelMap := range labels {
		for key, value := range labelMap {
			if !utils.MapContains(deployment.Labels, key, value) {
				return fmt.Errorf("deployment labels do not have key %s, value: %s", key, value)
			}
		}
	}

	return nil
}

func (d *DeploymentEqualChecker) ExpectPodTemplateLabelContains(
	rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec,
	labels ...map[string]string,
) error {
	// check deployment exist
	deployment := &appsv1.Deployment{}
	err := d.client.Get(
		d.ctx, client.ObjectKey{
			Name:      rbg.GetWorkloadName(&role),
			Namespace: rbg.Namespace,
		}, deployment,
	)
	if err != nil {
		return fmt.Errorf("failed to get existing Deployment: %w", err)
	}

	if len(labels) == 0 {
		return fmt.Errorf("labels is empty")
	}

	for _, labelMap := range labels {
		for key, value := range labelMap {
			if !utils.MapContains(deployment.Spec.Template.Labels, key, value) {
				return fmt.Errorf("pod labels do not have key %s, value: %s", key, value)
			}
		}
	}

	return nil
}

func (d *DeploymentEqualChecker) ExpectPodTemplateAnnotationContains(
	rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec,
	annotations ...map[string]string,
) error {
	// check deployment exist
	deployment := &appsv1.Deployment{}
	err := d.client.Get(
		d.ctx, client.ObjectKey{
			Name:      rbg.GetWorkloadName(&role),
			Namespace: rbg.Namespace,
		}, deployment,
	)
	if err != nil {
		return fmt.Errorf("failed to get existing Deployment: %w", err)
	}

	if len(annotations) == 0 {
		return fmt.Errorf("annotations is empty")
	}

	for _, annotationMap := range annotations {
		for key, value := range annotationMap {
			if !utils.MapContains(deployment.Spec.Template.Annotations, key, value) {
				return fmt.Errorf("pod anotations do not have key %s, value: %s", key, value)
			}
		}
	}
	return nil
}

func (d *DeploymentEqualChecker) ExpectWorkloadNotExist(rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec) error {
	deployment := &appsv1.Deployment{}
	err := d.client.Get(
		d.ctx, client.ObjectKey{
			Name:      rbg.GetWorkloadName(&role),
			Namespace: rbg.Namespace,
		}, deployment,
	)
	if err == nil {
		return fmt.Errorf("workload still exists")
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func (d *DeploymentEqualChecker) ExpectTopologyAffinity(
	rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec, topologyKey string,
) error {
	deployment := &appsv1.Deployment{}
	gomega.Eventually(
		func() error {
			return d.client.Get(
				d.ctx, client.ObjectKey{
					Name:      rbg.GetWorkloadName(&role),
					Namespace: rbg.Namespace,
				}, deployment,
			)
		}, utils.Timeout, utils.Interval,
	).ToNot(gomega.HaveOccurred())

	if deployment.Spec.Template.Spec.Affinity == nil {
		return fmt.Errorf("affinity not equal")
	}

	rbgTopologyKey, found := rbg.GetExclusiveKey()
	if !found {
		return fmt.Errorf("exclusive key not found")
	}
	if rbgTopologyKey != topologyKey {
		return fmt.Errorf("topologyKey not equal, got: %s, expect: %s", rbgTopologyKey, topologyKey)
	}

	err := semanticallyEqualAffinity(
		deployment.Spec.Template.Spec.Affinity, topologyKey, rbg.GenGroupUniqueKey(),
	)
	if err != nil {
		return fmt.Errorf("semanticallyEqualAffinity error: %s", err.Error())
	}
	return nil
}
