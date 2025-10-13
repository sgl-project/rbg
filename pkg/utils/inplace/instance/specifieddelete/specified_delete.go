/*
Copyright 2025.

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

package specifieddelete

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

// IsSpecifiedDelete return true if this object has specific-delete label
func IsSpecifiedDelete(obj metav1.Object) bool {
	_, ok := obj.GetLabels()[appsv1alpha1.SpecifiedDeleteKey]
	return ok
}

// PatchInstanceSpecifiedDelete patch specific-delete label for the given Instance.
func PatchInstanceSpecifiedDelete(c client.Client, instance *appsv1alpha1.Instance, value string) (bool, error) {
	if _, ok := instance.Labels[appsv1alpha1.SpecifiedDeleteKey]; ok {
		return false, nil
	}

	body := fmt.Sprintf(
		`{"metadata":{"labels":{"%s":"%s"}}}`,
		appsv1alpha1.SpecifiedDeleteKey,
		value,
	)
	return true, c.Patch(context.TODO(), instance, client.RawPatch(types.MergePatchType, []byte(body)))
}
