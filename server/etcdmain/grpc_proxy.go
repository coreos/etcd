// Copyright 2016 The etcd Authors
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

package etcdmain

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/grpc/metadata"

	grpc_prometheus "github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus"
	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/soheilhy/cmux"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"go.uber.org/zap/zapgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/grpclog"
	"google.golang.org/grpc/keepalive"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/client/pkg/v3/logutil"
	"go.etcd.io/etcd/client/pkg/v3/tlsutil"
	"go.etcd.io/etcd/client/pkg/v3/transport"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/leasing"
	"go.etcd.io/etcd/client/v3/namespace"
	"go.etcd.io/etcd/client/v3/ordering"
	"go.etcd.io/etcd/pkg/v3/debugutil"
	"go.etcd.io/etcd/server/v3/embed"
	"go.etcd.io/etcd/server/v3/etcdserver/api/v3election/v3electionpb"
	"go.etcd.io/etcd/server/v3/etcdserver/api/v3lock/v3lockpb"
	"go.etcd.io/etcd/server/v3/proxy/grpcproxy"
)

var (
	grpcProxyListenAddr                string
	grpcProxyMetricsListenAddr         string
	grpcProxyEndpoints                 []string
	grpcProxyEndpointsAutoSyncInterval time.Duration
	grpcProxyDialKeepAliveTime         time.Duration
	grpcProxyDialKeepAliveTimeout      time.Duration
	grpcProxyPermitWithoutStream       bool
	grpcProxyDNSCluster                string
	grpcProxyDNSClusterServiceName     string
	grpcProxyInsecureDiscovery         bool
	grpcProxyDataDir                   string
	grpcMaxCallSendMsgSize             int
	grpcMaxCallRecvMsgSize             int

	// tls for connecting to etcd

	grpcProxyCA                    string
	grpcProxyCert                  string
	grpcProxyKey                   string
	grpcProxyInsecureSkipTLSVerify bool

	// tls for clients connecting to proxy

	grpcProxyListenCA            string
	grpcProxyListenCert          string
	grpcProxyListenKey           string
	grpcProxyListenCipherSuites  []string
	grpcProxyListenAutoTLS       bool
	grpcProxyListenCRL           string
	grpcProxyListenTLSMinVersion string
	grpcProxyListenTLSMaxVersion string

	selfSignedCertValidity uint

	grpcProxyAdvertiseClientURL string
	grpcProxyResolverPrefix     string
	grpcProxyResolverTTL        int

	grpcProxyNamespace string
	grpcProxyLeasing   string

	grpcProxyEnablePprof    bool
	grpcProxyEnableOrdering bool
	grpcProxyEnableLogging  bool

	grpcProxyDebug bool

	// GRPC keep alive related options.
	grpcKeepAliveMinTime  time.Duration
	grpcKeepAliveTimeout  time.Duration
	grpcKeepAliveInterval time.Duration

	maxConcurrentStreams uint32
)

const defaultGRPCMaxCallSendMsgSize = 1.5 * 1024 * 1024

func init() {
	rootCmd.AddCommand(newGRPCProxyCommand())
}

// newGRPCProxyCommand returns the cobra command for "grpc-proxy".
func newGRPCProxyCommand() *cobra.Command {
	lpc := &cobra.Command{
		Use:   "grpc-proxy <subcommand>",
		Short: "grpc-proxy related command",
	}
	lpc.AddCommand(newGRPCProxyStartCommand())

	return lpc
}

