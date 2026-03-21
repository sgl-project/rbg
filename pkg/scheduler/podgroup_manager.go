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
// Returns an error if the plugin type is not supported.
func NewPodGroupManager(schedulerName SchedulerPluginType, c client.Client) (PodGroupManager, error) {
	switch schedulerName {
	case KubeSchedulerPlugin:
		return kubeschedulerplugin.New(c), nil
	case VolcanoSchedulerPlugin:
		return volcanoplugin.New(c), nil
	default:
		return nil, fmt.Errorf("unsupported scheduler-name %q: supported values are %q and %q",
			schedulerName, KubeSchedulerPlugin, VolcanoSchedulerPlugin)
	}
}
