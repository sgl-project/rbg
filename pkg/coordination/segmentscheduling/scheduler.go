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
	"fmt"
	"math"

	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

// SegmentScheduler manages segment-based deployment strategy for all coordinations
type SegmentScheduler struct {
	rbg                       *workloadsv1alpha1.RoleBasedGroup
	roleStatuses              []workloadsv1alpha1.RoleStatus
	segmentSchedulingPolicies []*workloadsv1alpha1.SegmentScheduling
}

// NewSegmentScheduler creates a new segment scheduler instance for the entire RBG
func NewSegmentScheduler(
	rbg *workloadsv1alpha1.RoleBasedGroup,
	roleStatuses []workloadsv1alpha1.RoleStatus,
) *SegmentScheduler {
	// Collect all segment schedulings from all coordinations
	var segmentSchedulingPolicies []*workloadsv1alpha1.SegmentScheduling
	for i := range rbg.Spec.CoordinationRequirements {
		coord := &rbg.Spec.CoordinationRequirements[i]
		if coord.Strategy != nil && coord.Strategy.SegmentScheduling != nil {
			segmentSchedulingPolicies = append(segmentSchedulingPolicies, coord.Strategy.SegmentScheduling)
		}
	}
	return &SegmentScheduler{
		rbg:                       rbg,
		roleStatuses:              roleStatuses,
		segmentSchedulingPolicies: segmentSchedulingPolicies,
	}
}

// CalculateTargetReplicas calculates the target replicas for each role based on all segment schedulings
// It handles multiple coordination requirements with segment scheduling by:
// 1. Detecting conflicts where the same role has different segment sizes
// 2. Merging target replicas from multiple coordinations
// 3. Ensuring coordinated progression across all coordinations with SequentialWait strategy
func (s *SegmentScheduler) CalculateTargetReplicas() (map[string]int32, error) {
	if len(s.segmentSchedulingPolicies) == 0 {
		return nil, nil
	}

	// Check for conflicts: same role with different segment sizes
	if err := s.detectSegmentConflicts(s.segmentSchedulingPolicies); err != nil {
		return nil, err
	}

	// Calculate target replicas for each segment scheduling
	// Also determine if each can progress by comparing targets with current replicas
	segmentTargets := make([]map[string]int32, len(s.segmentSchedulingPolicies))
	segmentCanProgress := make([]bool, len(s.segmentSchedulingPolicies))

	// Build current replicas map
	currentReplicasMap := make(map[string]int32)
	for _, status := range s.roleStatuses {
		currentReplicasMap[status.Name] = status.Replicas
	}

	for i, segmentScheduling := range s.segmentSchedulingPolicies {
		targets := s.calculateSingleSegmentTargets(segmentScheduling)
		segmentTargets[i] = targets

		// A segment scheduling can progress if ANY of its roles has a target > current
		// This indicates that the segment is ready and we're moving forward
		canProgress := false
		for roleName, targetReplicas := range targets {
			if current, ok := currentReplicasMap[roleName]; ok {
				if targetReplicas > current {
					canProgress = true
					break
				}
			} else if targetReplicas > 0 {
				// New role starting from 0
				canProgress = true
				break
			}
		}
		segmentCanProgress[i] = canProgress
	}

	// Merge all target replicas with coordination progress awareness
	mergedTargets := s.mergeSegmentTargets(segmentTargets, segmentCanProgress)

	return mergedTargets, nil
}