func newGRPCProxyStartCommand() *cobra.Command {
	cmd := cobra.Command{
		Use:   "start",
		Short: "start the grpc proxy",
		Run:   startGRPCProxy,
	}

	cmd.Flags().StringVar(&grpcProxyListenAddr, "listen-addr", "127.0.0.1:23790", "listen address")
	cmd.Flags().StringVar(&grpcProxyDNSCluster, "discovery-srv", "", "domain name to query for SRV records describing cluster endpoints")
	cmd.Flags().StringVar(&grpcProxyDNSClusterServiceName, "discovery-srv-name", "", "service name to query when using DNS discovery")
	cmd.Flags().StringVar(&grpcProxyMetricsListenAddr, "metrics-addr", "", "listen for endpoint /metrics requests on an additional interface")
	cmd.Flags().BoolVar(&grpcProxyInsecureDiscovery, "insecure-discovery", false, "accept insecure SRV records")
	cmd.Flags().StringSliceVar(&grpcProxyEndpoints, "endpoints", []string{"127.0.0.1:2379"}, "comma separated etcd cluster endpoints")
	cmd.Flags().DurationVar(&grpcProxyEndpointsAutoSyncInterval, "endpoints-auto-sync-interval", 0, "etcd endpoints auto sync interval (disabled by default)")
	cmd.Flags().DurationVar(&grpcProxyDialKeepAliveTime, "dial-keepalive-time", 0, "keepalive time for client(grpc-proxy) connections (default 0, disable).")
	cmd.Flags().DurationVar(&grpcProxyDialKeepAliveTimeout, "dial-keepalive-timeout", embed.DefaultGRPCKeepAliveTimeout, "keepalive timeout for client(grpc-proxy) connections (default 20s).")
	cmd.Flags().BoolVar(&grpcProxyPermitWithoutStream, "permit-without-stream", false, "Enable client(grpc-proxy) to send keepalive pings even with no active RPCs.")
	cmd.Flags().StringVar(&grpcProxyAdvertiseClientURL, "advertise-client-url", "127.0.0.1:23790", "advertise address to register (must be reachable by client)")
	cmd.Flags().StringVar(&grpcProxyResolverPrefix, "resolver-prefix", "", "prefix to use for registering proxy (must be shared with other grpc-proxy members)")
	cmd.Flags().IntVar(&grpcProxyResolverTTL, "resolver-ttl", 0, "specify TTL, in seconds, when registering proxy endpoints")
	cmd.Flags().StringVar(&grpcProxyNamespace, "namespace", "", "string to prefix to all keys for namespacing requests")
	cmd.Flags().BoolVar(&grpcProxyEnablePprof, "enable-pprof", false, `Enable runtime profiling data via HTTP server. Address is at client URL + "/debug/pprof/"`)
	cmd.Flags().StringVar(&grpcProxyDataDir, "data-dir", "default.proxy", "Data directory for persistent data")
	cmd.Flags().IntVar(&grpcMaxCallSendMsgSize, "max-send-bytes", defaultGRPCMaxCallSendMsgSize, "message send limits in bytes (default value is 1.5 MiB)")
	cmd.Flags().IntVar(&grpcMaxCallRecvMsgSize, "max-recv-bytes", math.MaxInt32, "message receive limits in bytes (default value is math.MaxInt32)")
	cmd.Flags().DurationVar(&grpcKeepAliveMinTime, "grpc-keepalive-min-time", embed.DefaultGRPCKeepAliveMinTime, "Minimum interval duration that a client should wait before pinging proxy.")
	cmd.Flags().DurationVar(&grpcKeepAliveInterval, "grpc-keepalive-interval", embed.DefaultGRPCKeepAliveInterval, "Frequency duration of server-to-client ping to check if a connection is alive (0 to disable).")
	cmd.Flags().DurationVar(&grpcKeepAliveTimeout, "grpc-keepalive-timeout", embed.DefaultGRPCKeepAliveTimeout, "Additional duration of wait before closing a non-responsive connection (0 to disable).")

	// client TLS for connecting to server
	cmd.Flags().StringVar(&grpcProxyCert, "cert", "", "identify secure connections with etcd servers using this TLS certificate file")
	cmd.Flags().StringVar(&grpcProxyKey, "key", "", "identify secure connections with etcd servers using this TLS key file")
	cmd.Flags().StringVar(&grpcProxyCA, "cacert", "", "verify certificates of TLS-enabled secure etcd servers using this CA bundle")
	cmd.Flags().BoolVar(&grpcProxyInsecureSkipTLSVerify, "insecure-skip-tls-verify", false, "skip authentication of etcd server TLS certificates (CAUTION: this option should be enabled only for testing purposes)")

	// client TLS for connecting to proxy
	cmd.Flags().StringVar(&grpcProxyListenCert, "cert-file", "", "identify secure connections to the proxy using this TLS certificate file")
	cmd.Flags().StringVar(&grpcProxyListenKey, "key-file", "", "identify secure connections to the proxy using this TLS key file")
	cmd.Flags().StringVar(&grpcProxyListenCA, "trusted-ca-file", "", "verify certificates of TLS-enabled secure proxy using this CA bundle")
	cmd.Flags().StringSliceVar(&grpcProxyListenCipherSuites, "listen-cipher-suites", grpcProxyListenCipherSuites, "Comma-separated list of supported TLS cipher suites between client/proxy (empty will be auto-populated by Go).")
	cmd.Flags().BoolVar(&grpcProxyListenAutoTLS, "auto-tls", false, "proxy TLS using generated certificates")
	cmd.Flags().StringVar(&grpcProxyListenCRL, "client-crl-file", "", "proxy client certificate revocation list file.")
	cmd.Flags().UintVar(&selfSignedCertValidity, "self-signed-cert-validity", 1, "The validity period of the proxy certificates, unit is year")
	cmd.Flags().StringVar(&grpcProxyListenTLSMinVersion, "tls-min-version", string(tlsutil.TLSVersion12), "Minimum TLS version supported by grpc proxy. Possible values: TLS1.2, TLS1.3.")
	cmd.Flags().StringVar(&grpcProxyListenTLSMaxVersion, "tls-max-version", string(tlsutil.TLSVersionDefault), "Maximum TLS version supported by grpc proxy. Possible values: TLS1.2, TLS1.3 (empty defers to Go).")

	// experimental flags
	cmd.Flags().BoolVar(&grpcProxyEnableOrdering, "experimental-serializable-ordering", false, "Ensure serializable reads have monotonically increasing store revisions across endpoints.")
	cmd.Flags().StringVar(&grpcProxyLeasing, "experimental-leasing-prefix", "", "leasing metadata prefix for disconnected linearized reads.")
	cmd.Flags().BoolVar(&grpcProxyEnableLogging, "experimental-enable-grpc-logging", false, "logging all grpc requests and responses")

	cmd.Flags().BoolVar(&grpcProxyDebug, "debug", false, "Enable debug-level logging for grpc-proxy.")

	cmd.Flags().Uint32Var(&maxConcurrentStreams, "max-concurrent-streams", math.MaxUint32, "Maximum concurrent streams that each client can open at a time.")

	return &cmd
}

