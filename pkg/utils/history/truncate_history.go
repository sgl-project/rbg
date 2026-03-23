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

import apps "k8s.io/api/apps/v1"

// TruncateControllerRevisions deletes the oldest non-live revisions until the
// number of retained non-live revisions is within historyLimit.
//
// The revisions slice is expected to be sorted from oldest to newest.
func TruncateControllerRevisions(
	revisions []*apps.ControllerRevision,
	current *apps.ControllerRevision,
	update *apps.ControllerRevision,
	historyLimit int,
	isRevisionInUse func(string) bool,
	deleteRevision func(*apps.ControllerRevision) error,
) error {
	nonLiveRevisions := make([]*apps.ControllerRevision, 0, len(revisions))

	for i := range revisions {
		revision := revisions[i]
		if current != nil && revision.Name == current.Name {
			continue
		}
		if update != nil && revision.Name == update.Name {
			continue
		}
		if isRevisionInUse != nil && isRevisionInUse(revision.Name) {
			continue
		}
		nonLiveRevisions = append(nonLiveRevisions, revision)
	}

	if len(nonLiveRevisions) <= historyLimit {
		return nil
	}

	for _, revision := range nonLiveRevisions[:len(nonLiveRevisions)-historyLimit] {
		if err := deleteRevision(revision); err != nil {
			return err
		}
	}

	return nil
}
