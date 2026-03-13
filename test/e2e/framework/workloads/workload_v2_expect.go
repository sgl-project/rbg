package workloads

import (
	"context"
	"fmt"

	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/constants"
	"sigs.k8s.io/rbgs/test/utils"
)

// WorkloadV2EqualChecker is the interface for checking v1alpha2 workloads.
type WorkloadV2EqualChecker interface {
	ExpectWorkloadEqualV2(rbg *workloadsv1alpha2.RoleBasedGroup, role workloadsv1alpha2.RoleSpec) error
	ExpectPodTemplateLabelContainsV2(rbg *workloadsv1alpha2.RoleBasedGroup, role workloadsv1alpha2.RoleSpec, labels ...map[string]string) error
	ExpectPodTemplateAnnotationContainsV2(rbg *workloadsv1alpha2.RoleBasedGroup, role workloadsv1alpha2.RoleSpec, annotations ...map[string]string) error
	ExpectWorkloadNotExistV2(rbg *workloadsv1alpha2.RoleBasedGroup, role workloadsv1alpha2.RoleSpec) error
	ExpectTopologyAffinityV2(rbg *workloadsv1alpha2.RoleBasedGroup, role workloadsv1alpha2.RoleSpec, topologyKey string) error
}

// NewWorkloadEqualCheckerV2 creates a WorkloadV2EqualChecker for the given workload type.
func NewWorkloadEqualCheckerV2(
	ctx context.Context, c client.Client, workloadType string,
) (WorkloadV2EqualChecker, error) {
	switch workloadType {
	case constants.RoleInstanceSetWorkloadType:
		return NewRoleInstanceSetCheckerV2(ctx, c), nil
	case constants.LeaderWorkerSetWorkloadType:
		return NewLeaderWorkerSetCheckerV2(ctx, c), nil
	case constants.DeploymentWorkloadType:
		return NewDeploymentCheckerV2(ctx, c), nil
	case constants.StatefulSetWorkloadType:
		return NewStatefulSetCheckerV2(ctx, c), nil
	default:
		// Default: treat unknown as RoleInstanceSet (standalone pattern default)
		return NewRoleInstanceSetCheckerV2(ctx, c), nil
	}
}

// ============================================================
// RoleInstanceSetCheckerV2 — for StandalonePattern (default)
// ============================================================

// RoleInstanceSetCheckerV2 checks v1alpha2 standalone roles by looking up the named
// workload under the rbg. In v1alpha2 the backing workload is determined at runtime
// by the controller; we probe readiness via the RBG status.
type RoleInstanceSetCheckerV2 struct {
	ctx    context.Context
	client client.Client
}

func NewRoleInstanceSetCheckerV2(ctx context.Context, c client.Client) *RoleInstanceSetCheckerV2 {
	return &RoleInstanceSetCheckerV2{ctx: ctx, client: c}
}

func (s *RoleInstanceSetCheckerV2) ExpectWorkloadEqualV2(
	rbg *workloadsv1alpha2.RoleBasedGroup, role workloadsv1alpha2.RoleSpec,
) error {
	newRbg := &workloadsv1alpha2.RoleBasedGroup{}
	err := s.client.Get(s.ctx, client.ObjectKey{Name: rbg.Name, Namespace: rbg.Namespace}, newRbg)
	if err != nil {
		return fmt.Errorf("failed to get rbg v2: %w", err)
	}

	roleStatus, found := newRbg.GetRoleStatus(role.Name)
	if !found {
		return fmt.Errorf("role %s status not found", role.Name)
	}

	if roleStatus.ReadyReplicas != *role.Replicas {
		return fmt.Errorf(
			"role %s not ready: readyReplicas=%d, desired=%d",
			role.Name, roleStatus.ReadyReplicas, *role.Replicas,
		)
	}
	return nil
}

