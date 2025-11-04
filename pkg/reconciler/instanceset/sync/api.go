package sync

import (
	apps "k8s.io/api/apps/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/inplace/instance/inplaceupdate"
	"sigs.k8s.io/rbgs/pkg/inplace/instance/lifecycle"
	"sigs.k8s.io/rbgs/pkg/reconciler/instanceset/utils"
)

// Interface for managing instance scaling and updating.
type Interface interface {
	Scale(
		currentSet *appsv1alpha1.InstanceSet,
		updateSet *appsv1alpha1.InstanceSet,
		currentRevision string,
		updateRevision string,
		instances []*appsv1alpha1.Instance,
	) (bool, error)

	Update(
		set *appsv1alpha1.InstanceSet,
		currentRevision *apps.ControllerRevision,
		updateRevision *apps.ControllerRevision,
		revisions []*apps.ControllerRevision,
		instances []*appsv1alpha1.Instance,
	) error
}

type realControl struct {
	client.Client
	lifecycleControl lifecycle.Interface
	inplaceControl   inplaceupdate.Interface
	recorder         record.EventRecorder
}

func New(c client.Client, recorder record.EventRecorder) Interface {
	return &realControl{
		Client:           c,
		inplaceControl:   inplaceupdate.New(c, utils.RevisionAdapterImpl),
		lifecycleControl: lifecycle.New(c),
		recorder:         recorder,
	}
}
