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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
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

	scanner.markDrift(rbg)
	assert.False(t, EngineRuntimeLabelConverged(),
		"an unlabeled referencing object must un-converge every webhook replica")
	select {
	case <-scanner.drift:
	default:
		t.Fatal("a drift event must wake the startup scanner")
	}
}
