/*
Copyright 2025.

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

package reconciler

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/utils/ptr"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

func TestSegmentScheduler_CalculateTargetReplicas(t *testing.T) {
	tests := []struct {
		name             string
		rbg              *workloadsv1alpha1.RoleBasedGroup
		coordination     *workloadsv1alpha1.Coordination
		roleStatuses     []workloadsv1alpha1.RoleStatus
		expectedTargets  map[string]int32
		expectedComplete bool
		expectError      bool
	}{
		{
			name: "Initial deployment - first segment",
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{Name: "prefill", Replicas: ptr.To(int32(300))},
						{Name: "decode", Replicas: ptr.To(int32(100))},
					},
				},
			},
			coordination: &workloadsv1alpha1.Coordination{
				Strategy: &workloadsv1alpha1.CoordinationStrategy{
					SegmentScheduling: &workloadsv1alpha1.SegmentScheduling{
						Segment: map[string]int{
							"prefill": 4,
							"decode":  2,
						},
						PartitionStrategy: workloadsv1alpha1.SegmentPartitionStrategySequentialWait,
					},
				},
			},
			roleStatuses: []workloadsv1alpha1.RoleStatus{
				{Name: "prefill", Replicas: 0, ReadyReplicas: 0},
				{Name: "decode", Replicas: 0, ReadyReplicas: 0},
			},
			expectedTargets: map[string]int32{
				"prefill": 4,
				"decode":  2,
			},
			expectedComplete: false,
		},
		{
			name: "Segment ready - proceed to next segment",
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{Name: "prefill", Replicas: ptr.To(int32(300))},
						{Name: "decode", Replicas: ptr.To(int32(100))},
					},
				},
			},
			coordination: &workloadsv1alpha1.Coordination{
				Strategy: &workloadsv1alpha1.CoordinationStrategy{
					SegmentScheduling: &workloadsv1alpha1.SegmentScheduling{
						Segment: map[string]int{
							"prefill": 4,
							"decode":  2,
						},
						PartitionStrategy: workloadsv1alpha1.SegmentPartitionStrategySequentialWait,
					},
				},
			},
			roleStatuses: []workloadsv1alpha1.RoleStatus{
				{Name: "prefill", Replicas: 4, ReadyReplicas: 4},
				{Name: "decode", Replicas: 2, ReadyReplicas: 2},
			},
			expectedTargets: map[string]int32{
				"prefill": 8, // Segment #1 ready, proceed to #2
				"decode":  4,
			},
			expectedComplete: false,
		},
		{
			name: "SequentialWait - prefill ready but decode not ready, wait",
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{Name: "prefill", Replicas: ptr.To(int32(300))},
						{Name: "decode", Replicas: ptr.To(int32(100))},
					},
				},
			},
			coordination: &workloadsv1alpha1.Coordination{
				Strategy: &workloadsv1alpha1.CoordinationStrategy{
					SegmentScheduling: &workloadsv1alpha1.SegmentScheduling{
						Segment: map[string]int{
							"prefill": 4,
							"decode":  2,
						},
						PartitionStrategy: workloadsv1alpha1.SegmentPartitionStrategySequentialWait,
					},
				},
			},
			roleStatuses: []workloadsv1alpha1.RoleStatus{
				{Name: "prefill", Replicas: 4, ReadyReplicas: 4}, // Prefill ready
				{Name: "decode", Replicas: 2, ReadyReplicas: 1},  // Decode NOT ready
			},
			expectedTargets: map[string]int32{
				"prefill": 4, // Wait for decode, keep current
				"decode":  2,
			},
			expectedComplete: false,
		},
		{
			name: "SequentialWait - decode ready but prefill not ready, wait",
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{Name: "prefill", Replicas: ptr.To(int32(300))},
						{Name: "decode", Replicas: ptr.To(int32(100))},
					},
				},
			},
			coordination: &workloadsv1alpha1.Coordination{
				Strategy: &workloadsv1alpha1.CoordinationStrategy{
					SegmentScheduling: &workloadsv1alpha1.SegmentScheduling{
						Segment: map[string]int{
							"prefill": 4,
							"decode":  2,
						},
						PartitionStrategy: workloadsv1alpha1.SegmentPartitionStrategySequentialWait,
					},
				},
			},
			roleStatuses: []workloadsv1alpha1.RoleStatus{
				{Name: "prefill", Replicas: 4, ReadyReplicas: 2}, // Prefill NOT ready
				{Name: "decode", Replicas: 2, ReadyReplicas: 2},  // Decode ready
			},
			expectedTargets: map[string]int32{
				"prefill": 4, // Wait for prefill, keep current
				"decode":  2,
			},
			expectedComplete: false,
		},
		{
			name: "Sequential strategy - don't wait for ready",
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{Name: "prefill", Replicas: ptr.To(int32(300))},
						{Name: "decode", Replicas: ptr.To(int32(100))},
					},
				},
			},
			coordination: &workloadsv1alpha1.Coordination{
				Strategy: &workloadsv1alpha1.CoordinationStrategy{
					SegmentScheduling: &workloadsv1alpha1.SegmentScheduling{
						Segment: map[string]int{
							"prefill": 4,
							"decode":  2,
						},
						PartitionStrategy: workloadsv1alpha1.SegmentPartitionStrategySequential,
					},
				},
			},
			roleStatuses: []workloadsv1alpha1.RoleStatus{
				{Name: "prefill", Replicas: 4, ReadyReplicas: 2}, // Not all ready
				{Name: "decode", Replicas: 2, ReadyReplicas: 1},  // Not all ready
			},
			expectedTargets: map[string]int32{
				"prefill": 8, // Proceed anyway with Sequential
				"decode":  4,
			},
			expectedComplete: false,
		},
		{
			name: "Decode reaches max first, prefill continues in same segment",
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{Name: "prefill", Replicas: ptr.To(int32(300))},
						{Name: "decode", Replicas: ptr.To(int32(100))},
					},
				},
			},
			coordination: &workloadsv1alpha1.Coordination{
				Strategy: &workloadsv1alpha1.CoordinationStrategy{
					SegmentScheduling: &workloadsv1alpha1.SegmentScheduling{
						Segment: map[string]int{
							"prefill": 4,
							"decode":  2,
						},
						PartitionStrategy: workloadsv1alpha1.SegmentPartitionStrategySequentialWait,
					},
				},
			},
			roleStatuses: []workloadsv1alpha1.RoleStatus{
				{Name: "prefill", Replicas: 96, ReadyReplicas: 96},
				{Name: "decode", Replicas: 100, ReadyReplicas: 100}, // Already at max
			},
			expectedTargets: map[string]int32{
				"prefill": 100, // Current segment is min(96/4, 100/2) = min(24, 50) = 24, next is 25
				"decode":  100, // Cap at max
			},
			expectedComplete: false,
		},
		{
			name: "All replicas deployed and ready",
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{Name: "prefill", Replicas: ptr.To(int32(300))},
						{Name: "decode", Replicas: ptr.To(int32(100))},
					},
				},
			},
			coordination: &workloadsv1alpha1.Coordination{
				Strategy: &workloadsv1alpha1.CoordinationStrategy{
					SegmentScheduling: &workloadsv1alpha1.SegmentScheduling{
						Segment: map[string]int{
							"prefill": 4,
							"decode":  2,
						},
						PartitionStrategy: workloadsv1alpha1.SegmentPartitionStrategySequentialWait,
					},
				},
			},
			roleStatuses: []workloadsv1alpha1.RoleStatus{
				{Name: "prefill", Replicas: 300, ReadyReplicas: 300},
				{Name: "decode", Replicas: 100, ReadyReplicas: 100},
			},
			expectedTargets: map[string]int32{
				"prefill": 300,
				"decode":  100,
			},
			expectedComplete: true,
		},
		{
			name: "User reduced replicas during deployment",
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{Name: "prefill", Replicas: ptr.To(int32(50))},
						{Name: "decode", Replicas: ptr.To(int32(20))},
					},
				},
			},
			coordination: &workloadsv1alpha1.Coordination{
				Strategy: &workloadsv1alpha1.CoordinationStrategy{
					SegmentScheduling: &workloadsv1alpha1.SegmentScheduling{
						Segment: map[string]int{
							"prefill": 4,
							"decode":  2,
						},
						PartitionStrategy: workloadsv1alpha1.SegmentPartitionStrategySequentialWait,
					},
				},
			},
			roleStatuses: []workloadsv1alpha1.RoleStatus{
				{Name: "prefill", Replicas: 100, ReadyReplicas: 100},
				{Name: "decode", Replicas: 40, ReadyReplicas: 40},
			},
			expectedTargets: map[string]int32{
				"prefill": 50, // Cap at desired replicas
				"decode":  20,
			},
			expectedComplete: false,
		},
		{
			name: "No segment scheduling configured",
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{Name: "prefill", Replicas: ptr.To(int32(10))},
					},
				},
			},
			coordination: &workloadsv1alpha1.Coordination{
				Strategy: &workloadsv1alpha1.CoordinationStrategy{},
			},
			roleStatuses: []workloadsv1alpha1.RoleStatus{
				{Name: "prefill", Replicas: 10, ReadyReplicas: 10},
			},
			expectedTargets:  nil,
			expectedComplete: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheduler := NewSegmentScheduler(tt.rbg, tt.coordination, tt.roleStatuses)

			// Test CalculateTargetReplicas
			targets, err := scheduler.CalculateTargetReplicas()

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.expectedTargets, targets)

			// Test IsSegmentSchedulingComplete
			complete := scheduler.IsSegmentSchedulingComplete()
			assert.Equal(t, tt.expectedComplete, complete)
		})
	}
}

func TestSegmentScheduler_IncrementalGrowth(t *testing.T) {
	// Test the segment-based deployment with coordinated progression across roles
	rbg := &workloadsv1alpha1.RoleBasedGroup{
		Spec: workloadsv1alpha1.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha1.RoleSpec{
				{Name: "prefill", Replicas: ptr.To(int32(300))},
				{Name: "decode", Replicas: ptr.To(int32(100))},
			},
		},
	}

	coordination := &workloadsv1alpha1.Coordination{
		Strategy: &workloadsv1alpha1.CoordinationStrategy{
			SegmentScheduling: &workloadsv1alpha1.SegmentScheduling{
				Segment: map[string]int{
					"prefill": 4,
					"decode":  2,
				},
				PartitionStrategy: workloadsv1alpha1.SegmentPartitionStrategySequentialWait,
			},
		},
	}

	// Iteration 1: Initial deployment - Segment #0 -> #1
	roleStatuses := []workloadsv1alpha1.RoleStatus{
		{Name: "prefill", Replicas: 0, ReadyReplicas: 0},
		{Name: "decode", Replicas: 0, ReadyReplicas: 0},
	}
	scheduler := NewSegmentScheduler(rbg, coordination, roleStatuses)
	targets, _ := scheduler.CalculateTargetReplicas()
	assert.Equal(t, int32(4), targets["prefill"])
	assert.Equal(t, int32(2), targets["decode"])

	// Iteration 2: Prefill ready but decode not - wait for whole segment
	roleStatuses = []workloadsv1alpha1.RoleStatus{
		{Name: "prefill", Replicas: 4, ReadyReplicas: 4},
		{Name: "decode", Replicas: 2, ReadyReplicas: 1}, // NOT ready
	}
	scheduler = NewSegmentScheduler(rbg, coordination, roleStatuses)
	targets, _ = scheduler.CalculateTargetReplicas()
	assert.Equal(t, int32(4), targets["prefill"]) // Wait, keep current
	assert.Equal(t, int32(2), targets["decode"])  // Wait, keep current

	// Iteration 3: Segment #1 fully ready - proceed to Segment #2
	roleStatuses = []workloadsv1alpha1.RoleStatus{
		{Name: "prefill", Replicas: 4, ReadyReplicas: 4},
		{Name: "decode", Replicas: 2, ReadyReplicas: 2},
	}
	scheduler = NewSegmentScheduler(rbg, coordination, roleStatuses)
	targets, _ = scheduler.CalculateTargetReplicas()
	assert.Equal(t, int32(8), targets["prefill"]) // Segment #2: 2 * 4
	assert.Equal(t, int32(4), targets["decode"])  // Segment #2: 2 * 2

	// Iteration 4: Decode ready but prefill not - wait for whole segment
	roleStatuses = []workloadsv1alpha1.RoleStatus{
		{Name: "prefill", Replicas: 8, ReadyReplicas: 6}, // NOT ready
		{Name: "decode", Replicas: 4, ReadyReplicas: 4},
	}
	scheduler = NewSegmentScheduler(rbg, coordination, roleStatuses)
	targets, _ = scheduler.CalculateTargetReplicas()
	assert.Equal(t, int32(8), targets["prefill"]) // Wait for prefill
	assert.Equal(t, int32(4), targets["decode"])  // Wait even though decode is ready

	// Iteration 5: Segment #2 fully ready - proceed to Segment #3
	roleStatuses = []workloadsv1alpha1.RoleStatus{
		{Name: "prefill", Replicas: 8, ReadyReplicas: 8},
		{Name: "decode", Replicas: 4, ReadyReplicas: 4},
	}
	scheduler = NewSegmentScheduler(rbg, coordination, roleStatuses)
	targets, _ = scheduler.CalculateTargetReplicas()
	assert.Equal(t, int32(12), targets["prefill"]) // Segment #3: 3 * 4
	assert.Equal(t, int32(6), targets["decode"])   // Segment #3: 3 * 2

	// Iteration 6: Decode reaches max, but segment continues together
	roleStatuses = []workloadsv1alpha1.RoleStatus{
		{Name: "prefill", Replicas: 196, ReadyReplicas: 196},
		{Name: "decode", Replicas: 98, ReadyReplicas: 98},
	}
	scheduler = NewSegmentScheduler(rbg, coordination, roleStatuses)
	targets, _ = scheduler.CalculateTargetReplicas()
	// Current segment: min(196/4, 98/2) = min(49, 49) = 49, next is 50
	assert.Equal(t, int32(200), targets["prefill"]) // Segment #50: 50 * 4
	assert.Equal(t, int32(100), targets["decode"])  // Segment #50: 50 * 2 (capped at max)

	// Iteration 7: Decode at max, prefill continues
	roleStatuses = []workloadsv1alpha1.RoleStatus{
		{Name: "prefill", Replicas: 200, ReadyReplicas: 200},
		{Name: "decode", Replicas: 100, ReadyReplicas: 100}, // At max
	}
	scheduler = NewSegmentScheduler(rbg, coordination, roleStatuses)
	targets, _ = scheduler.CalculateTargetReplicas()
	// Current segment: min(200/4, 100/2) = min(50, 50) = 50, next is 51
	assert.Equal(t, int32(204), targets["prefill"]) // Segment #51: 51 * 4
	assert.Equal(t, int32(100), targets["decode"])  // Stay at max

	// Iteration 8: All complete
	roleStatuses = []workloadsv1alpha1.RoleStatus{
		{Name: "prefill", Replicas: 300, ReadyReplicas: 300},
		{Name: "decode", Replicas: 100, ReadyReplicas: 100},
	}
	scheduler = NewSegmentScheduler(rbg, coordination, roleStatuses)
	targets, _ = scheduler.CalculateTargetReplicas()
	assert.Equal(t, int32(300), targets["prefill"])
	assert.Equal(t, int32(100), targets["decode"])
	assert.True(t, scheduler.IsSegmentSchedulingComplete())
}
