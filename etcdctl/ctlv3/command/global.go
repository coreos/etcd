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

package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/bgentry/speakeasy"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"go.uber.org/zap"
	"google.golang.org/grpc/grpclog"

	"go.etcd.io/etcd/client/pkg/v3/logutil"
	"go.etcd.io/etcd/client/pkg/v3/srv"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/pkg/v3/cobrautl"
	"go.etcd.io/etcd/pkg/v3/flags"
)

type discoveryCfg struct {
	domain      string
	insecure    bool
	serviceName string
}

var display printer = &simplePrinter{}

var newClientFunc = clientv3.New

const (
	FlagEndpoints             = "endpoints"
	FlagDebug                 = "debug"
	FlagWriteOut              = "write-out"
	FlagHex                   = "hex"
	FlagDialTimeout           = "dial-timeout"
	FlagCommandTimeout        = "command-timeout"
	FlagKeepAliveTime         = "keepalive-time"
	FlagKeepAliveTimeout      = "keepalive-timeout"
	FlagMaxRequestBytes       = "max-request-bytes"
	FlagMaxRecvBytes          = "max-recv-bytes"
	FlagInsecureTransport     = "insecure-transport"
	FlagInsecureDiscovery     = "insecure-discovery"
	FlagInsecureSkipTLSVerify = "insecure-skip-tls-verify"
	FlagCert                  = "cert"
	FlagKey                   = "key"
	FlagCACert                = "cacert"
	FlagAuthJWTToken          = "auth-jwt-token"
	FlagUser                  = "user"
	FlagPassword              = "password"
	FlagDiscoverySRV          = "discovery-srv"
	FlagDiscoverySRVName      = "discovery-srv-name"
)

func RegisterGlobalFlags(cmd *cobra.Command) {
	cmd.PersistentFlags().StringSlice(FlagEndpoints, []string{"127.0.0.1:2379"}, "gRPC endpoints")
	cmd.PersistentFlags().Bool(FlagDebug, false, "enable client-side debug logging")
	cmd.PersistentFlags().StringP(FlagWriteOut, "w", "simple", "set the output format (fields, json, protobuf, simple, table)")
	cmd.PersistentFlags().Bool(FlagHex, false, "print byte strings as hex encoded strings")
	cmd.PersistentFlags().Duration(FlagDialTimeout, 2*time.Second, "dial timeout for client connections")
	cmd.PersistentFlags().Duration(FlagCommandTimeout, 5*time.Second, "timeout for short running command (excluding dial timeout)")
	cmd.PersistentFlags().Duration(FlagKeepAliveTime, 2*time.Second, "keepalive time for client connections")
	cmd.PersistentFlags().Duration(FlagKeepAliveTimeout, 6*time.Second, "keepalive timeout for client connections")
	cmd.PersistentFlags().Int(FlagMaxRequestBytes, 0, "client-side request send limit in bytes (if 0, it defaults to 2.0 MiB (2 * 1024 * 1024).)")
	cmd.PersistentFlags().Int(FlagMaxRecvBytes, 0, "client-side response receive limit in bytes (if 0, it defaults to \"math.MaxInt32\")")
	cmd.PersistentFlags().Bool(FlagInsecureTransport, true, "disable transport security for client connections")
	cmd.PersistentFlags().Bool(FlagInsecureDiscovery, true, "accept insecure SRV records describing cluster endpoints")
	cmd.PersistentFlags().Bool(FlagInsecureSkipTLSVerify, false, "skip server certificate verification (CAUTION: this option should be enabled only for testing purposes)")
	cmd.PersistentFlags().String(FlagCert, "", "identify secure client using this TLS certificate file")
	cmd.PersistentFlags().String(FlagKey, "", "identify secure client using this TLS key file")
	cmd.PersistentFlags().String(FlagCACert, "", "verify certificates of TLS-enabled secure servers using this CA bundle")
	cmd.PersistentFlags().String(FlagAuthJWTToken, "", "JWT token used for authentication (if this option is used, --user and --password should not be set)")
	cmd.PersistentFlags().String(FlagUser, "", "username[:password] for authentication (prompt if password is not supplied)")
	cmd.PersistentFlags().String(FlagPassword, "", "password for authentication (if this option is used, --user option shouldn't include password)")
	cmd.PersistentFlags().StringP(FlagDiscoverySRV, "d", "", "domain name to query for SRV records describing cluster endpoints")
	cmd.PersistentFlags().String(FlagDiscoverySRVName, "", "service name to query when using DNS discovery")
}

