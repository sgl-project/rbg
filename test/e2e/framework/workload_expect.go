package framework

import (
	"context"
	"errors"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/test/utils"
)

type WorkloadEqualChecker interface {
	ExpectWorkloadEqual(rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec) error
	ExpectLabelContains(rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec, labels ...map[string]string) error
	ExpectAnnotationContains(rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec, annotations ...map[string]string) error
	ExpectWorkloadNotExist(rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec) error
}

func NewWorkloadEqualChecker(
	ctx context.Context, client client.Client,
	workloadType string,
) (WorkloadEqualChecker, error) {
	switch workloadType {
	case v1alpha1.DeploymentWorkloadType:
		return NewDeploymentEqualChecker(ctx, client), nil
	case v1alpha1.StatefulSetWorkloadType:
		return NewStatefulSetEqualChecker(ctx, client), nil
	case v1alpha1.LeaderWorkerSetWorkloadType:
		return NewLeaderWorkerSetEqualChecker(ctx, client), nil
	default:
		return nil, fmt.Errorf("unsupported workload type: %s", workloadType)
	}
}

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

func (d *DeploymentEqualChecker) ExpectLabelContains(
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

	for key, value := range labels[0] {
		if !utils.MapContains(deployment.Spec.Template.Labels, key, value) {
			return fmt.Errorf("pod labels do not have key %s, value: %s", key, value)
		}
	}

	return nil
}

