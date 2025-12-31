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
	"fmt"

	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

// SegmentScheduler manages segment-based deployment strategy
type SegmentScheduler struct {
	rbg          *workloadsv1alpha1.RoleBasedGroup
	coordination *workloadsv1alpha1.Coordination
	roleStatuses []workloadsv1alpha1.RoleStatus
}

// NewSegmentScheduler creates a new segment scheduler instance
func NewSegmentScheduler(
	rbg *workloadsv1alpha1.RoleBasedGroup,
	coordination *workloadsv1alpha1.Coordination,
	roleStatuses []workloadsv1alpha1.RoleStatus,
) *SegmentScheduler {
	return &SegmentScheduler{
		rbg:          rbg,
		coordination: coordination,
		roleStatuses: roleStatuses,
	}
}

// CalculateTargetReplicas calculates the target replicas for each role based on segment scheduling strategy
// Returns a map of role name to target replicas that should be deployed in this reconcile iteration
// The target replicas will incrementally increase by segment size until reaching the desired replicas
// A segment is a group of replicas across all roles that must be deployed together
func (s *SegmentScheduler) CalculateTargetReplicas() (map[string]int32, error) {
	if s.coordination == nil ||
		s.coordination.Strategy == nil ||
		s.coordination.Strategy.SegmentScheduling == nil {
		return nil, nil
	}

	segmentScheduling := s.coordination.Strategy.SegmentScheduling
	if segmentScheduling.Segment == nil || len(segmentScheduling.Segment) == 0 {
		return nil, nil
	}

	// Get partition strategy, default to SequentialWait
	partitionStrategy := segmentScheduling.PartitionStrategy
	if partitionStrategy == "" {
		partitionStrategy = workloadsv1alpha1.SegmentPartitionStrategySequentialWait
	}

	targetReplicas := make(map[string]int32)
	roleStatusMap := s.buildRoleStatusMap()

	// For SequentialWait strategy, check if current segment is ready before proceeding
	if partitionStrategy == workloadsv1alpha1.SegmentPartitionStrategySequentialWait {
		if !s.isCurrentSegmentReady(roleStatusMap, segmentScheduling.Segment) {
			// Current segment not ready, keep current targets
			for roleName := range segmentScheduling.Segment {
				if status, ok := roleStatusMap[roleName]; ok {
					targetReplicas[roleName] = status.Replicas
				} else {
					targetReplicas[roleName] = 0
				}
			}
			return targetReplicas, nil
		}
	}

	// Current segment is ready (or using Sequential strategy), calculate next target
	for roleName, segmentSize := range segmentScheduling.Segment {
		// Get current deployed replicas
		currentReplicas := int32(0)
		if status, ok := roleStatusMap[roleName]; ok {
			currentReplicas = status.Replicas
		}

		// Get desired replicas from role spec
		desiredReplicas := s.getDesiredReplicas(roleName)
		if desiredReplicas == 0 {
			continue
		}

		// Calculate next target: current + segmentSize
		nextTarget := currentReplicas + int32(segmentSize)
		if nextTarget > desiredReplicas {
			nextTarget = desiredReplicas
		}

		targetReplicas[roleName] = nextTarget
	}

	return targetReplicas, nil
}

// buildRoleStatusMap builds a map of role name to role status for quick lookup
func (s *SegmentScheduler) buildRoleStatusMap() map[string]workloadsv1alpha1.RoleStatus {
	statusMap := make(map[string]workloadsv1alpha1.RoleStatus)
	for _, status := range s.roleStatuses {
		statusMap[status.Name] = status
	}
	return statusMap
}

// getDesiredReplicas returns the desired replicas for a role
func (s *SegmentScheduler) getDesiredReplicas(roleName string) int32 {
	for _, role := range s.rbg.Spec.Roles {
		if role.Name == roleName && role.Replicas != nil {
			return *role.Replicas
		}
	}
	return 0
}