type ClientFactory func(clientv3.Config) (*clientv3.Client, error)

type clientFactoryKey struct{}

// WithClientFactory attaches a custom client factory to the provided context.
// Tests can inject fakes without mutating the global client constructor.
func WithClientFactory(ctx context.Context, factory ClientFactory) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if factory == nil {
		return ctx
	}
	return context.WithValue(ctx, clientFactoryKey{}, factory)
}

func WithClient(ctx context.Context, cli *clientv3.Client) context.Context {
	if cli == nil {
		return ctx
	}
	return WithClientFactory(ctx, func(clientv3.Config) (*clientv3.Client, error) {
		return cli, nil
	})
}

func clientFactoryFromCmd(cmd *cobra.Command) ClientFactory {
	if cmd != nil {
		if ctx := cmd.Context(); ctx != nil {
			if factory, ok := ctx.Value(clientFactoryKey{}).(ClientFactory); ok && factory != nil {
				return factory
			}
		}
	}
	return newClientFunc
}

func initDisplayFromCmd(cmd *cobra.Command) {
	isHex, err := cmd.Flags().GetBool(FlagHex)
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitError, err)
	}
	outputType, err := cmd.Flags().GetString(FlagWriteOut)
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitError, err)
	}
	if display = NewPrinter(outputType, isHex); display == nil {
		cobrautl.ExitWithError(cobrautl.ExitBadFeature, errors.New("unsupported output format"))
	}
}

type discardValue struct{}

func (*discardValue) String() string   { return "" }
func (*discardValue) Set(string) error { return nil }
func (*discardValue) Type() string     { return "" }

func clientConfigFromCmd(cmd *cobra.Command) *clientv3.ConfigSpec {
	lg, err := logutil.CreateDefaultZapLogger(zap.InfoLevel)
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitError, err)
	}
	fs := cmd.InheritedFlags()
	if strings.HasPrefix(cmd.Use, "watch") {
		// silence "pkg/flags: unrecognized environment variable ETCDCTL_WATCH_KEY=foo" warnings
		// silence "pkg/flags: unrecognized environment variable ETCDCTL_WATCH_RANGE_END=bar" warnings
		fs.AddFlag(&pflag.Flag{Name: "watch-key", Value: &discardValue{}})
		fs.AddFlag(&pflag.Flag{Name: "watch-range-end", Value: &discardValue{}})
	}
	flags.SetPflagsFromEnv(lg, "ETCDCTL", fs)

	debug, err := cmd.Flags().GetBool(FlagDebug)
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitError, err)
	}
	if debug {
		grpclog.SetLoggerV2(grpclog.NewLoggerV2WithVerbosity(os.Stderr, os.Stderr, os.Stderr, 4))
		fs.VisitAll(func(f *pflag.Flag) {
			fmt.Fprintf(os.Stderr, "%s=%v\n", flags.FlagToEnv("ETCDCTL", f.Name), f.Value)
		})
	} else {
		// WARNING logs contain important information like TLS misconfirugation, but spams
		// too many routine connection disconnects to turn on by default.
		//
		// See https://github.com/etcd-io/etcd/pull/9623 for background
		grpclog.SetLoggerV2(grpclog.NewLoggerV2(io.Discard, io.Discard, os.Stderr))
	}

	cfg := &clientv3.ConfigSpec{}
	cfg.Endpoints, err = endpointsFromCmd(cmd)
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitError, err)
	}

	cfg.DialTimeout = dialTimeoutFromCmd(cmd)
	cfg.KeepAliveTime = keepAliveTimeFromCmd(cmd)
	cfg.KeepAliveTimeout = keepAliveTimeoutFromCmd(cmd)
	cfg.MaxCallSendMsgSize = maxCallSendMsgSizeFromCmd(cmd)
	cfg.MaxCallRecvMsgSize = maxCallRecvMsgSizeFromCmd(cmd)

	cfg.Secure = secureCfgFromCmd(cmd)
	cfg.Auth = authCfgFromCmd(cmd)

	initDisplayFromCmd(cmd)
	return cfg
}

