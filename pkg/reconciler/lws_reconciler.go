package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"time"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/apimachinery/pkg/util/wait"
	metaapplyv1 "k8s.io/client-go/applyconfigurations/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
	lwsapplyv1 "sigs.k8s.io/lws/client-go/applyconfiguration/leaderworkerset/v1"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/utils"
)

type LeaderWorkerSetReconciler struct {
	scheme *runtime.Scheme
	client client.Client
}

var _ WorkloadReconciler = &LeaderWorkerSetReconciler{}

func NewLeaderWorkerSetReconciler(scheme *runtime.Scheme, client client.Client) *LeaderWorkerSetReconciler {
	return &LeaderWorkerSetReconciler{scheme: scheme, client: client}
}

func (r *LeaderWorkerSetReconciler) Validate(
	ctx context.Context, roleData *RoleData) error {
	logger := log.FromContext(ctx)
	logger.V(1).Info("start to validate role declaration")
	role := roleData.Spec
	if role.Template == nil {
		if role.LeaderWorkerSet == nil {
			return fmt.Errorf("either 'template' or 'leaderWorkerSet' field must be provided")
		}
		if role.LeaderWorkerSet.PatchLeaderTemplate == nil || role.LeaderWorkerSet.PatchWorkerTemplate == nil {
			return fmt.Errorf("both 'patchLeaderTemplate' and 'patchWorkerTemplate' fields must be provided when 'template' field not set")
		}
	}

	return nil
}

func (r *LeaderWorkerSetReconciler) Reconciler(
	ctx context.Context, roleData *RoleData,
) error {
	logger := log.FromContext(ctx)
	logger.V(1).Info("start to reconciling lws workload")

	role := roleData.Spec
	lwsApplyConfig, err := r.constructLWSApplyConfiguration(ctx, roleData)
	if err != nil {
		logger.Error(err, "Failed to construct lws apply configuration")
		return err
	}
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(lwsApplyConfig)
	if err != nil {
		logger.Error(err, "Converting obj apply configuration to json")
		return err
	}
	newLWS := &lwsv1.LeaderWorkerSet{}
	if err = runtime.DefaultUnstructuredConverter.FromUnstructured(obj, newLWS); err != nil {
		logger.Error(err, "convert lwsApplyConfig to lws")
		return err
	}
	oldLWS := &lwsv1.LeaderWorkerSet{}
	err = r.client.Get(ctx, types.NamespacedName{Name: roleData.WorkloadName, Namespace: roleData.OwnerInfo.Namespace}, oldLWS)
	if err != nil && !apierrors.IsNotFound(err) {
		logger.Error(err, "get lws failed")
		return err
	}

	// the err value was used to pass the differences between the old and new objects,
	// not to indicate an actual processing error.
	semanticallyEqual, err := semanticallyEqualLeaderWorkerSet(oldLWS, newLWS, false)
	if err != nil {
		logger.Info(fmt.Sprintf("lws not equal, diff: %s", err.Error()))
	}
	roleHashKey := fmt.Sprintf(workloadsv1alpha1.RoleRevisionLabelKeyFmt, role.Name)
	revisionHashEqual := newLWS.Labels[roleHashKey] == oldLWS.Labels[roleHashKey]
	if !revisionHashEqual {
		logger.Info(fmt.Sprintf("lws hash not equal, old: %s, new: %s",
			oldLWS.Labels[roleHashKey], newLWS.Labels[roleHashKey]))
	}
	if semanticallyEqual && revisionHashEqual {
		logger.Info("lws equal, skip reconcile")
		return nil
	}

	if err = utils.PatchObjectApplyConfiguration(ctx, r.client, lwsApplyConfig, utils.PatchSpec); err != nil {
		logger.Error(err, "Failed to patch lws apply configuration")
		return err
	}
	return nil
}

