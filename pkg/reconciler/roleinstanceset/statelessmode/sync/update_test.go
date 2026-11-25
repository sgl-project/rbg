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

package sync

import (
	"context"
	"strings"
	"testing"

	apps "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	inplaceupdate "sigs.k8s.io/rbgs/pkg/inplace/instance/inplaceupdate"
	roleinstancelifecycle "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/lifecycle"
)

// fakeStatelessInplaceControl is a recording fake for the stateless inplace interface.
type fakeStatelessInplaceControl struct {
	canUpdateCalls int
	updateCalls    int
}

func (f *fakeStatelessInplaceControl) CanUpdateInPlace(_, _ *apps.ControllerRevision, _ *inplaceupdate.UpdateOptions) bool {
	f.canUpdateCalls++
	return false
}

func (f *fakeStatelessInplaceControl) Update(_ *workloadsv1alpha2.RoleInstance, _, _ *apps.ControllerRevision, _ *inplaceupdate.UpdateOptions) inplaceupdate.UpdateResult {
	f.updateCalls++
	return inplaceupdate.UpdateResult{}
}

func (f *fakeStatelessInplaceControl) Refresh(_ *workloadsv1alpha2.RoleInstance, _ *inplaceupdate.UpdateOptions) inplaceupdate.RefreshResult {
	return inplaceupdate.RefreshResult{}
}

func TestUpdateInstanceRecreatePodSkipsInPlace(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha2.AddToScheme(scheme)

	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "inst-0",
			Namespace:       "default",
			UID:             types.UID("uid-0"),
			ResourceVersion: "1",
			Labels: map[string]string{
				apps.ControllerRevisionHashLabelKey: "old-rev",
			},
		},
	}

	set := &workloadsv1alpha2.RoleInstanceSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test-set", Namespace: "default"},
		Spec: workloadsv1alpha2.RoleInstanceSetSpec{
			UpdateStrategy: workloadsv1alpha2.RoleInstanceSetUpdateStrategy{
				Type: workloadsv1alpha2.RecreatePodUpdateStrategyType,
			},
		},
	}

	oldRevision := &apps.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: "old-rev"}}
	updateRevision := &apps.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: "new-rev"}}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance).Build()
	inplaceControl := &fakeStatelessInplaceControl{}

	rc := &realControl{
		Client:         fakeClient,
		inplaceControl: inplaceControl,
		recorder:       record.NewFakeRecorder(10),
	}

	// coreControl with a minimal RoleInstanceSet (no UpdateStrategy.InPlaceUpdateStrategy)
	coreCtrl := &fakeStatelessCoreControl{}

	_, err := rc.updateInstance(context.Background(), set, coreCtrl, updateRevision, []*apps.ControllerRevision{oldRevision}, instance)
	if err != nil {
		t.Fatalf("updateInstance() error = %v", err)
	}
	if inplaceControl.canUpdateCalls != 0 || inplaceControl.updateCalls != 0 {
		t.Fatalf("expected in-place control not called, got canUpdate=%d update=%d",
			inplaceControl.canUpdateCalls, inplaceControl.updateCalls)
	}
	// Verify specified-delete label was applied
	updated := &workloadsv1alpha2.RoleInstance{}
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: instance.Name, Namespace: instance.Namespace}, updated); err != nil {
		t.Fatalf("failed to get updated instance: %v", err)
	}
	if _, ok := updated.Labels[constants.RoleInstanceDeleteLabelKey]; !ok {
		t.Fatal("expected specified-delete label to be set on instance")
	}
}

