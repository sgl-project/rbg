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

package rbg

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"sigs.k8s.io/rbgs/test/envtest/testutil"
)

// The KEP-531 cross-field presence rule (targetContainers requires
// component, except under standalonePattern) is enforced by
// ValidateEngineRuntimes in the webhooks and re-run at render time, not by
// the RoleSpec-scoped CEL rule the KEP originally sketched. This spec pins
// why against the envtest kube-apiserver: the rule is a comprehension over
// the uncapped engineRuntimes list at the uncapped roles[] scope, and the
// apiserver refuses a CRD carrying it because the estimated CEL cost blows
// the per-CRD budget. If the apiserver's cost model ever changes enough to
// admit it, this spec fails and the CEL placement can be revisited against
// the KEP text.
const kep531PresenceRule = `!has(self.engineRuntimes) || self.engineRuntimes.all(p, !has(p.targetContainers) || has(p.component) || has(self.standalonePattern))`

// celCostScratchGroup renames the generated CRDs so the server-side dry-run
// creates below never collide with the CRDs the suite installed.
const celCostScratchGroup = "celcost.rbg.test"

// injectRoleSpecPresenceRule appends the presence rule to every schema node
// carrying an engineRuntimes property (every RoleSpec embedding, the way
// controller-gen stamps a RoleSpec-scoped marker) and returns how many nodes
// it touched.
func injectRoleSpecPresenceRule(schema *apiextensionsv1.JSONSchemaProps) int {
	touched := 0
	if _, ok := schema.Properties["engineRuntimes"]; ok {
		schema.XValidations = append(schema.XValidations, apiextensionsv1.ValidationRule{
			Rule:    kep531PresenceRule,
			Message: "targetContainers requires component to be set, except under standalonePattern",
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

// statusCauses returns the field-level messages of an apiserver Invalid error.
func statusCauses(err error) []string {
	var status apierrors.APIStatus
	if !errors.As(err, &status) || status.Status().Details == nil {
		return nil
	}
	causes := make([]string, 0, len(status.Status().Details.Causes))
	for _, cause := range status.Status().Details.Causes {
		causes = append(causes, cause.Field+": "+cause.Message)
	}
	return causes
}

var _ = Describe("KEP-531 presence rule placement", func() {
	loadScratchCRD := func(file string) *apiextensionsv1.CustomResourceDefinition {
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "config", "crd", "bases", file))
		Expect(err).NotTo(HaveOccurred())
		crd := &apiextensionsv1.CustomResourceDefinition{}
		Expect(yaml.Unmarshal(raw, crd)).To(Succeed())
		crd.Spec.Group = celCostScratchGroup
		crd.Name = crd.Spec.Names.Plural + "." + celCostScratchGroup
		return crd
	}

	DescribeTable("the apiserver rejects the RoleSpec-scoped CEL rule for exceeding the cost budget",
		func(file string) {
			crd := loadScratchCRD(file)

			// Control: the generated CRD as shipped is admitted, so the
			// rejection below is attributable to the injected rule alone.
			Expect(testutil.K8sClient.Create(testutil.Ctx, crd.DeepCopy(), client.DryRunAll)).To(Succeed())

			touched := 0
			for i := range crd.Spec.Versions {
				Expect(crd.Spec.Versions[i].Schema).NotTo(BeNil())
				touched += injectRoleSpecPresenceRule(crd.Spec.Versions[i].Schema.OpenAPIV3Schema)
			}
			Expect(touched).To(Equal(len(crd.Spec.Versions)),
				"expected one RoleSpec schema per served version")

			err := testutil.K8sClient.Create(testutil.Ctx, crd, client.DryRunAll)
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected an Invalid error, got: %v", err)
			Expect(strings.Join(statusCauses(err), "\n")).To(ContainSubstring(
				"estimated rule cost total for entire OpenAPIv3 schema exceeds budget by factor of more than 100x"),
				"the apiserver's CEL cost model may have changed; revisit the webhook placement against the KEP text")
		},
		Entry("RoleBasedGroup", "workloads.x-k8s.io_rolebasedgroups.yaml"),
		Entry("RoleBasedGroupSet", "workloads.x-k8s.io_rolebasedgroupsets.yaml"),
	)
})
