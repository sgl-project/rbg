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

package v1alpha2

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

func countCompletionPolicyEnums(schema *apiextensionsv1.JSONSchemaProps) (count int, allAllowEmpty bool) {
	allAllowEmpty = true
	if property, ok := schema.Properties["completionPolicy"]; ok {
		count++
		allowsEmpty := false
		for _, value := range property.Enum {
			if string(value.Raw) == `""` {
				allowsEmpty = true
				break
			}
		}
		allAllowEmpty = allowsEmpty
	}
	for _, property := range schema.Properties {
		nestedCount, nestedAllowsEmpty := countCompletionPolicyEnums(&property)
		count += nestedCount
		allAllowEmpty = allAllowEmpty && nestedAllowsEmpty
	}
	if schema.AdditionalProperties != nil && schema.AdditionalProperties.Schema != nil {
		nestedCount, nestedAllowsEmpty := countCompletionPolicyEnums(schema.AdditionalProperties.Schema)
		count += nestedCount
		allAllowEmpty = allAllowEmpty && nestedAllowsEmpty
	}
	if schema.Items != nil && schema.Items.Schema != nil {
		nestedCount, nestedAllowsEmpty := countCompletionPolicyEnums(schema.Items.Schema)
		count += nestedCount
		allAllowEmpty = allAllowEmpty && nestedAllowsEmpty
	}
	return count, allAllowEmpty
}

// The RoleBasedGroupWarmup CRD intentionally does NOT validate customized action
// container images.
//
// Why not: a customized action container is a bare corev1.Container embedded in the
// Warmup spec, so the Pod API's own image validation ("Required value" / "must not
// have leading or trailing whitespace") never runs on it. Two mechanisms were tried
// and both were rejected:
//
//   - An item-scoped CEL rule (`items:XValidation`). CEL is evaluated on every write
//     against the full object. On Kubernetes <= 1.32 the status subresource strategy
//     calls the CEL validator without ratcheting options (ratcheting was only wired
//     into the status path in 1.33), so an already-stored object with a missing or
//     blank image can no longer have its status written at all: the controller cannot
//     record phase/conditions and the object is stuck. On 1.33+, and on the main
//     resource path in every version, CRDValidationRatcheting exempts unchanged
//     fields, which is why the freeze only shows up on <= 1.32.
//   - Structural validation (`required` + `pattern`) requires post-processing the
//     generated CRD, because controller-gen cannot mark a slice item as required
//     (`items:Required` is a silent no-op: only ValidationMarkers are cloned with the
//     `items:` prefix, and Required lives in FieldOnlyMarkers) and an array-level CEL
//     rule exceeds the API server's rule-cost budget.
//
// So the image is validated by the controller instead: an invalid action fails the
// Warmup with reason InvalidWarmupSpec and creates no Pods. This matches how every
// other pod template in the project is handled - RoleBasedGroup, RoleBasedGroupSet,
// Instance and InstanceSet CRDs do not validate container images either.
//
// This test guards the invariant so the CEL rule is not reintroduced. If admission
// level validation is ever wanted here, add a validating webhook rather than a CRD
// rule, and update this test with the reasoning.
func TestGeneratedWarmupCRDDoesNotValidateContainerImages(t *testing.T) {
	manifest, err := os.ReadFile("../../../config/crd/bases/workloads.x-k8s.io_rolebasedgroupwarmups.yaml")
	if err != nil {
		t.Fatalf("read generated Warmup CRD: %v", err)
	}
	body := string(manifest)

	// The rule installed by #466 must not come back. Banned as two separate markers so
	// that a reworded message is still caught:
	//   - the message text, and
	//   - any x-kubernetes-validations attached to the customizedAction containers
	//     schema (the CEL rule was the only such rule there).
	for _, forbidden := range []string{
		"customized action container image must not be empty",
		"has(self.image) && self.image != ''",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("generated CRD contains %q; this rule freezes status updates for "+
				"pre-existing objects on Kubernetes <= 1.32 and must not be re-added", forbidden)
		}
	}

	// The other validations from #466 stay: images items must still have minLength 1.
	if !strings.Contains(body, "minLength: 1") {
		t.Error("imagePreload images minLength marker is missing; it is ratcheting-safe and was kept")
	}
}