func mustClientCfgFromCmd(cmd *cobra.Command) *clientv3.Config {
	cc := clientConfigFromCmd(cmd)
	lg, _ := logutil.CreateDefaultZapLogger(zap.InfoLevel)
	cfg, err := clientv3.NewClientConfig(cc, lg)
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitBadArgs, err)
	}
	return cfg
}

func mustClientFromCmd(cmd *cobra.Command) *clientv3.Client {
	cfg := clientConfigFromCmd(cmd)
	return mustClientWithFactory(cmd, cfg)
}

func mustClient(cc *clientv3.ConfigSpec) *clientv3.Client {
	return mustClientWithFactory(nil, cc)
}

func mustClientWithFactory(cmd *cobra.Command, cc *clientv3.ConfigSpec) *clientv3.Client {
	lg, _ := logutil.CreateDefaultZapLogger(zap.InfoLevel)
	cfg, err := clientv3.NewClientConfig(cc, lg)
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitBadArgs, err)
	}

	client, err := clientFactoryFromCmd(cmd)(*cfg)
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitBadConnection, err)
	}

	return client
}

func argOrStdin(args []string, stdin io.Reader, i int) (string, error) {
	if i < len(args) {
		return args[i], nil
	}
	bytes, err := io.ReadAll(stdin)
	if string(bytes) == "" || err != nil {
		return "", errors.New("no available argument and stdin")
	}
	return string(bytes), nil
}

func dialTimeoutFromCmd(cmd *cobra.Command) time.Duration {
	dialTimeout, err := cmd.Flags().GetDuration(FlagDialTimeout)
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitError, err)
	}
	return dialTimeout
}

func keepAliveTimeFromCmd(cmd *cobra.Command) time.Duration {
	keepAliveTime, err := cmd.Flags().GetDuration(FlagKeepAliveTime)
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitError, err)
	}
	return keepAliveTime
}

func keepAliveTimeoutFromCmd(cmd *cobra.Command) time.Duration {
	keepAliveTimeout, err := cmd.Flags().GetDuration(FlagKeepAliveTimeout)
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitError, err)
	}
	return keepAliveTimeout
}

func maxCallSendMsgSizeFromCmd(cmd *cobra.Command) int {
	maxRequestBytes, err := cmd.Flags().GetInt(FlagMaxRequestBytes)
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitError, err)
	}
	return maxRequestBytes
}

func maxCallRecvMsgSizeFromCmd(cmd *cobra.Command) int {
	maxReceiveBytes, err := cmd.Flags().GetInt(FlagMaxRecvBytes)
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitError, err)
	}
	return maxReceiveBytes
}

func secureCfgFromCmd(cmd *cobra.Command) *clientv3.SecureConfig {
	cert, key, cacert := keyAndCertFromCmd(cmd)
	insecureTr := insecureTransportFromCmd(cmd)
	skipVerify := insecureSkipVerifyFromCmd(cmd)
	discoveryCfg := discoveryCfgFromCmd(cmd)

	if discoveryCfg.insecure {
		discoveryCfg.domain = ""
	}

	return &clientv3.SecureConfig{
		Cert:       cert,
		Key:        key,
		Cacert:     cacert,
		ServerName: discoveryCfg.domain,

		InsecureTransport:  insecureTr,
		InsecureSkipVerify: skipVerify,
	}
}

func insecureTransportFromCmd(cmd *cobra.Command) bool {
	insecureTr, err := cmd.Flags().GetBool(FlagInsecureTransport)
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitError, err)
	}
	return insecureTr
}

func insecureSkipVerifyFromCmd(cmd *cobra.Command) bool {
	skipVerify, err := cmd.Flags().GetBool(FlagInsecureSkipTLSVerify)
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitError, err)
	}
	return skipVerify
}

