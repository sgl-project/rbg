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

package fieldindex

import (
	"context"
	"sync"

	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	IndexNameForOwnerRefUID = "ownerRefUID"
)

var (
	registerOnce sync.Once
)

var ownerIndexFunc = func(obj client.Object) []string {
	var owners []string
	for _, ref := range obj.GetOwnerReferences() {
		owners = append(owners, string(ref.UID))
	}
	return owners
}

func RegisterFieldIndexes(c cache.Cache) error {
	var (
		err error
		ctx = context.Background()
	)
	registerOnce.Do(func() {
		// pod ownerReference
		if err = c.IndexField(ctx, &v1.Pod{}, IndexNameForOwnerRefUID, ownerIndexFunc); err != nil {
			return
		}
	})
	return err
}
