package utils

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash"
	"hash/fnv"
	"sort"

	"github.com/davecgh/go-spew/spew"
	appsv1 "k8s.io/api/apps/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

const (
	DefaultRevisionHistoryLimit = 5
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

func EqualRevision(lhs, rhs *appsv1.ControllerRevision) bool {
	if lhs == nil || rhs == nil {
		return lhs == rhs
	}

	return bytes.Equal(lhs.Data.Raw, rhs.Data.Raw) && apiequality.Semantic.DeepEqual(lhs.Data.Object, rhs.Data.Object)
}

// ApplyRevision deserializes the historical RBG Roles data stored in a ControllerRevision and applies it to the current RBG.
// Note: The ControllerRevision does not store the actual Role replica counts. After deserialization, the replica counts from the current RBG Roles are used.
// If a Role from the historical ControllerRevision does not exist in the current RBG, its replica count will default to 1.
func ApplyRevision(
	rbg *workloadsv1alpha1.RoleBasedGroup,
	revision *appsv1.ControllerRevision) (*workloadsv1alpha1.RoleBasedGroup, error) {
	currentRolesReplicas := make(map[string]int32)
	for _, role := range rbg.Spec.Roles {
		currentRolesReplicas[role.Name] = *role.Replicas
	}
	str := &bytes.Buffer{}
	err := unstructured.UnstructuredJSONScheme.Encode(rbg, str)
	if err != nil {
		return nil, err
	}
	patched, err := strategicpatch.StrategicMergePatch(str.Bytes(), revision.Data.Raw, rbg)
	if err != nil {
		return nil, err
	}
	restoredRbg := &workloadsv1alpha1.RoleBasedGroup{}
	if err = json.Unmarshal(patched, restoredRbg); err != nil {
		return nil, err
	}
	for i := range restoredRbg.Spec.Roles {
		currentNum, exist := currentRolesReplicas[restoredRbg.Spec.Roles[i].Name]
		if exist {
			restoredRbg.Spec.Roles[i].Replicas = &currentNum
		} else {
			restoredRbg.Spec.Roles[i].Replicas = ptr.To(int32(1))
		}
	}

	return restoredRbg, nil
}

func CleanExpiredRevision(
	ctx context.Context, client client.Client,
	rbg *workloadsv1alpha1.RoleBasedGroup) ([]*appsv1.ControllerRevision, error) {
	selector, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{
		MatchLabels: map[string]string{
			workloadsv1alpha1.SetNameLabelKey: rbg.Name,
		},
	})
	if err != nil {
		return nil, err
	}
	revisions, err := ListRevisions(ctx, client, rbg, selector)
	if err != nil {
		return nil, err
	}
	// todo: Use the default value temporarily, and add new attribute fields in RBG later
	exceedNum := len(revisions) - DefaultRevisionHistoryLimit
	if exceedNum <= 0 {
		return revisions, nil
	}

	sort.SliceStable(revisions, func(i, j int) bool {
		if revisions[i].Revision == revisions[j].Revision {
			if revisions[i].CreationTimestamp.Equal(&revisions[j].CreationTimestamp) {
				return revisions[i].Name < revisions[j].Name
			}
			return revisions[i].CreationTimestamp.Before(&revisions[j].CreationTimestamp)
		}
		return revisions[i].Revision < revisions[j].Revision
	})

	for i, revision := range revisions {
		if i >= exceedNum {
			break
		}

		if err := client.Delete(ctx, revision); err != nil {
			return revisions, err
		}
	}
	cleanedRevisions := revisions[exceedNum:]

	return cleanedRevisions, nil
}

func NewRevision(ctx context.Context, client client.Client,
	rbg *workloadsv1alpha1.RoleBasedGroup, currentRevision *appsv1.ControllerRevision) (*appsv1.ControllerRevision, error) {
	revision := int64(1)
	if currentRevision != nil {
		revision = currentRevision.Revision + 1
	}

	rawPatch, err := getRBGPatch(rbg)
	if err != nil {
		return nil, err
	}

	cr := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: rbg.Namespace,
			Labels: map[string]string{
				workloadsv1alpha1.SetNameLabelKey: rbg.Name,
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(rbg, rbg.GroupVersionKind()),
			},
		},
		Data: runtime.RawExtension{
			Raw: rawPatch,
		},
		Revision: revision,
	}

	rgbHash, err := hashRevision(cr)
	if err != nil {
		return nil, err
	}
	cr.Labels[workloadsv1alpha1.RevisionLabelKey] = rgbHash
	cr.Name = revisionName(rbg.Name, rgbHash, revision)
	return cr, nil
}

// revisionName returns the Name for a ControllerRevision in the form prefix-hash-revisionnumber. If the length
// of prefix is greater than 220 bytes, it is truncated to allow for a name that is no larger than 253 bytes.
// revision-number allows us to avoid collisions if the created prefix-hash already exists in the history,
// since revision will be unique.
func revisionName(prefix string, hash string, revisionNumber int64) string {
	if len(prefix) > 220 {
		prefix = prefix[:220]
	}

	return fmt.Sprintf("%s-%s-%v", prefix, hash, revisionNumber)
}