func (r *LeaderWorkerSetReconciler) ConstructRoleStatus(
	ctx context.Context, roleData *RoleData,
) (workloadsv1alpha1.RoleStatus, bool, error) {
	role := roleData.Spec
	lws := &lwsv1.LeaderWorkerSet{}
	if err := r.client.Get(
		ctx, types.NamespacedName{Name: roleData.WorkloadName, Namespace: roleData.OwnerInfo.Namespace}, lws,
	); err != nil {
		return workloadsv1alpha1.RoleStatus{Name: role.Name}, false, err
	}

	currentReplicas := lws.Status.Replicas
	currentReady := lws.Status.ReadyReplicas
	updatedReplicas := lws.Status.UpdatedReplicas

	// Return current status, let controller decide if update is needed
	status := workloadsv1alpha1.RoleStatus{
		Name:            role.Name,
		Replicas:        currentReplicas,
		ReadyReplicas:   currentReady,
		UpdatedReplicas: updatedReplicas,
	}

	return status, true, nil
}

func (r *LeaderWorkerSetReconciler) CheckWorkloadReady(
	ctx context.Context, roleData *RoleData,
) (bool, error) {
	lws := &lwsv1.LeaderWorkerSet{}
	if err := r.client.Get(
		ctx, types.NamespacedName{Name: roleData.WorkloadName, Namespace: roleData.OwnerInfo.Namespace}, lws,
	); err != nil {
		return false, err
	}
	return lws.Status.ReadyReplicas == lws.Status.Replicas, nil
}

func (r *LeaderWorkerSetReconciler) CleanupOrphanedWorkloads(
	ctx context.Context, roles []*RoleData,
) error {
	logger := log.FromContext(ctx)
	if len(roles) == 0 {
		return nil
	}
	// Get ownerInfo from the first role (all roles have the same owner)
	ownerInfo := roles[0].OwnerInfo

	err := utils.CheckCrdExists(r.client, utils.LwsCrdName)
	if err != nil {
		logger.V(1).Info(
			fmt.Sprintf(
				"LeaderWorkerSetReconciler CleanupOrphanedWorkloads check lws crd failed: %s", err.Error(),
			),
		)
		return nil
	}
	// list lws managed by rbg
	lwsList := &lwsv1.LeaderWorkerSetList{}
	if err := r.client.List(
		ctx, lwsList, client.InNamespace(ownerInfo.Namespace),
		client.MatchingLabels(
			map[string]string{
				workloadsv1alpha1.SetNameLabelKey: ownerInfo.Name,
			},
		),
	); err != nil {
		return err
	}

	for _, lws := range lwsList.Items {
		// Check if controlled by this RBG
		isControlled := false
		for _, owner := range lws.OwnerReferences {
			if owner.UID == ownerInfo.UID {
				isControlled = true
				break
			}
		}
		if !isControlled {
			continue
		}

		found := false
		for _, roleData := range roles {
			if roleData.Spec.Workload.Kind == "LeaderWorkerSet" && roleData.WorkloadName == lws.Name {
				found = true
				break
			}
		}
		if !found {
			logger.Info("delete lws", "lws", lws.Name)
			if err := r.client.Delete(ctx, &lws); err != nil {
				return fmt.Errorf("delete lws %s error: %s", lws.Name, err.Error())
			}
		}
	}
	return nil
}

