package scheduler

import (
	"context"
	"sync"

	coreapplyv1 "k8s.io/client-go/applyconfigurations/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	kubeschedulerplugin "sigs.k8s.io/rbgs/pkg/scheduler/k8s-scheduler-plugin"
	volcanoplugin "sigs.k8s.io/rbgs/pkg/scheduler/volcano"
)

const (
	// KubePodGroupLabelKey is the pod label key used by the kube scheduler-plugins PodGroup.
	// Kept here for external consumers (e.g. e2e tests).
	KubePodGroupLabelKey = kubeschedulerplugin.LabelKey

	// VolcanoPodGroupAnnotationKey is the pod annotation key used by Volcano PodGroup.
	// Kept here for external consumers (e.g. e2e tests).
	VolcanoPodGroupAnnotationKey = volcanoplugin.AnnotationKey

	// KubePodGroupCrdName is the CRD name for the kube scheduler-plugins PodGroup.
	// Kept here for external consumers (e.g. controller SetupWithManager).
	KubePodGroupCrdName = kubeschedulerplugin.CrdName

	// VolcanoPodGroupCrdName is the CRD name for the Volcano PodGroup.
	// Kept here for external consumers (e.g. controller SetupWithManager).
	VolcanoPodGroupCrdName = volcanoplugin.CrdName
)

// SchedulerPluginType defines the supported scheduler plugin types.
type SchedulerPluginType string

const (
	// KubeSchedulerPlugin uses the Kubernetes scheduler-plugins PodGroup.
	KubeSchedulerPlugin SchedulerPluginType = "scheduler-plugins"

	// VolcanoSchedulerPlugin uses the Volcano PodGroup.
	VolcanoSchedulerPlugin SchedulerPluginType = "volcano"
)

// PodGroupManager is the interface for managing PodGroups in gang scheduling scenarios.
// Implementations are selected at controller startup based on the --scheduler-name flag.
type PodGroupManager interface {
	// ReconcilePodGroup creates, updates, or deletes the PodGroup for the given RBG
	// based on the gang-scheduling annotation.
	ReconcilePodGroup(
		ctx context.Context,
		rbg *workloadsv1alpha2.RoleBasedGroup,
		runtimeController *builder.TypedBuilder[reconcile.Request],
		watchedWorkload *sync.Map,
		apiReader client.Reader,
	) error

	// InjectPodGroupLabels injects the scheduler-specific pod labels/annotations
	// required for the pod to join the PodGroup.
	InjectPodGroupLabels(rbg *workloadsv1alpha2.RoleBasedGroup, pts *coreapplyv1.PodTemplateSpecApplyConfiguration)
}

// NewPodGroupManager returns a PodGroupManager for the given plugin type.
func NewPodGroupManager(plugin SchedulerPluginType, c client.Client) PodGroupManager {
	switch plugin {
	case VolcanoSchedulerPlugin:
		return volcanoplugin.New(c)
	default:
		return kubeschedulerplugin.New(c)
	}
}
