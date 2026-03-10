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
	"fmt"

	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/util/retry"
	sigsclient "sigs.k8s.io/controller-runtime/pkg/client"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	roleinstancelisters "sigs.k8s.io/rbgs/client-go/listers/workloads/v1alpha2"
)

// StatusUpdaterInterface is an interface used to update the RoleInstanceSetStatus associated with a RoleInstanceSet.
// For any use other than testing, clients should create an instance using NewRealStatefulInstanceSetStatusUpdater.
type StatusUpdaterInterface interface {
	// UpdateInstanceSetStatus sets the set's Status to status. Implementations are required to retry on conflicts,
	// but fail on other errors. If the returned error is nil set's Status has been successfully set to status.
	UpdateInstanceSetStatus(ctx context.Context, set *workloadsv1alpha2.RoleInstanceSet, status *workloadsv1alpha2.RoleInstanceSetStatus) error
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
	// don't wait due to limited number of clients, but backoff after the default number of steps
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		set.Status = *status
		updateErr := ssu.client.Status().Update(ctx, set)
		if updateErr == nil {
			return nil
		}
		if updated, err := ssu.setLister.RoleInstanceSets(set.Namespace).Get(set.Name); err == nil {
			// make a copy so we don't mutate the shared cache
			set = updated.DeepCopy()
		} else {
			utilruntime.HandleError(fmt.Errorf("error getting updated RoleInstanceSet %s/%s from lister: %v", set.Namespace, set.Name, err))
		}

		return updateErr
	})
}

var _ StatusUpdaterInterface = &realStatefulInstanceSetStatusUpdater{}
