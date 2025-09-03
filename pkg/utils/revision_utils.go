package utils

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ListRevisions lists all ControllerRevisions matching selector and owned by parent or no other
// controller. If the returned error is nil the returned slice of ControllerRevisions is valid. If the
// returned error is not nil, the returned slice is not valid.
func ListRevisions(
	ctx context.Context, k8sClient client.Client, parent metav1.Object, selector labels.Selector,
) ([]*appsv1.ControllerRevision, error) {
	// List all revisions in the namespace that match the selector
	revisionList := new(appsv1.ControllerRevisionList)
	err := k8sClient.List(
		ctx, revisionList, client.InNamespace(parent.GetNamespace()), client.MatchingLabelsSelector{Selector: selector},
	)
	if err != nil {
		return nil, err
	}
	history := revisionList.Items
	var owned []*appsv1.ControllerRevision
	for i := range history {
		ref := metav1.GetControllerOfNoCopy(&history[i])
		if ref == nil || ref.UID == parent.GetUID() {
			owned = append(owned, &history[i])
		}

	}
	return owned, err
}

func GetHighestRevision(revisions []*appsv1.ControllerRevision) *appsv1.ControllerRevision {
	count := len(revisions)
	if count <= 0 {
		return nil
	}

	max := int64(0)
	var maxRevision *appsv1.ControllerRevision
	for _, revision := range revisions {
		if max <= revision.Revision {
			max = revision.Revision
			maxRevision = revision
		}
	}
	return maxRevision
}