func (r *LeaderWorkerSetReconciler) constructLWSApplyConfiguration(
	ctx context.Context,
	roleData *RoleData,
) (*lwsapplyv1.LeaderWorkerSetApplyConfiguration, error) {
	logger := log.FromContext(ctx)
	role := roleData.Spec
	ownerInfo := roleData.OwnerInfo
	rollingUpdateStrategy := roleData.RollingUpdateStrategy
	revisionKey := roleData.ExpectedRevisionHash

	leaderWorkerSet := role.LeaderWorkerSet
	if leaderWorkerSet == nil {
		leaderWorkerSet = &workloadsv1alpha1.LeaderWorkerTemplate{
			Size: ptr.To(int32(1)),
		}
	}

	// leaderTemplate
	podReconciler := NewPodReconciler(r.scheme, r.client)
	leaderTemp, err := patchPodTemplate(role.Template, leaderWorkerSet.PatchLeaderTemplate)
	if err != nil {
		logger.Error(err, "patch leader podTemplate failed", "rbg", fmt.Sprintf("%s/%s", ownerInfo.Namespace, ownerInfo.Name))
		return nil, err
	}
	matchLabels := workloadsv1alpha1.GetCommonLabelsFromRole(ownerInfo.Name, ownerInfo.Namespace, role)
	leaderTemplateApplyCfg, err := podReconciler.ConstructPodTemplateSpecApplyConfiguration(
		ctx, roleData, maps.Clone(matchLabels), *leaderTemp,
	)
	if err != nil {
		logger.Error(err, "patch Construct PodTemplateSpecApplyConfiguration failed", "rbg", fmt.Sprintf("%s/%s", ownerInfo.Namespace, ownerInfo.Name))
		return nil, err
	}

	// workerTemplate
	workerTemp, err := patchPodTemplate(role.Template, leaderWorkerSet.PatchWorkerTemplate)
	if err != nil {
		logger.Error(err, "patch worker podTemplate failed", "rbg", fmt.Sprintf("%s/%s", ownerInfo.Namespace, ownerInfo.Name))
		return nil, err
	}
	workerPodReconciler := NewPodReconciler(r.scheme, r.client)
	// workerTemplate do not need to inject sidecar
	workerPodReconciler.SetInjectors([]string{"config", "common_env"})
	workerTemplateApplyCfg, err := workerPodReconciler.ConstructPodTemplateSpecApplyConfiguration(
		ctx, roleData, maps.Clone(matchLabels), *workerTemp,
	)
	if err != nil {
		logger.Error(err, "patch Construct PodTemplateSpecApplyConfiguration failed", "rbg", fmt.Sprintf("%s/%s", ownerInfo.Namespace, ownerInfo.Name))
		return nil, err
	}
	// TODO support SubGroupPolicy
	if role.Replicas == nil {
		role.Replicas = ptr.To(int32(1))
	}

	// RestartPolicy
	var restartPolicy lwsv1.RestartPolicyType
	if role.RestartPolicy == "None" {
		restartPolicy = lwsv1.NoneRestartPolicy
	} else {
		// if role has RecreateRBGOnPodRestart or RecreateRoleInstanceOnPodRestart policy,
		// set RecreateGroupOnPodRestart for lws
		// it's safe to do so since
		// 1. RecreateGroupOnPodRestart is the default restart policy for lws
		// 2. RecreateRBGOnPodRestart will delete lws if pod recreated or containers restarted
		restartPolicy = lwsv1.RecreateGroupOnPodRestart
	}

	lwsSpecConfig := lwsapplyv1.LeaderWorkerSetSpec().WithReplicas(*role.Replicas).
		WithLeaderWorkerTemplate(
			lwsapplyv1.LeaderWorkerTemplate().
				WithLeaderTemplate(leaderTemplateApplyCfg).
				WithWorkerTemplate(workerTemplateApplyCfg).
				WithSize(*leaderWorkerSet.Size).
				WithRestartPolicy(restartPolicy),
		)

	// RollingUpdate
	if role.RolloutStrategy != nil && role.RolloutStrategy.RollingUpdate != nil {
		rollingUpdateConfiguration := lwsapplyv1.RollingUpdateConfiguration()
		if role.RolloutStrategy.RollingUpdate.MaxSurge != nil {
			rollingUpdateConfiguration = rollingUpdateConfiguration.WithMaxSurge(*role.RolloutStrategy.RollingUpdate.MaxSurge)
		}
		if role.RolloutStrategy.RollingUpdate.MaxUnavailable != nil {
			rollingUpdateConfiguration = rollingUpdateConfiguration.WithMaxUnavailable(*role.RolloutStrategy.RollingUpdate.MaxUnavailable)
		}

		if rollingUpdateStrategy != nil && rollingUpdateStrategy.MaxUnavailable != nil {
			rollingUpdateConfiguration =
				rollingUpdateConfiguration.WithMaxUnavailable(*rollingUpdateStrategy.MaxUnavailable)
		}

		var partitionIntOrStr *intstr.IntOrString
		if rollingUpdateStrategy != nil && rollingUpdateStrategy.Partition != nil {
			partitionIntOrStr = rollingUpdateStrategy.Partition
		} else if role.RolloutStrategy.RollingUpdate.Partition != nil {
			partitionIntOrStr = role.RolloutStrategy.RollingUpdate.Partition
		}

		if partitionIntOrStr != nil {
			partition, err := intstr.GetScaledValueFromIntOrPercent(partitionIntOrStr, int(*role.Replicas), true)
			if err != nil {
				return nil, err
			}
			rollingUpdateConfiguration = rollingUpdateConfiguration.WithPartition(int32(partition))
		}

		lwsSpecConfig = lwsSpecConfig.WithRolloutStrategy(
			lwsapplyv1.RolloutStrategy().WithRollingUpdateConfiguration(rollingUpdateConfiguration),
		)
	}

	lwsLabel := maps.Clone(matchLabels)
	lwsLabel[fmt.Sprintf(workloadsv1alpha1.RoleRevisionLabelKeyFmt, role.Name)] = revisionKey

	// construct lws apply configuration
	lwsConfig := lwsapplyv1.LeaderWorkerSet(roleData.WorkloadName, ownerInfo.Namespace).
		WithSpec(lwsSpecConfig).
		WithAnnotations(labels.Merge(maps.Clone(role.Annotations), workloadsv1alpha1.GetCommonAnnotationsFromRole(ownerInfo.Name, role))).
		WithLabels(labels.Merge(maps.Clone(role.Labels), lwsLabel)).
		WithOwnerReferences(
			metaapplyv1.OwnerReference().
				WithAPIVersion(workloadsv1alpha1.GroupVersion.String()).
				WithKind("RoleBasedGroup").
				WithName(ownerInfo.Name).
				WithUID(ownerInfo.UID).
				WithBlockOwnerDeletion(true).
				WithController(true),
		)
	return lwsConfig, nil

}

