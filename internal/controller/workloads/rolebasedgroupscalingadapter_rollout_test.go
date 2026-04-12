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

package workloads

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/wrappers"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

// newTestScheme creates a scheme with all types needed for RBGSA tests.
func newTestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha2.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	return scheme
}

// buildBoundRBGSA creates a bound RBGSA with the given desired and current replicas.
func buildBoundRBGSA(desiredReplicas int32) *workloadsv1alpha2.RoleBasedGroupScalingAdapter {
	return &workloadsv1alpha2.RoleBasedGroupScalingAdapter{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-adapter",
			Namespace:  "default",
			Generation: 1,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "workloads.x-k8s.io/v1alpha2",
					Kind:               "RoleBasedGroup",
					Name:               "test-rbg",
					UID:                "rbg-test-uid",
					BlockOwnerDeletion: ptr.To(true),
				},
			},
		},
		Spec: workloadsv1alpha2.RoleBasedGroupScalingAdapterSpec{
			Replicas: ptr.To(desiredReplicas),
			ScaleTargetRef: &workloadsv1alpha2.AdapterScaleTargetRef{
				Name: "test-rbg",
				Role: "worker",
			},
		},
		Status: workloadsv1alpha2.RoleBasedGroupScalingAdapterStatus{
			Phase:    constants.AdapterPhaseBound,
			Replicas: ptr.To(int32(10)),
		},
	}
}

// buildRBGWithRoleAndStatus creates an RBG with a single role of the given workload type
// and optional role status. scaleDownPolicy can be nil (default: DeferDuringRollout).
func buildRBGWithRoleAndStatus(
	workloadAPIVersion, workloadKind string,
	roleReplicas int32,
	roleStatuses []workloadsv1alpha2.RoleStatus,
	scaleDownPolicy *workloadsv1alpha2.ScaleDownPolicyType,
) *workloadsv1alpha2.RoleBasedGroup {
	role := wrappersv2.BuildStandaloneRole("worker").
		WithWorkload(workloadAPIVersion, workloadKind).
		WithReplicas(roleReplicas).
		WithScalingAdapter(true)
	roleSpec := role.Obj()
	if scaleDownPolicy != nil {
		roleSpec.ScalingAdapter.ScaleDownPolicy = scaleDownPolicy
	}
	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").
		WithRoles([]workloadsv1alpha2.RoleSpec{roleSpec}).Obj()
	if len(roleStatuses) > 0 {
		rbg.Status.RoleStatuses = roleStatuses
	}
	return rbg
}

// buildSTS creates a StatefulSet for the given RBG role.
func buildSTS(replicas int32) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg-worker",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "workloads.x-k8s.io/v1alpha2",
					Kind:               "RoleBasedGroup",
					Name:               "test-rbg",
					UID:                "rbg-test-uid",
					BlockOwnerDeletion: ptr.To(true),
				},
			},
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: ptr.To(replicas),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					constants.GroupNameLabelKey: "test-rbg",
					constants.RoleNameLabelKey:  "worker",
				},
			},
			Template: wrappers.BuildBasicPodTemplateSpec().Obj(),
		},
	}
}

// reconcileRBGSA runs a single reconciliation cycle for the RBGSA controller.
func reconcileRBGSA(t *testing.T, c client.Client, recorder *record.FakeRecorder) (ctrl.Result, error) {
	t.Helper()
	reconciler := &RoleBasedGroupScalingAdapterReconciler{
		client:   c,
		recorder: recorder,
	}
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-adapter",
			Namespace: "default",
		},
	}
	logger := zap.New().WithValues("env", "unit-test")
	ctx := ctrl.LoggerInto(context.TODO(), logger)
	return reconciler.Reconcile(ctx, req)
}

// getUpdatedRBGSA fetches the RBGSA after reconciliation.
func getUpdatedRBGSA(t *testing.T, c client.Client) *workloadsv1alpha2.RoleBasedGroupScalingAdapter {
	t.Helper()
	adapter := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{}
	err := c.Get(context.TODO(), types.NamespacedName{Name: "test-adapter", Namespace: "default"}, adapter)
	require.NoError(t, err)
	return adapter
}

