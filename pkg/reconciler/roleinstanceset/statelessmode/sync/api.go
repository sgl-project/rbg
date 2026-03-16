package sync

import (
	apps "k8s.io/api/apps/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/inplace/instance/inplaceupdate"
	"sigs.k8s.io/rbgs/pkg/inplace/instance/lifecycle"
	"sigs.k8s.io/rbgs/pkg/reconciler/roleinstanceset/statelessmode/utils"
)

// Interface for managing instance scaling and updating.
type Interface interface {
	Scale(
		currentSet *workloadsv1alpha2.RoleInstanceSet,
		updateSet *workloadsv1alpha2.RoleInstanceSet,
		currentRevision string,
		updateRevision string,
		instances []*workloadsv1alpha2.RoleInstance,
	) (bool, error)

	Update(
		set *workloadsv1alpha2.RoleInstanceSet,
		currentRevision *apps.ControllerRevision,
		updateRevision *apps.ControllerRevision,
		revisions []*apps.ControllerRevision,
		instances []*workloadsv1alpha2.RoleInstance,
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
