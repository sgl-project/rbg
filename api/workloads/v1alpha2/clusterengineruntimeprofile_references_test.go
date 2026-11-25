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

package v1alpha2

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"sigs.k8s.io/rbgs/api/workloads/constants"
)

func TestEngineRuntimeLabelConvergenceDriftEpoch(t *testing.T) {
	SetEngineRuntimeLabelConverged(false)
	t.Cleanup(func() { SetEngineRuntimeLabelConverged(false) })

	epoch := BeginEngineRuntimeLabelScan()
	MarkEngineRuntimeLabelDrift()
	CompleteEngineRuntimeLabelScan(epoch)
	assert.False(t, EngineRuntimeLabelConverged(),
		"a drift event during a scan must invalidate that scan")

	epoch = BeginEngineRuntimeLabelScan()
	CompleteEngineRuntimeLabelScan(epoch)
	assert.True(t, EngineRuntimeLabelConverged(),
		"a scan with no intervening drift may converge")
}

func TestEngineRuntimeLabelStartupScanner(t *testing.T) {
	SetEngineRuntimeLabelConverged(false)
	t.Cleanup(func() { SetEngineRuntimeLabelConverged(false) })

	scheme := runtime.NewScheme()
	require.NoError(t, AddToScheme(scheme))
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		rbgReferencing("grp", "ns-a", "ik"),
		rbgsReferencing("set", "ns-a", "ik", 0),
	).Build()
	scanner := &engineRuntimeLabelStartupScanner{
		reader:     reader,
		retryAfter: time.Millisecond,
	}
	assert.False(t, scanner.NeedLeaderElection(),
		"the scanner must run on non-leader webhook replicas")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- scanner.Start(ctx) }()

	require.Eventually(t, func() bool {
		return EngineRuntimeLabelConverged()
	}, time.Second, 5*time.Millisecond, "a clean startup scan should converge this replica")

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("scanner did not stop after the manager context was canceled")
	}
}

func TestEngineRuntimeLabelStartupScannerDoesNotConvergePreLabelObjects(t *testing.T) {
	SetEngineRuntimeLabelConverged(false)
	t.Cleanup(func() { SetEngineRuntimeLabelConverged(false) })

	scheme := runtime.NewScheme()
	require.NoError(t, AddToScheme(scheme))
	unlabeled := rbgReferencing("grp-old", "ns-old", "ik")
	delete(unlabeled.Labels, constants.HasEngineRuntimesLabelKey)
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(unlabeled).Build()

	converged, err := scanEngineRuntimeLabelState(context.Background(), reader)
	require.NoError(t, err)
	assert.False(t, converged,
		"a referencing object written before the label shipped must keep the startup scan unconverged")
}

func TestEngineRuntimeLabelStartupScannerMarksDrift(t *testing.T) {
	SetEngineRuntimeLabelConverged(true)
	t.Cleanup(func() { SetEngineRuntimeLabelConverged(false) })

	scanner := &engineRuntimeLabelStartupScanner{drift: make(chan struct{}, 1)}
	rbg := rbgReferencing("grp-old", "ns-old", "ik")
	delete(rbg.Labels, constants.HasEngineRuntimesLabelKey)

	scanner.eventHandler().OnAdd(rbg, false)
	assert.False(t, EngineRuntimeLabelConverged(),
		"an unlabeled referencing object must un-converge every webhook replica")
	select {
	case <-scanner.drift:
	default:
		t.Fatal("a drift event must wake the startup scanner")
	}
}

func TestEngineRuntimeLabelStartupScannerWaitsForLabelRepair(t *testing.T) {
	SetEngineRuntimeLabelConverged(false)
	t.Cleanup(func() { SetEngineRuntimeLabelConverged(false) })

	unlabeled := rbgReferencing("grp-old", "ns-old", "ik")
	delete(unlabeled.Labels, constants.HasEngineRuntimesLabelKey)
	reader := &engineRuntimeLabelScannerTestReader{rbg: unlabeled}
	scanner := &engineRuntimeLabelStartupScanner{
		reader:     reader,
		retryAfter: time.Millisecond,
		drift:      make(chan struct{}, 1),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- scanner.Start(ctx) }()

	require.Eventually(t, func() bool {
		return reader.listCount() >= 1
	}, time.Second, 5*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 1, reader.listCount(),
		"an unresolved mismatch must not be polled every retry interval")

	fixed := rbgReferencing("grp-old", "ns-old", "ik")
	reader.setRBG(fixed)
	scanner.eventHandler().OnUpdate(unlabeled, fixed)

	require.Eventually(t, func() bool {
		return EngineRuntimeLabelConverged()
	}, time.Second, 5*time.Millisecond, "a label repair event must wake the scanner")

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("scanner did not stop after the manager context was canceled")
	}
}

func TestEngineRuntimeLabelStartupScannerIgnoresStatusOnlyUpdates(t *testing.T) {
	scanner := &engineRuntimeLabelStartupScanner{drift: make(chan struct{}, 1)}
	old := rbgReferencing("grp-old", "ns-old", "ik")
	newObj := old.DeepCopy()
	newObj.Status.Conditions = []metav1.Condition{{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
	}}

	scanner.eventHandler().OnUpdate(old, newObj)
	select {
	case <-scanner.drift:
		t.Fatal("a status-only update must not wake the scanner")
	default:
	}
}

func TestEngineRuntimeLabelStartupScannerWakesOnMismatchDelete(t *testing.T) {
	scanner := &engineRuntimeLabelStartupScanner{drift: make(chan struct{}, 1)}
	unlabeled := rbgReferencing("grp-old", "ns-old", "ik")
	delete(unlabeled.Labels, constants.HasEngineRuntimesLabelKey)

	scanner.eventHandler().OnDelete(unlabeled)
	select {
	case <-scanner.drift:
	default:
		t.Fatal("deleting the last mismatching object must wake the scanner")
	}
}

type engineRuntimeLabelScannerTestReader struct {
	mu    sync.Mutex
	rbg   *RoleBasedGroup
	lists int
}

func (r *engineRuntimeLabelScannerTestReader) Get(
	context.Context, client.ObjectKey, client.Object, ...client.GetOption,
) error {
	return nil
}

func (r *engineRuntimeLabelScannerTestReader) List(
	_ context.Context, list client.ObjectList, _ ...client.ListOption,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lists++
	switch typed := list.(type) {
	case *RoleBasedGroupList:
		typed.Items = []RoleBasedGroup{*r.rbg.DeepCopy()}
	case *RoleBasedGroupSetList:
		typed.Items = nil
	}
	return nil
}

func (r *engineRuntimeLabelScannerTestReader) setRBG(rbg *RoleBasedGroup) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rbg = rbg.DeepCopy()
}

func (r *engineRuntimeLabelScannerTestReader) listCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lists
}
