package sync

import (
	"context"
	"time"

	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/reconciler/instance/inplaceupdate"
	"sigs.k8s.io/rbgs/pkg/reconciler/instance/lifecycle"
)

type Interface interface {
	Scale(ctx context.Context, updateInstance *v1alpha1.Instance, currentRevision, updateRevision *apps.ControllerRevision, revisions []*apps.ControllerRevision, pods []*v1.Pod) (bool, error)
	Update(ctx context.Context, instance *v1alpha1.Instance, currentRevision, updateRevision *apps.ControllerRevision, revisions []*apps.ControllerRevision, pods []*v1.Pod) (time.Duration, error)
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