// getUpdatedRBG fetches the RBG after reconciliation.
func getUpdatedRBG(t *testing.T, c client.Client) *workloadsv1alpha2.RoleBasedGroup {
	t.Helper()
	rbg := &workloadsv1alpha2.RoleBasedGroup{}
	err := c.Get(context.TODO(), types.NamespacedName{Name: "test-rbg", Namespace: "default"}, rbg)
	require.NoError(t, err)
	return rbg
}

func TestScaleDownDeferredDuringRollout(t *testing.T) {
	scheme := newTestScheme()

	tests := []struct {
		name string
		// Setup
		rbgsa              *workloadsv1alpha2.RoleBasedGroupScalingAdapter
		workloadAPIVersion string
		workloadKind       string
		scaleDownPolicy    *workloadsv1alpha2.ScaleDownPolicyType
		roleReplicas       int32
		roleStatuses       []workloadsv1alpha2.RoleStatus
		// Expectations
		expectDeferred       bool
		expectScaleProceeds  bool
		expectRequeueAfter   time.Duration
		expectConditionSet   bool
		expectWarningEvent   bool
		expectRBGReplicasSet *int32 // if scale proceeds, the RBG role should have this replicas value
	}{
		{
			name:               "scale-down during rollout on StatefulSet with DeferDuringRollout is deferred",
			rbgsa:              buildBoundRBGSA(6), // HPA wants 6, current is 10
			workloadAPIVersion: "apps/v1",
			workloadKind:       "StatefulSet",
			scaleDownPolicy:    ptr.To(workloadsv1alpha2.ScaleDownPolicyDeferDuringRollout),
			roleReplicas:       10,
			roleStatuses: []workloadsv1alpha2.RoleStatus{
				{Name: "worker", Replicas: 10, ReadyReplicas: 7, UpdatedReplicas: 3},
			},
			expectDeferred:     true,
			expectRequeueAfter: 30 * time.Second,
			expectConditionSet: true,
			expectWarningEvent: true,
		},
		{
			name:               "scale-down during rollout on LeaderWorkerSet with DeferDuringRollout is deferred",
			rbgsa:              buildBoundRBGSA(6),
			workloadAPIVersion: "leaderworkerset.x-k8s.io/v1",
			workloadKind:       "LeaderWorkerSet",
			scaleDownPolicy:    ptr.To(workloadsv1alpha2.ScaleDownPolicyDeferDuringRollout),
			roleReplicas:       10,
			roleStatuses: []workloadsv1alpha2.RoleStatus{
				{Name: "worker", Replicas: 10, ReadyReplicas: 7, UpdatedReplicas: 3},
			},
			expectDeferred:     true,
			expectRequeueAfter: 30 * time.Second,
			expectConditionSet: true,
			expectWarningEvent: true,
		},
		{
			name:               "scale-down during rollout on Deployment with DeferDuringRollout proceeds (not partition-based)",
			rbgsa:              buildBoundRBGSA(6),
			workloadAPIVersion: "apps/v1",
			workloadKind:       "Deployment",
			scaleDownPolicy:    ptr.To(workloadsv1alpha2.ScaleDownPolicyDeferDuringRollout),
			roleReplicas:       10,
			roleStatuses: []workloadsv1alpha2.RoleStatus{
				{Name: "worker", Replicas: 10, ReadyReplicas: 7, UpdatedReplicas: 3},
			},
			expectDeferred:       false,
			expectScaleProceeds:  true,
			expectRBGReplicasSet: ptr.To(int32(6)),
		},
		{
			name:               "scale-down during rollout on RoleInstanceSet with DeferDuringRollout is deferred",
			rbgsa:              buildBoundRBGSA(6),
			workloadAPIVersion: "workloads.x-k8s.io/v1alpha2",
			workloadKind:       "RoleInstanceSet",
			scaleDownPolicy:    ptr.To(workloadsv1alpha2.ScaleDownPolicyDeferDuringRollout),
			roleReplicas:       10,
			roleStatuses: []workloadsv1alpha2.RoleStatus{
				{Name: "worker", Replicas: 10, ReadyReplicas: 7, UpdatedReplicas: 3},
			},
			expectDeferred:     true,
			expectRequeueAfter: 30 * time.Second,
			expectConditionSet: true,
			expectWarningEvent: true,
		},
		{
			name:               "scale-down during rollout with default policy (nil) proceeds (backward compatible)",
			rbgsa:              buildBoundRBGSA(6),
			workloadAPIVersion: "apps/v1",
			workloadKind:       "StatefulSet",
			roleReplicas:       10,
			roleStatuses: []workloadsv1alpha2.RoleStatus{
				{Name: "worker", Replicas: 10, ReadyReplicas: 7, UpdatedReplicas: 3},
			},
			expectDeferred:       false,
			expectScaleProceeds:  true,
			expectRBGReplicasSet: ptr.To(int32(6)),
		},
		{
			name:               "scale-down when no rollout in progress proceeds normally",
			rbgsa:              buildBoundRBGSA(6),
			workloadAPIVersion: "apps/v1",
			workloadKind:       "StatefulSet",
			roleReplicas:       10,
			roleStatuses: []workloadsv1alpha2.RoleStatus{
				{Name: "worker", Replicas: 10, ReadyReplicas: 10, UpdatedReplicas: 10},
			},
			expectDeferred:       false,
			expectScaleProceeds:  true,
			expectRBGReplicasSet: ptr.To(int32(6)),
		},
		{
			name:               "scale-up during rollout on StatefulSet proceeds normally",
			rbgsa:              buildBoundRBGSA(15), // HPA wants 15, current is 10
			workloadAPIVersion: "apps/v1",
			workloadKind:       "StatefulSet",
			roleReplicas:       10,
			roleStatuses: []workloadsv1alpha2.RoleStatus{
				{Name: "worker", Replicas: 10, ReadyReplicas: 7, UpdatedReplicas: 3},
			},
			expectDeferred:       false,
			expectScaleProceeds:  true,
			expectRBGReplicasSet: ptr.To(int32(15)),
		},
		{
			name:                 "scale-down with role status not found proceeds (don't block on missing status)",
			rbgsa:                buildBoundRBGSA(6),
			workloadAPIVersion:   "apps/v1",
			workloadKind:         "StatefulSet",
			roleReplicas:         10,
			roleStatuses:         nil, // no role statuses
			expectDeferred:       false,
			expectScaleProceeds:  true,
			expectRBGReplicasSet: ptr.To(int32(6)),
		},
		{
			name:               "scale-down during initial creation (UpdatedReplicas=0) is deferred",
			rbgsa:              buildBoundRBGSA(6),
			workloadAPIVersion: "apps/v1",
			workloadKind:       "StatefulSet",
			scaleDownPolicy:    ptr.To(workloadsv1alpha2.ScaleDownPolicyDeferDuringRollout),
			roleReplicas:       10,
			roleStatuses: []workloadsv1alpha2.RoleStatus{
				{Name: "worker", Replicas: 10, ReadyReplicas: 0, UpdatedReplicas: 0},
			},
			expectDeferred:     true,
			expectRequeueAfter: 30 * time.Second,
			expectConditionSet: true,
			expectWarningEvent: true,
		},
		{
			name:               "scale-down with maxSurge active (Replicas=12 includes surge) is deferred",
			rbgsa:              buildBoundRBGSA(6),
			workloadAPIVersion: "apps/v1",
			workloadKind:       "StatefulSet",
			scaleDownPolicy:    ptr.To(workloadsv1alpha2.ScaleDownPolicyDeferDuringRollout),
			roleReplicas:       10,
			roleStatuses: []workloadsv1alpha2.RoleStatus{
				{Name: "worker", Replicas: 12, ReadyReplicas: 10, UpdatedReplicas: 8},
			},
			expectDeferred:     true,
			expectRequeueAfter: 30 * time.Second,
			expectConditionSet: true,
			expectWarningEvent: true,
		},
		{
			name:               "scale-down to zero during rollout is deferred",
			rbgsa:              buildBoundRBGSA(0),
			workloadAPIVersion: "apps/v1",
			workloadKind:       "StatefulSet",
			scaleDownPolicy:    ptr.To(workloadsv1alpha2.ScaleDownPolicyDeferDuringRollout),
			roleReplicas:       10,
			roleStatuses: []workloadsv1alpha2.RoleStatus{
				{Name: "worker", Replicas: 10, ReadyReplicas: 7, UpdatedReplicas: 3},
			},
			expectDeferred:     true,
			expectRequeueAfter: 30 * time.Second,
			expectConditionSet: true,
			expectWarningEvent: true,
		},
		{
			name:               "scale-down during rollout with Unrestricted policy proceeds",
			rbgsa:              buildBoundRBGSA(6),
			workloadAPIVersion: "apps/v1",
			workloadKind:       "StatefulSet",
			scaleDownPolicy:    ptr.To(workloadsv1alpha2.ScaleDownPolicyUnrestricted),
			roleReplicas:       10,
			roleStatuses: []workloadsv1alpha2.RoleStatus{
				{Name: "worker", Replicas: 10, ReadyReplicas: 7, UpdatedReplicas: 3},
			},
			expectDeferred:       false,
			expectScaleProceeds:  true,
			expectRBGReplicasSet: ptr.To(int32(6)),
		},
		{
			name:               "scale-down during rollout with explicit DeferDuringRollout policy is deferred",
			rbgsa:              buildBoundRBGSA(6),
			workloadAPIVersion: "apps/v1",
			workloadKind:       "StatefulSet",
			scaleDownPolicy:    ptr.To(workloadsv1alpha2.ScaleDownPolicyDeferDuringRollout),
			roleReplicas:       10,
			roleStatuses: []workloadsv1alpha2.RoleStatus{
				{Name: "worker", Replicas: 10, ReadyReplicas: 7, UpdatedReplicas: 3},
			},
			expectDeferred:     true,
			expectRequeueAfter: 30 * time.Second,
			expectConditionSet: true,
			expectWarningEvent: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rbg := buildRBGWithRoleAndStatus(tt.workloadAPIVersion, tt.workloadKind, tt.roleReplicas, tt.roleStatuses, tt.scaleDownPolicy)
			sts := buildSTS(tt.roleReplicas)

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.rbgsa, rbg, sts).
				WithStatusSubresource(tt.rbgsa).
				Build()
			recorder := record.NewFakeRecorder(100)

			result, err := reconcileRBGSA(t, fakeClient, recorder)
			require.NoError(t, err)

			if tt.expectDeferred {
				// Verify requeue
				assert.Equal(t, tt.expectRequeueAfter, result.RequeueAfter,
					"expected RequeueAfter=%v, got %v", tt.expectRequeueAfter, result.RequeueAfter)

				// Verify RBG replicas NOT changed
				updatedRBG := getUpdatedRBG(t, fakeClient)
				role, roleErr := updatedRBG.GetRole("worker")
				require.NoError(t, roleErr)
				assert.Equal(t, tt.roleReplicas, *role.Replicas,
					"RBG role replicas should be unchanged when scale-down is deferred")
			}

			if tt.expectScaleProceeds && tt.expectRBGReplicasSet != nil {
				// Verify RBG replicas changed
				updatedRBG := getUpdatedRBG(t, fakeClient)
				role, roleErr := updatedRBG.GetRole("worker")
				require.NoError(t, roleErr)
				assert.Equal(t, *tt.expectRBGReplicasSet, *role.Replicas,
					"RBG role replicas should be updated to desired value")

				// Verify no requeue (immediate completion)
				assert.Zero(t, result.RequeueAfter, "should not requeue when scale proceeds")
			}

			if tt.expectConditionSet {
				// Verify ScaleDownDeferred condition is set
				updatedAdapter := getUpdatedRBGSA(t, fakeClient)
				cond := apimeta.FindStatusCondition(updatedAdapter.Status.Conditions,
					workloadsv1alpha2.AdapterConditionScaleDownDeferred)
				require.NotNil(t, cond, "ScaleDownDeferred condition should be set")
				assert.Equal(t, metav1.ConditionTrue, cond.Status)
				assert.Equal(t, "RollingUpdateInProgress", cond.Reason)
				assert.Equal(t, updatedAdapter.Generation, cond.ObservedGeneration,
					"ObservedGeneration should match adapter's Generation")
			}

			if tt.expectWarningEvent {
				// Verify Warning event emitted
				select {
				case evt := <-recorder.Events:
					assert.Contains(t, evt, "Warning")
					assert.Contains(t, evt, ScaleDownDeferred)
				default:
					t.Error("expected a Warning ScaleDownDeferred event but none was emitted")
				}
			}
		})
	}
}

