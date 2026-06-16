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

package sync

import (
	"context"
	"sync"
	"time"

	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/inplaceupdate"
	"sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/lifecycle"
)

type Interface interface {
	Scale(ctx context.Context, updateInstance *workloadsv1alpha2.RoleInstance, currentRevision, updateRevision *apps.ControllerRevision, revisions []*apps.ControllerRevision, pods []*v1.Pod, inactivePods []*v1.Pod) (bool, error)
	Update(ctx context.Context, instance *workloadsv1alpha2.RoleInstance, newStatus *workloadsv1alpha2.RoleInstanceStatus, currentRevision, updateRevision *apps.ControllerRevision, revisions []*apps.ControllerRevision, pods []*v1.Pod) (time.Duration, error)
	// ClearRestarting removes the instance from the in-memory restarting cache.
	// Called when the instance transitions to Ready.
	ClearRestarting(instance *workloadsv1alpha2.RoleInstance)
}

// restartingCache tracks instances currently undergoing restart-policy recreation.
// This in-memory cache provides immediate visibility that doesn't depend on the
// informer cache catching up with the latest status write.
var restartingCache sync.Map

// nodeBindings is the package-level node binding store shared across all
// RoleInstance reconcile loops. It uses a two-level map keyed by RBG UID,
// enabling O(1) eviction of all bindings when an RBG is deleted via
// EvictByUID (called from the RBG delete event handler).
var nodeBindings = NewNodeBindingStore()

type realControl struct {
	client.Client
	apiReader        client.Reader
	inplaceControl   inplaceupdate.Interface
	recorder         record.EventRecorder
	lifecycleControl lifecycle.Interface
}

// EvictNodeBindings removes all in-place scheduling bindings for the
// given RBG UID. Intended to be called from the RBG delete event handler
// so that bindings are cleaned up immediately when an RBG is deleted.
func EvictNodeBindings(uid string) {
	nodeBindings.EvictByUID(uid)
}

func New(c client.Client, apiReader client.Reader, recorder record.EventRecorder) Interface {
	return &realControl{
		Client:           c,
		apiReader:        apiReader,
		inplaceControl:   inplaceupdate.New(c),
		lifecycleControl: lifecycle.New(c),
		recorder:         recorder,
	}
}
