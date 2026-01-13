package workloads

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

type WorkloadEqualChecker interface {
	ExpectWorkloadEqual(rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec) error
	ExpectLabelContains(rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec, labels ...map[string]string) error
	ExpectPodTemplateLabelContains(rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec, labels ...map[string]string) error
	ExpectWorkloadNotExist(rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec) error
	ExpectPodTemplateAnnotationContains(rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec, annotations ...map[string]string) error
	ExpectTopologyAffinity(rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec, topologyKey string) error
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
	case v1alpha1.InstanceSetWorkloadType:
		return NewInstanceSetChecker(ctx, client), nil
	default:
		return nil, fmt.Errorf("unsupported workload type: %s", workloadType)
	}
}

func semanticallyEqualAffinity(affinity *corev1.Affinity, topologyKey string, uniqueKey string) error {
	if affinity.PodAffinity == nil || affinity.PodAntiAffinity == nil {
		return fmt.Errorf("podAffinity or podAntiAffinity is nil")
	}
	for _, term := range affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
		if term.TopologyKey != topologyKey {
			return fmt.Errorf("PodAffinity.topologyKey not equal")
		}

		if term.LabelSelector == nil || term.LabelSelector.MatchExpressions == nil {
			return fmt.Errorf("PodAffinity.LabelSelector not equal")
		}

		for _, expression := range term.LabelSelector.MatchExpressions {
			if expression.Key != v1alpha1.SetGroupUniqueHashLabelKey ||
				expression.Operator != v1.LabelSelectorOpIn ||
				expression.Values[0] != uniqueKey {
				return fmt.Errorf("PodAffinity.LabelSelector.MatchExpressions not equal")
			}
		}
	}

	for _, term := range affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
		if term.TopologyKey != topologyKey {
			return fmt.Errorf("PodAffinity.topologyKey not equal")
		}

		if term.LabelSelector == nil || term.LabelSelector.MatchExpressions == nil {
			return fmt.Errorf("PodAffinity.LabelSelector not equal")
		}

		for _, expression := range term.LabelSelector.MatchExpressions {
			if expression.Key != v1alpha1.SetGroupUniqueHashLabelKey {
				return fmt.Errorf("PodAntiAffinity.LabelSelector.MatchExpressions.Key not equal")
			}
			if expression.Operator != v1.LabelSelectorOpNotIn && expression.Operator != v1.LabelSelectorOpExists {
				return fmt.Errorf("PodAntiAffinity.LabelSelector.MatchExpressions.Operator not equal")
			}

			if expression.Operator == v1.LabelSelectorOpNotIn {
				if expression.Values[0] != uniqueKey {
					return fmt.Errorf("PodAntiAffinity.LabelSelector.MatchExpressions.Values not equal")
				}
			}
		}
	}

	return nil
}
