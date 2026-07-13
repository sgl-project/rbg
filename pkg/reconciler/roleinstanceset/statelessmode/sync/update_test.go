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
	"testing"

	apps "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	inplaceupdate "sigs.k8s.io/rbgs/pkg/inplace/instance/inplaceupdate"
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

	_, err := rc.updateInstance(set, coreCtrl, updateRevision, []*apps.ControllerRevision{oldRevision}, instance)
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
