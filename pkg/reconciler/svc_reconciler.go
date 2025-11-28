package reconciler

import (
	"context"
	"fmt"
	"reflect"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	coreapplyv1 "k8s.io/client-go/applyconfigurations/core/v1"
	metaapplyv1 "k8s.io/client-go/applyconfigurations/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/utils"
)

type ServiceReconciler struct {
	client client.Client
}

func NewServiceReconciler(client client.Client) *ServiceReconciler {
	return &ServiceReconciler{
		client: client,
	}
}

func (r *ServiceReconciler) reconcileHeadlessService(
	ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup, role *workloadsv1alpha1.RoleSpec,
) error {
	logger := log.FromContext(ctx)
	logger.V(1).Info("start to reconciling headless service")

	workload, err := r.getObjectByKind(ctx, rbg, role)
	if err != nil {
		return err
	}

	svcApplyConfig, err := r.constructServiceApplyConfiguration(ctx, rbg, role, workload)
	if err != nil {
		return fmt.Errorf("constructServiceApplyConfiguration error: %s", err.Error())
	}
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(svcApplyConfig)
	if err != nil {
		logger.Error(err, "Converting obj apply configuration to json.")
		return err
	}

	newSvc := &corev1.Service{}
	if err = runtime.DefaultUnstructuredConverter.FromUnstructured(obj, newSvc); err != nil {
		return fmt.Errorf("convert svcApplyConfig to svc error: %s", err.Error())
	}

	oldSvc := &corev1.Service{}
	svcName, err := utils.GetCompatibleHeadlessServiceName(ctx, r.client, rbg, role)
	if err != nil {
		return fmt.Errorf("GetCompatibleHeadlessServiceName error: %s", err.Error())
	}
	err = r.client.Get(ctx, types.NamespacedName{Name: svcName, Namespace: rbg.Namespace}, oldSvc)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	equal, err := semanticallyEqualService(oldSvc, newSvc)
	if equal {
		logger.V(1).Info("svc equal, skip reconcile")
		return nil
	}

	logger.V(1).Info(fmt.Sprintf("svc not equal, diff: %s", err.Error()))

	if err := utils.PatchObjectApplyConfiguration(ctx, r.client, svcApplyConfig, utils.PatchSpec); err != nil {
		logger.Error(err, "Failed to patch svc apply configuration")
		return err
	}

	return nil
}

func (r *ServiceReconciler) constructServiceApplyConfiguration(
	ctx context.Context,
	rbg *workloadsv1alpha1.RoleBasedGroup,
	role *workloadsv1alpha1.RoleSpec,
	workload client.Object,
) (*coreapplyv1.ServiceApplyConfiguration, error) {
	selectMap := map[string]string{
		workloadsv1alpha1.SetNameLabelKey: rbg.Name,
		workloadsv1alpha1.SetRoleLabelKey: role.Name,
	}
	svcName, err := utils.GetCompatibleHeadlessServiceName(ctx, r.client, rbg, role)
	if err != nil {
		return nil, err
	}
	gvk := workload.GetObjectKind().GroupVersionKind()
	serviceConfig := coreapplyv1.Service(svcName, rbg.Namespace).
		WithSpec(
			coreapplyv1.ServiceSpec().
				WithClusterIP("None").
				WithSelector(selectMap).
				WithPublishNotReadyAddresses(true),
		).
		WithLabels(rbg.GetCommonLabelsFromRole(role)).
		WithAnnotations(rbg.GetCommonAnnotationsFromRole(role)).
		WithOwnerReferences(
			metaapplyv1.OwnerReference().
				WithAPIVersion(gvk.GroupVersion().String()).
				WithKind(gvk.Kind).
				WithName(workload.GetName()).
				WithUID(workload.GetUID()).
				WithBlockOwnerDeletion(true),
		)
	return serviceConfig, nil
}

func (r *ServiceReconciler) getObjectByKind(
	ctx context.Context,
	rbg *workloadsv1alpha1.RoleBasedGroup,
	role *workloadsv1alpha1.RoleSpec,
) (client.Object, error) {
	workloadName := rbg.GetWorkloadName(role)

	switch role.Workload.String() {
	case workloadsv1alpha1.InstanceSetWorkloadType:
		obj := &workloadsv1alpha1.InstanceSet{}
		err := r.client.Get(ctx, types.NamespacedName{Name: workloadName, Namespace: rbg.Namespace}, obj)
		return obj, err
	case workloadsv1alpha1.StatefulSetWorkloadType:
		obj := &appsv1.StatefulSet{}
		err := r.client.Get(ctx, types.NamespacedName{Name: workloadName, Namespace: rbg.Namespace}, obj)
		return obj, err
	default:
		return nil, fmt.Errorf("unsupported workload type: %s", role.Workload.String())
	}
}

func semanticallyEqualService(svc1, svc2 *corev1.Service) (bool, error) {
	if svc1 == nil || svc2 == nil {
		if svc1 != svc2 {
			return false, fmt.Errorf("object is nil")
		} else {
			return true, nil
		}
	}

	if equal, err := objectMetaEqual(svc1.ObjectMeta, svc2.ObjectMeta); !equal {
		return false, fmt.Errorf("objectMeta not equal: %s", err.Error())
	}

	if !reflect.DeepEqual(svc1.Spec.Selector, svc2.Spec.Selector) {
		return false, fmt.Errorf("selector not equal, old: %v, new: %v", svc1.Spec.Selector, svc2.Spec.Selector)
	}

	return true, nil
}