func (s *RoleInstanceSetCheckerV2) ExpectPodTemplateLabelContainsV2(
	rbg *workloadsv1alpha2.RoleBasedGroup, role workloadsv1alpha2.RoleSpec, labelMaps ...map[string]string,
) error {
	podList := &corev1.PodList{}
	err := s.client.List(s.ctx, podList,
		client.InNamespace(rbg.Namespace),
		client.MatchingLabels{
			constants.GroupNameLabelKey: rbg.Name,
			constants.RoleNameLabelKey:  role.Name,
		},
	)
	if err != nil {
		return fmt.Errorf("list pods for role %s failed: %w", role.Name, err)
	}
	if len(podList.Items) == 0 {
		return fmt.Errorf("no pods found for role %s", role.Name)
	}
	for _, labelMap := range labelMaps {
		for key, value := range labelMap {
			if !utils.MapContains(podList.Items[0].Labels, key, value) {
				return fmt.Errorf("pod labels missing key=%s value=%s", key, value)
			}
		}
	}
	return nil
}

func (s *RoleInstanceSetCheckerV2) ExpectPodTemplateAnnotationContainsV2(
	rbg *workloadsv1alpha2.RoleBasedGroup, role workloadsv1alpha2.RoleSpec, annotationMaps ...map[string]string,
) error {
	podList := &corev1.PodList{}
	err := s.client.List(s.ctx, podList,
		client.InNamespace(rbg.Namespace),
		client.MatchingLabels{
			constants.GroupNameLabelKey: rbg.Name,
			constants.RoleNameLabelKey:  role.Name,
		},
	)
	if err != nil {
		return fmt.Errorf("list pods for role %s failed: %w", role.Name, err)
	}
	if len(podList.Items) == 0 {
		return fmt.Errorf("no pods found for role %s", role.Name)
	}
	for _, annotationMap := range annotationMaps {
		for key, value := range annotationMap {
			if !utils.MapContains(podList.Items[0].Annotations, key, value) {
				return fmt.Errorf("pod annotations missing key=%s value=%s", key, value)
			}
		}
	}
	return nil
}

func (s *RoleInstanceSetCheckerV2) ExpectWorkloadNotExistV2(
	rbg *workloadsv1alpha2.RoleBasedGroup, role workloadsv1alpha2.RoleSpec,
) error {
	podList := &corev1.PodList{}
	err := s.client.List(s.ctx, podList,
		client.InNamespace(rbg.Namespace),
		client.MatchingLabels{
			constants.GroupNameLabelKey: rbg.Name,
			constants.RoleNameLabelKey:  role.Name,
		},
	)
	if err != nil {
		return err
	}
	if len(podList.Items) > 0 {
		return fmt.Errorf("workload for role %s still has %d pods", role.Name, len(podList.Items))
	}
	return nil
}

func (s *RoleInstanceSetCheckerV2) ExpectTopologyAffinityV2(
	rbg *workloadsv1alpha2.RoleBasedGroup, role workloadsv1alpha2.RoleSpec, topologyKey string,
) error {
	podList := &corev1.PodList{}
	gomega.Eventually(
		func() error {
			if err := s.client.List(s.ctx, podList,
				client.InNamespace(rbg.Namespace),
				client.MatchingLabels{
					constants.GroupNameLabelKey: rbg.Name,
					constants.RoleNameLabelKey:  role.Name,
				},
			); err != nil {
				return err
			}
			if len(podList.Items) == 0 {
				return fmt.Errorf("no pods found for role %s", role.Name)
			}
			return nil
		}, utils.Timeout, utils.Interval,
	).Should(gomega.Succeed())

	rbgTopologyKey, found := rbg.GetExclusiveKey()
	if !found {
		return fmt.Errorf("exclusive key not found")
	}
	if rbgTopologyKey != topologyKey {
		return fmt.Errorf("topologyKey not equal, got: %s, expect: %s", rbgTopologyKey, topologyKey)
	}

	pod := podList.Items[0]
	if pod.Spec.Affinity == nil {
		return fmt.Errorf("pod affinity is nil")
	}
	return semanticallyEqualAffinityV2(pod.Spec.Affinity, topologyKey, rbg.GenGroupUniqueKey())
}

