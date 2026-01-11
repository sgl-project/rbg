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

package segmentscheduling

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/utils/ptr"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

func TestCalculateSegmentTargetReplicas_MultiCoordination(t *testing.T) {
	tests := []struct {
		name             string
		rbg              *workloadsv1alpha1.RoleBasedGroup
		roleStatuses     []workloadsv1alpha1.RoleStatus
		expectedTargets  map[string]int32
		expectedError    bool
		expectedErrorMsg string
	}{
		{
			name: "No conflict - different roles in different coordinations",
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{Name: "prefill", Replicas: ptr.To(int32(300))},
						{Name: "decode", Replicas: ptr.To(int32(100))},
						{Name: "router", Replicas: ptr.To(int32(50))},
					},
					CoordinationRequirements: []workloadsv1alpha1.Coordination{
						{
							Strategy: &workloadsv1alpha1.CoordinationStrategy{
								SegmentScheduling: &workloadsv1alpha1.SegmentScheduling{
									SegmentSize: map[string]int32{
										"prefill": 4,
										"decode":  2,
									},
									PartitionStrategy: workloadsv1alpha1.SegmentPartitionStrategySequentialWait,
								},
							},
						},
						{
							Strategy: &workloadsv1alpha1.CoordinationStrategy{
								SegmentScheduling: &workloadsv1alpha1.SegmentScheduling{
									SegmentSize: map[string]int32{
										"router": 1,
									},
									PartitionStrategy: workloadsv1alpha1.SegmentPartitionStrategySequentialWait,
								},
							},
						},
					},
				},
			},
			roleStatuses: []workloadsv1alpha1.RoleStatus{
				{Name: "prefill", Replicas: 0, ReadyReplicas: 0},
				{Name: "decode", Replicas: 0, ReadyReplicas: 0},
				{Name: "router", Replicas: 0, ReadyReplicas: 0},
			},
			expectedTargets: map[string]int32{
				"prefill": 4,
				"decode":  2,
				"router":  1,
			},
			expectedError: false,
		},
		{
			name: "Conflict - same role with different segment sizes",
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{Name: "prefill", Replicas: ptr.To(int32(300))},
						{Name: "decode", Replicas: ptr.To(int32(100))},
					},
					CoordinationRequirements: []workloadsv1alpha1.Coordination{
						{
							Strategy: &workloadsv1alpha1.CoordinationStrategy{
								SegmentScheduling: &workloadsv1alpha1.SegmentScheduling{
									SegmentSize: map[string]int32{
										"prefill": 4,
										"decode":  2,
									},
								},
							},
						},
						{
							Strategy: &workloadsv1alpha1.CoordinationStrategy{
								SegmentScheduling: &workloadsv1alpha1.SegmentScheduling{
									SegmentSize: map[string]int32{
										"prefill": 2, // Conflict: different size for prefill
										"decode":  4, // Conflict: different size for decode
									},
								},
							},
						},
					},
				},
			},
			roleStatuses: []workloadsv1alpha1.RoleStatus{
				{Name: "prefill", Replicas: 0, ReadyReplicas: 0},
				{Name: "decode", Replicas: 0, ReadyReplicas: 0},
			},
			expectedError:    true,
			expectedErrorMsg: "segment size conflict for role",
		},
		{
			name: "Conflict - same role with different partition strategies",
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{Name: "prefill", Replicas: ptr.To(int32(300))},
						{Name: "decode", Replicas: ptr.To(int32(100))},
					},
					CoordinationRequirements: []workloadsv1alpha1.Coordination{
						{
							Strategy: &workloadsv1alpha1.CoordinationStrategy{
								SegmentScheduling: &workloadsv1alpha1.SegmentScheduling{
									SegmentSize: map[string]int32{
										"prefill": 4,
										"decode":  2,
									},
									PartitionStrategy: workloadsv1alpha1.SegmentPartitionStrategySequentialWait,
								},
							},
						},
						{
							Strategy: &workloadsv1alpha1.CoordinationStrategy{
								SegmentScheduling: &workloadsv1alpha1.SegmentScheduling{
									SegmentSize: map[string]int32{
										"prefill": 4, // Same size as first coordination
										"decode":  2, // Same size as first coordination
									},
									PartitionStrategy: workloadsv1alpha1.SegmentPartitionStrategySequential, // Conflict: different strategy
								},
							},
						},
					},
				},
			},
			roleStatuses: []workloadsv1alpha1.RoleStatus{
				{Name: "prefill", Replicas: 0, ReadyReplicas: 0},
				{Name: "decode", Replicas: 0, ReadyReplicas: 0},
			},
			expectedError:    true,
			expectedErrorMsg: "partition strategy conflict for role",
		},
		{
			name: "Partial overlap - one role in both coordinations with same size",
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{Name: "prefill", Replicas: ptr.To(int32(300))},
						{Name: "decode", Replicas: ptr.To(int32(100))},
						{Name: "router", Replicas: ptr.To(int32(50))},
					},
					CoordinationRequirements: []workloadsv1alpha1.Coordination{
						{
							Strategy: &workloadsv1alpha1.CoordinationStrategy{
								SegmentScheduling: &workloadsv1alpha1.SegmentScheduling{
									SegmentSize: map[string]int32{
										"prefill": 5,
										"decode":  3,
									},
									PartitionStrategy: workloadsv1alpha1.SegmentPartitionStrategySequentialWait,
								},
							},
						},
						{
							Strategy: &workloadsv1alpha1.CoordinationStrategy{
								SegmentScheduling: &workloadsv1alpha1.SegmentScheduling{
									SegmentSize: map[string]int32{
										"decode": 3, // Same size as first coordination
										"router": 1,
									},
									PartitionStrategy: workloadsv1alpha1.SegmentPartitionStrategySequentialWait,
								},
							},
						},
					},
				},
			},
			roleStatuses: []workloadsv1alpha1.RoleStatus{
				{Name: "prefill", Replicas: 0, ReadyReplicas: 0},
				{Name: "decode", Replicas: 0, ReadyReplicas: 0},
				{Name: "router", Replicas: 0, ReadyReplicas: 0},
			},
			expectedTargets: map[string]int32{
				"prefill": 5,
				"decode":  3,
				"router":  1,
			},
			expectedError: false,
		},
		{
			name: "Complex scenario - multiple roles, partial ready",
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{Name: "prefill", Replicas: ptr.To(int32(300))},
						{Name: "decode", Replicas: ptr.To(int32(100))},
						{Name: "router", Replicas: ptr.To(int32(50))},
					},
					CoordinationRequirements: []workloadsv1alpha1.Coordination{
						{
							Strategy: &workloadsv1alpha1.CoordinationStrategy{
								SegmentScheduling: &workloadsv1alpha1.SegmentScheduling{
									SegmentSize: map[string]int32{
										"prefill": 5,
										"decode":  3,
									},
									PartitionStrategy: workloadsv1alpha1.SegmentPartitionStrategySequentialWait,
								},
							},
						},
						{
							Strategy: &workloadsv1alpha1.CoordinationStrategy{
								SegmentScheduling: &workloadsv1alpha1.SegmentScheduling{
									SegmentSize: map[string]int32{
										"decode": 3,
										"router": 2,
									},
									PartitionStrategy: workloadsv1alpha1.SegmentPartitionStrategySequentialWait,
								},
							},
						},
					},
				},
			},
			roleStatuses: []workloadsv1alpha1.RoleStatus{
				{Name: "prefill", Replicas: 5, ReadyReplicas: 5},
				{Name: "decode", Replicas: 3, ReadyReplicas: 3},
				{Name: "router", Replicas: 2, ReadyReplicas: 2},
			},
			// All current segments ready, should progress to next
			expectedTargets: map[string]int32{
				"prefill": 10, // 5 + 5
				"decode":  6,  // 3 + 3
				"router":  4,  // 2 + 2
			},
			expectedError: false,
		},
		{
			name: "Complex scenario - coordination 1 not ready blocks coordination 2",
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{Name: "prefill", Replicas: ptr.To(int32(300))},
						{Name: "decode", Replicas: ptr.To(int32(100))},
						{Name: "router", Replicas: ptr.To(int32(50))},
					},
					CoordinationRequirements: []workloadsv1alpha1.Coordination{
						{
							Strategy: &workloadsv1alpha1.CoordinationStrategy{
								SegmentScheduling: &workloadsv1alpha1.SegmentScheduling{
									SegmentSize: map[string]int32{
										"prefill": 5,
										"decode":  3,
									},
									PartitionStrategy: workloadsv1alpha1.SegmentPartitionStrategySequentialWait,
								},
							},
						},
						{
							Strategy: &workloadsv1alpha1.CoordinationStrategy{
								SegmentScheduling: &workloadsv1alpha1.SegmentScheduling{
									SegmentSize: map[string]int32{
										"decode": 3,
										"router": 2,
									},
									PartitionStrategy: workloadsv1alpha1.SegmentPartitionStrategySequentialWait,
								},
							},
						},
					},
				},
			},
			roleStatuses: []workloadsv1alpha1.RoleStatus{
				{Name: "prefill", Replicas: 5, ReadyReplicas: 3}, // Not ready
				{Name: "decode", Replicas: 3, ReadyReplicas: 3},
				{Name: "router", Replicas: 2, ReadyReplicas: 2},
			},
			// Coordination 1 not ready, should wait
			expectedTargets: map[string]int32{
				"prefill": 5, // Keep current
				"decode":  3, // Keep current
				"router":  2, // Keep current (blocked by coordination 1)
			},
			expectedError: false,
		},
		{
			name: "Sequential strategy - always advances without waiting",
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{Name: "prefill", Replicas: ptr.To(int32(100))},
						{Name: "decode", Replicas: ptr.To(int32(50))},
					},
					CoordinationRequirements: []workloadsv1alpha1.Coordination{
						{
							Strategy: &workloadsv1alpha1.CoordinationStrategy{
								SegmentScheduling: &workloadsv1alpha1.SegmentScheduling{
									SegmentSize: map[string]int32{
										"prefill": 10,
										"decode":  5,
									},
									PartitionStrategy: workloadsv1alpha1.SegmentPartitionStrategySequential,
								},
							},
						},
					},
				},
			},
			roleStatuses: []workloadsv1alpha1.RoleStatus{
				{Name: "prefill", Replicas: 10, ReadyReplicas: 5}, // Not all ready
				{Name: "decode", Replicas: 5, ReadyReplicas: 3},   // Not all ready
			},
			// Sequential strategy should advance even though current segment not fully ready
			expectedTargets: map[string]int32{
				"prefill": 20, // Advance to next segment
				"decode":  10, // Advance to next segment
			},
			expectedError: false,
		},
		{
			name: "SequentialWait strategy - waits for partial segment",
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{Name: "prefill", Replicas: ptr.To(int32(100))},
						{Name: "decode", Replicas: ptr.To(int32(50))},
					},
					CoordinationRequirements: []workloadsv1alpha1.Coordination{
						{
							Strategy: &workloadsv1alpha1.CoordinationStrategy{
								SegmentScheduling: &workloadsv1alpha1.SegmentScheduling{
									SegmentSize: map[string]int32{
										"prefill": 10,
										"decode":  5,
									},
									PartitionStrategy: workloadsv1alpha1.SegmentPartitionStrategySequentialWait,
								},
							},
						},
					},
				},
			},
			roleStatuses: []workloadsv1alpha1.RoleStatus{
				{Name: "prefill", Replicas: 7, ReadyReplicas: 7}, // Partial segment deployed
				{Name: "decode", Replicas: 3, ReadyReplicas: 3},  // Partial segment deployed
			},
			// minFullSegment = 0 (no complete segment), isSegmentReady(0) = true, advance to segment 1
			expectedTargets: map[string]int32{
				"prefill": 10, // Advance to first segment
				"decode":  5,  // Advance to first segment
			},
			expectedError: false,
		},
		{
			name: "Complete segment calculation - exact multiples",
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{Name: "prefill", Replicas: ptr.To(int32(100))},
						{Name: "decode", Replicas: ptr.To(int32(60))},
					},
					CoordinationRequirements: []workloadsv1alpha1.Coordination{
						{
							Strategy: &workloadsv1alpha1.CoordinationStrategy{
								SegmentScheduling: &workloadsv1alpha1.SegmentScheduling{
									SegmentSize: map[string]int32{
										"prefill": 10,
										"decode":  6,
									},
									PartitionStrategy: workloadsv1alpha1.SegmentPartitionStrategySequentialWait,
								},
							},
						},
					},
				},
			},
			roleStatuses: []workloadsv1alpha1.RoleStatus{
				{Name: "prefill", Replicas: 30, ReadyReplicas: 30}, // 3 complete segments
				{Name: "decode", Replicas: 12, ReadyReplicas: 12},  // 2 complete segments
			},
			// minFullSegment = 2, all ready, advance to segment 3
			expectedTargets: map[string]int32{
				"prefill": 30, // 3 * 10
				"decode":  18, // 3 * 6
			},
			expectedError: false,
		},
		{
			name: "Respect desired replicas limit",
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{Name: "prefill", Replicas: ptr.To(int32(25))}, // Desired less than 3 segments
						{Name: "decode", Replicas: ptr.To(int32(15))},
					},
					CoordinationRequirements: []workloadsv1alpha1.Coordination{
						{
							Strategy: &workloadsv1alpha1.CoordinationStrategy{
								SegmentScheduling: &workloadsv1alpha1.SegmentScheduling{
									SegmentSize: map[string]int32{
										"prefill": 10,
										"decode":  5,
									},
									PartitionStrategy: workloadsv1alpha1.SegmentPartitionStrategySequentialWait,
								},
							},
						},
					},
				},
			},
			roleStatuses: []workloadsv1alpha1.RoleStatus{
				{Name: "prefill", Replicas: 20, ReadyReplicas: 20}, // 2 complete segments
				{Name: "decode", Replicas: 10, ReadyReplicas: 10},  // 2 complete segments
			},
			// Should cap at desired replicas, not exceed
			expectedTargets: map[string]int32{
				"prefill": 25, // min(30, 25) = 25
				"decode":  15, // min(15, 15) = 15
			},
			expectedError: false,
		},
		{
			name: "Initial deployment - no replicas yet",
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{Name: "prefill", Replicas: ptr.To(int32(100))},
						{Name: "decode", Replicas: ptr.To(int32(50))},
					},
					CoordinationRequirements: []workloadsv1alpha1.Coordination{
						{
							Strategy: &workloadsv1alpha1.CoordinationStrategy{
								SegmentScheduling: &workloadsv1alpha1.SegmentScheduling{
									SegmentSize: map[string]int32{
										"prefill": 10,
										"decode":  5,
									},
									PartitionStrategy: workloadsv1alpha1.SegmentPartitionStrategySequentialWait,
								},
							},
						},
					},
				},
			},
			roleStatuses: []workloadsv1alpha1.RoleStatus{
				{Name: "prefill", Replicas: 0, ReadyReplicas: 0},
				{Name: "decode", Replicas: 0, ReadyReplicas: 0},
			},
			// minFullSegment = 0, isSegmentReady(0) = true, advance to segment 1
			expectedTargets: map[string]int32{
				"prefill": 10,
				"decode":  5,
			},
			expectedError: false,
		},
		{
			name: "Replicas deployed but not all ready - SequentialWait blocks",
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{Name: "prefill", Replicas: ptr.To(int32(100))},
						{Name: "decode", Replicas: ptr.To(int32(50))},
					},
					CoordinationRequirements: []workloadsv1alpha1.Coordination{
						{
							Strategy: &workloadsv1alpha1.CoordinationStrategy{
								SegmentScheduling: &workloadsv1alpha1.SegmentScheduling{
									SegmentSize: map[string]int32{
										"prefill": 10,
										"decode":  5,
									},
									PartitionStrategy: workloadsv1alpha1.SegmentPartitionStrategySequentialWait,
								},
							},
						},
					},
				},
			},
			roleStatuses: []workloadsv1alpha1.RoleStatus{
				{Name: "prefill", Replicas: 10, ReadyReplicas: 7}, // Deployed but not ready
				{Name: "decode", Replicas: 5, ReadyReplicas: 5},
			},
			// minFullSegment = 1, but not all ready, keep current
			expectedTargets: map[string]int32{
				"prefill": 10, // Keep current
				"decode":  5,  // Keep current
			},
			expectedError: false,
		},
		{
			name: "Multi-coordination with different readiness states",
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{Name: "prefill", Replicas: ptr.To(int32(300))},
						{Name: "decode", Replicas: ptr.To(int32(100))},
						{Name: "router", Replicas: ptr.To(int32(50))},
					},
					CoordinationRequirements: []workloadsv1alpha1.Coordination{
						{
							Strategy: &workloadsv1alpha1.CoordinationStrategy{
								SegmentScheduling: &workloadsv1alpha1.SegmentScheduling{
									SegmentSize: map[string]int32{
										"prefill": 5,
										"decode":  3,
									},
									PartitionStrategy: workloadsv1alpha1.SegmentPartitionStrategySequentialWait,
								},
							},
						},
						{
							Strategy: &workloadsv1alpha1.CoordinationStrategy{
								SegmentScheduling: &workloadsv1alpha1.SegmentScheduling{
									SegmentSize: map[string]int32{
										"decode": 3,
										"router": 2,
									},
									PartitionStrategy: workloadsv1alpha1.SegmentPartitionStrategySequentialWait,
								},
							},
						},
					},
				},
			},
			roleStatuses: []workloadsv1alpha1.RoleStatus{
				{Name: "prefill", Replicas: 10, ReadyReplicas: 10}, // Coordination 1: 2 complete segments, ready
				{Name: "decode", Replicas: 6, ReadyReplicas: 6},    // Both coordinations: 2 complete segments, ready
				{Name: "router", Replicas: 2, ReadyReplicas: 2},    // Coordination 2: 1 complete segment, ready
			},
			// Coordination 1: minFullSegment=2, ready, would advance to 15,9
			// Coordination 2: minFullSegment=1, ready, would advance to 6,4
			// Merged: take minimum for decode (6), keep individual for others
			expectedTargets: map[string]int32{
				"prefill": 15, // Coordination 1 advances
				"decode":  6,  // min(9, 6) = 6 (coordination 2 slower)
				"router":  4,  // Coordination 2 advances
			},
			expectedError: false,
		},
		{
			name: "Default partition strategy - should use SequentialWait",
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{Name: "prefill", Replicas: ptr.To(int32(100))},
					},
					CoordinationRequirements: []workloadsv1alpha1.Coordination{
						{
							Strategy: &workloadsv1alpha1.CoordinationStrategy{
								SegmentScheduling: &workloadsv1alpha1.SegmentScheduling{
									SegmentSize: map[string]int32{
										"prefill": 10,
									},
									// No PartitionStrategy specified, should default to SequentialWait
								},
							},
						},
					},
				},
			},
			roleStatuses: []workloadsv1alpha1.RoleStatus{
				{Name: "prefill", Replicas: 10, ReadyReplicas: 7}, // Not all ready
			},
			// Should wait because default is SequentialWait
			expectedTargets: map[string]int32{
				"prefill": 10, // Keep current (wait for ready)
			},
			expectedError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheduler := NewSegmentScheduler(tt.rbg, tt.roleStatuses)
			targets, err := scheduler.CalculateTargetReplicas()

			if tt.expectedError {
				assert.Error(t, err)
				if tt.expectedErrorMsg != "" {
					assert.Contains(t, err.Error(), tt.expectedErrorMsg)
				}
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedTargets, targets)
			}
		})
	}
}