func (r *LeaderWorkerSetReconciler) RecreateWorkload(
	ctx context.Context, roleData *RoleData,
) error {
	logger := log.FromContext(ctx)
	if roleData == nil || roleData.Spec == nil {
		return nil
	}

	lwsName := roleData.WorkloadName
	var lws lwsv1.LeaderWorkerSet
	err := r.client.Get(ctx, types.NamespacedName{Name: lwsName, Namespace: roleData.OwnerInfo.Namespace}, &lws)
	// if lws is not found, skip delete lws
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if lws.UID == "" {
		return nil
	}

	logger.Info(fmt.Sprintf("Recreate lws workload, delete lws %s", lws.Name))
	if err := r.client.Delete(ctx, &lws); err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	// wait new lws create
	var retErr error
	err = wait.PollUntilContextTimeout(
		ctx, 5*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
			var newLws lwsv1.LeaderWorkerSet
			retErr = r.client.Get(ctx, types.NamespacedName{Name: lwsName, Namespace: roleData.OwnerInfo.Namespace}, &newLws)
			if retErr != nil {
				if apierrors.IsNotFound(retErr) {
					return false, nil
				}
				return false, retErr
			}
			return true, nil
		},
	)

	if err != nil {
		logger.Error(retErr, "wait new lws creating error")
		return retErr
	}

	return nil
}