func TestScaleDownDeferredConditionLifecycle(t *testing.T) {
	scheme := newTestScheme()

	t.Run("condition removed after rollout completes and scale-down proceeds", func(t *testing.T) {
		// Setup: RBGSA has an existing ScaleDownDeferred condition (from prior deferral)
		rbgsa := buildBoundRBGSA(6)
		rbgsa.Status.Conditions = []metav1.Condition{
			{
				Type:               workloadsv1alpha2.AdapterConditionScaleDownDeferred,
				Status:             metav1.ConditionTrue,
				Reason:             "RollingUpdateInProgress",
				Message:            "scale-down deferred",
				LastTransitionTime: metav1.Now(),
				ObservedGeneration: 1,
			},
		}

		// RBG with rollout COMPLETED (UpdatedReplicas == Replicas)
		rbg := buildRBGWithRoleAndStatus("apps/v1", "StatefulSet", 10,
			[]workloadsv1alpha2.RoleStatus{
				{Name: "worker", Replicas: 10, ReadyReplicas: 10, UpdatedReplicas: 10},
			}, nil)
		sts := buildSTS(10)

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(rbgsa, rbg, sts).
			WithStatusSubresource(rbgsa).
			Build()
		recorder := record.NewFakeRecorder(100)

		result, err := reconcileRBGSA(t, fakeClient, recorder)
		require.NoError(t, err)

		// Scale should proceed (not deferred)
		assert.Zero(t, result.RequeueAfter, "should not requeue when scale proceeds")

		// RBG replicas should be updated
		updatedRBG := getUpdatedRBG(t, fakeClient)
		role, roleErr := updatedRBG.GetRole("worker")
		require.NoError(t, roleErr)
		assert.Equal(t, int32(6), *role.Replicas)
	})

	t.Run("HPA adjusts UP during deferral proceeds with scale-up", func(t *testing.T) {
		// Setup: RBGSA was deferred at 6, now HPA adjusts to 12 (scale-up)
		rbgsa := buildBoundRBGSA(12) // HPA now wants 12
		rbgsa.Status.Conditions = []metav1.Condition{
			{
				Type:               workloadsv1alpha2.AdapterConditionScaleDownDeferred,
				Status:             metav1.ConditionTrue,
				Reason:             "RollingUpdateInProgress",
				Message:            "previously deferred",
				LastTransitionTime: metav1.Now(),
				ObservedGeneration: 1,
			},
		}

		rbg := buildRBGWithRoleAndStatus("apps/v1", "StatefulSet", 10,
			[]workloadsv1alpha2.RoleStatus{
				{Name: "worker", Replicas: 10, ReadyReplicas: 7, UpdatedReplicas: 3},
			}, nil)
		sts := buildSTS(10)

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(rbgsa, rbg, sts).
			WithStatusSubresource(rbgsa).
			Build()
		recorder := record.NewFakeRecorder(100)

		result, err := reconcileRBGSA(t, fakeClient, recorder)
		require.NoError(t, err)

		// Scale-up should proceed (not deferred)
		assert.Zero(t, result.RequeueAfter)

		// RBG replicas should be updated to 12
		updatedRBG := getUpdatedRBG(t, fakeClient)
		role, roleErr := updatedRBG.GetRole("worker")
		require.NoError(t, roleErr)
		assert.Equal(t, int32(12), *role.Replicas)
	})

	t.Run("repeated requeue during deferral does not re-emit event", func(t *testing.T) {
		// Setup: RBGSA already has the condition set (simulating a previous requeue)
		rbgsa := buildBoundRBGSA(6)
		rbgsa.Status.Conditions = []metav1.Condition{
			{
				Type:               workloadsv1alpha2.AdapterConditionScaleDownDeferred,
				Status:             metav1.ConditionTrue,
				Reason:             "RollingUpdateInProgress",
				Message:            "previously deferred",
				LastTransitionTime: metav1.Now(),
				ObservedGeneration: 1,
			},
		}

		rbg := buildRBGWithRoleAndStatus("apps/v1", "StatefulSet", 10,
			[]workloadsv1alpha2.RoleStatus{
				{Name: "worker", Replicas: 10, ReadyReplicas: 7, UpdatedReplicas: 3},
			}, ptr.To(workloadsv1alpha2.ScaleDownPolicyDeferDuringRollout))
		sts := buildSTS(10)

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(rbgsa, rbg, sts).
			WithStatusSubresource(rbgsa).
			Build()
		recorder := record.NewFakeRecorder(100)

		result, err := reconcileRBGSA(t, fakeClient, recorder)
		require.NoError(t, err)

		// Should still be deferred
		assert.Equal(t, 30*time.Second, result.RequeueAfter)

		// Should NOT emit a Warning event (condition already existed)
		select {
		case evt := <-recorder.Events:
			t.Errorf("expected no event on repeated requeue, but got: %s", evt)
		default:
			// No event — correct
		}
	})

	t.Run("HPA adjusts to mid-value during deferral stays deferred", func(t *testing.T) {
		// RBGSA was deferred at 6, HPA now adjusts to 8 (still < current 10)
		rbgsa := buildBoundRBGSA(8) // HPA now wants 8
		rbgsa.Status.Conditions = []metav1.Condition{
			{
				Type:               workloadsv1alpha2.AdapterConditionScaleDownDeferred,
				Status:             metav1.ConditionTrue,
				Reason:             "RollingUpdateInProgress",
				Message:            "previously deferred",
				LastTransitionTime: metav1.Now(),
				ObservedGeneration: 1,
			},
		}

		rbg := buildRBGWithRoleAndStatus("apps/v1", "StatefulSet", 10,
			[]workloadsv1alpha2.RoleStatus{
				{Name: "worker", Replicas: 10, ReadyReplicas: 7, UpdatedReplicas: 3},
			}, ptr.To(workloadsv1alpha2.ScaleDownPolicyDeferDuringRollout))
		sts := buildSTS(10)

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(rbgsa, rbg, sts).
			WithStatusSubresource(rbgsa).
			Build()
		recorder := record.NewFakeRecorder(100)

		result, err := reconcileRBGSA(t, fakeClient, recorder)
		require.NoError(t, err)

		// Still deferred (8 < 10)
		assert.Equal(t, 30*time.Second, result.RequeueAfter)

		// RBG replicas should NOT change
		updatedRBG := getUpdatedRBG(t, fakeClient)
		role, roleErr := updatedRBG.GetRole("worker")
		require.NoError(t, roleErr)
		assert.Equal(t, int32(10), *role.Replicas,
			"RBG replicas should stay at 10 when mid-value scale-down is still deferred")

		// Condition should still be set
		updatedAdapter := getUpdatedRBGSA(t, fakeClient)
		cond := apimeta.FindStatusCondition(updatedAdapter.Status.Conditions,
			workloadsv1alpha2.AdapterConditionScaleDownDeferred)
		require.NotNil(t, cond, "ScaleDownDeferred condition should still be set")
		assert.Equal(t, metav1.ConditionTrue, cond.Status)
	})
}

