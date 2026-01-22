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
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

// CoordinationScaler calculates the target replicas for each role based on coordination scaling strategy.
type CoordinationScaler struct {
	coordination *workloadsv1alpha1.Coordination
	maxSkew      float64
}

// RoleScalingState represents the current scaling state of a role.
type RoleScalingState struct {
	RoleName          string
	DesiredReplicas   int32
	CurrentReplicas   int32
	ScheduledReplicas int32 // Number of replicas that have been scheduled (have nodeName)
	ReadyReplicas     int32 // Number of replicas that are ready
}

// NewCoordinationScaler creates a new CoordinationScaler instance.
func NewCoordinationScaler(coordination *workloadsv1alpha1.Coordination) (*CoordinationScaler, error) {
	if coordination == nil || coordination.Strategy == nil || coordination.Strategy.Scaling == nil {
		return nil, fmt.Errorf("invalid coordination configuration: scaling strategy is nil")
	}

	// Use default maxSkew of 100% if not specified (no coordination limit)
	maxSkewStr := "100%"
	if coordination.Strategy.Scaling.MaxSkew != nil {
		maxSkewStr = *coordination.Strategy.Scaling.MaxSkew
	}

	maxSkew, err := parsePercentage(maxSkewStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse maxSkew: %w", err)
	}

	return &CoordinationScaler{
		coordination: coordination,
		maxSkew:      maxSkew,
	}, nil
}

// CalculateTargetReplicas calculates the target replicas for each role in the coordination.
// It ensures that the deployment progress difference between roles does not exceed maxSkew.
// It also checks the progression strategy to decide if we should proceed to next batch.
func (s *CoordinationScaler) CalculateTargetReplicas(roleStates map[string]RoleScalingState) (map[string]int32, error) {
	if len(roleStates) == 0 {
		return nil, fmt.Errorf("no role states provided")
	}

	// Validate that all roles in coordination are present in roleStates
	for _, roleName := range s.coordination.Roles {
		if _, exists := roleStates[roleName]; !exists {
			return nil, fmt.Errorf("role %s not found in roleStates", roleName)
		}
	}

	// Get progression strategy (default to OrderScheduled)
	progression := workloadsv1alpha1.OrderScheduled
	if s.coordination.Strategy.Scaling.Progression != nil {
		progression = *s.coordination.Strategy.Scaling.Progression
	}

	// Check if current batch satisfies progression requirement
	if !s.canProceedToNextBatch(roleStates, progression) {
		// Cannot proceed to next batch yet, keep current targets
		result := make(map[string]int32)
		for _, roleName := range s.coordination.Roles {
			result[roleName] = roleStates[roleName].CurrentReplicas
		}
		return result, nil
	}

	// Calculate current progress for each role
	roleProgresses := make([]roleProgress, 0, len(s.coordination.Roles))
	for _, roleName := range s.coordination.Roles {
		state := roleStates[roleName]

		// Calculate progress, handling zero desired replicas (scale down to zero)
		var progress float64
		if state.DesiredReplicas == 0 {
			// When scaling down to zero, progress is 1.0 if already at zero,
			// otherwise 0.0 indicating scale-down has not completed
			if state.CurrentReplicas == 0 {
				progress = 1.0
			} else {
				progress = 0.0
			}
		} else {
			progress = float64(state.CurrentReplicas) / float64(state.DesiredReplicas)
		}

		roleProgresses = append(roleProgresses, roleProgress{
			roleName:        roleName,
			desiredReplicas: state.DesiredReplicas,
			currentReplicas: state.CurrentReplicas,
			progress:        progress,
		})
	}

	// Sort by progress (ascending) to find the role with minimum progress
	sort.Slice(roleProgresses, func(i, j int) bool {
		return roleProgresses[i].progress < roleProgresses[j].progress
	})

	// Find minimum progress among roles that haven't reached desired replicas
	// Roles that are already at desired replicas should not limit other roles
	minProgress := roleProgresses[0].progress
	for _, rp := range roleProgresses {
		if rp.currentReplicas < rp.desiredReplicas {
			minProgress = rp.progress
			break
		}
	}

	maxAllowedProgress := minProgress + s.maxSkew

	// Calculate target replicas for each role
	result := make(map[string]int32)
	for _, rp := range roleProgresses {
		// If already at desired replicas, keep it
		if rp.currentReplicas >= rp.desiredReplicas {
			result[rp.roleName] = rp.desiredReplicas
			continue
		}

		// If already at or beyond max allowed progress, keep current
		if rp.progress >= maxAllowedProgress {
			result[rp.roleName] = rp.currentReplicas
			continue
		}

		// Calculate target replicas based on max allowed progress
		targetReplicas := int32(math.Ceil(maxAllowedProgress * float64(rp.desiredReplicas)))

		// Ensure we don't exceed desired replicas
		if targetReplicas > rp.desiredReplicas {
			targetReplicas = rp.desiredReplicas
		}

		// Ensure we make progress (at least current + 1, unless already at desired)
		if targetReplicas <= rp.currentReplicas && rp.currentReplicas < rp.desiredReplicas {
			targetReplicas = rp.currentReplicas + 1
		}

		result[rp.roleName] = targetReplicas
	}

	return result, nil
}

// GetCoordinationRoles returns the list of roles in this coordination.
func (s *CoordinationScaler) GetCoordinationRoles() []string {
	return s.coordination.Roles
}

// canProceedToNextBatch checks if we can proceed to the next batch based on progression strategy.
// It verifies that all roles in the coordination meet the progression requirement.
func (s *CoordinationScaler) canProceedToNextBatch(
	roleStates map[string]RoleScalingState,
	progression workloadsv1alpha1.ProgressionType,
) bool {
	// If all roles are at desired replicas, we're done
	allAtDesired := true
	for _, roleName := range s.coordination.Roles {
		state := roleStates[roleName]
		if state.CurrentReplicas < state.DesiredReplicas {
			allAtDesired = false
			break
		}
	}
	if allAtDesired {
		return true
	}

	// Check if current batch meets progression requirement
	for _, roleName := range s.coordination.Roles {
		state := roleStates[roleName]

		// Skip if this role is already at desired replicas
		if state.CurrentReplicas >= state.DesiredReplicas {
			continue
		}

		// Skip if this role has no pods yet (initial state)
		if state.CurrentReplicas == 0 {
			continue
		}

		// Check based on progression type
		switch progression {
		case workloadsv1alpha1.OrderScheduled:
			// All current replicas must be scheduled
			if state.ScheduledReplicas < state.CurrentReplicas {
				return false
			}
		case workloadsv1alpha1.OrderReady:
			// All current replicas must be ready
			if state.ReadyReplicas < state.CurrentReplicas {
				return false
			}
		}
	}

	return true
}

// roleProgress is an internal struct to track role progress.
type roleProgress struct {
	roleName        string
	desiredReplicas int32
	currentReplicas int32
	progress        float64
}

// parsePercentage parses a percentage string (e.g., "5%") to a float64 (e.g., 0.05).
func parsePercentage(percentStr string) (float64, error) {
	percentStr = strings.TrimSpace(percentStr)
	if !strings.HasSuffix(percentStr, "%") {
		return 0, fmt.Errorf("percentage string must end with '%%'")
	}

	numStr := strings.TrimSuffix(percentStr, "%")
	num, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse percentage number: %w", err)
	}

	if num < 0 || num > 100 {
		return 0, fmt.Errorf("percentage must be between 0 and 100, got %f", num)
	}

	return num / 100.0, nil
}
