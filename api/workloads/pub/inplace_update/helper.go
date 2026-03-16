/*
Copyright 2026 The RBG Authors.
Copyright 2020 The Kruise Authors.

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

package inplace_update

import (
	"encoding/json"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/rbgs/api/workloads/constants"
)

func GetInPlaceUpdateState(obj metav1.Object) (string, bool) {
	v, ok := obj.GetAnnotations()[constants.InPlaceUpdateStateKey]
	return v, ok
}

func GetInPlaceUpdateGrace(obj metav1.Object) (string, bool) {
	v, ok := obj.GetAnnotations()[constants.InPlaceUpdateGraceKey]
	return v, ok
}

func RemoveInPlaceUpdateGrace(obj metav1.Object) {
	delete(obj.GetAnnotations(), constants.InPlaceUpdateGraceKey)
}

func GetRuntimeContainerMetaSet(obj metav1.Object) (*RuntimeContainerMetaSet, error) {
	str, ok := obj.GetAnnotations()[constants.RuntimeContainerMetaKey]
	if !ok {
		return nil, nil
	}

	s := RuntimeContainerMetaSet{}
	if err := json.Unmarshal([]byte(str), &s); err != nil {
		return nil, err
	}
	return &s, nil
}