func startGRPCProxy(cmd *cobra.Command, args []string) {
	checkArgs()
	lvl := zap.InfoLevel
	if grpcProxyDebug {
		lvl = zap.DebugLevel
		grpc.EnableTracing = true
	}
	lg, err := logutil.CreateDefaultZapLogger(lvl)
	if err != nil {
		panic(err)
	}
	defer lg.Sync()

	grpclog.SetLoggerV2(zapgrpc.NewLogger(lg))

	// The proxy itself (ListenCert) can have not-empty CN.
	// The empty CN is required for grpcProxyCert.
	// Please see https://github.com/etcd-io/etcd/issues/11970#issuecomment-687875315  for more context.
	tlsInfo := newTLS(grpcProxyListenCA, grpcProxyListenCert, grpcProxyListenKey, false)
	if tlsInfo == nil && grpcProxyListenAutoTLS {
		host := []string{"https://" + grpcProxyListenAddr}
		dir := filepath.Join(grpcProxyDataDir, "fixtures", "proxy")
		autoTLS, err := transport.SelfCert(lg, dir, host, selfSignedCertValidity)
		if err != nil {
			log.Fatal(err)
		}
		tlsInfo = &autoTLS
	}
	if tlsInfo != nil {
		if len(grpcProxyListenCipherSuites) > 0 {
			cs, err := tlsutil.GetCipherSuites(grpcProxyListenCipherSuites)
			if err != nil {
				log.Fatal(err)
			}
			tlsInfo.CipherSuites = cs
		}
		if grpcProxyListenTLSMinVersion != "" {
			version, err := tlsutil.GetTLSVersion(grpcProxyListenTLSMinVersion)
			if err != nil {
				log.Fatal(err)
			}
			tlsInfo.MinVersion = version
		}
		if grpcProxyListenTLSMaxVersion != "" {
			version, err := tlsutil.GetTLSVersion(grpcProxyListenTLSMaxVersion)
			if err != nil {
				log.Fatal(err)
			}
			tlsInfo.MaxVersion = version
		}

		lg.Info("gRPC proxy server TLS", zap.String("tls-info", fmt.Sprintf("%+v", tlsInfo)))
	}

	m := mustListenCMux(lg, tlsInfo)
	grpcl := m.Match(cmux.HTTP2())
	httpl := mustMatchHTTPListener(m, tlsInfo)
	defer func() {
		grpcl.Close()
		lg.Info("stop listening gRPC proxy client requests", zap.String("address", grpcProxyListenAddr))
	}()

	client := mustNewClient(lg)
	grpcServer := newGRPCProxyServer(lg, client)

	errc := make(chan error, 3)

	// NOTE:
	// Start gRPC + cmux before creating proxyClient.
	//
	// proxyClient dials the proxy endpoint with a 5-second timeout. If cmux is not
	// serving yet, the self-dial can time out because the gRPC path is not being
	// accepted/dispatched.
	//
	// It is safe to start cmux before the HTTP server goroutine: HTTP has already
	// been matched/registered with cmux, so accepted HTTP connections are queued
	// and served once http.Serve starts.
	startServe(errc, func() error { return grpcServer.Serve(grpcl) })
	startServe(errc, m.Serve)

	// The proxy client is used for self-healthchecking.
	// TODO: The mechanism should be refactored to use internal connection.
	//
	// Create it after gRPC/cmux serving goroutines have started
	proxyClient := newProxyHealthClient(lg, tlsInfo)

	httpClient := mustNewHTTPClient()
	srvhttp := mustHTTPServer(lg, tlsInfo, httpClient, client, proxyClient)

	startServe(errc, func() error { return srvhttp.Serve(httpl) })

	maybeServeMetrics(lg, tlsInfo, httpClient, client, proxyClient)

	lg.Info("started gRPC proxy", zap.String("address", grpcProxyListenAddr))

	// grpc-proxy is initialized, ready to serve
	notifySystemd(lg)

	fmt.Fprintln(os.Stderr, <-errc)
	os.Exit(1)
}