func TestGeneratedWarmupCRDBoundsCustomizedActionResults(t *testing.T) {
	manifest, err := os.ReadFile("../../../config/crd/bases/workloads.x-k8s.io_rolebasedgroupwarmups.yaml")
	if err != nil {
		t.Fatalf("read generated Warmup CRD: %v", err)
	}

	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(manifest, &crd); err != nil {
		t.Fatalf("decode generated Warmup CRD: %v", err)
	}
	root := crd.Spec.Versions[0].Schema.OpenAPIV3Schema
	status := root.Properties["status"]
	results := status.Properties["customizedActionResults"]
	if results.MaxItems == nil || *results.MaxItems != customizedActionResultsMaxItemsForCRDTest {
		t.Fatalf("expected customizedActionResults maxItems %d, got %v", customizedActionResultsMaxItemsForCRDTest, results.MaxItems)
	}
	truncated := status.Properties["customizedActionResultsTruncated"]
	if truncated.Type != "boolean" {
		t.Fatalf("expected customizedActionResultsTruncated boolean schema, got %#v", truncated)
	}
}

func TestGeneratedWarmupCRDValidatesCustomizedActionResultDetails(t *testing.T) {
	manifest, err := os.ReadFile("../../../config/crd/bases/workloads.x-k8s.io_rolebasedgroupwarmups.yaml")
	if err != nil {
		t.Fatalf("read generated Warmup CRD: %v", err)
	}

	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(manifest, &crd); err != nil {
		t.Fatalf("decode generated Warmup CRD: %v", err)
	}
	root := crd.Spec.Versions[0].Schema.OpenAPIV3Schema
	result := root.Properties["status"].Properties["customizedActionResults"].Items.Schema
	assertEnumValues(t, result.Properties["state"], "Failed", "Pending", "Running", "Succeeded")
	assertMaxLength(t, result.Properties["reason"], customizedActionReasonMaxLengthForCRDTest)
	assertMaxLength(t, result.Properties["message"], customizedActionMessageMaxLengthForCRDTest)

	containers := result.Properties["containers"]
	if containers.MaxItems == nil || *containers.MaxItems != customizedActionContainersMaxItemsForCRDTest {
		t.Fatalf("expected containers maxItems %d, got %v", customizedActionContainersMaxItemsForCRDTest, containers.MaxItems)
	}
	container := containers.Items.Schema
	assertEnumValues(t, container.Properties["state"], "Failed", "Running", "Succeeded", "Waiting")
	assertMaxLength(t, container.Properties["terminationReason"], customizedActionReasonMaxLengthForCRDTest)
	assertMaxLength(t, container.Properties["terminationMessage"], customizedActionMessageMaxLengthForCRDTest)
}

func assertEnumValues(t *testing.T, schema apiextensionsv1.JSONSchemaProps, want ...string) {
	t.Helper()
	got := make([]string, 0, len(schema.Enum))
	for _, value := range schema.Enum {
		got = append(got, string(value.Raw))
	}
	for i := range got {
		got[i] = strings.Trim(got[i], `"`)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("expected enum %v, got %v", want, got)
	}
}

func assertMaxLength(t *testing.T, schema apiextensionsv1.JSONSchemaProps, want int64) {
	t.Helper()
	if schema.MaxLength == nil || *schema.MaxLength != want {
		t.Fatalf("expected maxLength %d, got %v in schema %s", want, schema.MaxLength, fmt.Sprint(schema))
	}
}

func TestGeneratedWarmupCRDAllowsEmptyCompletionPolicy(t *testing.T) {
	manifest, err := os.ReadFile("../../../config/crd/bases/workloads.x-k8s.io_rolebasedgroupwarmups.yaml")
	if err != nil {
		t.Fatalf("read generated Warmup CRD: %v", err)
	}

	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(manifest, &crd); err != nil {
		t.Fatalf("decode generated Warmup CRD: %v", err)
	}
	root := crd.Spec.Versions[0].Schema.OpenAPIV3Schema
	count, allAllowEmpty := countCompletionPolicyEnums(root)
	if count != 2 || !allAllowEmpty {
		t.Fatalf("expected both completionPolicy schemas to allow an explicit empty value, count=%d allowEmpty=%t", count, allAllowEmpty)
	}
}

const customizedActionResultsMaxItemsForCRDTest int64 = 1024

const (
	customizedActionContainersMaxItemsForCRDTest int64 = 1024
	customizedActionReasonMaxLengthForCRDTest    int64 = 128
	customizedActionMessageMaxLengthForCRDTest   int64 = 1024
)
