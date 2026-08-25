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

package common

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// GetGangStrategy returns the gang scheduling strategy for the given RBG.
//
// It first checks the CoordinatedPolicy (same name/namespace as the RBG) for a
// Scheduling.Gang strategy. If none is found, it falls back to the legacy
// annotation compat path: when the rbg has the gang-scheduling annotation set
// to "true", a default (empty) GangSchedulingStrategy is returned so that
// callers can treat all gang-enabling paths uniformly via a non-nil pointer.
//
// Returns nil when gang scheduling is not enabled.
func GetGangStrategy(ctx context.Context, c client.Reader, rbg *workloadsv1alpha2.RoleBasedGroup) *workloadsv1alpha2.GangSchedulingStrategy {
	// 1. Check CoordinatedPolicy for an explicit gang strategy.
	coordinatedPolicy := &workloadsv1alpha2.CoordinatedPolicy{}
	if err := c.Get(ctx, types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace}, coordinatedPolicy); err == nil {
		for i := range coordinatedPolicy.Spec.Policies {
			policy := &coordinatedPolicy.Spec.Policies[i]
			if policy.Strategy.Scheduling != nil && policy.Strategy.Scheduling.Gang != nil {
				return policy.Strategy.Scheduling.Gang
			}
		}
	}

	// 2. Fall back to the legacy annotation compat path.
	if rbg.Annotations[constants.GangSchedulingAnnotationKey] == "true" {
		return &workloadsv1alpha2.GangSchedulingStrategy{}
	}

	return nil
}