// ============================================================
// LeaderWorkerSetCheckerV2
// ============================================================

type LeaderWorkerSetCheckerV2 struct {
	ctx    context.Context
	client client.Client
}

func NewLeaderWorkerSetCheckerV2(ctx context.Context, c client.Client) *LeaderWorkerSetCheckerV2 {
	return &LeaderWorkerSetCheckerV2{ctx: ctx, client: c}
}

func (s *LeaderWorkerSetCheckerV2) ExpectWorkloadEqualV2(
	rbg *workloadsv1alpha2.RoleBasedGroup, role workloadsv1alpha2.RoleSpec,
) error {
	newRbg := &workloadsv1alpha2.RoleBasedGroup{}
	err := s.client.Get(s.ctx, client.ObjectKey{Name: rbg.Name, Namespace: rbg.Namespace}, newRbg)
	if err != nil {
		return fmt.Errorf("failed to get rbg v2: %w", err)
	}
	roleStatus, found := newRbg.GetRoleStatus(role.Name)
	if !found {
		return fmt.Errorf("role %s status not found", role.Name)
	}
	if roleStatus.ReadyReplicas != *role.Replicas {
		// Also fetch the actual LWS to compare its readyReplicas with what is reflected in the RBG status.
		// This helps diagnose cases where the LWS is ready but the RBG controller has not updated the status yet.
		lwsObj := &lwsv1.LeaderWorkerSet{}
		lwsName := rbg.Name + "-" + role.Name
		if lwsErr := s.client.Get(s.ctx, types.NamespacedName{Name: lwsName, Namespace: rbg.Namespace}, lwsObj); lwsErr == nil {
			return fmt.Errorf(
				"role %s lws not ready: rbgStatus.readyReplicas=%d, lws.readyReplicas=%d, lws.replicas=%d, desired=%d",
				role.Name, roleStatus.ReadyReplicas, lwsObj.Status.ReadyReplicas, lwsObj.Status.Replicas, *role.Replicas,
			)
		}
		return fmt.Errorf("role %s lws not ready: readyReplicas=%d, desired=%d",
			role.Name, roleStatus.ReadyReplicas, *role.Replicas)
	}
	return nil
}

func (s *LeaderWorkerSetCheckerV2) ExpectPodTemplateLabelContainsV2(
	rbg *workloadsv1alpha2.RoleBasedGroup, role workloadsv1alpha2.RoleSpec, labelMaps ...map[string]string,
) error {
	podList := &corev1.PodList{}
	err := s.client.List(s.ctx, podList,
		client.InNamespace(rbg.Namespace),
		client.MatchingLabels{
			constants.GroupNameLabelKey: rbg.Name,
			constants.RoleNameLabelKey:  role.Name,
		},
	)
	if err != nil {
		return fmt.Errorf("list pods for lws role %s failed: %w", role.Name, err)
	}
	if len(podList.Items) == 0 {
		return fmt.Errorf("no pods found for lws role %s", role.Name)
	}
	for _, labelMap := range labelMaps {
		for key, value := range labelMap {
			if !utils.MapContains(podList.Items[0].Labels, key, value) {
				return fmt.Errorf("pod labels missing key=%s value=%s", key, value)
			}
		}
	}
	return nil
}

func (s *LeaderWorkerSetCheckerV2) ExpectPodTemplateAnnotationContainsV2(
	rbg *workloadsv1alpha2.RoleBasedGroup, role workloadsv1alpha2.RoleSpec, annotations ...map[string]string,
) error {
	return (&RoleInstanceSetCheckerV2{ctx: s.ctx, client: s.client}).ExpectPodTemplateAnnotationContainsV2(rbg, role, annotations...)
}

