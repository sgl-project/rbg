package utils

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	intstrutil "k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/integer"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

const (
	FieldManager = "rbg"

	PatchAll    PatchType = "all"
	PatchSpec   PatchType = "spec"
	PatchStatus PatchType = "status"
)

type PatchType string

// nolint:staticcheck
// TODO:  SA1019: Use client.Client.Apply() instead
func PatchObjectApplyConfiguration(
	ctx context.Context, k8sClient client.Client,
	objApplyConfig interface{}, patchType PatchType,
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

	// Use server side apply and add fieldmanager to the rbg owned fields
	// If there are conflicts in the fields owned by the rbg controller, rbg will obtain the ownership and force override
	// these fields to the ones desired by the rbg controller
	// TODO b/316776287 add E2E test for SSA
	if patchType == PatchSpec || patchType == PatchAll {
		err = k8sClient.Patch(
			ctx, patch, client.Apply, &client.PatchOptions{
				FieldManager: FieldManager,
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
					FieldManager: FieldManager,
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

func GetRoleReplicas(rbg *v1alpha1.RoleBasedGroup, roleName string) int32 {
	for _, role := range rbg.Spec.Roles {
		if role.Name == roleName {
			return *role.Replicas
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

func RoleInMaxSkewCoordination(rbg *v1alpha1.RoleBasedGroup, role string) bool {
	for _, coordination := range rbg.Spec.CoordinationRequirements {
		if !slices.Contains(coordination.Roles, role) {
			continue
		}
		if coordination.Strategy != nil &&
			coordination.Strategy.RollingUpdate != nil &&
			coordination.Strategy.RollingUpdate.MaxSkew != nil {
			return true
		}
	}
	return false
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