func keyAndCertFromCmd(cmd *cobra.Command) (cert, key, cacert string) {
	var err error
	if cert, err = cmd.Flags().GetString(FlagCert); err != nil {
		cobrautl.ExitWithError(cobrautl.ExitBadArgs, err)
	} else if cert == "" && cmd.Flags().Changed(FlagCert) {
		cobrautl.ExitWithError(cobrautl.ExitBadArgs, errors.New("empty string is passed to --cert option"))
	}

	if key, err = cmd.Flags().GetString(FlagKey); err != nil {
		cobrautl.ExitWithError(cobrautl.ExitBadArgs, err)
	} else if key == "" && cmd.Flags().Changed(FlagKey) {
		cobrautl.ExitWithError(cobrautl.ExitBadArgs, errors.New("empty string is passed to --key option"))
	}

	if cacert, err = cmd.Flags().GetString(FlagCACert); err != nil {
		cobrautl.ExitWithError(cobrautl.ExitBadArgs, err)
	} else if cacert == "" && cmd.Flags().Changed(FlagCACert) {
		cobrautl.ExitWithError(cobrautl.ExitBadArgs, errors.New("empty string is passed to --cacert option"))
	}

	return cert, key, cacert
}

func authCfgFromCmd(cmd *cobra.Command) *clientv3.AuthConfig {
	userFlag, err := cmd.Flags().GetString(FlagUser)
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitBadArgs, err)
	}
	passwordFlag, err := cmd.Flags().GetString(FlagPassword)
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitBadArgs, err)
	}
	tokenFlag, err := cmd.Flags().GetString(FlagAuthJWTToken)
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitBadArgs, err)
	}

	if userFlag == "" && tokenFlag == "" {
		return nil
	}

	var cfg clientv3.AuthConfig

	if tokenFlag != "" {
		cfg.Token = tokenFlag
		return &cfg
	}

	if passwordFlag == "" {
		splitted := strings.SplitN(userFlag, ":", 2)
		if len(splitted) < 2 {
			cfg.Username = userFlag
			cfg.Password, err = speakeasy.Ask("Password: ")
			if err != nil {
				cobrautl.ExitWithError(cobrautl.ExitError, err)
			}
		} else {
			cfg.Username = splitted[0]
			cfg.Password = splitted[1]
		}
	} else {
		cfg.Username = userFlag
		cfg.Password = passwordFlag
	}

	return &cfg
}

func insecureDiscoveryFromCmd(cmd *cobra.Command) bool {
	discovery, err := cmd.Flags().GetBool(FlagInsecureDiscovery)
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitError, err)
	}
	return discovery
}

func discoverySrvFromCmd(cmd *cobra.Command) string {
	domainStr, err := cmd.Flags().GetString(FlagDiscoverySRV)
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitBadArgs, err)
	}
	return domainStr
}

func discoveryDNSClusterServiceNameFromCmd(cmd *cobra.Command) string {
	serviceNameStr, err := cmd.Flags().GetString(FlagDiscoverySRVName)
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitBadArgs, err)
	}
	return serviceNameStr
}

func discoveryCfgFromCmd(cmd *cobra.Command) *discoveryCfg {
	return &discoveryCfg{
		domain:      discoverySrvFromCmd(cmd),
		insecure:    insecureDiscoveryFromCmd(cmd),
		serviceName: discoveryDNSClusterServiceNameFromCmd(cmd),
	}
}

func endpointsFromCmd(cmd *cobra.Command) ([]string, error) {
	eps, err := endpointsFromFlagValue(cmd)
	if err != nil {
		return nil, err
	}
	// If domain discovery returns no endpoints, check endpoints flag
	if len(eps) == 0 {
		eps, err = cmd.Flags().GetStringSlice(FlagEndpoints)
		if err == nil {
			for i, ip := range eps {
				eps[i] = strings.TrimSpace(ip)
			}
		}
	}
	return eps, err
}

func endpointsFromFlagValue(cmd *cobra.Command) ([]string, error) {
	discoveryCfg := discoveryCfgFromCmd(cmd)

	// If we still don't have domain discovery, return nothing
	if discoveryCfg.domain == "" {
		return []string{}, nil
	}

	srvs, err := srv.GetClient("etcd-client", discoveryCfg.domain, discoveryCfg.serviceName)
	if err != nil {
		return nil, err
	}
	eps := srvs.Endpoints
	if discoveryCfg.insecure {
		return eps, err
	}
	// strip insecure connections
	var ret []string
	for _, ep := range eps {
		if strings.HasPrefix(ep, "http://") {
			fmt.Fprintf(os.Stderr, "ignoring discovered insecure endpoint %q\n", ep)
			continue
		}
		ret = append(ret, ep)
	}
	return ret, err
}