func (s *LeaderWorkerSetCheckerV2) ExpectWorkloadNotExistV2(
	rbg *workloadsv1alpha2.RoleBasedGroup, role workloadsv1alpha2.RoleSpec,
) error {
	podList := &corev1.PodList{}
	err := s.client.List(s.ctx, podList,
		client.InNamespace(rbg.Namespace),
		client.MatchingLabels{
			constants.GroupNameLabelKey: rbg.Name,
			constants.RoleNameLabelKey:  role.Name,
		},
	)
	if err != nil {
		return err
	}
	if len(podList.Items) > 0 {
		return fmt.Errorf("lws workload for role %s still has %d pods", role.Name, len(podList.Items))
	}
	return nil
}

func (s *LeaderWorkerSetCheckerV2) ExpectTopologyAffinityV2(
	rbg *workloadsv1alpha2.RoleBasedGroup, role workloadsv1alpha2.RoleSpec, topologyKey string,
) error {
	return (&RoleInstanceSetCheckerV2{ctx: s.ctx, client: s.client}).ExpectTopologyAffinityV2(rbg, role, topologyKey)
}

// ============================================================
// DeploymentCheckerV2 — for explicit Deployment workload type
// ============================================================

type DeploymentCheckerV2 struct {
	ctx    context.Context
	client client.Client
}

func NewDeploymentCheckerV2(ctx context.Context, c client.Client) *DeploymentCheckerV2 {
	return &DeploymentCheckerV2{ctx: ctx, client: c}
}

func (s *DeploymentCheckerV2) ExpectWorkloadEqualV2(
	rbg *workloadsv1alpha2.RoleBasedGroup, role workloadsv1alpha2.RoleSpec,
) error {
	return (&RoleInstanceSetCheckerV2{ctx: s.ctx, client: s.client}).ExpectWorkloadEqualV2(rbg, role)
}

func (s *DeploymentCheckerV2) ExpectPodTemplateLabelContainsV2(
	rbg *workloadsv1alpha2.RoleBasedGroup, role workloadsv1alpha2.RoleSpec, labels ...map[string]string,
) error {
	return (&RoleInstanceSetCheckerV2{ctx: s.ctx, client: s.client}).ExpectPodTemplateLabelContainsV2(rbg, role, labels...)
}

func (s *DeploymentCheckerV2) ExpectPodTemplateAnnotationContainsV2(
	rbg *workloadsv1alpha2.RoleBasedGroup, role workloadsv1alpha2.RoleSpec, annotations ...map[string]string,
) error {
	return (&RoleInstanceSetCheckerV2{ctx: s.ctx, client: s.client}).ExpectPodTemplateAnnotationContainsV2(rbg, role, annotations...)
}

func (s *DeploymentCheckerV2) ExpectWorkloadNotExistV2(
	rbg *workloadsv1alpha2.RoleBasedGroup, role workloadsv1alpha2.RoleSpec,
) error {
	return (&RoleInstanceSetCheckerV2{ctx: s.ctx, client: s.client}).ExpectWorkloadNotExistV2(rbg, role)
}

func (s *DeploymentCheckerV2) ExpectTopologyAffinityV2(
	rbg *workloadsv1alpha2.RoleBasedGroup, role workloadsv1alpha2.RoleSpec, topologyKey string,
) error {
	return (&RoleInstanceSetCheckerV2{ctx: s.ctx, client: s.client}).ExpectTopologyAffinityV2(rbg, role, topologyKey)
}

// ============================================================
// StatefulSetCheckerV2
// ============================================================

type StatefulSetCheckerV2 struct {
	ctx    context.Context
	client client.Client
}

func NewStatefulSetCheckerV2(ctx context.Context, c client.Client) *StatefulSetCheckerV2 {
	return &StatefulSetCheckerV2{ctx: ctx, client: c}
}