// isCurrentSegmentReady checks if the currently deployed segment is ready across all roles
// A segment is ready when ALL roles have their current replicas fully ready
// This ensures coordinated progression across all roles in a segment
func (s *SegmentScheduler) isCurrentSegmentReady(
	roleStatusMap map[string]workloadsv1alpha1.RoleStatus,
	segmentConfig map[string]int,
) bool {
	// Find the minimum number of deployed segments across all roles
	// This represents the "current segment" we're working on
	minDeployedSegments := -1
	firstRole := true

	for roleName, segmentSize := range segmentConfig {
		if segmentSize == 0 {
			continue
		}

		currentReplicas := int32(0)
		if status, ok := roleStatusMap[roleName]; ok {
			currentReplicas = status.Replicas
		}

		// Calculate how many segments have been deployed for this role
		deployedSegments := int(currentReplicas) / segmentSize
		if currentReplicas%int32(segmentSize) != 0 {
			// Partial segment counts as deployed but not complete
			deployedSegments++
		}

		if firstRole {
			minDeployedSegments = deployedSegments
			firstRole = false
		} else if deployedSegments < minDeployedSegments {
			minDeployedSegments = deployedSegments
		}
	}

	if firstRole || minDeployedSegments == 0 {
		// No roles configured or no segments deployed yet, ready to deploy first
		return true
	}

	// Check if all roles in the current segment are ready
	// The current segment is the minDeployedSegments'th segment (1-indexed)
	for roleName, segmentSize := range segmentConfig {
		// Calculate expected replicas for the current segment
		expectedReplicas := int32(minDeployedSegments * segmentSize)

		// Get desired replicas to ensure we don't exceed it
		desiredReplicas := s.getDesiredReplicas(roleName)
		if expectedReplicas > desiredReplicas {
			expectedReplicas = desiredReplicas
		}

		status, ok := roleStatusMap[roleName]
		if !ok {
			// No status yet, not ready
			return false
		}

		// Check if this role has all current replicas ready
		if status.Replicas < expectedReplicas || status.ReadyReplicas < expectedReplicas {
			return false
		}
	}

	return true
}

// IsSegmentSchedulingComplete checks if segment-based deployment is complete for all roles
func (s *SegmentScheduler) IsSegmentSchedulingComplete() bool {
	if s.coordination == nil ||
		s.coordination.Strategy == nil ||
		s.coordination.Strategy.SegmentScheduling == nil {
		return true
	}

	roleStatusMap := s.buildRoleStatusMap()

	// Check if all roles have reached their target replicas and are ready
	for _, role := range s.rbg.Spec.Roles {
		if role.Replicas == nil {
			continue
		}

		status, ok := roleStatusMap[role.Name]
		if !ok {
			return false
		}

		// Check if current replicas match desired replicas and all are ready
		if status.Replicas != *role.Replicas || status.ReadyReplicas != *role.Replicas {
			return false
		}
	}

	return true
}

// GetSegmentSchedulingMessage returns a human-readable message about current segment scheduling status
func (s *SegmentScheduler) GetSegmentSchedulingMessage() string {
	if s.coordination == nil ||
		s.coordination.Strategy == nil ||
		s.coordination.Strategy.SegmentScheduling == nil {
		return ""
	}

	roleStatusMap := s.buildRoleStatusMap()
	segmentScheduling := s.coordination.Strategy.SegmentScheduling

	var message string
	for _, role := range s.rbg.Spec.Roles {
		if _, ok := segmentScheduling.Segment[role.Name]; !ok {
			continue
		}

		status, ok := roleStatusMap[role.Name]
		if !ok {
			message += fmt.Sprintf("Role %s: waiting to start; ", role.Name)
			continue
		}

		if role.Replicas == nil {
			continue
		}

		message += fmt.Sprintf(
			"Role %s: %d/%d replicas, %d ready; ",
			role.Name,
			status.Replicas,
			*role.Replicas,
			status.ReadyReplicas,
		)
	}

	if message == "" {
		return "Segment scheduling in progress"
	}

	return message
}