// calculateSingleSegmentTargets calculates target replicas for a single segment scheduling
func (s *SegmentScheduler) calculateSingleSegmentTargets(
	segmentScheduling *workloadsv1alpha1.SegmentScheduling,
) map[string]int32 {
	if segmentScheduling == nil ||
		segmentScheduling.SegmentSize == nil ||
		len(segmentScheduling.SegmentSize) == 0 {
		return nil
	}

	// Get partition strategy, default to SequentialWait
	partitionStrategy := segmentScheduling.PartitionStrategy
	if partitionStrategy == "" {
		partitionStrategy = workloadsv1alpha1.SegmentPartitionStrategySequentialWait
	}

	roleStatusMap := s.buildRoleStatusMap()
	segmentConfig := segmentScheduling.SegmentSize

	// Calculate the minimum completed full segment across all roles
	minFullSegment := s.calculateMinFullSegment(roleStatusMap, segmentConfig)

	// For SequentialWait strategy, check if current segment is fully ready
	if partitionStrategy == workloadsv1alpha1.SegmentPartitionStrategySequentialWait &&
		!s.isSegmentReady(minFullSegment, roleStatusMap, segmentConfig) {
		// Current segment is not ready, keep the current segment
		return s.getSegmentTargets(minFullSegment, roleStatusMap, segmentConfig)
	}

	// For Sequential strategy or SequentialWait with ready segment, advance to next segment
	return s.getSegmentTargets(minFullSegment+1, roleStatusMap, segmentConfig)
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

// calculateMinFullSegment calculates the minimum number of complete segments across all roles
// Returns the index of the last fully deployed segment (0-based)
// Returns 0 if no segments are deployed yet
func (s *SegmentScheduler) calculateMinFullSegment(
	roleStatusMap map[string]workloadsv1alpha1.RoleStatus,
	segmentConfig map[string]int32,
) int {
	minFullSegment := math.MaxInt

	for roleName, segmentSize := range segmentConfig {
		if segmentSize == 0 {
			continue
		}

		currentReplicas := int32(0)
		if status, ok := roleStatusMap[roleName]; ok {
			currentReplicas = status.Replicas
		}

		// Calculate how many complete segments have been deployed for this role
		completeSegments := int(currentReplicas / segmentSize)

		if completeSegments < minFullSegment {
			minFullSegment = completeSegments
		}
	}

	if minFullSegment == math.MaxInt {
		// No roles configured
		return 0
	}

	return minFullSegment
}

// isSegmentReady checks if the specified segment is fully ready across all roles
// segmentIndex is 0-based: 0 means first segment, 1 means second segment, etc.
// Returns true if all roles have deployed and ready replicas for this segment
func (s *SegmentScheduler) isSegmentReady(
	segmentIndex int,
	roleStatusMap map[string]workloadsv1alpha1.RoleStatus,
	segmentConfig map[string]int32,
) bool {
	if segmentIndex == 0 {
		// No segments deployed yet, ready to deploy first segment
		return true
	}

	// Check if all roles have the expected replicas for this segment ready
	for roleName, segmentSize := range segmentConfig {
		// Calculate expected replicas for this segment
		expectedReplicas := int32(segmentIndex) * segmentSize

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

		// Check if this role has deployed and ready replicas for this segment
		if status.Replicas < expectedReplicas || status.ReadyReplicas < expectedReplicas {
			return false
		}
	}

	return true
}

// getSegmentTargets calculates the target replicas for a specific segment
// segmentIndex is 0-based: 0 means keep current, 1 means first segment, 2 means second segment, etc.
func (s *SegmentScheduler) getSegmentTargets(
	segmentIndex int,
	roleStatusMap map[string]workloadsv1alpha1.RoleStatus,
	segmentConfig map[string]int32,
) map[string]int32 {
	targetReplicas := make(map[string]int32)

	for roleName, segmentSize := range segmentConfig {
		if segmentIndex == 0 {
			// Keep current replicas
			if status, ok := roleStatusMap[roleName]; ok {
				targetReplicas[roleName] = status.Replicas
			} else {
				targetReplicas[roleName] = 0
			}
			continue
		}

		// Calculate target for this segment
		targetForSegment := int32(segmentIndex) * segmentSize

		// Get desired replicas to ensure we don't exceed it
		desiredReplicas := s.getDesiredReplicas(roleName)
		if desiredReplicas == 0 {
			continue
		}
		if targetForSegment > desiredReplicas {
			targetForSegment = desiredReplicas
		}

		targetReplicas[roleName] = targetForSegment
	}

	return targetReplicas
}

// detectSegmentConflicts checks if there are conflicts in segment configurations
// A conflict occurs when the same role has different segment sizes or partition strategies in different coordinations
func (s *SegmentScheduler) detectSegmentConflicts(
	segmentSchedulings []*workloadsv1alpha1.SegmentScheduling,
) error {
	// Build maps of role name to segment size and partition strategy for conflict detection
	type roleConfig struct {
		segmentSize       int32
		partitionStrategy workloadsv1alpha1.SegmentPartitionStrategyType
	}
	roleConfigs := make(map[string]roleConfig)

	for _, scheduling := range segmentSchedulings {
		// Get partition strategy with default
		partitionStrategy := scheduling.PartitionStrategy
		if partitionStrategy == "" {
			partitionStrategy = workloadsv1alpha1.SegmentPartitionStrategySequentialWait
		}

		for roleName, segmentSize := range scheduling.SegmentSize {
			if existingConfig, exists := roleConfigs[roleName]; exists {
				// Check segment size conflict
				if existingConfig.segmentSize != segmentSize {
					return fmt.Errorf(
						"segment size conflict for role %q: coordination has segment size %d, but another coordination has %d",
						roleName, segmentSize, existingConfig.segmentSize,
					)
				}
				// Check partition strategy conflict
				if existingConfig.partitionStrategy != partitionStrategy {
					return fmt.Errorf(
						"partition strategy conflict for role %q: coordination has strategy %q, but another coordination has %q",
						roleName, partitionStrategy, existingConfig.partitionStrategy,
					)
				}
			} else {
				roleConfigs[roleName] = roleConfig{
					segmentSize:       segmentSize,
					partitionStrategy: partitionStrategy,
				}
			}
		}
	}

	return nil
}

// mergeSegmentTargets merges target replicas from multiple coordinations
// It ensures coordinated progression with cross-coordination dependencies:
// 1. For each coordination, check if any of its roles is blocked by another coordination
// 2. If a coordination has any blocked role, ALL its roles are blocked
// 3. Take MINIMUM target across all non-blocked coordination targets for each role
func (s *SegmentScheduler) mergeSegmentTargets(
	coordinationTargets []map[string]int32,
	coordinationCanProgress []bool,
) map[string]int32 {
	if len(coordinationTargets) == 0 {
		return nil
	}

	// Build current replicas map
	currentReplicas := make(map[string]int32)
	for _, status := range s.roleStatuses {
		currentReplicas[status.Name] = status.Replicas
	}

	// Track which coordinations each role appears in
	roleCoordinations := make(map[string][]int)
	for coordIdx, targets := range coordinationTargets {
		if targets == nil {
			continue
		}
		for roleName := range targets {
			roleCoordinations[roleName] = append(roleCoordinations[roleName], coordIdx)
		}
	}

	// For each role, check if it can progress (all its coordinations must agree)
	roleCanProgress := make(map[string]bool)
	for roleName, coordIndices := range roleCoordinations {
		allCanProgress := true
		for _, coordIdx := range coordIndices {
			if !coordinationCanProgress[coordIdx] {
				allCanProgress = false
				break
			}
		}
		roleCanProgress[roleName] = allCanProgress
	}

	// Now check if each coordination as a whole can progress
	// A coordination is blocked if ANY of its roles cannot progress
	coordinationActuallyCanProgress := make([]bool, len(coordinationTargets))
	for coordIdx, targets := range coordinationTargets {
		if targets == nil {
			coordinationActuallyCanProgress[coordIdx] = true
			continue
		}

		canProgress := true
		for roleName := range targets {
			if !roleCanProgress[roleName] {
				// This role is blocked by another coordination
				canProgress = false
				break
			}
		}
		coordinationActuallyCanProgress[coordIdx] = canProgress
	}

	// Collect targets for each role based on whether its coordination can progress
	roleProgressTargets := make(map[string][]int32)
	for coordIdx, targets := range coordinationTargets {
		if targets == nil {
			continue
		}

		for roleName, targetReplicas := range targets {
			if coordinationActuallyCanProgress[coordIdx] {
				// This coordination can progress, use its target
				roleProgressTargets[roleName] = append(roleProgressTargets[roleName], targetReplicas)
			} else {
				// This coordination is blocked, keep current replicas for all its roles
				if current, ok := currentReplicas[roleName]; ok {
					roleProgressTargets[roleName] = append(roleProgressTargets[roleName], current)
				} else {
					roleProgressTargets[roleName] = append(roleProgressTargets[roleName], 0)
				}
			}
		}
	}

	// Merge: take minimum across all coordinations for each role
	merged := make(map[string]int32)
	for roleName, targets := range roleProgressTargets {
		if len(targets) == 0 {
			continue
		}

		minTarget := targets[0]
		for _, target := range targets[1:] {
			if target < minTarget {
				minTarget = target
			}
		}

		merged[roleName] = minTarget
	}

	return merged
}

// IsActive checks if any coordination has segment scheduling enabled
func IsActive(rbg *workloadsv1alpha1.RoleBasedGroup) bool {
	for _, coordination := range rbg.Spec.CoordinationRequirements {
		if coordination.Strategy != nil && coordination.Strategy.SegmentScheduling != nil {
			return true
		}
	}
	return false
}
