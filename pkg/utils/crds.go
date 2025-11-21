package utils

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CheckCrdExists checks if a Custom Resource Definition (CRD) with the specified name exists and is established.
func CheckCrdExists(reader client.Reader, crdName string) error {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	ctx := context.Background()
	if err := reader.Get(ctx, client.ObjectKey{Name: crdName}, crd); err != nil {
		return err
	}

	// Check Established status
	for _, cond := range crd.Status.Conditions {
		if cond.Type == apiextensionsv1.Established && cond.Status == apiextensionsv1.ConditionTrue {
			return nil
		}
	}
	return fmt.Errorf("CRD %s exists but not established", crdName)
}

// CheckOwnerReference checks if any OwnerReference matches the target GVK.
func CheckOwnerReference(ownerReferences []metav1.OwnerReference, targetGVK schema.GroupVersionKind) bool {
	for _, ref := range ownerReferences {
		// // Parse the APIVersion from OwnerReference
		refGV, err := schema.ParseGroupVersion(ref.APIVersion)
		if err != nil {
			// Log invalid APIVersion format if needed (e.g., for debugging)
			// log.Printf("Invalid APIVersion in OwnerReference: %s", ref.APIVersion)
			continue
		}
		// Compare Group and Version
		if refGV.Group == targetGVK.Group &&
			refGV.Version == targetGVK.Version &&
			ref.Kind == targetGVK.Kind {
			return true
		}
	}
	return false
}

func GetRbgGVK() schema.GroupVersionKind {
	return schema.FromAPIVersionAndKind(workloadsv1alpha1.GroupVersion.String(), "RoleBasedGroup")
}

func GetRbgScalingAdapterGVK() schema.GroupVersionKind {
	return schema.FromAPIVersionAndKind(workloadsv1alpha1.GroupVersion.String(), "RoleBasedGroupScalingAdapter")
}

func GetLwsGVK() schema.GroupVersionKind {
	return schema.FromAPIVersionAndKind(lwsv1.GroupVersion.String(), "LeaderWorkerSet")
}

func GetInstanceSetGVK() schema.GroupVersionKind {
	return schema.FromAPIVersionAndKind(workloadsv1alpha1.GroupVersion.String(), "InstanceSet")
}
