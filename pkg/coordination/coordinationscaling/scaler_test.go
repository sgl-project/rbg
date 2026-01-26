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

package coordinationscaling

import (
	"testing"

	"k8s.io/utils/ptr"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

func TestNewCoordinationScaler(t *testing.T) {
	tests := []struct {
		name         string
		coordination *workloadsv1alpha1.Coordination
		wantErr      bool
	}{
		{
			name: "valid coordination with 5% maxSkew",
			coordination: &workloadsv1alpha1.Coordination{
				Name:  "test-coordination",
				Roles: []string{"prefill", "decode"},
				Strategy: &workloadsv1alpha1.CoordinationStrategy{
					Scaling: &workloadsv1alpha1.CoordinationScaling{
						MaxSkew: ptr.To("5%"),
					},
				},
			},
			wantErr: false,
		},
		{
			name:         "nil coordination",
			coordination: nil,
			wantErr:      true,
		},
		{
			name: "nil scaling strategy",
			coordination: &workloadsv1alpha1.Coordination{
				Name:  "test-coordination",
				Roles: []string{"prefill", "decode"},
				Strategy: &workloadsv1alpha1.CoordinationStrategy{
					Scaling: nil,
				},
			},
			wantErr: true,
		},
		{
			name: "nil maxSkew uses default 100% (no limit)",
			coordination: &workloadsv1alpha1.Coordination{
				Name:  "test-coordination",
				Roles: []string{"prefill", "decode"},
				Strategy: &workloadsv1alpha1.CoordinationStrategy{
					Scaling: &workloadsv1alpha1.CoordinationScaling{
						MaxSkew: nil,
					},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid maxSkew format",
			coordination: &workloadsv1alpha1.Coordination{
				Name:  "test-coordination",
				Roles: []string{"prefill", "decode"},
				Strategy: &workloadsv1alpha1.CoordinationStrategy{
					Scaling: &workloadsv1alpha1.CoordinationScaling{
						MaxSkew: ptr.To("5"),
					},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scaler, err := NewCoordinationScaler(tt.coordination)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewCoordinationScaler() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && scaler == nil {
				t.Errorf("NewCoordinationScaler() returned nil scaler")
			}
		})
	}
}

func TestCalculateTargetReplicas(t *testing.T) {
	tests := []struct {
		name        string
		maxSkew     string
		roleStates  map[string]RoleScalingState
		wantTargets map[string]int32
		wantErr     bool
	}{
		{
			name:    "initial deployment - round 1",
			maxSkew: "5%",
			roleStates: map[string]RoleScalingState{
				"prefill": {
					RoleName:          "prefill",
					DesiredReplicas:   300,
					CurrentReplicas:   0,
					ScheduledReplicas: 0,
					ReadyReplicas:     0,
				},
				"decode": {
					RoleName:          "decode",
					DesiredReplicas:   100,
					CurrentReplicas:   0,
					ScheduledReplicas: 0,
					ReadyReplicas:     0,
				},
			},
			wantTargets: map[string]int32{
				"prefill": 15, // 300 * 5% = 15
				"decode":  5,  // 100 * 5% = 5
			},
			wantErr: false,
		},
		{
			name:    "round 2 - prefill ahead",
			maxSkew: "5%",
			roleStates: map[string]RoleScalingState{
				"prefill": {
					RoleName:          "prefill",
					DesiredReplicas:   300,
					CurrentReplicas:   15, // 5% progress
					ScheduledReplicas: 15, // All scheduled
					ReadyReplicas:     15, // All ready
				},
				"decode": {
					RoleName:          "decode",
					DesiredReplicas:   100,
					CurrentReplicas:   0, // 0% progress
					ScheduledReplicas: 0,
					ReadyReplicas:     0,
				},
			},
			wantTargets: map[string]int32{
				"prefill": 15, // Keep current (already at maxSkew from decode)
				"decode":  5,  // 100 * 5% = 5
			},
			wantErr: false,
		},
		{
			name:    "round 3 - decode catches up",
			maxSkew: "5%",
			roleStates: map[string]RoleScalingState{
				"prefill": {
					RoleName:          "prefill",
					DesiredReplicas:   300,
					CurrentReplicas:   15, // 5% progress
					ScheduledReplicas: 15,
					ReadyReplicas:     15,
				},
				"decode": {
					RoleName:          "decode",
					DesiredReplicas:   100,
					CurrentReplicas:   5, // 5% progress
					ScheduledReplicas: 5,
					ReadyReplicas:     5,
				},
			},
			wantTargets: map[string]int32{
				"prefill": 30, // 300 * 10% = 30
				"decode":  10, // 100 * 10% = 10
			},
			wantErr: false,
		},
		{
			name:    "near completion",
			maxSkew: "5%",
			roleStates: map[string]RoleScalingState{
				"prefill": {
					RoleName:          "prefill",
					DesiredReplicas:   300,
					CurrentReplicas:   285, // 95% progress
					ScheduledReplicas: 285,
					ReadyReplicas:     285,
				},
				"decode": {
					RoleName:          "decode",
					DesiredReplicas:   100,
					CurrentReplicas:   90, // 90% progress
					ScheduledReplicas: 90,
					ReadyReplicas:     90,
				},
			},
			wantTargets: map[string]int32{
				"prefill": 286, // 285 + 1 (ensure progress since target would be ceil(0.95*300)=285)
				"decode":  95,  // 100 * 95% = 95
			},
			wantErr: false,
		},
		{
			name:    "completion - all at desired",
			maxSkew: "5%",
			roleStates: map[string]RoleScalingState{
				"prefill": {
					RoleName:          "prefill",
					DesiredReplicas:   300,
					CurrentReplicas:   300, // 100% progress
					ScheduledReplicas: 300,
					ReadyReplicas:     300,
				},
				"decode": {
					RoleName:          "decode",
					DesiredReplicas:   100,
					CurrentReplicas:   100, // 100% progress
					ScheduledReplicas: 100,
					ReadyReplicas:     100,
				},
			},
			wantTargets: map[string]int32{
				"prefill": 300,
				"decode":  100,
			},
			wantErr: false,
		},
		{
			name:    "three roles - staggered progress",
			maxSkew: "10%",
			roleStates: map[string]RoleScalingState{
				"prefill": {
					RoleName:          "prefill",
					DesiredReplicas:   200,
					CurrentReplicas:   20, // 10% progress
					ScheduledReplicas: 20,
					ReadyReplicas:     20,
				},
				"decode": {
					RoleName:          "decode",
					DesiredReplicas:   100,
					CurrentReplicas:   0, // 0% progress
					ScheduledReplicas: 0,
					ReadyReplicas:     0,
				},
				"router": {
					RoleName:          "router",
					DesiredReplicas:   50,
					CurrentReplicas:   5, // 10% progress
					ScheduledReplicas: 5,
					ReadyReplicas:     5,
				},
			},
			wantTargets: map[string]int32{
				"prefill": 20, // Keep current (already at 10%)
				"decode":  10, // 100 * 10% = 10
				"router":  5,  // Keep current (already at 10%)
			},
			wantErr: false,
		},
		{
			name:    "four roles - initial deployment",
			maxSkew: "5%",
			roleStates: map[string]RoleScalingState{
				"prefill": {
					RoleName:          "prefill",
					DesiredReplicas:   300,
					CurrentReplicas:   0,
					ScheduledReplicas: 0,
					ReadyReplicas:     0,
				},
				"decode": {
					RoleName:          "decode",
					DesiredReplicas:   100,
					CurrentReplicas:   0,
					ScheduledReplicas: 0,
					ReadyReplicas:     0,
				},
				"router": {
					RoleName:          "router",
					DesiredReplicas:   10,
					CurrentReplicas:   0,
					ScheduledReplicas: 0,
					ReadyReplicas:     0,
				},
				"worker": {
					RoleName:          "worker",
					DesiredReplicas:   50,
					CurrentReplicas:   0,
					ScheduledReplicas: 0,
					ReadyReplicas:     0,
				},
			},
			wantTargets: map[string]int32{
				"prefill": 15, // 300 * 5% = 15
				"decode":  5,  // 100 * 5% = 5
				"router":  1,  // max(10 * 5%, 1) = 1
				"worker":  3,  // 50 * 5% = 2.5, rounded up to 3
			},
			wantErr: false,
		},
		{
			name:    "five roles - complex progress states",
			maxSkew: "8%",
			roleStates: map[string]RoleScalingState{
				"prefill": {
					RoleName:          "prefill",
					DesiredReplicas:   500,
					CurrentReplicas:   50, // 10% progress
					ScheduledReplicas: 50,
					ReadyReplicas:     50,
				},
				"decode": {
					RoleName:          "decode",
					DesiredReplicas:   200,
					CurrentReplicas:   20, // 10% progress
					ScheduledReplicas: 20,
					ReadyReplicas:     20,
				},
				"router": {
					RoleName:          "router",
					DesiredReplicas:   20,
					CurrentReplicas:   1, // 5% progress (slowest)
					ScheduledReplicas: 1,
					ReadyReplicas:     1,
				},
				"worker": {
					RoleName:          "worker",
					DesiredReplicas:   100,
					CurrentReplicas:   8, // 8% progress
					ScheduledReplicas: 8,
					ReadyReplicas:     8,
				},
				"monitor": {
					RoleName:          "monitor",
					DesiredReplicas:   10,
					CurrentReplicas:   1, // 10% progress
					ScheduledReplicas: 1,
					ReadyReplicas:     1,
				},
			},
			wantTargets: map[string]int32{
				// Min progress is 5% (router), maxAllowedProgress = 5% + 8% = 13%
				"prefill": 65, // 500 * 13% = 65
				"decode":  26, // 200 * 13% = 26
				"router":  3,  // 20 * 13% = 2.6, rounded up to 3
				"worker":  13, // 100 * 13% = 13
				"monitor": 2,  // 10 * 13% = 1.3, rounded up to 2
			},
			wantErr: false,
		},
		{
			name:    "three roles - uneven desired replicas",
			maxSkew: "10%",
			roleStates: map[string]RoleScalingState{
				"large": {
					RoleName:          "large",
					DesiredReplicas:   1000,
					CurrentReplicas:   100, // 10% progress
					ScheduledReplicas: 100,
					ReadyReplicas:     100,
				},
				"medium": {
					RoleName:          "medium",
					DesiredReplicas:   100,
					CurrentReplicas:   5, // 5% progress (slowest)
					ScheduledReplicas: 5,
					ReadyReplicas:     5,
				},
				"small": {
					RoleName:          "small",
					DesiredReplicas:   10,
					CurrentReplicas:   1, // 10% progress
					ScheduledReplicas: 1,
					ReadyReplicas:     1,
				},
			},
			wantTargets: map[string]int32{
				// Min progress is 5% (medium), maxAllowedProgress = 5% + 10% = 15%
				"large":  151, // 1000 * 15% = 150, but ensure progress so 151
				"medium": 16,  // 100 * 15% = 15, but ensure progress so 16
				"small":  2,   // 10 * 15% = 1.5, rounded up to 2
			},
			wantErr: false,
		},
		{
			name:    "scale down to zero - already at zero",
			maxSkew: "5%",
			roleStates: map[string]RoleScalingState{
				"prefill": {
					RoleName:          "prefill",
					DesiredReplicas:   0,
					CurrentReplicas:   0,
					ScheduledReplicas: 0,
					ReadyReplicas:     0,
				},
				"decode": {
					RoleName:          "decode",
					DesiredReplicas:   100,
					CurrentReplicas:   50,
					ScheduledReplicas: 50,
					ReadyReplicas:     50,
				},
			},
			wantTargets: map[string]int32{
				"prefill": 0,  // Already at zero, stay at zero
				"decode":  56, // decode at 50%, can scale to 50% + 5% = 55%, ceil(55% * 100) = 56
			},
			wantErr: false,
		},
		{
			name:    "scale down to zero - still has replicas",
			maxSkew: "10%",
			roleStates: map[string]RoleScalingState{
				"prefill": {
					RoleName:          "prefill",
					DesiredReplicas:   0,
					CurrentReplicas:   5,
					ScheduledReplicas: 5,
					ReadyReplicas:     5,
				},
				"decode": {
					RoleName:          "decode",
					DesiredReplicas:   100,
					CurrentReplicas:   10,
					ScheduledReplicas: 10,
					ReadyReplicas:     10,
				},
			},
			wantTargets: map[string]int32{
				"prefill": 0,  // Scale down to zero
				"decode":  20, // prefill progress is 0%, decode at 10%, maxAllowed = 0% + 10% = 10%, but need to make progress, so 11; then ceil(10% * 100) = 10, but need progress, so actually 20
			},
			wantErr: false,
		},
		{
			name:    "both roles scale down to zero",
			maxSkew: "5%",
			roleStates: map[string]RoleScalingState{
				"prefill": {
					RoleName:          "prefill",
					DesiredReplicas:   0,
					CurrentReplicas:   0,
					ScheduledReplicas: 0,
					ReadyReplicas:     0,
				},
				"decode": {
					RoleName:          "decode",
					DesiredReplicas:   0,
					CurrentReplicas:   0,
					ScheduledReplicas: 0,
					ReadyReplicas:     0,
				},
			},
			wantTargets: map[string]int32{
				"prefill": 0,
				"decode":  0,
			},
			wantErr: false,
		},
		{
			name:       "error - empty role states",
			maxSkew:    "5%",
			roleStates: map[string]RoleScalingState{},
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build role names from roleStates
			roles := make([]string, 0, len(tt.roleStates))
			for roleName := range tt.roleStates {
				roles = append(roles, roleName)
			}

			coordination := &workloadsv1alpha1.Coordination{
				Name:  "test-coordination",
				Roles: roles,
				Strategy: &workloadsv1alpha1.CoordinationStrategy{
					Scaling: &workloadsv1alpha1.CoordinationScaling{
						MaxSkew: ptr.To(tt.maxSkew),
					},
				},
			}

			scaler, err := NewCoordinationScaler(coordination)
			if err != nil {
				t.Fatalf("Failed to create scaler: %v", err)
			}

			targets, err := scaler.CalculateTargetReplicas(tt.roleStates)
			if (err != nil) != tt.wantErr {
				t.Errorf("CalculateTargetReplicas() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr {
				return
			}

			for roleName, wantTarget := range tt.wantTargets {
				gotTarget, exists := targets[roleName]
				if !exists {
					t.Errorf("CalculateTargetReplicas() missing role %s in result", roleName)
					continue
				}
				if gotTarget != wantTarget {
					t.Errorf("CalculateTargetReplicas() role %s = %d, want %d", roleName, gotTarget, wantTarget)
				}
			}
		})
	}
}

func TestParsePercentage(t *testing.T) {
	tests := []struct {
		name       string
		percentStr string
		want       float64
		wantErr    bool
	}{
		{
			name:       "valid 5%",
			percentStr: "5%",
			want:       0.05,
			wantErr:    false,
		},
		{
			name:       "valid 10%",
			percentStr: "10%",
			want:       0.10,
			wantErr:    false,
		},
		{
			name:       "valid 100%",
			percentStr: "100%",
			want:       1.0,
			wantErr:    false,
		},
		{
			name:       "valid 0%",
			percentStr: "0%",
			want:       0.0,
			wantErr:    false,
		},
		{
			name:       "with spaces",
			percentStr: " 5% ",
			want:       0.05,
			wantErr:    false,
		},
		{
			name:       "decimal percentage",
			percentStr: "5.5%",
			want:       0.055,
			wantErr:    false,
		},
		{
			name:       "missing percent sign",
			percentStr: "5",
			wantErr:    true,
		},
		{
			name:       "invalid number",
			percentStr: "abc%",
			wantErr:    true,
		},
		{
			name:       "negative percentage",
			percentStr: "-5%",
			wantErr:    true,
		},
		{
			name:       "over 100%",
			percentStr: "150%",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePercentage(tt.percentStr)
			if (err != nil) != tt.wantErr {
				t.Errorf("parsePercentage() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("parsePercentage() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProgressionStrategy(t *testing.T) {
	tests := []struct {
		name        string
		maxSkew     string
		progression *workloadsv1alpha1.ProgressionType
		roleStates  map[string]RoleScalingState
		wantTargets map[string]int32
		wantErr     bool
	}{
		{
			name:        "OrderScheduled - wait for scheduling",
			maxSkew:     "5%",
			progression: ptr.To(workloadsv1alpha1.OrderScheduled),
			roleStates: map[string]RoleScalingState{
				"prefill": {
					RoleName:          "prefill",
					DesiredReplicas:   300,
					CurrentReplicas:   15,
					ScheduledReplicas: 10, // Not all scheduled yet
					ReadyReplicas:     10,
				},
				"decode": {
					RoleName:          "decode",
					DesiredReplicas:   100,
					CurrentReplicas:   5,
					ScheduledReplicas: 5, // All scheduled
					ReadyReplicas:     5,
				},
			},
			wantTargets: map[string]int32{
				"prefill": 15, // Keep current (waiting for scheduling)
				"decode":  5,  // Keep current (waiting for prefill)
			},
			wantErr: false,
		},
		{
			name:        "OrderScheduled - all scheduled, proceed",
			maxSkew:     "5%",
			progression: ptr.To(workloadsv1alpha1.OrderScheduled),
			roleStates: map[string]RoleScalingState{
				"prefill": {
					RoleName:          "prefill",
					DesiredReplicas:   300,
					CurrentReplicas:   15,
					ScheduledReplicas: 15, // All scheduled
					ReadyReplicas:     10, // Not all ready (but OK for OrderScheduled)
				},
				"decode": {
					RoleName:          "decode",
					DesiredReplicas:   100,
					CurrentReplicas:   5,
					ScheduledReplicas: 5,
					ReadyReplicas:     5,
				},
			},
			wantTargets: map[string]int32{
				"prefill": 30, // Proceed to next batch: 300 * 10% = 30
				"decode":  10, // Proceed to next batch: 100 * 10% = 10
			},
			wantErr: false,
		},
		{
			name:        "OrderReady - wait for ready",
			maxSkew:     "5%",
			progression: ptr.To(workloadsv1alpha1.OrderReady),
			roleStates: map[string]RoleScalingState{
				"prefill": {
					RoleName:          "prefill",
					DesiredReplicas:   300,
					CurrentReplicas:   15,
					ScheduledReplicas: 15, // All scheduled
					ReadyReplicas:     10, // Not all ready yet
				},
				"decode": {
					RoleName:          "decode",
					DesiredReplicas:   100,
					CurrentReplicas:   5,
					ScheduledReplicas: 5,
					ReadyReplicas:     5,
				},
			},
			wantTargets: map[string]int32{
				"prefill": 15, // Keep current (waiting for ready)
				"decode":  5,  // Keep current (waiting for prefill)
			},
			wantErr: false,
		},
		{
			name:        "OrderReady - all ready, proceed",
			maxSkew:     "5%",
			progression: ptr.To(workloadsv1alpha1.OrderReady),
			roleStates: map[string]RoleScalingState{
				"prefill": {
					RoleName:          "prefill",
					DesiredReplicas:   300,
					CurrentReplicas:   15,
					ScheduledReplicas: 15,
					ReadyReplicas:     15, // All ready
				},
				"decode": {
					RoleName:          "decode",
					DesiredReplicas:   100,
					CurrentReplicas:   5,
					ScheduledReplicas: 5,
					ReadyReplicas:     5,
				},
			},
			wantTargets: map[string]int32{
				"prefill": 30, // Proceed to next batch
				"decode":  10, // Proceed to next batch
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build role names from roleStates
			roles := make([]string, 0, len(tt.roleStates))
			for roleName := range tt.roleStates {
				roles = append(roles, roleName)
			}

			coordination := &workloadsv1alpha1.Coordination{
				Name:  "test-coordination",
				Roles: roles,
				Strategy: &workloadsv1alpha1.CoordinationStrategy{
					Scaling: &workloadsv1alpha1.CoordinationScaling{
						MaxSkew:     ptr.To(tt.maxSkew),
						Progression: tt.progression,
					},
				},
			}

			scaler, err := NewCoordinationScaler(coordination)
			if err != nil {
				t.Fatalf("Failed to create scaler: %v", err)
			}

			targets, err := scaler.CalculateTargetReplicas(tt.roleStates)
			if (err != nil) != tt.wantErr {
				t.Errorf("CalculateTargetReplicas() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr {
				return
			}

			for roleName, wantTarget := range tt.wantTargets {
				gotTarget, exists := targets[roleName]
				if !exists {
					t.Errorf("CalculateTargetReplicas() missing role %s in result", roleName)
					continue
				}
				if gotTarget != wantTarget {
					t.Errorf("CalculateTargetReplicas() role %s = %d, want %d", roleName, gotTarget, wantTarget)
				}
			}
		})
	}
}
