/*
Copyright 2026 The RBG Authors.
Copyright 2019 The Kruise Authors.
Copyright 2017 The Kubernetes Authors.

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

package statefulmode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/util/retry"
	sigsclient "sigs.k8s.io/controller-runtime/pkg/client"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	roleinstancelisters "sigs.k8s.io/rbgs/client-go/listers/workloads/v1alpha2"
	setstatus "sigs.k8s.io/rbgs/pkg/reconciler/roleinstanceset"
)

var errRolloutSpecChanged = errors.New("RoleInstanceSet changed while persisting rollout replacement state")

// StatusUpdaterInterface is an interface used to update the RoleInstanceSetStatus associated with a RoleInstanceSet.
// For any use other than testing, clients should create an instance using NewRealStatefulInstanceSetStatusUpdater.
type StatusUpdaterInterface interface {
	// UpdateInstanceSetStatus sets the set's Status to status. Conflicts are retried
	// only while the set UID, generation, and replacement state are unchanged.
	// Otherwise the stale status is discarded for the next reconciliation.
	// Other errors fail without changing the reconcile snapshot.
	UpdateInstanceSetStatus(ctx context.Context, set *workloadsv1alpha2.RoleInstanceSet, status *workloadsv1alpha2.RoleInstanceSetStatus) error
	// PersistRolloutReplacementState writes replacement intent before destructive
	// work. A stale resource version must fail rather than retry against a new spec.
	PersistRolloutReplacementState(ctx context.Context, set *workloadsv1alpha2.RoleInstanceSet, state *workloadsv1alpha2.RolloutReplacementState) error
}

// NewRealStatefulInstanceSetStatusUpdater returns a StatusUpdaterInterface that updates the Status of a RoleInstanceSet,
// using the supplied client and setLister.
func NewRealStatefulInstanceSetStatusUpdater(
	client sigsclient.Client,
	setLister roleinstancelisters.RoleInstanceSetLister) StatusUpdaterInterface {
	return &realStatefulInstanceSetStatusUpdater{client, setLister}
}

type realStatefulInstanceSetStatusUpdater struct {
	client    sigsclient.Client
	setLister roleinstancelisters.RoleInstanceSetLister
}

func (ssu *realStatefulInstanceSetStatusUpdater) UpdateInstanceSetStatus(
	ctx context.Context,
	set *workloadsv1alpha2.RoleInstanceSet,
	status *workloadsv1alpha2.RoleInstanceSetStatus) error {
	// Skip status update if the object is being deleted to avoid StorageError
	// caused by precondition failures when the object's UID becomes empty during deletion.
	if set.DeletionTimestamp != nil {
		return nil
	}
	expectedUID, expectedGeneration := set.UID, set.Generation
	expectedReplacementState := set.Status.RolloutReplacementState.DeepCopy()
	working := set.DeepCopy()
	// don't wait due to limited number of clients, but backoff after the default number of steps
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		merged := status.DeepCopy()
		merged.Conditions = setstatus.MergeConditions(working.Status.Conditions, merged.Conditions)
		if equality.Semantic.DeepEqual(working.Status, *merged) {
			set.Status = *working.Status.DeepCopy()
			set.ResourceVersion = working.ResourceVersion
			return nil
		}
		candidate := working.DeepCopy()
		candidate.Status = *merged
		updateErr := ssu.client.Status().Update(ctx, candidate)
		if updateErr == nil {
			if !equality.Semantic.DeepEqual(expectedReplacementState, candidate.Status.RolloutReplacementState) {
				rolloutStatusExpectations.Expect(candidate)
			}
			set.Status = *candidate.Status.DeepCopy()
			set.ResourceVersion = candidate.ResourceVersion
			return nil
		}
		if !apierrors.IsConflict(updateErr) {
			return updateErr
		}
		if updated, err := ssu.setLister.RoleInstanceSets(set.Namespace).Get(set.Name); err == nil {
			if updated.UID != expectedUID || updated.Generation != expectedGeneration ||
				!equality.Semantic.DeepEqual(updated.Status.RolloutReplacementState, expectedReplacementState) {
				return nil
			}
			// make a copy so we don't mutate the shared cache
			working = updated.DeepCopy()
			// Re-check deletion timestamp after refreshing from lister
			if working.DeletionTimestamp != nil {
				return nil
			}
		} else {
			utilruntime.HandleError(fmt.Errorf("error getting updated RoleInstanceSet %s/%s from lister: %v", set.Namespace, set.Name, err))
		}

		return updateErr
	})
}

func (ssu *realStatefulInstanceSetStatusUpdater) PersistRolloutReplacementState(
	ctx context.Context,
	set *workloadsv1alpha2.RoleInstanceSet,
	state *workloadsv1alpha2.RolloutReplacementState,
) error {
	if set.DeletionTimestamp != nil {
		return fmt.Errorf("RoleInstanceSet %s/%s is deleting", set.Namespace, set.Name)
	}
	patch, err := json.Marshal(map[string]interface{}{
		"metadata": map[string]interface{}{"uid": set.UID, "resourceVersion": set.ResourceVersion},
		"status":   map[string]interface{}{"rolloutReplacementState": state},
	})
	if err != nil {
		return err
	}
	// Decode into a fresh object so an outdated CRD pruning the field cannot
	// leave the requested state in memory and masquerade as a successful write.
	updated := &workloadsv1alpha2.RoleInstanceSet{ObjectMeta: metav1.ObjectMeta{Name: set.Name, Namespace: set.Namespace}}
	if err := ssu.client.Status().Patch(ctx, updated, sigsclient.RawPatch(types.MergePatchType, patch)); err != nil {
		if apierrors.IsConflict(err) {
			return fmt.Errorf("%w: %w", errRolloutSpecChanged, err)
		}
		return err
	}
	if updated.UID != set.UID || updated.Generation != set.Generation || updated.DeletionTimestamp != nil {
		return fmt.Errorf("%w: %s/%s", errRolloutSpecChanged, set.Namespace, set.Name)
	}
	if !equality.Semantic.DeepEqual(updated.Status.RolloutReplacementState, state) {
		return fmt.Errorf("RoleInstanceSet %s/%s did not persist rolloutReplacementState; upgrade the RoleInstanceSet CRD to match the controller", set.Namespace, set.Name)
	}
	rolloutStatusExpectations.Expect(updated)
	set.ResourceVersion = updated.ResourceVersion
	set.Status.RolloutReplacementState = updated.Status.RolloutReplacementState.DeepCopy()
	return nil
}

var _ StatusUpdaterInterface = &realStatefulInstanceSetStatusUpdater{}
