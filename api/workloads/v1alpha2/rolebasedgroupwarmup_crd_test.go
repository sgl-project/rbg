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
	"bytes"
	"os"
	"testing"
)

func TestGeneratedWarmupCRDValidatesCustomizedContainerImages(t *testing.T) {
	manifest, err := os.ReadFile("../../../config/crd/bases/workloads.x-k8s.io_rolebasedgroupwarmups.yaml")
	if err != nil {
		t.Fatalf("read generated Warmup CRD: %v", err)
	}

	validationRule := []byte("self.all(c, has(c.image) && c.image.matches('.*[^[:space:]].*'))")
	if got := bytes.Count(manifest, validationRule); got != 2 {
		t.Fatalf("expected customized container image validation in both target schemas, got %d occurrences", got)
	}
}
