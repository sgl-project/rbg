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
	"os"
	"strings"
	"testing"
)

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