// A Stateless update with the KEP-527 preUpdate hook configured must install
// the signal labels and hold the mutation until the executor removes them.
func TestUpdateInstanceWaitsForPreUpdateLifecycleHook(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha2.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "inst-0",
			Namespace:       "default",
			UID:             types.UID("uid-0"),
			ResourceVersion: "1",
			Labels: map[string]string{
				apps.ControllerRevisionHashLabelKey: "old-rev",
			},
		},
	}
	set := &workloadsv1alpha2.RoleInstanceSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test-set", Namespace: "default"},
		Spec: workloadsv1alpha2.RoleInstanceSetSpec{
			UpdateStrategy: workloadsv1alpha2.RoleInstanceSetUpdateStrategy{
				Type: workloadsv1alpha2.RecreatePodUpdateStrategyType,
			},
			RoleInstanceTemplate: workloadsv1alpha2.RoleInstanceTemplate{
				RoleInstanceSpec: workloadsv1alpha2.RoleInstanceSpec{
					Lifecycle: &workloadsv1alpha2.RoleInstanceLifecycle{
						PreUpdate: &workloadsv1alpha2.RoleInstanceLifecycleHook{
							SignalLabels: map[string]string{"consumer.example/update": "blocked"},
						},
					},
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance).Build()
	inplaceControl := &fakeStatelessInplaceControl{}
	rc := &realControl{
		Client:         fakeClient,
		inplaceControl: inplaceControl,
		hookControl:    roleinstancelifecycle.NewHookControl(fakeClient),
		recorder:       record.NewFakeRecorder(10),
	}

	_, err := rc.updateInstance(context.Background(), set, &fakeStatelessCoreControl{},
		&apps.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: "new-rev"}},
		[]*apps.ControllerRevision{{ObjectMeta: metav1.ObjectMeta{Name: "old-rev"}}}, instance)
	if err != nil {
		t.Fatalf("updateInstance() error = %v", err)
	}
	if inplaceControl.updateCalls != 0 {
		t.Fatalf("in-place update calls = %d, want 0 while preUpdate is pending", inplaceControl.updateCalls)
	}

	stored := &workloadsv1alpha2.RoleInstance{}
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: instance.Name, Namespace: instance.Namespace}, stored); err != nil {
		t.Fatalf("failed to get instance: %v", err)
	}
	if _, ok := stored.Labels[constants.RoleInstanceDeleteLabelKey]; ok {
		t.Fatal("specified-delete label must not be set while preUpdate is pending")
	}
	if got := stored.Labels["consumer.example/update"]; got != "blocked" {
		t.Fatalf("preUpdate signal label = %q, want blocked", got)
	}
	if got := stored.Labels[constants.RoleInstanceLifecycleHookStateKey]; got != string(constants.RoleInstanceHookStatePreUpdate) {
		t.Fatalf("hook state = %q, want %q", got, constants.RoleInstanceHookStatePreUpdate)
	}
}

// Updating is durable authorization: after preUpdate was released, a failed
// mutation retries without re-entering the hook or reinstalling the signals.
func TestUpdateInstanceResumesUpdatingWithoutReplay(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha2.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "inst-0",
			Namespace: "default",
			UID:       types.UID("uid-0"),
			Labels: map[string]string{
				apps.ControllerRevisionHashLabelKey:         "old-rev",
				constants.RoleInstanceLifecycleHookStateKey: string(constants.RoleInstanceHookStateUpdating),
			},
		},
	}
	set := &workloadsv1alpha2.RoleInstanceSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test-set", Namespace: "default"},
		Spec: workloadsv1alpha2.RoleInstanceSetSpec{
			UpdateStrategy: workloadsv1alpha2.RoleInstanceSetUpdateStrategy{
				Type: workloadsv1alpha2.RecreatePodUpdateStrategyType,
			},
			RoleInstanceTemplate: workloadsv1alpha2.RoleInstanceTemplate{
				RoleInstanceSpec: workloadsv1alpha2.RoleInstanceSpec{
					Lifecycle: &workloadsv1alpha2.RoleInstanceLifecycle{
						PreUpdate: &workloadsv1alpha2.RoleInstanceLifecycleHook{
							SignalLabels: map[string]string{"consumer.example/update": "blocked"},
						},
					},
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance).Build()
	rc := &realControl{
		Client:         fakeClient,
		inplaceControl: &fakeStatelessInplaceControl{},
		hookControl:    roleinstancelifecycle.NewHookControl(fakeClient),
		recorder:       record.NewFakeRecorder(10),
	}

	_, err := rc.updateInstance(context.Background(), set, &fakeStatelessCoreControl{},
		&apps.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: "new-rev"}},
		[]*apps.ControllerRevision{{ObjectMeta: metav1.ObjectMeta{Name: "old-rev"}}}, instance)
	if err != nil {
		t.Fatalf("updateInstance() error = %v", err)
	}

	stored := &workloadsv1alpha2.RoleInstance{}
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: instance.Name, Namespace: instance.Namespace}, stored); err != nil {
		t.Fatalf("failed to get instance: %v", err)
	}
	if _, ok := stored.Labels["consumer.example/update"]; ok {
		t.Fatal("a retry in Updating must not reinstall preUpdate signal labels")
	}
	if got := stored.Labels[constants.RoleInstanceLifecycleHookStateKey]; got != string(constants.RoleInstanceHookStateUpdating) {
		t.Fatalf("hook state = %q, want Updating", got)
	}
}

