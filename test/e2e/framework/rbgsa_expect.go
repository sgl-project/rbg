package framework

import (
	"fmt"

	"github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/rbgs/pkg/scale"
	"sigs.k8s.io/rbgs/test/utils"

	autoscalingv1 "k8s.io/api/autoscaling/v1"
	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

func (f *Framework) ExpectRoleScalingAdapterEqual(rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec,
	expectedReplicas *int32) {
	logger := log.FromContext(f.Ctx).WithValues("rbg", rbg.Name)

	gomega.Eventually(func() bool {
		rbgSa := &v1alpha1.RoleBasedGroupScalingAdapter{}
		err := f.Client.Get(f.Ctx, client.ObjectKey{
			Name:      scale.GenerateScalingAdapterName(rbg.Name, role.Name),
			Namespace: rbg.Namespace,
		}, rbgSa)
		if err != nil {
			if !apierrors.IsNotFound(err) {
				logger.Error(err, "get rbg error")
			}
			return false
		}

		scale := &autoscalingv1.Scale{}
		if err := f.Client.SubResource("scale").Get(f.Ctx, rbgSa, scale); err != nil {
			logger.Info("get subresource-scale for rbg scaling adapter failed, wait next time", "reason", err.Error())
			return false
		}
		if err := f.Client.Get(f.Ctx, client.ObjectKey{
			Name:      rbg.Name,
			Namespace: rbg.Namespace,
		}, rbg); err != nil {
			logger.Info("get subresource-scale for rbg scaling adapter failed, wait next time", "reason", err.Error())
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
		err = expectRbgScalingAdapterEqual(rbgSa, rbg, role, scale, expectedReplicas)
		if err != nil {
			logger.Info("rbgScalingAdapter not equal, wait next time", "reason", err.Error())
		}
		return err == nil
	}, utils.Timeout, utils.Interval).Should(gomega.BeTrue())
}

func (f *Framework) ExpectRbgScalingAdapterEqual(rbg *v1alpha1.RoleBasedGroup) {
	for _, role := range rbg.Spec.Roles {
		if role.ScalingAdapter == nil || !role.ScalingAdapter.Enable {
			f.ExpectScalingAdapterNotExist(rbg, role)
		} else {
			f.ExpectRoleScalingAdapterEqual(rbg, role, nil)
		}
	}
}

func (f *Framework) ExpectScalingAdapterNotExist(rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec) {
	checkScalingAdapterNotExist := func() error {
		rbgSa := &v1alpha1.RoleBasedGroupScalingAdapter{}
		err := f.Client.Get(f.Ctx, client.ObjectKey{
			Name:      scale.GenerateScalingAdapterName(rbg.Name, role.Name),
			Namespace: rbg.Namespace,
		}, rbgSa)
		if err == nil {
			return fmt.Errorf("rbg scalingAdapter still exists")
		}
		if !apierrors.IsNotFound(err) {
			return err
		}
		return nil
	}

	gomega.Eventually(func() bool {
		return checkScalingAdapterNotExist() == nil
	}, utils.Timeout, utils.Interval).Should(gomega.BeTrue())
}

func expectRbgScalingAdapterEqual(rbgScalingAdapter *v1alpha1.RoleBasedGroupScalingAdapter,
	rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec, scale *autoscalingv1.Scale,
	expectedReplicas *int32) error {
	if rbgScalingAdapter == nil || rbgScalingAdapter.Spec.Replicas == nil {
		return fmt.Errorf("rbgScalingAdapter is nil")
	}

	targetRef := rbgScalingAdapter.Spec.ScaleTargetRef
	if targetRef == nil {
		return fmt.Errorf("ScaleTargetRef is nil")
	}

	if targetRef.Name != rbg.Name {
		return fmt.Errorf("ScaleTargetRef.Name %s does not match rbg.Name %s", targetRef.Name, rbg.Name)
	}

	if targetRef.Role != role.Name {
		return fmt.Errorf("ScaleTargetRef.Role %s does not match role.Name %s", targetRef.Role, role.Name)
	}

	if expectedReplicas != nil {
		if scale.Spec.Replicas != *expectedReplicas {
			return fmt.Errorf("Scale.Spec.Replicas %v does not match role.Replicas %v", scale.Spec.Replicas, *expectedReplicas)
		}

		if scale.Status.Replicas != *expectedReplicas {
			return fmt.Errorf("Scale.Status.Replicas %v does not match role.Replicas %v",
				scale.Status.Replicas, *expectedReplicas)
		}
	} else {
		expectedReplicas = role.Replicas // initial replicas
	}

	if *rbgScalingAdapter.Spec.Replicas != *expectedReplicas {
		return fmt.Errorf("ScalingAdapter.Spec.Replicas %v does not match expectedReplicas %v",
			*rbgScalingAdapter.Spec.Replicas, *expectedReplicas)
	}

	if *rbgScalingAdapter.Status.Replicas != *expectedReplicas {
		return fmt.Errorf("ScalingAdapter.Spec.Replicas %v does not match expectedReplicas %v",
			*rbgScalingAdapter.Status.Replicas, *expectedReplicas)
	}

	if *role.Replicas != *expectedReplicas {
		return fmt.Errorf("Role.Replicas %v does not match expectedReplicas %v", *role.Replicas,
			*expectedReplicas)
	}

	if rbgScalingAdapter.Status.Phase != v1alpha1.AdapterPhaseBound {
		return fmt.Errorf("ScalingAdapter.Status.Phase %s is not AdapterPhaseBound",
			rbgScalingAdapter.Status.Phase)
	}

	return nil
}
