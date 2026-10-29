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
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func TestTransientFailureConditionsClearAfterRecovery(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	instance.Spec.Components = instance.Spec.Components[:1]
	pod := newReadinessTestPod(instance, "router")
	pod.Status.Phase = v1.PodRunning
	pod.Status.Conditions[0].Status = v1.ConditionTrue
	instance.Status.Conditions = []workloadsv1alpha2.RoleInstanceCondition{
		{
			Type:   workloadsv1alpha2.RoleInstanceInPlaceUpdateReady,
			Status: v1.ConditionTrue,
		},
		{
			Type:   workloadsv1alpha2.RoleInstanceFailedScale,
			Status: v1.ConditionTrue,
		},
		{
			Type:   workloadsv1alpha2.RoleInstanceFailedUpdate,
			Status: v1.ConditionTrue,
		},
		{
			Type:   workloadsv1alpha2.RoleInstanceCustomReady,
			Status: v1.ConditionTrue,
		},
	}

	c := fake.NewClientBuilder().WithScheme(readinessTestScheme(t)).
		WithStatusSubresource(&workloadsv1alpha2.RoleInstance{}, &v1.Pod{}).
		WithObjects(instance, pod).Build()
	updater := newStatusUpdater(c)

	current := &workloadsv1alpha2.RoleInstance{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(instance), current))
	require.NoError(t, updater.UpdateInstanceStatus(ctx, current, &workloadsv1alpha2.RoleInstanceStatus{}, []*v1.Pod{pod}))
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(instance), current))

	assertCondition := func(conditionType workloadsv1alpha2.RoleInstanceConditionType, want bool) {
		t.Helper()
		found := false
		for _, condition := range current.Status.Conditions {
			if condition.Type == conditionType {
				found = true
				break
			}
		}
		assert.Equal(t, want, found, "unexpected presence of condition %s", conditionType)
	}

	assertCondition(workloadsv1alpha2.RoleInstanceFailedScale, false)
	assertCondition(workloadsv1alpha2.RoleInstanceFailedUpdate, false)
	assertCondition(workloadsv1alpha2.RoleInstanceCustomReady, true)
	assertCondition(workloadsv1alpha2.RoleInstanceReady, true)
}
