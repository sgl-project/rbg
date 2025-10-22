/*
Copyright 2025.

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

package rollout

import (
	"sort"

	"github.com/spf13/cobra"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

type RolloutOptions struct {
	cf       *genericclioptions.ConfigFlags
	revision int64
}

var rolloutOpts RolloutOptions

func NewRolloutCmd(cf *genericclioptions.ConfigFlags) *cobra.Command {
	rolloutOpts.cf = cf
	rolloutCmd := &cobra.Command{
		Use:   "rollout [SUBCOMMAND]",
		Short: "Manage the rollout of a rbg object",
		Example: "  # Show all historical revisions of rbg\n" +
			"  kubectl rbg rollout history abc\n" +
			"  # Rollback to the previous deployment\n" +
			"  kubectl rbg rollout undo abc\n",
		Args:               cobra.ExactArgs(1),
		DisableAutoGenTag:  true,
		SilenceUsage:       true,
		FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
	}

	rolloutCmd.AddCommand(rolloutHistoryCmd)
	rolloutCmd.AddCommand(rolloutDiffCmd)
	rolloutCmd.AddCommand(rolloutUndoCmd)
	return rolloutCmd
}

func sortRevisionsStable(items []*appsv1.ControllerRevision) []*appsv1.ControllerRevision {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Revision == items[j].Revision {
			if items[i].CreationTimestamp.Equal(&items[j].CreationTimestamp) {
				return items[i].Name < items[j].Name
			}
			return items[i].CreationTimestamp.Before(&items[j].CreationTimestamp)
		}
		return items[i].Revision < items[j].Revision
	})
	return items
}