func GetRolesRevisionHash(revision *appsv1.ControllerRevision) (map[string]string, error) {
	result := make(map[string]string)

	if len(revision.Data.Raw) == 0 {
		return result, nil
	}

	var obj map[string]interface{}
	if err := json.Unmarshal(revision.Data.Raw, &obj); err != nil {
		return nil, fmt.Errorf("failed to unmarshal ControllerRevision data: %w", err)
	}

	spec, ok := obj["spec"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("spec not found or wrong type")
	}

	roles, ok := spec["roles"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("roles not found or wrong type")
	}

	templateLookup := make(map[string]map[string]interface{})
	if rawTemplates, ok := spec["roleTemplates"]; ok {
		templateSlice, ok := rawTemplates.([]interface{})
		if !ok {
			return nil, fmt.Errorf("roleTemplates not found or wrong type")
		}

		for _, t := range templateSlice {
			templateMap, ok := t.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("invalid roleTemplate structure")
			}
			if patch, ok := templateMap["$patch"].(string); ok && patch == "replace" {
				continue
			}

			nameVal, ok := templateMap["name"].(string)
			if !ok || nameVal == "" {
				return nil, fmt.Errorf("roleTemplate missing name field")
			}
			templateLookup[nameVal] = templateMap
		}
	}

	for _, r := range roles {
		roleMap, ok := r.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("invalid role structure")
		}
		nameVal, ok := roleMap["name"].(string)
		if !ok || nameVal == "" {
			if roleMap["$patch"] == "replace" {
				continue
			}
			return nil, fmt.Errorf("role missing name field")
		}

		roleBytes, err := json.Marshal(roleMap)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal role: %w", err)
		}

		hf := fnv.New32a()
		if len(roleBytes) > 0 {
			hf.Write(roleBytes)
		}

		if templateRef, ok := roleMap["templateRef"].(map[string]interface{}); ok {
			if templateName, ok := templateRef["name"].(string); ok && templateName != "" {
				template, found := templateLookup[templateName]
				if !found {
					return nil, fmt.Errorf("role references unknown roleTemplate %q", templateName)
				}

				if templateSpec, ok := template["template"]; ok {
					templateBytes, err := json.Marshal(templateSpec)
					if err != nil {
						return nil, fmt.Errorf("failed to marshal roleTemplate %q: %w", templateName, err)
					}
					hf.Write(templateBytes)
				}
			}
		}

		result[nameVal] = rand.SafeEncodeString(fmt.Sprint(hf.Sum32()))
	}

	return result, nil
}

// getRBGPatch returns a strategic merge patch that can be applied to restore a RoleBasedGroup to a
// previous version.
// Note: This approach creates a copy of the original RBG object before performing the serialization.
// In the serialized output, the replica count for each role will be set to the default value of 1.
func getRBGPatch(rbg *workloadsv1alpha1.RoleBasedGroup) ([]byte, error) {
	clone := rbg.DeepCopy()
	for i := range clone.Spec.Roles {
		clone.Spec.Roles[i].Replicas = nil
	}

	str := &bytes.Buffer{}
	err := unstructured.UnstructuredJSONScheme.Encode(clone, str)
	if err != nil {
		return nil, err
	}
	var raw map[string]interface{}
	err = json.Unmarshal(str.Bytes(), &raw)
	if err != nil {
		return nil, err
	}

	objCopy := make(map[string]interface{})
	specCopy := make(map[string]interface{})
	spec := raw["spec"].(map[string]interface{})
	roles := spec["roles"].([]interface{})
	rolesPatch := []interface{}{
		map[string]interface{}{"$patch": "replace"},
	}
	rolesPatch = append(rolesPatch, roles...)

	specCopy["roles"] = rolesPatch

	roleTemplatesPatch := []interface{}{
		map[string]interface{}{"$patch": "replace"},
	}
	if roleTemplates, ok := spec["roleTemplates"].([]interface{}); ok {
		roleTemplatesPatch = append(roleTemplatesPatch, roleTemplates...)
	}
	specCopy["roleTemplates"] = roleTemplatesPatch

	objCopy["spec"] = specCopy

	return json.Marshal(objCopy)
}

// hashRevision hashes the contents of revision's Data using FNV hashing.
// The returned hash will be a safe encoded string to avoid bad words.
func hashRevision(revision *appsv1.ControllerRevision) (string, error) {
	hf := fnv.New32a()
	if len(revision.Data.Raw) > 0 {
		hf.Write(revision.Data.Raw)
	}
	if revision.Data.Object != nil {
		// hashutil.DeepHashObject(hf, revision.Data.Object)
		if err := deepHashObject(hf, revision.Data.Object); err != nil {
			return "", err
		}
	}
	return rand.SafeEncodeString(fmt.Sprint(hf.Sum32())), nil
}

func deepHashObject(hasher hash.Hash, objectToWrite interface{}) error {
	hasher.Reset()
	printer := spew.ConfigState{
		Indent:         " ",
		SortKeys:       true,
		DisableMethods: true,
		SpewKeys:       true,
	}
	_, err := printer.Fprintf(hasher, "%#v", objectToWrite)
	return err
}
