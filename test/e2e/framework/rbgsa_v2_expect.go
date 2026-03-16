package framework

import (
	"fmt"
	"strconv"

	"github.com/onsi/gomega"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/constants"
	"sigs.k8s.io/rbgs/pkg/scale"
	"sigs.k8s.io/rbgs/test/utils"
)

// ExpectRbgV2ScalingAdapterEqual checks the ScalingAdapter for each role.
func (f *Framework) ExpectRbgV2ScalingAdapterEqual(rbg *workloadsv1alpha2.RoleBasedGroup) {
	for _, role := range rbg.Spec.Roles {
		if role.ScalingAdapter == nil || !role.ScalingAdapter.Enable {
			f.ExpectScalingAdapterV2NotExist(rbg, role)
		} else {
			f.ExpectRoleScalingAdapterV2Equal(rbg, role, nil)
		}
	}
}

// ExpectRoleScalingAdapterV2Equal checks that the ScalingAdapter for the given role matches expectations.
func (f *Framework) ExpectRoleScalingAdapterV2Equal(
	rbg *workloadsv1alpha2.RoleBasedGroup, role workloadsv1alpha2.RoleSpec, expectedReplicas *int32,
) {
	logger := log.FromContext(f.Ctx).WithValues("rbg", rbg.Name)

	gomega.Eventually(func() bool {
		rbgSa := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{}
		err := f.Client.Get(f.Ctx, client.ObjectKey{
			Name:      scale.GenerateScalingAdapterName(rbg.Name, role.Name),
			Namespace: rbg.Namespace,
		}, rbgSa)
		if err != nil {
			if !apierrors.IsNotFound(err) {
				logger.Error(err, "get v2 rbgsa error")
			}
			return false
		}

		scaleObj := &autoscalingv1.Scale{}
		if err := f.Client.SubResource("scale").Get(f.Ctx, rbgSa, scaleObj); err != nil {
			logger.Info("get subresource-scale for v2 rbgsa failed, wait next time", "reason", err.Error())
			return false
		}
		if err := f.Client.Get(f.Ctx, client.ObjectKey{
			Name:      rbg.Name,
			Namespace: rbg.Namespace,
		}, rbg); err != nil {
			logger.Info("get rbg v2 failed, wait next time", "reason", err.Error())
			return false
		}

		newRoleFound := false
		for _, newRole := range rbg.Spec.Roles {
			if newRole.Name == role.Name {
				role, newRoleFound = newRole, true
				break
			}
		}
		if !newRoleFound {
			return false
		}

		err = expectRbgV2ScalingAdapterEqual(rbgSa, rbg, role, scaleObj, expectedReplicas)
		if err != nil {
			logger.Info("v2 rbgScalingAdapter not equal, wait next time", "reason", err.Error())
		}
		return err == nil
	}, utils.Timeout, utils.Interval).Should(gomega.BeTrue())
}

// ExpectScalingAdapterV2NotExist checks that the ScalingAdapter for the given role does not exist.
func (f *Framework) ExpectScalingAdapterV2NotExist(rbg *workloadsv1alpha2.RoleBasedGroup, role workloadsv1alpha2.RoleSpec) {
	checkNotExist := func() error {
		rbgSa := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{}
		err := f.Client.Get(f.Ctx, client.ObjectKey{
			Name:      scale.GenerateScalingAdapterName(rbg.Name, role.Name),
			Namespace: rbg.Namespace,
		}, rbgSa)
		if err == nil {
			return fmt.Errorf("v2 rbg scalingAdapter still exists")
		}
		if !apierrors.IsNotFound(err) {
			return err
		}
		return nil
	}

	gomega.Eventually(func() bool {
		return checkNotExist() == nil
	}, utils.Timeout, utils.Interval).Should(gomega.BeTrue())
}

func expectRbgV2ScalingAdapterEqual(
	rbgSa *workloadsv1alpha2.RoleBasedGroupScalingAdapter,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	role workloadsv1alpha2.RoleSpec,
	scaleObj *autoscalingv1.Scale,
	expectedReplicas *int32,
) error {
	if rbgSa == nil || rbgSa.Spec.Replicas == nil {
		return fmt.Errorf("rbgSa is nil or replicas is nil")
	}

	targetRef := rbgSa.Spec.ScaleTargetRef
	if targetRef == nil {
		return fmt.Errorf("ScaleTargetRef is nil")
	}

	if targetRef.Name != rbg.Name {
		return fmt.Errorf("ScaleTargetRef.Name %s != rbg.Name %s", targetRef.Name, rbg.Name)
	}

	if targetRef.Role != role.Name {
		return fmt.Errorf("ScaleTargetRef.Role %s != role.Name %s", targetRef.Role, role.Name)
	}

	if expectedReplicas != nil {
		if scaleObj.Spec.Replicas != *expectedReplicas {
			return fmt.Errorf("Scale.Spec.Replicas %v != expectedReplicas %v", scaleObj.Spec.Replicas, *expectedReplicas)
		}
		if scaleObj.Status.Replicas != *expectedReplicas {
			return fmt.Errorf("Scale.Status.Replicas %v != expectedReplicas %v", scaleObj.Status.Replicas, *expectedReplicas)
		}
	} else {
		expectedReplicas = role.Replicas
	}

	if *rbgSa.Spec.Replicas != *expectedReplicas {
		return fmt.Errorf("ScalingAdapter.Spec.Replicas %v != expectedReplicas %v",
			*rbgSa.Spec.Replicas, *expectedReplicas)
	}

	if rbgSa.Status.Replicas == nil || *rbgSa.Status.Replicas != *expectedReplicas {
		current := "nil"
		if rbgSa.Status.Replicas != nil {
			current = strconv.Itoa(int(*rbgSa.Status.Replicas))
		}
		return fmt.Errorf("ScalingAdapter.Status.Replicas %s != expectedReplicas %v", current, *expectedReplicas)
	}

	if *role.Replicas != *expectedReplicas {
		return fmt.Errorf("Role.Replicas %v != expectedReplicas %v", *role.Replicas, *expectedReplicas)
	}

	if rbgSa.Status.Phase != constants.AdapterPhaseBound {
		return fmt.Errorf("ScalingAdapter.Status.Phase %s is not AdapterPhaseBound", rbgSa.Status.Phase)
	}

	return nil
}