func (d *DeploymentEqualChecker) ExpectAnnotationContains(rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec, annotations ...map[string]string) error {
	// check deployment exist
	deployment := &appsv1.Deployment{}
	err := d.client.Get(d.ctx, client.ObjectKey{
		Name:      rbg.GetWorkloadName(&role),
		Namespace: rbg.Namespace,
	}, deployment)
	if err != nil {
		return fmt.Errorf("failed to get existing Deployment: %w", err)
	}

	for key, value := range annotations[0] {
		if !utils.MapContains(deployment.Spec.Template.Annotations, key, value) {
			return fmt.Errorf("pod annotations do not have key %s, value: %s", key, value)
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
		return errors.New("workload still exists")
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

type StatefulSetEqualChecker struct {
	ctx    context.Context
	client client.Client
}

var _ WorkloadEqualChecker = &StatefulSetEqualChecker{}

func NewStatefulSetEqualChecker(ctx context.Context, client client.Client) *StatefulSetEqualChecker {
	return &StatefulSetEqualChecker{
		ctx:    ctx,
		client: client,
	}
}

func (s *StatefulSetEqualChecker) ExpectWorkloadEqual(rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec) error {
	// check sts exists
	sts := &appsv1.StatefulSet{}
	err := s.client.Get(
		s.ctx, client.ObjectKey{
			Name:      rbg.GetWorkloadName(&role),
			Namespace: rbg.Namespace,
		}, sts,
	)
	if err != nil {
		return fmt.Errorf("failed to get existing StatefulSet: %w", err)
	}

	// check svc exists
	svc := &v1.Service{}
	err = s.client.Get(
		s.ctx, client.ObjectKey{
			Name:      rbg.GetWorkloadName(&role),
			Namespace: rbg.Namespace,
		}, svc,
	)
	if err != nil {
		return fmt.Errorf("failed to get existing headless svc: %w", err)
	}

	// check sts ready
	if sts.Status.ReadyReplicas != *sts.Spec.Replicas {
		return fmt.Errorf(
			"sts not all ready, status.ready: %d, status.replicas: %d",
			sts.Status.ReadyReplicas, *sts.Spec.Replicas,
		)
	}

	// check engine runtime container exist
	if role.EngineRuntimes != nil {
		for _, container := range sts.Spec.Template.Spec.Containers {
			if container.Name == role.EngineRuntimes[0].ProfileName {
				return nil
			}
		}
		return fmt.Errorf("not found engine runtime container")
	}
	return nil
}

func (s *StatefulSetEqualChecker) ExpectLabelContains(
	rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec, labels ...map[string]string,
) error {
	// check sts exists
	sts := &appsv1.StatefulSet{}
	err := s.client.Get(
		s.ctx, client.ObjectKey{
			Name:      rbg.GetWorkloadName(&role),
			Namespace: rbg.Namespace,
		}, sts,
	)
	if err != nil {
		return fmt.Errorf("failed to get existing StatefulSet: %w", err)
	}

	for key, value := range labels[0] {
		if !utils.MapContains(sts.Spec.Template.Labels, key, value) {
			return fmt.Errorf("pod labels do not have key %s, value: %s", key, value)
		}
	}

	return nil
}

func (s *StatefulSetEqualChecker) ExpectAnnotationContains(rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec, annotations ...map[string]string) error {
	// check sts exists
	sts := &appsv1.StatefulSet{}
	err := s.client.Get(s.ctx, client.ObjectKey{
		Name:      rbg.GetWorkloadName(&role),
		Namespace: rbg.Namespace,
	}, sts)
	if err != nil {
		return fmt.Errorf("failed to get existing StatefulSet: %w", err)
	}

	for key, value := range annotations[0] {
		if !utils.MapContains(sts.Spec.Template.Annotations, key, value) {
			return fmt.Errorf("pod annotations do not have key %s, value: %s", key, value)
		}
	}

	return nil
}

func (s *StatefulSetEqualChecker) ExpectWorkloadNotExist(rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec) error {
	sts := &appsv1.StatefulSet{}
	err := s.client.Get(
		s.ctx, client.ObjectKey{
			Name:      rbg.GetWorkloadName(&role),
			Namespace: rbg.Namespace,
		}, sts,
	)
	if err == nil {
		return errors.New("workload still exists")
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

type LeaderWorkerSetEqualChecker struct {
	ctx    context.Context
	client client.Client
}

var _ WorkloadEqualChecker = &LeaderWorkerSetEqualChecker{}

func NewLeaderWorkerSetEqualChecker(ctx context.Context, client client.Client) *LeaderWorkerSetEqualChecker {
	return &LeaderWorkerSetEqualChecker{
		ctx:    ctx,
		client: client,
	}
}

func (s *LeaderWorkerSetEqualChecker) ExpectWorkloadEqual(rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec) error {
	// 1. check lws exists
	lws := &lwsv1.LeaderWorkerSet{}
	err := s.client.Get(
		s.ctx, client.ObjectKey{
			Name:      rbg.GetWorkloadName(&role),
			Namespace: rbg.Namespace,
		}, lws,
	)
	if err != nil {
		return fmt.Errorf("failed to get existing lws: %w", err)
	}

	// check lws ready
	if lws.Status.ReadyReplicas != lws.Status.Replicas {
		return fmt.Errorf(
			"lws not all ready, status.ready: %d, status.replicas: %d",
			lws.Status.ReadyReplicas, lws.Status.Replicas,
		)
	}

	// 2. check engine runtime container exist
	if role.EngineRuntimes != nil && lws.Spec.LeaderWorkerTemplate.LeaderTemplate != nil {
		for _, container := range lws.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec.Containers {
			if container.Name == role.EngineRuntimes[0].ProfileName {
				return nil
			}
		}
		return fmt.Errorf("not found engine runtime container")
	}

	return nil
}

func (s *LeaderWorkerSetEqualChecker) ExpectLabelContains(
	rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec, labels ...map[string]string,
) error {
	// 1. check lws exists
	lws := &lwsv1.LeaderWorkerSet{}
	err := s.client.Get(
		s.ctx, client.ObjectKey{
			Name:      rbg.GetWorkloadName(&role),
			Namespace: rbg.Namespace,
		}, lws,
	)
	if err != nil {
		return fmt.Errorf("failed to get existing lws: %w", err)
	}

	var leaderLabel, workerLabel map[string]string

	if len(labels) == 0 {
		return fmt.Errorf("labels is empty")
	} else if len(labels) == 1 {
		workerLabel = labels[0]
	} else {
		leaderLabel, workerLabel = labels[0], labels[1]
	}

	if lws.Spec.LeaderWorkerTemplate.LeaderTemplate != nil {
		for key, value := range leaderLabel {
			if !utils.MapContains(lws.Spec.LeaderWorkerTemplate.LeaderTemplate.Labels, key, value) {
				return fmt.Errorf("leader sts labels do not have key %s, value: %s", key, value)
			}
		}
	}

	for key, value := range workerLabel {
		if !utils.MapContains(lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Labels, key, value) {
			return fmt.Errorf("worker sts labels do not have key %s, value: %s", key, value)
		}
	}

	return nil
}

func (s *LeaderWorkerSetEqualChecker) ExpectAnnotationContains(rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec, annotations ...map[string]string) error {
	// 1. check lws exists
	lws := &lwsv1.LeaderWorkerSet{}
	err := s.client.Get(s.ctx, client.ObjectKey{
		Name:      rbg.GetWorkloadName(&role),
		Namespace: rbg.Namespace,
	}, lws)
	if err != nil {
		return fmt.Errorf("failed to get existing lws: %w", err)
	}

	var leaderAnnotation, workerAnnotation map[string]string

	if len(annotations) == 0 {
		return fmt.Errorf("annotations is empty")
	} else if len(annotations) == 1 {
		workerAnnotation = annotations[0]
	} else {
		leaderAnnotation, workerAnnotation = annotations[0], annotations[1]
	}

	if lws.Spec.LeaderWorkerTemplate.LeaderTemplate != nil {
		for key, value := range leaderAnnotation {
			if !utils.MapContains(lws.Spec.LeaderWorkerTemplate.LeaderTemplate.Annotations, key, value) {
				return fmt.Errorf("leader sts annotations do not have key %s, value: %s", key, value)
			}
		}
	}

	for key, value := range workerAnnotation {
		if !utils.MapContains(lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Annotations, key, value) {
			return fmt.Errorf("worker sts annotations do not have key %s, value: %s", key, value)
		}
	}

	return nil
}

func (s *LeaderWorkerSetEqualChecker) ExpectWorkloadNotExist(rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec) error {
	lws := &lwsv1.LeaderWorkerSet{}
	err := s.client.Get(
		s.ctx, client.ObjectKey{
			Name:      rbg.GetWorkloadName(&role),
			Namespace: rbg.Namespace,
		}, lws,
	)
	if err == nil {
		return errors.New("workload still exists")
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}
