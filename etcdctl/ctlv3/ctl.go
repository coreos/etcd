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
	"time"

	"go.etcd.io/etcd/api/v3/version"
	"go.etcd.io/etcd/etcdctl/v3/ctlv3/command"
	"go.etcd.io/etcd/pkg/v3/cobrautl"

	"github.com/spf13/cobra"
)

const (
	cliName        = "etcdctl"
	cliDescription = "A simple command line client for etcd3."

	defaultDialTimeout      = 2 * time.Second
	defaultCommandTimeOut   = 5 * time.Second
	defaultKeepAliveTime    = 2 * time.Second
	defaultKeepAliveTimeOut = 6 * time.Second
)

var (
	globalFlags = command.GlobalFlags{}
)

var (
	rootCmd = &cobra.Command{
		Use:        cliName,
		Short:      cliDescription,
		SuggestFor: []string{"etcdctl"},
	}
)

func init() {
	rootCmd.PersistentFlags().StringSliceVar(&globalFlags.Endpoints, "endpoints", []string{"127.0.0.1:2379"}, "gRPC endpoints")
	rootCmd.PersistentFlags().BoolVar(&globalFlags.Debug, "debug", false, "enable client-side debug logging")

	rootCmd.PersistentFlags().StringVarP(&globalFlags.OutputFormat, "write-out", "w", "simple", "set the output format (fields, json, protobuf, simple, table)")
	rootCmd.PersistentFlags().BoolVar(&globalFlags.IsHex, "hex", false, "print byte strings as hex encoded strings")
	rootCmd.RegisterFlagCompletionFunc("write-out", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"fields", "json", "protobuf", "simple", "table"}, cobra.ShellCompDirectiveDefault
	})

	rootCmd.PersistentFlags().DurationVar(&globalFlags.DialTimeout, "dial-timeout", defaultDialTimeout, "dial timeout for client connections")
	rootCmd.PersistentFlags().DurationVar(&globalFlags.CommandTimeOut, "command-timeout", defaultCommandTimeOut, "timeout for short running command (excluding dial timeout)")
	rootCmd.PersistentFlags().DurationVar(&globalFlags.KeepAliveTime, "keepalive-time", defaultKeepAliveTime, "keepalive time for client connections")
	rootCmd.PersistentFlags().DurationVar(&globalFlags.KeepAliveTimeout, "keepalive-timeout", defaultKeepAliveTimeOut, "keepalive timeout for client connections")

	// TODO: secure by default when etcd enables secure gRPC by default.
	rootCmd.PersistentFlags().BoolVar(&globalFlags.Insecure, "insecure-transport", true, "disable transport security for client connections")
	rootCmd.PersistentFlags().BoolVar(&globalFlags.InsecureDiscovery, "insecure-discovery", true, "accept insecure SRV records describing cluster endpoints")
	rootCmd.PersistentFlags().BoolVar(&globalFlags.InsecureSkipVerify, "insecure-skip-tls-verify", false, "skip server certificate verification (CAUTION: this option should be enabled only for testing purposes)")
	rootCmd.PersistentFlags().StringVar(&globalFlags.TLS.CertFile, "cert", "", "identify secure client using this TLS certificate file")
	rootCmd.PersistentFlags().StringVar(&globalFlags.TLS.KeyFile, "key", "", "identify secure client using this TLS key file")
	rootCmd.PersistentFlags().StringVar(&globalFlags.TLS.TrustedCAFile, "cacert", "", "verify certificates of TLS-enabled secure servers using this CA bundle")
	rootCmd.PersistentFlags().StringVar(&globalFlags.User, "user", "", "username[:password] for authentication (prompt if password is not supplied)")
	rootCmd.PersistentFlags().StringVar(&globalFlags.Password, "password", "", "password for authentication (if this option is used, --user option shouldn't include password)")
	rootCmd.PersistentFlags().StringVarP(&globalFlags.TLS.ServerName, "discovery-srv", "d", "", "domain name to query for SRV records describing cluster endpoints")
	rootCmd.PersistentFlags().StringVarP(&globalFlags.DNSClusterServiceName, "discovery-srv-name", "", "", "service name to query when using DNS discovery")

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
		command.NewTryLockCommand(),
		command.NewElectCommand(),
		command.NewAuthCommand(),
		command.NewUserCommand(),
		command.NewRoleCommand(),
		command.NewCheckCommand(),
		command.NewCompletionCommand(),
	)
}

func usageFunc(c *cobra.Command) error {
	return cobrautl.UsageFunc(c, version.Version, version.APIVersion)
}

func Start() error {
	rootCmd.SetUsageFunc(usageFunc)
	// Make help just show the usage
	rootCmd.SetHelpTemplate(`{{.UsageString}}`)
	return rootCmd.Execute()
}

func MustStart() {
	if err := Start(); err != nil {
		cobrautl.ExitWithError(cobrautl.ExitError, err)
	}
}

func init() {
	cobra.EnablePrefixMatching = true
}
