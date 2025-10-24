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

package cmd

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/klog/v2"
	"sigs.k8s.io/rbgs/cmd/cli/cmd/rollout"
	"sigs.k8s.io/rbgs/cmd/cli/cmd/status"
	"sigs.k8s.io/rbgs/version"
)

var (
	cf *genericclioptions.ConfigFlags
)

var rootCmd = &cobra.Command{
	Use:               "kubectl rbg [command]",
	Short:             "Kubectl plugin for RoleBasedGroup",
	SilenceUsage:      true,
	DisableAutoGenTag: true,
	Args:              cobra.MaximumNArgs(1),
	Version:           getVersion(),
}

func getVersion() string {
	return fmt.Sprintf(
		"RBG CLI Version: %s, git commit: %s, build date: %s",
		version.Version, version.GitCommit, version.BuildDate,
	)
}

func Execute() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sig
		os.Exit(1)
	}()

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	klog.InitFlags(nil)
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)

	flag.CommandLine.VisitAll(func(f *flag.Flag) {
		if f.Name != "v" {
			pflag.Lookup(f.Name).Hidden = true
		}
	})

	cf = genericclioptions.NewConfigFlags(true)
	cf.AddFlags(rootCmd.PersistentFlags())

	rootCmd.AddCommand(status.NewStatusCmd(cf))
	rootCmd.AddCommand(rollout.NewRolloutCmd(cf))
}