func semanticallyEqualLeaderWorkerSet(oldLws, newLws *lwsv1.LeaderWorkerSet, checkStatus bool) (bool, error) {
	if oldLws == nil || oldLws.UID == "" {
		return false, errors.New("old lws not exist")
	}
	if newLws == nil {
		return false, fmt.Errorf("new lws is nil")
	}

	if equal, err := objectMetaEqual(oldLws.ObjectMeta, newLws.ObjectMeta); !equal {
		retErr := fmt.Errorf("objectMeta not equal")
		if err != nil {
			retErr = fmt.Errorf("objectMeta not equal: %s", err.Error())
		}
		return false, retErr

	}

	if equal, err := lwsSpecEqual(oldLws.Spec, newLws.Spec); !equal {
		retErr := fmt.Errorf("spec not equal")
		if err != nil {
			retErr = fmt.Errorf("spec not equal: %s", err.Error())
		}
		return false, retErr
	}

	if checkStatus {
		if equal, err := lwsStatusEqual(oldLws.Status, newLws.Status); !equal {
			retErr := fmt.Errorf("lws status not equal")
			if err != nil {
				retErr = fmt.Errorf("lws status not equal: %s", err.Error())
			}
			return false, retErr
		}
	}

	return true, nil
}

func lwsSpecEqual(lws1, lws2 lwsv1.LeaderWorkerSetSpec) (bool, error) {
	if equal, err := leaderWorkerTemplateEqual(lws1.LeaderWorkerTemplate, lws2.LeaderWorkerTemplate); !equal {
		retErr := fmt.Errorf("leaderWorkerTemplate not equal")
		if err != nil {
			retErr = fmt.Errorf("leaderWorkerTemplate not equal: %s", err.Error())
		}
		return false, retErr
	}
	if lws1.Replicas == nil {
		lws1.Replicas = ptr.To(int32(1))
	}
	if lws2.Replicas == nil {
		lws2.Replicas = ptr.To(int32(1))
	}
	if *lws1.Replicas != *lws2.Replicas {
		return false, fmt.Errorf("LeaderWorkerSetSpec replicas not equal")
	}
	return true, nil
}

func lwsStatusEqual(oldStatus, newStatus lwsv1.LeaderWorkerSetStatus) (bool, error) {
	if !reflect.DeepEqual(oldStatus, newStatus) {
		return false, fmt.Errorf("status not equal")
	}
	return true, nil
}

func leaderWorkerTemplateEqual(oldLwt, newLwt lwsv1.LeaderWorkerTemplate) (bool, error) {
	if equal, err := podTemplateSpecEqual(*oldLwt.LeaderTemplate, *newLwt.LeaderTemplate); !equal {
		retErr := fmt.Errorf("leaderTemplate not equal")
		if err != nil {
			retErr = fmt.Errorf("leaderTemplate not equal: %s", err.Error())
		}
		return false, retErr
	}

	if equal, err := podTemplateSpecEqual(oldLwt.WorkerTemplate, newLwt.WorkerTemplate); !equal {
		retErr := fmt.Errorf("workerTemplate not equal")
		if err != nil {
			retErr = fmt.Errorf("workerTemplate not equal: %s", err.Error())
		}
		return false, retErr
	}

	if *oldLwt.Size != *newLwt.Size {
		return false, fmt.Errorf("lws size not equal")
	}

	if !reflect.DeepEqual(oldLwt.RestartPolicy, newLwt.RestartPolicy) {
		return false, fmt.Errorf(
			"restart policy not equal, oldLwt.RestartPolicy: %s, newLwt.RestartPolicy: %s",
			oldLwt.RestartPolicy, newLwt.RestartPolicy,
		)
	}
	return true, nil
}

func patchPodTemplate(template *corev1.PodTemplateSpec, patch *runtime.RawExtension) (*corev1.PodTemplateSpec, error) {
	if template == nil {
		template = &corev1.PodTemplateSpec{}
	}
	if patch == nil || patch.Raw == nil {
		return template, nil
	}
	tempBytes, _ := json.Marshal(template)
	modified, err := strategicpatch.StrategicMergePatch(tempBytes, patch.Raw, &corev1.PodTemplateSpec{})
	if err != nil {
		return template, err
	}
	newTemp := &corev1.PodTemplateSpec{}
	if err = json.Unmarshal(modified, newTemp); err != nil {
		return template, err
	}
	return newTemp, nil
}

func keyOfRbg(rbg *workloadsv1alpha1.RoleBasedGroup) string {
	return fmt.Sprintf("%s/%s", rbg.Namespace, rbg.Name)
}