func checkArgs() {
	if grpcProxyResolverPrefix != "" && grpcProxyResolverTTL < 1 {
		fmt.Fprintln(os.Stderr, fmt.Errorf("invalid resolver-ttl %d", grpcProxyResolverTTL))
		os.Exit(1)
	}
	if grpcProxyResolverPrefix == "" && grpcProxyResolverTTL > 0 {
		fmt.Fprintln(os.Stderr, fmt.Errorf("invalid resolver-prefix %q", grpcProxyResolverPrefix))
		os.Exit(1)
	}
	if grpcProxyResolverPrefix != "" && grpcProxyResolverTTL > 0 && grpcProxyAdvertiseClientURL == "" {
		fmt.Fprintln(os.Stderr, fmt.Errorf("invalid advertise-client-url %q", grpcProxyAdvertiseClientURL))
		os.Exit(1)
	}
	if grpcProxyListenAutoTLS && selfSignedCertValidity == 0 {
		fmt.Fprintln(os.Stderr, fmt.Errorf("selfSignedCertValidity is invalid,it should be greater than 0"))
		os.Exit(1)
	}

	minVersion, err := tlsutil.GetTLSVersion(grpcProxyListenTLSMinVersion)
	if err != nil {
		fmt.Fprintln(os.Stderr, fmt.Errorf("tls-min-version is invalid: %w", err))
		os.Exit(1)
	}
	maxVersion, err := tlsutil.GetTLSVersion(grpcProxyListenTLSMaxVersion)
	if err != nil {
		fmt.Fprintln(os.Stderr, fmt.Errorf("tls-max-version is invalid: %w", err))
		os.Exit(1)
	}

	// maxVersion == 0 means that Go selects the highest available version.
	if maxVersion != 0 && minVersion > maxVersion {
		fmt.Fprintln(os.Stderr, fmt.Errorf("min version (%s) is greater than max version (%s)", grpcProxyListenTLSMinVersion, grpcProxyListenTLSMaxVersion))
		os.Exit(1)
	}

	// Check if user attempted to configure ciphers for TLS1.3 only: Go does not support that currently.
	if minVersion == tls.VersionTLS13 && len(grpcProxyListenCipherSuites) > 0 {
		fmt.Fprintln(os.Stderr, fmt.Errorf("cipher suites cannot be configured when only TLS1.3 is enabled"))
		os.Exit(1)
	}
}

