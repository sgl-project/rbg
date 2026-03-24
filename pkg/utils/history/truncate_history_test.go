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

package history

import (
	"testing"

	apps "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestTruncateControllerRevisionsDeletesOldestNonLiveRevisions(t *testing.T) {
	revisions := []*apps.ControllerRevision{
		newRevision("rev-1", 1),
		newRevision("rev-2", 2),
		newRevision("rev-3", 3),
		newRevision("rev-4", 4),
		newRevision("rev-5", 5),
		newRevision("rev-6", 6),
	}

	var deleted []string
	err := TruncateControllerRevisions(
		revisions,
		revisions[4],
		revisions[5],
		1,
		func(revisionName string) bool {
			return revisionName == "rev-4"
		},
		func(revision *apps.ControllerRevision) error {
			deleted = append(deleted, revision.Name)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("TruncateControllerRevisions() error = %v", err)
	}

	want := []string{"rev-1", "rev-2"}
	if len(deleted) != len(want) {
		t.Fatalf("deleted revisions count = %d, want %d", len(deleted), len(want))
	}
	for i := range want {
		if deleted[i] != want[i] {
			t.Fatalf("deleted[%d] = %q, want %q", i, deleted[i], want[i])
		}
	}
}

func TestTruncateControllerRevisionsSkipsDeleteWhenWithinLimit(t *testing.T) {
	revisions := []*apps.ControllerRevision{
		newRevision("rev-1", 1),
		newRevision("rev-2", 2),
		newRevision("rev-3", 3),
		newRevision("rev-4", 4),
	}

	deleted := false
	err := TruncateControllerRevisions(
		revisions,
		revisions[2],
		revisions[3],
		2,
		func(revisionName string) bool {
			return revisionName == "rev-2"
		},
		func(revision *apps.ControllerRevision) error {
			deleted = true
			return nil
		},
	)
	if err != nil {
		t.Fatalf("TruncateControllerRevisions() error = %v", err)
	}
	if deleted {
		t.Fatal("expected no revision deletion")
	}
}

func TestTruncateControllerRevisionsHandlesNilCurrentAndUpdate(t *testing.T) {
	revisions := []*apps.ControllerRevision{
		newRevision("rev-1", 1),
		newRevision("rev-2", 2),
		newRevision("rev-3", 3),
	}

	var deleted []string
	err := TruncateControllerRevisions(
		revisions,
		nil,
		nil,
		1,
		nil,
		func(revision *apps.ControllerRevision) error {
			deleted = append(deleted, revision.Name)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("TruncateControllerRevisions() error = %v", err)
	}
	want := []string{"rev-1", "rev-2"}
	if len(deleted) != len(want) {
		t.Fatalf("deleted revisions count = %d, want %d", len(deleted), len(want))
	}
	for i := range want {
		if deleted[i] != want[i] {
			t.Fatalf("deleted[%d] = %q, want %q", i, deleted[i], want[i])
		}
	}
}

func TestTruncateControllerRevisionsReturnsErrorOnNegativeHistoryLimit(t *testing.T) {
	revisions := []*apps.ControllerRevision{
		newRevision("rev-1", 1),
	}

	err := TruncateControllerRevisions(
		revisions,
		nil,
		nil,
		-1,
		nil,
		func(*apps.ControllerRevision) error {
			return nil
		},
	)
	if err == nil {
		t.Fatal("expected error for negative historyLimit")
	}
	if err.Error() != "historyLimit must be non-negative: -1" {
		t.Fatalf("error = %q, want %q", err.Error(), "historyLimit must be non-negative: -1")
	}
}

func TestTruncateControllerRevisionsReturnsErrorOnNilDeleteRevision(t *testing.T) {
	revisions := []*apps.ControllerRevision{
		newRevision("rev-1", 1),
	}

	err := TruncateControllerRevisions(
		revisions,
		nil,
		nil,
		0,
		nil,
		nil,
	)
	if err == nil {
		t.Fatal("expected error for nil deleteRevision")
	}
	if err.Error() != "deleteRevision must not be nil" {
		t.Fatalf("error = %q, want %q", err.Error(), "deleteRevision must not be nil")
	}
}

func newRevision(name string, revision int64) *apps.ControllerRevision {
	return &apps.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Revision:   revision,
	}
}
