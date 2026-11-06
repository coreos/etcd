// Copyright 2015 The etcd Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package ctlv3 contains the main entry point for the etcdctl for v3 API.
package ctlv3

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"go.etcd.io/etcd/etcdctl/v3/ctlv3/command"
	"go.etcd.io/etcd/pkg/v3/cobrautl"
)

const (
	cliName        = "etcdctl"
	cliDescription = "A simple command line client for etcd3."
)

var rootCmd = &cobra.Command{
	Use:        cliName,
	Short:      cliDescription,
	SuggestFor: []string{"etcdctl"},
}

func init() {
	command.RegisterGlobalFlags(rootCmd)
	rootCmd.RegisterFlagCompletionFunc(command.FlagWriteOut, func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"fields", "json", "protobuf", "simple", "table"}, cobra.ShellCompDirectiveDefault
	})

	rootCmd.AddGroup(
		command.NewKVGroup(),
		command.NewClusterMaintenanceGroup(),
		command.NewConcurrencyGroup(),
		command.NewAuthenticationGroup(),
		command.NewUtilityGroup(),
	)

	rootCmd.AddCommand(
		command.NewGetCommand(),
		command.NewPutCommand(),
		command.NewDelCommand(),
		command.NewTxnCommand(),
		command.NewCompactionCommand(),
		command.NewAlarmCommand(),
		command.NewDefragCommand(),
		command.NewEndpointCommand(),
		command.NewMoveLeaderCommand(),
		command.NewWatchCommand(),
		command.NewVersionCommand(),
		command.NewLeaseCommand(),
		command.NewMemberCommand(),
		command.NewSnapshotCommand(),
		command.NewMakeMirrorCommand(),
		command.NewLockCommand(),
		command.NewElectCommand(),
		command.NewAuthCommand(),
		command.NewUserCommand(),
		command.NewRoleCommand(),
		command.NewCheckCommand(),
		command.NewCompletionCommand(),
		command.NewDowngradeCommand(),
		command.NewOptionsCommand(rootCmd),
	)
	command.SetHelpCmdGroup(rootCmd)

	hideAllGlobalFlags()
	hideHelpFlag()
	addOptionsPrompt()
}

func Start() error {
	return rootCmd.Execute()
}

func MustStart() {
	if err := Start(); err != nil {
		if rootCmd.SilenceErrors {
			cobrautl.ExitWithError(cobrautl.ExitError, err)
		}
		os.Exit(cobrautl.ExitError)
	}
}

func hideAllGlobalFlags() {
	rootCmd.PersistentFlags().VisitAll(func(f *pflag.Flag) {
		rootCmd.PersistentFlags().MarkHidden(f.Name)
	})
}

func hideHelpFlag() {
	if rootCmd.Flags().Lookup("help") == nil {
		rootCmd.Flags().BoolP("help", "h", false, "help for "+rootCmd.Name())
	}
	rootCmd.Flags().MarkHidden("help")
}

func addOptionsPrompt() {
	defaultHelpFunc := rootCmd.HelpFunc()
	rootCmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		defaultHelpFunc(cmd, args)
		fmt.Fprintln(cmd.OutOrStdout(), `Use "etcdctl options" for a list of global command-line options (applies to all commands).`)
	})
}

func init() {
	cobra.EnablePrefixMatching = true
}