func mustNewClient(lg *zap.Logger) *clientv3.Client {
	srvs := discoverEndpoints(lg, grpcProxyDNSCluster, grpcProxyCA, grpcProxyInsecureDiscovery, grpcProxyDNSClusterServiceName)
	eps := srvs.Endpoints
	if len(eps) == 0 {
		eps = grpcProxyEndpoints
	}
	cfg, err := newClientCfg(lg, eps)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	cfg.DialOptions = append(cfg.DialOptions,
		grpc.WithUnaryInterceptor(grpcproxy.AuthUnaryClientInterceptor))
	cfg.DialOptions = append(cfg.DialOptions,
		grpc.WithStreamInterceptor(grpcproxy.AuthStreamClientInterceptor))
	cfg.Logger = lg.Named("client")
	client, err := clientv3.New(*cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	return client
}

func mustNewProxyClient(lg *zap.Logger, tls *transport.TLSInfo) *clientv3.Client {
	eps := []string{grpcProxyAdvertiseClientURL}
	cfg, err := newProxyClientCfg(lg.Named("client"), eps, tls)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	client, err := clientv3.New(*cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	lg.Info("create proxy client", zap.String("grpcProxyAdvertiseClientURL", grpcProxyAdvertiseClientURL))
	return client
}

func newProxyHealthClient(lg *zap.Logger, tls *transport.TLSInfo) *clientv3.Client {
	if grpcProxyAdvertiseClientURL == "" {
		return nil
	}
	return mustNewProxyClient(lg, tls)
}

func newProxyClientCfg(lg *zap.Logger, eps []string, tls *transport.TLSInfo) (*clientv3.Config, error) {
	cfg := clientv3.Config{
		Endpoints:   eps,
		DialTimeout: 5 * time.Second,
		Logger:      lg,
	}
	if tls != nil {
		clientTLS, err := tls.ClientConfig()
		if err != nil {
			return nil, err
		}
		cfg.TLS = clientTLS
	}
	return &cfg, nil
}

func newClientCfg(lg *zap.Logger, eps []string) (*clientv3.Config, error) {
	// set tls if any one tls option set
	cfg := clientv3.Config{
		Endpoints:        eps,
		AutoSyncInterval: grpcProxyEndpointsAutoSyncInterval,
		DialTimeout:      5 * time.Second,
	}

	if grpcMaxCallSendMsgSize > 0 {
		cfg.MaxCallSendMsgSize = grpcMaxCallSendMsgSize
	}
	if grpcMaxCallRecvMsgSize > 0 {
		cfg.MaxCallRecvMsgSize = grpcMaxCallRecvMsgSize
	}
	if grpcProxyDialKeepAliveTime > 0 {
		cfg.DialKeepAliveTime = grpcProxyDialKeepAliveTime
	}
	if grpcProxyDialKeepAliveTimeout > 0 {
		cfg.DialKeepAliveTimeout = grpcProxyDialKeepAliveTimeout
	}
	cfg.PermitWithoutStream = grpcProxyPermitWithoutStream

	tls := newTLS(grpcProxyCA, grpcProxyCert, grpcProxyKey, true)
	if tls == nil && grpcProxyInsecureSkipTLSVerify {
		tls = &transport.TLSInfo{}
	}
	if tls != nil {
		clientTLS, err := tls.ClientConfig()
		if err != nil {
			return nil, err
		}
		clientTLS.InsecureSkipVerify = grpcProxyInsecureSkipTLSVerify
		if clientTLS.InsecureSkipVerify {
			lg.Warn("--insecure-skip-tls-verify was given, this grpc proxy process skips authentication of etcd server TLS certificates. This option should be enabled only for testing purposes.")
		}
		cfg.TLS = clientTLS
		lg.Info("gRPC proxy client TLS", zap.String("tls-info", fmt.Sprintf("%+v", tls)))
	}
	return &cfg, nil
}

func contextPropagationUnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			ctx = metadata.NewOutgoingContext(ctx, md)
		}
		return handler(ctx, req)
	}
}

func newTLS(ca, cert, key string, requireEmptyCN bool) *transport.TLSInfo {
	if ca == "" && cert == "" && key == "" {
		return nil
	}
	return &transport.TLSInfo{TrustedCAFile: ca, CertFile: cert, KeyFile: key, EmptyCN: requireEmptyCN}
}