func TestRBGStatusChangedPredicate(t *testing.T) {
	tests := []struct {
		name      string
		oldStatus workloadsv1alpha2.RoleBasedGroupStatus
		newStatus workloadsv1alpha2.RoleBasedGroupStatus
		expect    bool
	}{
		{
			name: "role status changed triggers predicate",
			oldStatus: workloadsv1alpha2.RoleBasedGroupStatus{
				RoleStatuses: []workloadsv1alpha2.RoleStatus{
					{Name: "worker", Replicas: 10, UpdatedReplicas: 3},
				},
			},
			newStatus: workloadsv1alpha2.RoleBasedGroupStatus{
				RoleStatuses: []workloadsv1alpha2.RoleStatus{
					{Name: "worker", Replicas: 10, UpdatedReplicas: 10},
				},
			},
			expect: true,
		},
		{
			name: "spec-only change does not trigger predicate",
			oldStatus: workloadsv1alpha2.RoleBasedGroupStatus{
				RoleStatuses: []workloadsv1alpha2.RoleStatus{
					{Name: "worker", Replicas: 10, UpdatedReplicas: 10},
				},
			},
			newStatus: workloadsv1alpha2.RoleBasedGroupStatus{
				RoleStatuses: []workloadsv1alpha2.RoleStatus{
					{Name: "worker", Replicas: 10, UpdatedReplicas: 10},
				},
			},
			expect: false,
		},
		{
			name:      "empty to populated status triggers predicate",
			oldStatus: workloadsv1alpha2.RoleBasedGroupStatus{},
			newStatus: workloadsv1alpha2.RoleBasedGroupStatus{
				RoleStatuses: []workloadsv1alpha2.RoleStatus{
					{Name: "worker", Replicas: 10, UpdatedReplicas: 3},
				},
			},
			expect: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pred := RBGRoleStatusPredicate()

			oldRBG := &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbg", Namespace: "default"},
				Status:     tt.oldStatus,
			}
			newRBG := &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbg", Namespace: "default"},
				Status:     tt.newStatus,
			}

			updateEvent := event.UpdateEvent{
				ObjectOld: oldRBG,
				ObjectNew: newRBG,
			}
			assert.Equal(t, tt.expect, pred.UpdateFunc(updateEvent))
		})
	}
}

