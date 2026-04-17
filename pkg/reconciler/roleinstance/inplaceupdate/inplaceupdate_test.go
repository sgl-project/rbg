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

package inplaceupdate

import (
	"errors"
	"testing"
)

func TestSplitComponentControllerRevisionNilInput(t *testing.T) {
	c := &realControl{}
	_, err := c.splitComponentControllerRevision(nil)
	if err == nil {
		t.Fatal("expected error for nil revision, got nil")
	}
	if !errors.Is(err, ErrNilControllerRevision) {
		t.Fatalf("expected ErrNilControllerRevision, got %v", err)
	}
}
