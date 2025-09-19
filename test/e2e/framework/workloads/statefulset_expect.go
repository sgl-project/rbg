package workloads

import (
	"context"
	"fmt"

	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/test/utils"
)

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

func (s *StatefulSetEqualChecker) ExpectWorkloadNotExist(rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec) error {
	sts := &appsv1.StatefulSet{}
	err := s.client.Get(
		s.ctx, client.ObjectKey{
			Name:      rbg.GetWorkloadName(&role),
			Namespace: rbg.Namespace,
		}, sts,
	)
	if err == nil {
		return fmt.Errorf("workload still exists")
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func (s *StatefulSetEqualChecker) ExpectTopologyAffinity(
	rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec, topologyKey string,
) error {
	sts := &appsv1.StatefulSet{}
	gomega.Eventually(
		func() error {
			return s.client.Get(
				s.ctx, client.ObjectKey{
					Name:      rbg.GetWorkloadName(&role),
					Namespace: rbg.Namespace,
				}, sts,
			)
		}, utils.Timeout, utils.Interval,
	).ToNot(gomega.HaveOccurred())

	if sts.Spec.Template.Spec.Affinity == nil {
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
		sts.Spec.Template.Spec.Affinity, topologyKey, rbg.GenGroupUniqueKey(),
	)
	if err != nil {
		return fmt.Errorf("semanticallyEqualAffinity error: %s", err.Error())
	}
	return nil
}
