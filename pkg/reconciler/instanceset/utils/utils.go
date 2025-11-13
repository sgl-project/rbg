/*
Copyright 2019 The Kruise Authors.

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

package utils

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/openkruise/kruise/pkg/util/expectations"
	"github.com/openkruise/kruise/pkg/util/requeueduration"
	apps "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/integer"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	inplaceutil "sigs.k8s.io/rbgs/pkg/inplace/instance"
)

var (
	// ControllerKind is GroupVersionKind for GroupSet.
	ControllerKind      = v1alpha1.SchemeGroupVersion.WithKind("InstanceSet")
	RevisionAdapterImpl = &revisionAdapterImpl{}
	EqualToRevisionHash = RevisionAdapterImpl.EqualToRevisionHash
	WriteRevisionHash   = RevisionAdapterImpl.WriteRevisionHash

	ScaleExpectations           = expectations.NewScaleExpectations()
	ResourceVersionExpectations = expectations.NewResourceVersionExpectation()

	// DurationStore is a short cut for any sub-functions to notify the reconcile how long to wait to requeue
	DurationStore = requeueduration.DurationStore{}
)

type revisionAdapterImpl struct {
}

func (r *revisionAdapterImpl) EqualToRevisionHash(_ string, obj metav1.Object, hash string) bool {
	objHash := obj.GetLabels()[apps.ControllerRevisionHashLabelKey]
	if objHash == hash {
		return true
	}
	return GetShortHash(hash) == GetShortHash(objHash)
}

func (r *revisionAdapterImpl) WriteRevisionHash(obj metav1.Object, hash string) {
	if obj.GetLabels() == nil {
		obj.SetLabels(make(map[string]string, 1))
	}
	shortHash := GetShortHash(hash)
	obj.GetLabels()[apps.ControllerRevisionHashLabelKey] = shortHash
}

func GetShortHash(hash string) string {
	// This makes sure the real hash must be the last '-' substring of revision name
	// vendor/k8s.io/kubernetes/pkg/controller/history/controller_history.go#82
	list := strings.Split(hash, "-")
	return list[len(list)-1]
}

// GetControllerKey return key of GroupSet.
func GetControllerKey(set *v1alpha1.InstanceSet) string {
	return types.NamespacedName{Namespace: set.Namespace, Name: set.Name}.String()
}

// GetActiveInstance returns all active podGroups in this namespace.
func GetActiveInstance(reader client.Reader, set *v1alpha1.InstanceSet, opts *client.ListOptions) ([]*v1alpha1.Instance, error) {
	setList := &v1alpha1.InstanceList{}
	if err := reader.List(context.TODO(), setList, opts); err != nil {
		return nil, err
	}

	activeInstances := make([]*v1alpha1.Instance, 0, len(setList.Items))
	for i, instance := range setList.Items {
		if instance.DeletionTimestamp != nil {
			continue
		}
		if owner := metav1.GetControllerOfNoCopy(&instance); owner == nil || owner.UID != set.UID {
			continue
		}
		activeInstances = append(activeInstances, &setList.Items[i])
	}
	return activeInstances, nil
}

// NextRevision finds the next valid revision number based on revisions. If the length of revisions
// is 0 this is 1. Otherwise, it is 1 greater than the largest revision's Revision. This method
// assumes that revisions has been sorted by Revision.
func NextRevision(revisions []*apps.ControllerRevision) int64 {
	count := len(revisions)
	if count <= 0 {
		return 1
	}
	return revisions[count-1].Revision + 1
}

// IsRunningAndReady returns true if podGroup is in the PodGroupRunning Phase, if it is ready.
func IsRunningAndReady(instance *v1alpha1.Instance) bool {
	readyCondition := inplaceutil.GetInstanceCondition(instance, v1alpha1.InstanceReady)
	if readyCondition == nil || readyCondition.Status != corev1.ConditionTrue {
		return false
	}
	for _, rg := range instance.Spec.ReadinessGates {
		c := inplaceutil.GetInstanceCondition(instance, rg.ConditionType)
		if c == nil || c.Status != corev1.ConditionTrue {
			return false
		}
	}
	return isInstanceAllComponentReady(instance)
}

func isInstanceAllComponentReady(instance *v1alpha1.Instance) bool {
	if len(instance.Spec.Components) != len(instance.Status.ComponentStatuses) {
		return false
	}
	componentSize := make(map[string]int32, len(instance.Spec.Components))
	for _, comp := range instance.Spec.Components {
		componentSize[comp.Name] = inplaceutil.GetComponentSize(&comp)
	}
	for _, status := range instance.Status.ComponentStatuses {
		replicas, ok := componentSize[status.Name]
		if !ok {
			return false
		}
		if status.ReadyReplicas != replicas {
			return false
		}
	}
	return true
}

// IsRunningAndAvailable returns true if podGroup is in the Running Phase, if it is available.
func IsRunningAndAvailable(instance *v1alpha1.Instance, minReadySeconds int32) bool {
	if !IsRunningAndReady(instance) {
		return false
	}
	readyCondition := inplaceutil.GetInstanceCondition(instance, v1alpha1.InstanceReady)
	return time.Since(readyCondition.LastTransitionTime.Time) > time.Duration(minReadySeconds)*time.Second
}

// SplitInstancesByRevision returns Instances matched and unmatched the given revision
func SplitInstancesByRevision(instances []*v1alpha1.Instance, rev string) (matched, unmatched []*v1alpha1.Instance) {
	for _, g := range instances {
		if EqualToRevisionHash("", g, rev) {
			matched = append(matched, g)
		} else {
			unmatched = append(unmatched, g)
		}
	}
	return
}

// GenSelectorLabel returns a label selector for InstanceSet
func GenSelectorLabel(set *v1alpha1.InstanceSet) map[string]string {
	return map[string]string{
		v1alpha1.SetInstanceOwnerLabelKey: string(set.UID),
	}
}

func DumpJSON(instance *v1alpha1.Instance) string {
	b, err := json.Marshal(instance)
	if err != nil {
		return err.Error()
	}
	return string(b)
}

// DoItSlowly tries to call the provided function a total of 'count' times,
// starting slow to check for errors, then speeding up if calls succeed.
//
// It groups the calls into batches, starting with a group of initialBatchSize.
// Within each batch, it may call the function multiple times concurrently.
//
// If a whole batch succeeds, the next batch may get exponentially larger.
// If there are any failures in a batch, all remaining batches are skipped
// after waiting for the current batch to complete.
//
// It returns the number of successful calls to the function.
func DoItSlowly(count int, initialBatchSize int, fn func() error) (int, error) {
	remaining := count
	successes := 0
	for batchSize := integer.IntMin(remaining, initialBatchSize); batchSize > 0; batchSize = integer.IntMin(2*batchSize, remaining) {
		errCh := make(chan error, batchSize)
		var wg sync.WaitGroup
		wg.Add(batchSize)
		for i := 0; i < batchSize; i++ {
			go func() {
				defer wg.Done()
				if err := fn(); err != nil {
					errCh <- err
				}
			}()
		}
		wg.Wait()
		curSuccesses := batchSize - len(errCh)
		successes += curSuccesses
		if len(errCh) > 0 {
			return successes, <-errCh
		}
		remaining -= batchSize
	}
	return successes, nil
}