func (s *StatefulSetCheckerV2) ExpectWorkloadEqualV2(
	rbg *workloadsv1alpha2.RoleBasedGroup, role workloadsv1alpha2.RoleSpec,
) error {
	return (&RoleInstanceSetCheckerV2{ctx: s.ctx, client: s.client}).ExpectWorkloadEqualV2(rbg, role)
}

func (s *StatefulSetCheckerV2) ExpectPodTemplateLabelContainsV2(
	rbg *workloadsv1alpha2.RoleBasedGroup, role workloadsv1alpha2.RoleSpec, labels ...map[string]string,
) error {
	return (&RoleInstanceSetCheckerV2{ctx: s.ctx, client: s.client}).ExpectPodTemplateLabelContainsV2(rbg, role, labels...)
}

func (s *StatefulSetCheckerV2) ExpectPodTemplateAnnotationContainsV2(
	rbg *workloadsv1alpha2.RoleBasedGroup, role workloadsv1alpha2.RoleSpec, annotations ...map[string]string,
) error {
	return (&RoleInstanceSetCheckerV2{ctx: s.ctx, client: s.client}).ExpectPodTemplateAnnotationContainsV2(rbg, role, annotations...)
}

func (s *StatefulSetCheckerV2) ExpectWorkloadNotExistV2(
	rbg *workloadsv1alpha2.RoleBasedGroup, role workloadsv1alpha2.RoleSpec,
) error {
	return (&RoleInstanceSetCheckerV2{ctx: s.ctx, client: s.client}).ExpectWorkloadNotExistV2(rbg, role)
}

func (s *StatefulSetCheckerV2) ExpectTopologyAffinityV2(
	rbg *workloadsv1alpha2.RoleBasedGroup, role workloadsv1alpha2.RoleSpec, topologyKey string,
) error {
	return (&RoleInstanceSetCheckerV2{ctx: s.ctx, client: s.client}).ExpectTopologyAffinityV2(rbg, role, topologyKey)
}

// ============================================================
// semanticallyEqualAffinityV2 — same logic as v1alpha1 but uses v1alpha2 label keys
// ============================================================

func semanticallyEqualAffinityV2(affinity *corev1.Affinity, topologyKey string, uniqueKey string) error {
	if affinity.PodAffinity == nil || affinity.PodAntiAffinity == nil {
		return fmt.Errorf("podAffinity or podAntiAffinity is nil")
	}
	for _, term := range affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
		if term.TopologyKey != topologyKey {
			return fmt.Errorf("PodAffinity.topologyKey not equal")
		}
		if term.LabelSelector == nil || term.LabelSelector.MatchExpressions == nil {
			return fmt.Errorf("PodAffinity.LabelSelector not set")
		}
		for _, expression := range term.LabelSelector.MatchExpressions {
			if expression.Key != constants.GroupUniqueHashLabelKey ||
				expression.Operator != v1.LabelSelectorOpIn ||
				expression.Values[0] != uniqueKey {
				return fmt.Errorf("PodAffinity.LabelSelector.MatchExpressions not equal")
			}
		}
	}
	for _, term := range affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
		if term.TopologyKey != topologyKey {
			return fmt.Errorf("PodAntiAffinity.topologyKey not equal")
		}
		if term.LabelSelector == nil || term.LabelSelector.MatchExpressions == nil {
			return fmt.Errorf("PodAntiAffinity.LabelSelector not set")
		}
		for _, expression := range term.LabelSelector.MatchExpressions {
			if expression.Key != constants.GroupUniqueHashLabelKey {
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

// ExpectWorkloadNotExistByName checks that a workload with the given name doesn't exist
// using NotFound error check.
func ExpectWorkloadNotExistByName(ctx context.Context, c client.Client, namespace, name string, obj client.Object) error {
	err := c.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, obj)
	if err == nil {
		return fmt.Errorf("workload %s/%s still exists", namespace, name)
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}
