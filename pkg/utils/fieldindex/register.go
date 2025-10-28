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