func mustListenCMux(lg *zap.Logger, tlsinfo *transport.TLSInfo) cmux.CMux {
	l, err := net.Listen("tcp", grpcProxyListenAddr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if l, err = transport.NewKeepAliveListener(l, "tcp", nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if tlsinfo != nil {
		tlsinfo.CRLFile = grpcProxyListenCRL
		if l, err = transport.NewTLSListener(l, tlsinfo); err != nil {
			lg.Fatal("failed to create TLS listener", zap.Error(err))
		}
	}

	lg.Info("listening for gRPC proxy client requests", zap.String("address", grpcProxyListenAddr))
	return cmux.New(l)
}

func newGRPCProxyServer(lg *zap.Logger, client *clientv3.Client) *grpc.Server {
	if grpcProxyEnableOrdering {
		vf := ordering.NewOrderViolationSwitchEndpointClosure(client)
		client.KV = ordering.NewKV(client.KV, vf)
		lg.Info("waiting for linearized read from cluster to recover ordering")
		for {
			_, err := client.KV.Get(context.TODO(), "_", clientv3.WithKeysOnly())
			if err == nil {
				break
			}
			lg.Warn("ordering recovery failed, retrying in 1s", zap.Error(err))
			time.Sleep(time.Second)
		}
	}

	if len(grpcProxyNamespace) > 0 {
		client.KV = namespace.NewKV(client.KV, grpcProxyNamespace)
		client.Watcher = namespace.NewWatcher(client.Watcher, grpcProxyNamespace)
		client.Lease = namespace.NewLease(client.Lease, grpcProxyNamespace)
	}

	if len(grpcProxyLeasing) > 0 {
		client.KV, _, _ = leasing.NewKV(client, grpcProxyLeasing)
	}

	kvp, _ := grpcproxy.NewKvProxy(client)
	watchp, _ := grpcproxy.NewWatchProxy(client.Ctx(), lg, client)
	if grpcProxyResolverPrefix != "" {
		grpcproxy.Register(lg, client, grpcProxyResolverPrefix, grpcProxyAdvertiseClientURL, grpcProxyResolverTTL)
	}
	clusterp, _ := grpcproxy.NewClusterProxy(lg, client, grpcProxyAdvertiseClientURL, grpcProxyResolverPrefix)
	leasep, _ := grpcproxy.NewLeaseProxy(client.Ctx(), client)

	mainp := grpcproxy.NewMaintenanceProxy(client)
	authp := grpcproxy.NewAuthProxy(client)
	electionp := grpcproxy.NewElectionProxy(client)
	lockp := grpcproxy.NewLockProxy(client)

	serverMetrics := grpc_prometheus.NewServerMetrics()
	prometheus.MustRegister(serverMetrics)

	grpcChainStreamList := []grpc.StreamServerInterceptor{
		serverMetrics.StreamServerInterceptor(),
	}
	grpcChainUnaryList := []grpc.UnaryServerInterceptor{
		serverMetrics.UnaryServerInterceptor(),
		contextPropagationUnaryServerInterceptor(),
	}
	if grpcProxyEnableLogging {
		grpcChainStreamList = append(grpcChainStreamList,
			interceptors.StreamServerInterceptor(reportable(lg)),
		)
		grpcChainUnaryList = append(grpcChainUnaryList,
			interceptors.UnaryServerInterceptor(reportable(lg)),
		)
	}

	gopts := []grpc.ServerOption{
		grpc.ChainStreamInterceptor(grpcChainStreamList...),
		grpc.ChainUnaryInterceptor(grpcChainUnaryList...),
		grpc.MaxConcurrentStreams(math.MaxUint32),
	}
	if grpcKeepAliveMinTime > time.Duration(0) {
		gopts = append(gopts, grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             grpcKeepAliveMinTime,
			PermitWithoutStream: false,
		}))
	}
	if grpcKeepAliveInterval > time.Duration(0) ||
		grpcKeepAliveTimeout > time.Duration(0) {
		gopts = append(gopts, grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    grpcKeepAliveInterval,
			Timeout: grpcKeepAliveTimeout,
		}))
	}

	server := grpc.NewServer(gopts...)

	pb.RegisterKVServer(server, kvp)
	pb.RegisterWatchServer(server, watchp)
	pb.RegisterClusterServer(server, clusterp)
	pb.RegisterLeaseServer(server, leasep)
	pb.RegisterMaintenanceServer(server, mainp)
	pb.RegisterAuthServer(server, authp)
	v3electionpb.RegisterElectionServer(server, electionp)
	v3lockpb.RegisterLockServer(server, lockp)

	return server
}

func mustMatchHTTPListener(m cmux.CMux, tlsinfo *transport.TLSInfo) net.Listener {
	if tlsinfo == nil {
		return m.Match(cmux.HTTP1())
	}
	return m.Match(cmux.Any())
}

