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

package utils

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	intstrutil "k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/integer"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

const (
	FieldManager = "rbg"

	// RBGReplicaFieldManager is used when the RBGSA controller writes
	// spec.roles[].replicas back to its parent RBG.
	//
	// SSA-claim release semantics: when the same field manager re-applies with a
	// narrower claim, fields it previously owned but no longer claims are released
	// and (if no other manager owns them) removed by the API server. The discovery
	// annotation Apply, the RBGSet→RBG sync Apply, and this replica Apply each
	// claim disjoint subsets of the RBG, so they MUST run under distinct field
	// managers — otherwise they ping-pong and silently strip each other's writes.
	RBGReplicaFieldManager = "rbg-replicas"

	// RBGDiscoveryFieldManager is used when the RBG controller writes the
	// discovery-config-mode annotation. See RBGReplicaFieldManager for the
	// rationale behind a distinct field manager.
	RBGDiscoveryFieldManager = "rbg-discovery"

	// RBGSetSyncFieldManager is used when the RBGSet controller syncs metadata
	// and spec.roles from a parent RBGSet template down to its child RBGs. See
	// RBGReplicaFieldManager for the rationale behind a distinct field manager.
	RBGSetSyncFieldManager = "rbg-set-sync"

	PatchAll    PatchType = "all"
	PatchSpec   PatchType = "spec"
	PatchStatus PatchType = "status"
)

type PatchType string

func PatchObjectApplyConfiguration(
	ctx context.Context, k8sClient client.Client,
	objApplyConfig interface{}, patchType PatchType,
) error {
	return PatchObjectApplyConfigurationWithFieldManager(ctx, k8sClient, objApplyConfig, patchType, FieldManager)
}

// PatchObjectApplyConfigurationWithFieldManager is like PatchObjectApplyConfiguration but uses
// the provided field manager. Use a distinct field manager for each logical Apply path that
// targets disjoint fields on the same object, otherwise SSA-claim-release semantics will cause
// the Apply calls to strip each other's writes.
//
//nolint:staticcheck // SA1019: Use client.Client.Apply() instead
func PatchObjectApplyConfigurationWithFieldManager(
	ctx context.Context, k8sClient client.Client,
	objApplyConfig interface{}, patchType PatchType, fieldManager string,
) error {
	logger := log.FromContext(ctx)
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(objApplyConfig)
	if err != nil {
		logger.Error(err, "Converting obj apply configuration to json.")
		return err
	}

	patch := &unstructured.Unstructured{
		Object: obj,
	}

	logger.V(1).Info("patch content", "patchObject", patch.Object)

	if patchType == PatchSpec || patchType == PatchAll {
		err = k8sClient.Patch(
			ctx, patch, client.Apply, &client.PatchOptions{
				FieldManager: fieldManager,
				Force:        ptr.To[bool](true),
			},
		)
		if err != nil {
			logger.Error(err, "Using server side apply to patch object")
			return err
		}
	}

	if patchType == PatchStatus || patchType == PatchAll {
		err = k8sClient.Status().Patch(
			ctx, patch, client.Apply,
			&client.SubResourcePatchOptions{
				PatchOptions: client.PatchOptions{
					FieldManager: fieldManager,
					Force:        ptr.To[bool](true),
				},
			},
		)
		if err != nil {
			logger.Error(err, "Using server side apply to patch object status")
			return err
		}
	}

	return nil
}

// CalculatePartitionReplicas returns absolute value of partition for workload. This func can solve some
// corner cases about percentage-type partition, such as:
// - if partition > "0%" and replicas > 0, we will ensure at least 1 old pod is reserved.
// - if partition < "100%" and replicas > 1, we will ensure at least 1 pod is upgraded.
func CalculatePartitionReplicas(partition *intstrutil.IntOrString, replicasPointer *int32) (int, error) {
	if partition == nil {
		return 0, nil
	}

	replicas := 1
	if replicasPointer != nil {
		replicas = int(*replicasPointer)
	}

	// 'roundUp=true' will ensure at least 1 old pod is reserved if partition > "0%" and replicas > 0.
	pValue, err := intstrutil.GetScaledValueFromIntOrPercent(partition, replicas, true)
	if err != nil {
		return pValue, err
	}

	// if partition < "100%" and replicas >= 1, we will ensure at least 1 pod is upgraded.
	if replicas >= 1 && pValue == replicas && partition.Type == intstrutil.String && partition.StrVal != "100%" {
		pValue = replicas - 1
	}

	pValue = integer.IntMax(integer.IntMin(pValue, replicas), 0)
	return pValue, nil
}

// GetRoleReplicasV2 returns the replicas for a role from a v1alpha2 RoleBasedGroup.
func GetRoleReplicasV2(rbg *workloadsv1alpha2.RoleBasedGroup, roleName string) int32 {
	for _, role := range rbg.Spec.Roles {
		if role.Name == roleName {
			if role.Replicas != nil {
				return *role.Replicas
			}
			return 1 // default replicas
		}
	}
	return 0
}

func ParseIntStrAsNonZero(p intstrutil.IntOrString, replicas int32) (int32, error) {
	value, err := intstrutil.GetScaledValueFromIntOrPercent(&p, int(replicas), true)
	if err != nil {
		return 1, err
	} else if value < 1 {
		value = 1
	}
	return int32(value), nil
}

func ABSFloat64(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func ContainsString(slice []string, str string) bool {
	for _, v := range slice {
		if v == str {
			return true
		}
	}
	return false
}

func PrettyJson(object interface{}) string {
	b, err := json.MarshalIndent(object, "", "    ")
	if err != nil {
		fmt.Printf("ERROR: PrettyJson, %v\n %s\n", err, b)
		return ""
	}
	return string(b)
}

func FilterSystemAnnotations(annotations map[string]string) map[string]string {
	if annotations == nil {
		return nil
	}

	filtered := make(map[string]string)
	for k, v := range annotations {
		if !strings.HasPrefix(k, "deployment.kubernetes.io/revision") &&
			!strings.HasPrefix(k, "rolebasedgroup.workloads.x-k8s.io/") &&
			!strings.HasPrefix(k, "app.kubernetes.io/") {
			filtered[k] = v
		}
	}
	return filtered
}

func FilterSystemLabels(labels map[string]string) map[string]string {
	if labels == nil {
		return nil
	}

	filtered := make(map[string]string)
	for k, v := range labels {

		if !strings.HasPrefix(k, "app.kubernetes.io/") &&
			!strings.HasPrefix(k, "rolebasedgroup.workloads.x-k8s.io/") {
			filtered[k] = v
		}
	}
	return filtered

}

func FilterSystemEnvs(envs []corev1.EnvVar) []corev1.EnvVar {
	var filtered []corev1.EnvVar
	for _, env := range envs {
		if !strings.HasPrefix(env.Name, "ROLE_") && env.Name != "GROUP_NAME" {
			filtered = append(filtered, env)
		}
	}
	return filtered
}

// DumpJSON returns the JSON encoding
func DumpJSON(o interface{}) string {
	j, _ := json.Marshal(o)
	return string(j)
}

func NonZeroValue(value int32) int32 {
	if value < 0 {
		return 0
	}
	return value
}

// RoleInMaxSkewCoordinationV2 checks if the given role is part of a MaxSkew coordination
// in v1alpha2. In v1alpha2, coordination rules are managed via separate CoordinatedPolicy
// resources, not embedded in the RoleBasedGroup spec, so this always returns false.
func RoleInMaxSkewCoordinationV2(_ *workloadsv1alpha2.RoleBasedGroup, _ string) bool {
	return false
}
