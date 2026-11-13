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
	"context"
	"os"
	"strings"
	"testing"

	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsvalidation "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/yaml"
)

// The KEP-531 cross-field presence rule (targetContainers requires
// component, except under standalonePattern) is enforced by
// ValidateEngineRuntimes in the webhooks and re-run at render time, not by
// the CEL rule the KEP text prescribes. This test pins why: the rule is a
// comprehension over the uncapped engineRuntimes list at the uncapped
// roles[] scope, and apiextensions' ValidateCustomResourceDefinition
// rejects the CRD carrying it — the CEL cost estimate blows the per-CRD
// budget. Reintroducing the marker on RoleSpec would break CRD creation in
// every real cluster; if the apiserver's cost model ever changes enough to
// admit it, this test fails loudly and the CEL placement can be revisited
// against the KEP text. Injecting the v1alpha2 rule verbatim additionally
// fails CEL type-checking on the v1alpha1 RoleSpec embedding (no
// standalonePattern field there), the version-specific predicate the KEP
// itself prescribes — another reason the write-time rule lives in Go.
const kep531PresenceRule = `!has(self.engineRuntimes) || self.engineRuntimes.all(p, !has(p.targetContainers) || has(p.component) || has(self.standalonePattern))`

const kep531PresenceMessage = "targetContainers requires component to be set, except under standalonePattern"

func loadGeneratedCRD(t *testing.T, path string) *apiextensionsv1.CustomResourceDefinition {
	t.Helper()
	manifest, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated CRD %s: %v", path, err)
	}
	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := yaml.Unmarshal(manifest, crd); err != nil {
		t.Fatalf("decode generated CRD %s: %v", path, err)
	}
	return crd
}

// injectRoleSpecPresenceRule appends the KEP-531 CEL rule to every schema
// node carrying an engineRuntimes property — every RoleSpec embedding, the
// way controller-gen stamps a RoleSpec-scoped marker into each served
// version — and returns how many nodes were touched.
func injectRoleSpecPresenceRule(schema *apiextensionsv1.JSONSchemaProps) int {
	touched := 0
	if _, ok := schema.Properties["engineRuntimes"]; ok {
		schema.XValidations = append(schema.XValidations, apiextensionsv1.ValidationRule{
			Rule:    kep531PresenceRule,
			Message: kep531PresenceMessage,
		})
		touched++
	}
	for name, property := range schema.Properties {
		touched += injectRoleSpecPresenceRule(&property)
		schema.Properties[name] = property
	}
	if schema.Items != nil && schema.Items.Schema != nil {
		touched += injectRoleSpecPresenceRule(schema.Items.Schema)
	}
	if schema.AdditionalProperties != nil && schema.AdditionalProperties.Schema != nil {
		touched += injectRoleSpecPresenceRule(schema.AdditionalProperties.Schema)
	}
	return touched
}

func validateCRD(t *testing.T, crd *apiextensionsv1.CustomResourceDefinition) field.ErrorList {
	t.Helper()
	internal := &apiextensions.CustomResourceDefinition{}
	if err := apiextensionsv1.Convert_v1_CustomResourceDefinition_To_apiextensions_CustomResourceDefinition(crd, internal, nil); err != nil {
		t.Fatalf("convert CRD %s to internal: %v", crd.Name, err)
	}
	return apiextensionsvalidation.ValidateCustomResourceDefinition(context.Background(), internal)
}

func TestGeneratedCRDsAdmitNoRoleSpecCELPresenceRule(t *testing.T) {
	crds := map[string]string{
		"RoleBasedGroup":    "../../../config/crd/bases/workloads.x-k8s.io_rolebasedgroups.yaml",
		"RoleBasedGroupSet": "../../../config/crd/bases/workloads.x-k8s.io_rolebasedgroupsets.yaml",
	}
	for kind, path := range crds {
		t.Run(kind, func(t *testing.T) {
			crd := loadGeneratedCRD(t, path)
			// The generated manifest carries no status; the apiserver sets
			// stored versions at establishment, so supply them for the
			// validation call and keep the control below meaningful.
			for _, version := range crd.Spec.Versions {
				crd.Status.StoredVersions = append(crd.Status.StoredVersions, version.Name)
			}

			// Control: the generated CRD as shipped validates cleanly, so
			// any rejection below is attributable to the injected rule alone.
			for _, err := range validateCRD(t, crd) {
				t.Errorf("unmodified CRD must validate cleanly, got: %s", err.Error())
			}

			// A marker on RoleSpec stamps the rule into every RoleSpec
			// embedding of every served version — spec.roles for RBG, the
			// groupTemplate/template nesting for RBGS: one node per version.
			touched := 0
			for i := range crd.Spec.Versions {
				if crd.Spec.Versions[i].Schema == nil || crd.Spec.Versions[i].Schema.OpenAPIV3Schema == nil {
					t.Fatalf("version %q has no schema", crd.Spec.Versions[i].Name)
				}
				touched += injectRoleSpecPresenceRule(crd.Spec.Versions[i].Schema.OpenAPIV3Schema)
			}
			if touched != len(crd.Spec.Versions) {
				t.Fatalf("expected the presence rule injected into exactly one RoleSpec schema per version (%d), touched %d", len(crd.Spec.Versions), touched)
			}

			// The CEL cost estimate of the rule, multiplied over the
			// RoleSpec embeddings of the whole CRD, exceeds the per-CRD
			// budget — the apiserver refuses to admit the CRD.
			var costErrs []string
			for _, err := range validateCRD(t, crd) {
				if strings.Contains(err.Error(), "estimated rule cost total for entire OpenAPIv3 schema") &&
					strings.Contains(err.Error(), "exceeds budget") {
					costErrs = append(costErrs, err.Error())
				}
			}
			if len(costErrs) == 0 {
				all := make([]string, 0, 4)
				for _, err := range validateCRD(t, crd) {
					all = append(all, err.Error())
				}
				t.Fatalf("CRD carrying the KEP-531 RoleSpec presence rule must be rejected for exceeding the CEL cost budget; the apiserver's cost model may have changed — revisit the webhook placement against the KEP text. Validation errors: %s", strings.Join(all, "; "))
			}
			for _, costErr := range costErrs {
				if !strings.Contains(costErr, "more than 100x") {
					t.Errorf("presence rule cost must exceed the budget by more than 100x, got: %s", costErr)
				}
			}
		})
	}
}