// Entering preUpdate emits a normal event on the set so a blocked rollout is
// observable instead of appearing stuck.
func TestUpdateInstanceEmitsPreUpdateEvent(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha2.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "inst-0",
			Namespace: "default",
			UID:       types.UID("uid-0"),
			Labels: map[string]string{
				apps.ControllerRevisionHashLabelKey: "old-rev",
			},
		},
	}
	set := &workloadsv1alpha2.RoleInstanceSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test-set", Namespace: "default"},
		Spec: workloadsv1alpha2.RoleInstanceSetSpec{
			UpdateStrategy: workloadsv1alpha2.RoleInstanceSetUpdateStrategy{
				Type: workloadsv1alpha2.RecreatePodUpdateStrategyType,
			},
			RoleInstanceTemplate: workloadsv1alpha2.RoleInstanceTemplate{
				RoleInstanceSpec: workloadsv1alpha2.RoleInstanceSpec{
					Lifecycle: &workloadsv1alpha2.RoleInstanceLifecycle{
						PreUpdate: &workloadsv1alpha2.RoleInstanceLifecycleHook{
							SignalLabels: map[string]string{"consumer.example/update": "blocked"},
						},
					},
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance).Build()
	recorder := record.NewFakeRecorder(10)
	rc := &realControl{
		Client:         fakeClient,
		inplaceControl: &fakeStatelessInplaceControl{},
		hookControl:    roleinstancelifecycle.NewHookControl(fakeClient),
		recorder:       recorder,
	}

	_, err := rc.updateInstance(context.Background(), set, &fakeStatelessCoreControl{},
		&apps.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: "new-rev"}},
		[]*apps.ControllerRevision{{ObjectMeta: metav1.ObjectMeta{Name: "old-rev"}}}, instance)
	if err != nil {
		t.Fatalf("updateInstance() error = %v", err)
	}
	select {
	case event := <-recorder.Events:
		if !strings.Contains(event, "LifecycleHookEntered") {
			t.Fatalf("event = %q, want LifecycleHookEntered", event)
		}
	default:
		t.Fatal("entering preUpdate must emit a LifecycleHookEntered event")
	}
}

// fakeStatelessCoreControl satisfies core.Control just enough for updateInstance.
type fakeStatelessCoreControl struct{}

func (f *fakeStatelessCoreControl) IsInitializing() bool { return false }

func (f *fakeStatelessCoreControl) SetRevisionTemplate(_ map[string]interface{}, _ map[string]interface{}) {
}

func (f *fakeStatelessCoreControl) ApplyRevisionPatch(_ []byte) (*workloadsv1alpha2.RoleInstanceSet, error) {
	return nil, nil
}

func (f *fakeStatelessCoreControl) Selector() labels.Selector { return labels.Everything() }

func (f *fakeStatelessCoreControl) IsReadyToScale() bool { return true }

func (f *fakeStatelessCoreControl) NewVersionedInstances(_, _ *workloadsv1alpha2.RoleInstanceSet, _, _ string, _, _ int, _ []string) ([]*workloadsv1alpha2.RoleInstance, error) {
	return nil, nil
}

func (f *fakeStatelessCoreControl) IsInstanceUpdatePaused(_ *workloadsv1alpha2.RoleInstance) bool {
	return false
}

func (f *fakeStatelessCoreControl) IsInstanceUpdateReady(_ *workloadsv1alpha2.RoleInstance, _ int32) bool {
	return true
}

func (f *fakeStatelessCoreControl) GetInstancesSortFunc(_ []*workloadsv1alpha2.RoleInstance, _ []int) func(i, j int) bool {
	return func(i, j int) bool { return false }
}

func (f *fakeStatelessCoreControl) GetUpdateOptions() *inplaceupdate.UpdateOptions {
	return &inplaceupdate.UpdateOptions{}
}

func (f *fakeStatelessCoreControl) ExtraStatusCalculation(_ *workloadsv1alpha2.RoleInstanceSetStatus, _ []*workloadsv1alpha2.RoleInstance) error {
	return nil
}

func (f *fakeStatelessCoreControl) ValidateInstanceSetUpdate(_, _ *workloadsv1alpha2.RoleInstanceSet) error {
	return nil
}
