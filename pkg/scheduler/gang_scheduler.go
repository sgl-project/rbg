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

package scheduler

import (
	"context"
	"fmt"
	"sync"

	coreapplyv1 "k8s.io/client-go/applyconfigurations/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/scheduler/common"
	kubeschedulerplugin "sigs.k8s.io/rbgs/pkg/scheduler/k8s-scheduler-plugin"
	volcanoplugin "sigs.k8s.io/rbgs/pkg/scheduler/volcano"
)

const (
	// KubePodGroupLabelKey is the pod label key used by the kube scheduler-plugins PodGroup.
	// Kept here for external consumers (e.g. e2e tests).
	KubePodGroupLabelKey = kubeschedulerplugin.LabelKey

	// KubePodGroupUpstreamLabelKey is the pod label key upstream scheduler-plugins
	// coscheduling reads. Kept here for external consumers (e.g. e2e tests).
	KubePodGroupUpstreamLabelKey = kubeschedulerplugin.UpstreamLabelKey

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

// GangScheduler encapsulates gang scheduling for a specific scheduler implementation.
// It manages both PodGroup lifecycle and pod-template field injection.
// Implementations are selected at controller startup based on the --scheduler-name flag.
type GangScheduler interface {
	// ReconcilePodGroup creates/updates/deletes the PodGroup for the given RBG.
	// gangStrategy is the resolved strategy returned by common.GetGangStrategy.
	//
	// The implementation decides internally:
	// - gangStrategy == nil -> gang disabled, delete any existing PodGroup
	// - gangStrategy.MinReplicas empty -> all-or-nothing gang over the roles the
	//   strategy covers, i.e. minMember = common.GangSize (the legacy annotation
	//   resolves to a strategy covering every role)
	// - gangStrategy.MinReplicas non-empty -> subGroupPolicy (if supported): each
	//   named role is held to its minimum, and covered roles absent from the map
	//   participate in full
	ReconcilePodGroup(
		ctx context.Context,
		rbg *workloadsv1alpha2.RoleBasedGroup,
		gangStrategy *common.GangStrategy,
		runtimeController *builder.TypedBuilder[reconcile.Request],
		watchedWorkload *sync.Map,
		apiReader client.Reader,
	) error

	// InjectPodSchedulingFields injects scheduler-specific fields into the pod template:
	// - the PodGroup annotation (Volcano) or label (scheduler-plugins) that ties the pod
	//   to its PodGroup, only for roles the gang covers
	// - pod.spec.schedulerName, for every role, so a single scheduler owns the whole
	//   group. Volcano sets it unconditionally ("volcano"); scheduler-plugins sets it
	//   only when a profile name was configured via --scheduler-profile-name, since its
	//   profile name is chosen at deploy time.
	//
	// gangStrategy is the resolved gang scheduling strategy for the RBG, as returned by
	// common.GetGangStrategy. It is nil when gang scheduling is not enabled, in which
	// case implementations must inject nothing.
	InjectPodSchedulingFields(
		rbg *workloadsv1alpha2.RoleBasedGroup,
		role *workloadsv1alpha2.RoleSpec,
		gangStrategy *common.GangStrategy,
		pts *coreapplyv1.PodTemplateSpecApplyConfiguration,
	)
}

// NewGangScheduler returns a GangScheduler for the given plugin type.
// schedulerProfileName is the kube-scheduler profile name running the coscheduling
// plugin; it only applies to KubeSchedulerPlugin and may be empty, in which case
// pod.spec.schedulerName is left untouched. Volcano ignores it because its scheduler
// name is a fixed contract.
// Returns an error if the plugin type is not supported.
func NewGangScheduler(schedulerName SchedulerPluginType, c client.Client, schedulerProfileName string) (GangScheduler, error) {
	switch schedulerName {
	case KubeSchedulerPlugin:
		return kubeschedulerplugin.New(c, schedulerProfileName), nil
	case VolcanoSchedulerPlugin:
		return volcanoplugin.New(c), nil
	default:
		return nil, fmt.Errorf("unsupported scheduler-name %q: supported values are %q and %q",
			schedulerName, KubeSchedulerPlugin, VolcanoSchedulerPlugin)
	}
}