func mustHTTPServer(lg *zap.Logger, tlsinfo *transport.TLSInfo, httpClient *http.Client, c *clientv3.Client, proxyClient *clientv3.Client) *http.Server {
	httpmux := http.NewServeMux()
	httpmux.HandleFunc("/", http.NotFound)
	grpcproxy.HandleMetrics(httpmux, httpClient, c.Endpoints())
	grpcproxy.HandleHealth(lg, httpmux, c)
	grpcproxy.HandleProxyMetrics(httpmux)
	grpcproxy.HandleProxyHealth(lg, httpmux, proxyClient)
	if grpcProxyEnablePprof {
		for p, h := range debugutil.PProfHandlers() {
			httpmux.Handle(p, h)
		}
		lg.Info("gRPC proxy enabled pprof", zap.String("path", debugutil.HTTPPrefixPProf))
	}
	srvhttp := &http.Server{
		Handler:  httpmux,
		ErrorLog: log.New(io.Discard, "net/http", 0),
	}

	// net/http replaces any MaxConcurrentStreams above math.MaxInt32 with its
	// default (250), so clamp the uint32 value (default math.MaxUint32) to keep
	// it effectively unlimited.
	srvhttp.HTTP2 = &http.HTTP2Config{
		MaxConcurrentStreams: int(min(maxConcurrentStreams, math.MaxInt32)),
	}

	if tlsinfo == nil {
		return srvhttp
	}

	srvTLS, err := tlsinfo.ServerConfig()
	if err != nil {
		lg.Fatal("failed to set up TLS", zap.Error(err))
	}
	srvhttp.TLSConfig = srvTLS
	return srvhttp
}

func maybeServeMetrics(lg *zap.Logger, tlsinfo *transport.TLSInfo, httpClient *http.Client, c *clientv3.Client, proxyClient *clientv3.Client) {
	if len(grpcProxyMetricsListenAddr) == 0 {
		return
	}
	mhttpl := mustMetricsListener(lg, tlsinfo)
	go func() {
		mux := http.NewServeMux()
		grpcproxy.HandleMetrics(mux, httpClient, c.Endpoints())
		grpcproxy.HandleHealth(lg, mux, c)
		grpcproxy.HandleProxyMetrics(mux)
		grpcproxy.HandleProxyHealth(lg, mux, proxyClient)
		lg.Info("gRPC proxy server metrics URL serving")
		herr := http.Serve(mhttpl, mux)
		if herr != nil {
			lg.Fatal("gRPC proxy server metrics URL returned", zap.Error(herr))
		} else {
			lg.Info("gRPC proxy server metrics URL returned")
		}
	}()
}

func startServe(errc chan<- error, serve func() error) {
	go func() { errc <- serve() }()
}

func mustNewHTTPClient() *http.Client {
	transport, err := newHTTPTransport(grpcProxyCA, grpcProxyCert, grpcProxyKey)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	return &http.Client{Transport: transport}
}

func newHTTPTransport(ca, cert, key string) (*http.Transport, error) {
	tr := &http.Transport{}

	if ca != "" && cert != "" && key != "" {
		caCert, err := os.ReadFile(ca)
		if err != nil {
			return nil, err
		}
		keyPair, err := tls.LoadX509KeyPair(cert, key)
		if err != nil {
			return nil, err
		}
		caPool := x509.NewCertPool()
		caPool.AppendCertsFromPEM(caCert)

		tlsConfig := &tls.Config{
			Certificates: []tls.Certificate{keyPair},
			RootCAs:      caPool,
		}
		tr.TLSClientConfig = tlsConfig
	} else if grpcProxyInsecureSkipTLSVerify {
		tlsConfig := &tls.Config{InsecureSkipVerify: grpcProxyInsecureSkipTLSVerify}
		tr.TLSClientConfig = tlsConfig
	}
	return tr, nil
}

func mustMetricsListener(lg *zap.Logger, tlsinfo *transport.TLSInfo) net.Listener {
	murl, err := url.Parse(grpcProxyMetricsListenAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot parse %q", grpcProxyMetricsListenAddr)
		os.Exit(1)
	}
	ml, err := transport.NewListener(murl.Host, murl.Scheme, tlsinfo)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	lg.Info("gRPC proxy listening for metrics", zap.String("address", murl.String()))
	return ml
}
