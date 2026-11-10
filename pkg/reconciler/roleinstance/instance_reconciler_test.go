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

package roleinstance

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	synccontrol "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/sync"
	"sigs.k8s.io/rbgs/pkg/utils/fieldindex"
)

func TestGetOwnedPodsFiltersAuxiliaryReferencesBeforeScale(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	instance.Spec.ReadinessGates = nil
	instance.Status.ObservedGeneration = instance.Generation
	instance.Spec.Components = instance.Spec.Components[:1]
	instance.Spec.Components[0].Template.Spec.Containers = []v1.Container{{Name: "main", Image: "test"}}
	auxiliary := newReadinessTestPod(instance, "router")
	auxiliary.Name = "auxiliary-failed"
	auxiliary.UID = "auxiliary"
	auxiliary.OwnerReferences[0].Controller = ptr.To(false)
	auxiliary.Labels = map[string]string{constants.ComponentNameLabelKey: "router", constants.ComponentIDLabelKey: "0", constants.RoleInstanceNameLabelKey: instance.Name}
	auxiliary.Status.Phase = v1.PodFailed
	activeAuxiliary := auxiliary.DeepCopy()
	activeAuxiliary.Name = "auxiliary-running"
	activeAuxiliary.UID = "auxiliary-running"
	activeAuxiliary.Status.Phase = v1.PodRunning
	c := fake.NewClientBuilder().WithScheme(readinessTestScheme(t)).
		WithStatusSubresource(instance, &v1.Pod{}).WithObjects(instance, auxiliary, activeAuxiliary).
		WithIndex(&v1.Pod{}, fieldindex.IndexNameForOwnerRefUID, func(obj client.Object) []string {
			uids := make([]string, 0, len(obj.GetOwnerReferences()))
			for _, ref := range obj.GetOwnerReferences() {
				uids = append(uids, string(ref.UID))
			}
			return uids
		}).Build()
	r := &reconciler{Client: c}
	active, inactive, err := r.getOwnedPods(ctx, instance)
	require.NoError(t, err)
	assert.Empty(t, active)
	assert.Empty(t, inactive)
	ctrl := synccontrol.New(c, c, record.NewFakeRecorder(10), nil)
	revision := &apps.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: "revision-1"}}
	scaled, _, err := ctrl.Scale(ctx, instance, revision, revision, nil, active, inactive)
	require.NoError(t, err)
	assert.True(t, scaled, "auxiliary Failed Pod must not divert Scale into refusing deletion")
	member := &v1.Pod{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(newReadinessTestPod(instance, "router")), member))
	assert.True(t, metav1.IsControlledBy(member, instance))
	for _, pod := range []*v1.Pod{auxiliary, activeAuxiliary} {
		untouched := &v1.Pod{}
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pod), untouched))
		assert.Equal(t, pod.UID, untouched.UID)
		assert.Nil(t, untouched.DeletionTimestamp)
	}
}
