package workloads

import (
	"context"
	"fmt"

	"github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/test/utils"
)

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

func (s *LeaderWorkerSetEqualChecker) ExpectWorkloadNotExist(
	rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec,
) error {
	lws := &lwsv1.LeaderWorkerSet{}
	err := s.client.Get(
		s.ctx, client.ObjectKey{
			Name:      rbg.GetWorkloadName(&role),
			Namespace: rbg.Namespace,
		}, lws,
	)
	if err == nil {
		return fmt.Errorf("workload still exists")
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func (s *LeaderWorkerSetEqualChecker) ExpectTopologyAffinity(
	rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec, topologyKey string,
) error {
	lws := &lwsv1.LeaderWorkerSet{}
	gomega.Eventually(
		func() error {
			return s.client.Get(
				s.ctx, client.ObjectKey{
					Name:      rbg.GetWorkloadName(&role),
					Namespace: rbg.Namespace,
				}, lws,
			)
		}, utils.Timeout, utils.Interval,
	).ToNot(gomega.HaveOccurred())

	if lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Affinity == nil {
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
		lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Affinity, topologyKey, rbg.GenGroupUniqueKey(),
	)
	if err != nil {
		return fmt.Errorf("semanticallyEqualAffinity error: %s", err.Error())
	}
	return nil
}