func TestMapRBGToScalingAdaptersRollout(t *testing.T) {
	scheme := newTestScheme()

	t.Run("returns matching RBGSA for RBG", func(t *testing.T) {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("my-rbg", "default").Obj()

		matchingAdapter := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "adapter-1",
				Namespace: "default",
				Labels:    map[string]string{constants.GroupNameLabelKey: "my-rbg"},
			},
			Spec: workloadsv1alpha2.RoleBasedGroupScalingAdapterSpec{
				ScaleTargetRef: &workloadsv1alpha2.AdapterScaleTargetRef{
					Name: "my-rbg",
					Role: "worker",
				},
			},
		}
		nonMatchingAdapter := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "adapter-2",
				Namespace: "default",
				Labels:    map[string]string{constants.GroupNameLabelKey: "other-rbg"},
			},
			Spec: workloadsv1alpha2.RoleBasedGroupScalingAdapterSpec{
				ScaleTargetRef: &workloadsv1alpha2.AdapterScaleTargetRef{
					Name: "other-rbg",
					Role: "worker",
				},
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(rbg, matchingAdapter, nonMatchingAdapter).
			Build()

		reconciler := &RoleBasedGroupScalingAdapterReconciler{
			client: fakeClient,
		}

		requests := reconciler.mapRBGToScalingAdapters(context.TODO(), rbg)
		require.Len(t, requests, 1)
		assert.Equal(t, "adapter-1", requests[0].Name)
		assert.Equal(t, "default", requests[0].Namespace)
	})

	t.Run("returns empty when no RBGSA matches", func(t *testing.T) {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("my-rbg", "default").Obj()

		nonMatchingAdapter := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "adapter-2",
				Namespace: "default",
				Labels:    map[string]string{constants.GroupNameLabelKey: "other-rbg"},
			},
			Spec: workloadsv1alpha2.RoleBasedGroupScalingAdapterSpec{
				ScaleTargetRef: &workloadsv1alpha2.AdapterScaleTargetRef{
					Name: "other-rbg",
					Role: "worker",
				},
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(rbg, nonMatchingAdapter).
			Build()

		reconciler := &RoleBasedGroupScalingAdapterReconciler{
			client: fakeClient,
		}

		requests := reconciler.mapRBGToScalingAdapters(context.TODO(), rbg)
		assert.Empty(t, requests)
	})

	t.Run("returns multiple RBGSA for multi-role RBG", func(t *testing.T) {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("my-rbg", "default").Obj()

		adapter1 := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "adapter-worker",
				Namespace: "default",
				Labels:    map[string]string{constants.GroupNameLabelKey: "my-rbg"},
			},
			Spec: workloadsv1alpha2.RoleBasedGroupScalingAdapterSpec{
				ScaleTargetRef: &workloadsv1alpha2.AdapterScaleTargetRef{
					Name: "my-rbg",
					Role: "worker",
				},
			},
		}
		adapter2 := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "adapter-server",
				Namespace: "default",
				Labels:    map[string]string{constants.GroupNameLabelKey: "my-rbg"},
			},
			Spec: workloadsv1alpha2.RoleBasedGroupScalingAdapterSpec{
				ScaleTargetRef: &workloadsv1alpha2.AdapterScaleTargetRef{
					Name: "my-rbg",
					Role: "server",
				},
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(rbg, adapter1, adapter2).
			Build()

		reconciler := &RoleBasedGroupScalingAdapterReconciler{
			client: fakeClient,
		}

		requests := reconciler.mapRBGToScalingAdapters(context.TODO(), rbg)
		assert.Len(t, requests, 2)
	})
}
