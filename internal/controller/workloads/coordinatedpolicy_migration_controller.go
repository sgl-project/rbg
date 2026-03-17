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

// NOTE: This file exists solely to support the v1alpha1 → v1alpha2 migration
// compatibility layer. When v1alpha1 support is removed, this entire file and
// the annotationCoordMigration constant can be deleted in one shot.

package workloads

import (
	"context"
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/util/intstr"
	metaapplyv1 "k8s.io/client-go/applyconfigurations/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	applyv1alpha2 "sigs.k8s.io/rbgs/client-go/applyconfiguration/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/utils"
)

const (
	// annotationCoordMigration is the annotation key written by the conversion webhook
	// to carry v1alpha1 CoordinationRequirements that have no v1alpha2 schema equivalent.
	//
	// Lifecycle: this annotation is a persistent declaration of coordination intent for
	// objects that originated from v1alpha1. As long as it is present on an RBG, the
	// RBG reconciler ensures a matching CoordinatedPolicy exists. The annotation is never
	// removed — it stays until v1alpha1 support is dropped, at which point this file
	// can be deleted entirely.
	annotationCoordMigration = "conversion.workloads.x-k8s.io/v1alpha1-coordination"
)

// EnsureV1alpha1CoordinatedPolicy inspects the RBG for the v1alpha1 coordination
// annotation and, if present, creates or updates a CoordinatedPolicy to match
// using server-side apply.
// It is a no-op when the annotation is absent.
//
// This function is called from the RBG reconciler as a compatibility step for
// objects that originated from v1alpha1. When v1alpha1 support is removed,
// remove the call site in the RBG reconciler and delete this file.
func EnsureV1alpha1CoordinatedPolicy(ctx context.Context, c client.Client, rbg *workloadsv1alpha2.RoleBasedGroup) error {
	logger := log.FromContext(ctx)

	raw, ok := rbg.Annotations[annotationCoordMigration]
	if !ok || raw == "" {
		return nil
	}

	var coords []workloadsv1alpha1.Coordination
	if err := json.Unmarshal([]byte(raw), &coords); err != nil {
		return fmt.Errorf("unmarshal v1alpha1 coordination annotation on %s/%s: %w",
			rbg.Namespace, rbg.Name, err)
	}

	policyApplyConfig := buildCoordinatedPolicyApplyConfiguration(rbg, coords)

	logger.V(1).Info("applying CoordinatedPolicy from v1alpha1 annotation",
		"name", rbg.Name, "namespace", rbg.Namespace)

	if err := utils.PatchObjectApplyConfiguration(ctx, c, policyApplyConfig, utils.PatchSpec); err != nil {
		return fmt.Errorf("apply CoordinatedPolicy %s/%s: %w", rbg.Namespace, rbg.Name, err)
	}

	return nil
}

// buildCoordinatedPolicyApplyConfiguration converts v1alpha1 CoordinationRequirements
// into a CoordinatedPolicy apply configuration owned by the given RoleBasedGroup.
func buildCoordinatedPolicyApplyConfiguration(
	rbg *workloadsv1alpha2.RoleBasedGroup,
	coords []workloadsv1alpha1.Coordination,
) *applyv1alpha2.CoordinatedPolicyApplyConfiguration {
	rules := make([]*applyv1alpha2.CoordinatedPolicyRuleApplyConfiguration, 0, len(coords))
	for _, c := range coords {
		rule := applyv1alpha2.CoordinatedPolicyRule().
			WithRoles(c.Roles...)
		if strategy := convertCoordinationStrategyApplyConfiguration(c.Strategy); strategy != nil {
			rule = rule.WithStrategy(strategy)
		}
		rules = append(rules, rule)
	}

	return applyv1alpha2.CoordinatedPolicy(rbg.Name, rbg.Namespace).
		WithOwnerReferences(
			metaapplyv1.OwnerReference().
				WithAPIVersion(workloadsv1alpha2.GroupVersion.String()).
				WithKind("RoleBasedGroup").
				WithName(rbg.Name).
				WithUID(rbg.UID).
				WithBlockOwnerDeletion(true),
		).
		WithSpec(
			applyv1alpha2.CoordinatedPolicySpec().
				WithPolicies(rules...),
		)
}

// convertCoordinationStrategyApplyConfiguration maps a v1alpha1 CoordinationStrategy
// to a v1alpha2 CoordinatedPolicyStrategy apply configuration.
// Returns nil when src is nil. Fields with no direct equivalent are dropped.
func convertCoordinationStrategyApplyConfiguration(src *workloadsv1alpha1.CoordinationStrategy) *applyv1alpha2.CoordinatedPolicyStrategyApplyConfiguration {
	if src == nil {
		return nil
	}

	out := applyv1alpha2.CoordinatedPolicyStrategy()

	if src.RollingUpdate != nil {
		ru := applyv1alpha2.RollingUpdateCoordinationStrategy()
		if src.RollingUpdate.MaxSkew != nil {
			ru = ru.WithMaxSkew(intstr.FromString(*src.RollingUpdate.MaxSkew))
		}
		if src.RollingUpdate.Partition != nil {
			ru = ru.WithPartition(intstr.FromString(*src.RollingUpdate.Partition))
		}
		if src.RollingUpdate.MaxUnavailable != nil {
			ru = ru.WithMaxUnavailable(intstr.FromString(*src.RollingUpdate.MaxUnavailable))
		}
		out = out.WithRollingUpdate(ru)
	}

	if src.Scaling != nil {
		sc := applyv1alpha2.ScalingCoordinationStrategy()
		if src.Scaling.MaxSkew != nil {
			sc = sc.WithMaxSkew(intstr.FromString(*src.Scaling.MaxSkew))
		}
		if src.Scaling.Progression != nil {
			switch *src.Scaling.Progression {
			case workloadsv1alpha1.OrderScheduled:
				sc = sc.WithProgression(workloadsv1alpha2.OrderScheduledProgression)
				// OrderReady has no equivalent in v1alpha2; dropped.
			}
		}
		out = out.WithScaling(sc)
	}

	return out
}
