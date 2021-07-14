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
	"io/ioutil"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/client/pkg/v3/logutil"
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
	lruCache "go.etcd.io/etcd/v3/proxy/grpcproxy/cache"
	"go.uber.org/zap/zapgrpc"

	grpc_prometheus "github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/soheilhy/cmux"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/grpclog"
	"google.golang.org/grpc/keepalive"
)

var (
	grpcProxyListenAddr            string
	grpcProxyMetricsListenAddr     string
	grpcProxyEndpoints             []string
	grpcProxyDNSCluster            string
	grpcProxyDNSClusterServiceName string
	grpcProxyInsecureDiscovery     bool
	grpcProxyDataDir               string
	grpcMaxCallSendMsgSize         int
	grpcMaxCallRecvMsgSize         int

	// tls for connecting to etcd

	grpcProxyCA                    string
	grpcProxyCert                  string
	grpcProxyKey                   string
	grpcProxyInsecureSkipTLSVerify bool

	// tls for clients connecting to proxy

	grpcProxyListenCA      string
	grpcProxyListenCert    string
	grpcProxyListenKey     string
	grpcProxyListenAutoTLS bool
	grpcProxyListenCRL     string
	selfSignedCertValidity uint

	grpcProxyAdvertiseClientURL string
	grpcProxyResolverPrefix     string
	grpcProxyResolverTTL        int

	grpcProxyNamespace string
	grpcProxyLeasing   string

	grpcProxyEnablePprof    bool
	grpcProxyEnableOrdering bool

	grpcProxyDebug bool

	// GRPC keep alive related options.
	grpcKeepAliveMinTime  time.Duration
	grpcKeepAliveTimeout  time.Duration
	grpcKeepAliveInterval time.Duration

	grpcLruCacheTimeout time.Duration
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
	cmd.Flags().BoolVar(&grpcProxyListenAutoTLS, "auto-tls", false, "proxy TLS using generated certificates")
	cmd.Flags().StringVar(&grpcProxyListenCRL, "client-crl-file", "", "proxy client certificate revocation list file.")
	cmd.Flags().UintVar(&selfSignedCertValidity, "self-signed-cert-validity", 1, "The validity period of the proxy certificates, unit is year")

	// experimental flags
	cmd.Flags().BoolVar(&grpcProxyEnableOrdering, "experimental-serializable-ordering", false, "Ensure serializable reads have monotonically increasing store revisions across endpoints.")
	cmd.Flags().StringVar(&grpcProxyLeasing, "experimental-leasing-prefix", "", "leasing metadata prefix for disconnected linearized reads.")

	cmd.Flags().BoolVar(&grpcProxyDebug, "debug", false, "Enable debug-level logging for grpc-proxy.")

	cmd.Flags().DurationVar(&grpcLruCacheTimeout, "grpc-lrucache-timeout", embed.DefaultLRUCacheTimeout, "LRU cache key timeout.")
	return &cmd
}

