package sync

import (
	"context"
	"time"

	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/inplaceupdate"
	"sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/lifecycle"
)

type Interface interface {
	Scale(ctx context.Context, updateInstance *workloadsv1alpha2.RoleInstance, currentRevision, updateRevision *apps.ControllerRevision, revisions []*apps.ControllerRevision, pods []*v1.Pod) (bool, error)
	Update(ctx context.Context, instance *workloadsv1alpha2.RoleInstance, currentRevision, updateRevision *apps.ControllerRevision, revisions []*apps.ControllerRevision, pods []*v1.Pod) (time.Duration, error)
}

type realControl struct {
	client.Client
	inplaceControl   inplaceupdate.Interface
	recorder         record.EventRecorder
	lifecycleControl lifecycle.Interface
}

func New(c client.Client, recorder record.EventRecorder) Interface {
	return &realControl{
		Client:           c,
		inplaceControl:   inplaceupdate.New(c),
		lifecycleControl: lifecycle.New(c),
		recorder:         recorder,
	}
}