func startGRPCProxy(cmd *cobra.Command, args []string) {
	checkArgs()

	lcfg := logutil.DefaultZapLoggerConfig
	if grpcProxyDebug {
		lcfg.Level = zap.NewAtomicLevelAt(zap.DebugLevel)
		grpc.EnableTracing = true
	}

	lg, err := lcfg.Build()
	if err != nil {
		log.Fatal(err)
	}
	defer lg.Sync()

	grpclog.SetLoggerV2(zapgrpc.NewLogger(lg))

	// The proxy itself (ListenCert) can have not-empty CN.
	// The empty CN is required for grpcProxyCert.
	// Please see https://github.com/etcd-io/etcd/issues/11970#issuecomment-687875315  for more context.
	tlsinfo := newTLS(grpcProxyListenCA, grpcProxyListenCert, grpcProxyListenKey, false)

	if tlsinfo == nil && grpcProxyListenAutoTLS {
		host := []string{"https://" + grpcProxyListenAddr}
		dir := filepath.Join(grpcProxyDataDir, "fixtures", "proxy")
		autoTLS, err := transport.SelfCert(lg, dir, host, selfSignedCertValidity)
		if err != nil {
			log.Fatal(err)
		}
		tlsinfo = &autoTLS
	}
	if tlsinfo != nil {
		lg.Info("gRPC proxy server TLS", zap.String("tls-info", fmt.Sprintf("%+v", tlsinfo)))
	}
	m := mustListenCMux(lg, tlsinfo)
	grpcl := m.Match(cmux.HTTP2())
	defer func() {
		grpcl.Close()
		lg.Info("stop listening gRPC proxy client requests", zap.String("address", grpcProxyListenAddr))
	}()

	client := mustNewClient(lg)

	// The proxy client is used for self-healthchecking.
	// TODO: The mechanism should be refactored to use internal connection.
	var proxyClient *clientv3.Client
	if grpcProxyAdvertiseClientURL != "" {
		proxyClient = mustNewProxyClient(lg, tlsinfo)
	}
	httpClient := mustNewHTTPClient(lg)

	if grpcLruCacheTimeout > 0 {
		lruCache.DefaultLruCacheTimeout = grpcLruCacheTimeout
	}

	srvhttp, httpl := mustHTTPListener(lg, m, tlsinfo, client, proxyClient)
	errc := make(chan error, 3)
	go func() { errc <- newGRPCProxyServer(lg, client).Serve(grpcl) }()
	go func() { errc <- srvhttp.Serve(httpl) }()
	go func() { errc <- m.Serve() }()
	if len(grpcProxyMetricsListenAddr) > 0 {
		mhttpl := mustMetricsListener(lg, tlsinfo)
		go func() {
			mux := http.NewServeMux()
			grpcproxy.HandleMetrics(mux, httpClient, client.Endpoints())
			grpcproxy.HandleHealth(lg, mux, client)
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
		Endpoints:   eps,
		DialTimeout: 5 * time.Second,
	}

	if grpcMaxCallSendMsgSize > 0 {
		cfg.MaxCallSendMsgSize = grpcMaxCallSendMsgSize
	}
	if grpcMaxCallRecvMsgSize > 0 {
		cfg.MaxCallRecvMsgSize = grpcMaxCallRecvMsgSize
	}

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

	gopts := []grpc.ServerOption{
		grpc.StreamInterceptor(grpc_prometheus.StreamServerInterceptor),
		grpc.UnaryInterceptor(grpc_prometheus.UnaryServerInterceptor),
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

func mustHTTPListener(lg *zap.Logger, m cmux.CMux, tlsinfo *transport.TLSInfo, c *clientv3.Client, proxy *clientv3.Client) (*http.Server, net.Listener) {
	httpClient := mustNewHTTPClient(lg)
	httpmux := http.NewServeMux()
	httpmux.HandleFunc("/", http.NotFound)
	grpcproxy.HandleMetrics(httpmux, httpClient, c.Endpoints())
	grpcproxy.HandleHealth(lg, httpmux, c)
	grpcproxy.HandleProxyMetrics(httpmux)
	grpcproxy.HandleProxyHealth(lg, httpmux, proxy)
	if grpcProxyEnablePprof {
		for p, h := range debugutil.PProfHandlers() {
			httpmux.Handle(p, h)
		}
		lg.Info("gRPC proxy enabled pprof", zap.String("path", debugutil.HTTPPrefixPProf))
	}
	srvhttp := &http.Server{
		Handler:  httpmux,
		ErrorLog: log.New(ioutil.Discard, "net/http", 0),
	}

	if tlsinfo == nil {
		return srvhttp, m.Match(cmux.HTTP1())
	}

	srvTLS, err := tlsinfo.ServerConfig()
	if err != nil {
		lg.Fatal("failed to set up TLS", zap.Error(err))
	}
	srvhttp.TLSConfig = srvTLS
	return srvhttp, m.Match(cmux.Any())
}

func mustNewHTTPClient(lg *zap.Logger) *http.Client {
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
		caCert, err := ioutil.ReadFile(ca)
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
		tlsConfig.BuildNameToCertificate()
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
