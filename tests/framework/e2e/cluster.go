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

package e2e

import (
	"context"
	"flag"
	"fmt"
	"maps"
	"net/url"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Masterminds/semver/v3"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/pkg/v3/featuregate"
	"go.etcd.io/etcd/pkg/v3/proxy"
	"go.etcd.io/etcd/server/v3/embed"
	"go.etcd.io/etcd/server/v3/etcdserver"
	"go.etcd.io/etcd/tests/v3/framework/config"
)

const EtcdProcessBasePort = 20000

type ClientConnType int

const (
	ClientNonTLS ClientConnType = iota
	ClientTLS
	ClientTLSAndNonTLS
)

type ClientConfig struct {
	ConnectionType ClientConnType
	CertAuthority  bool
	AutoTLS        bool
	RevokeCerts    bool
	DialTimeout    time.Duration
}

// allow alphanumerics, underscores and dashes
var testNameCleanRegex = regexp.MustCompile(`[^a-zA-Z0-9 \-_]+`)

func NewConfigNoTLS() *EtcdProcessClusterConfig {
	return DefaultConfig()
}

func NewConfigAutoTLS() *EtcdProcessClusterConfig {
	return NewConfig(
		WithIsPeerTLS(true),
		WithIsPeerAutoTLS(true),
	)
}

func NewConfigTLS() *EtcdProcessClusterConfig {
	return NewConfig(
		WithClientConnType(ClientTLS),
		WithIsPeerTLS(true),
	)
}

func NewConfigClientTLS() *EtcdProcessClusterConfig {
	return NewConfig(WithClientConnType(ClientTLS))
}

func NewConfigClientAutoTLS() *EtcdProcessClusterConfig {
	return NewConfig(
		WithClusterSize(1),
		WithClientAutoTLS(true),
		WithClientConnType(ClientTLS),
	)
}

func NewConfigPeerTLS() *EtcdProcessClusterConfig {
	return NewConfig(
		WithIsPeerTLS(true),
	)
}

func NewConfigClientTLSCertAuth() *EtcdProcessClusterConfig {
	return NewConfig(
		WithClusterSize(1),
		WithClientConnType(ClientTLS),
		WithClientCertAuthority(true),
	)
}

func NewConfigClientTLSCertAuthWithNoCN() *EtcdProcessClusterConfig {
	return NewConfig(
		WithClusterSize(1),
		WithClientConnType(ClientTLS),
		WithClientCertAuthority(true),
		WithCN(false),
	)
}

func NewConfigJWT() *EtcdProcessClusterConfig {
	return NewConfig(
		WithClusterSize(1),
		WithAuthTokenOpts("jwt,pub-key="+path.Join(FixturesDir, "server.crt")+
			",priv-key="+path.Join(FixturesDir, "server.key.insecure")+",sign-method=RS256,ttl=5s"),
	)
}

func ConfigStandalone(cfg EtcdProcessClusterConfig) *EtcdProcessClusterConfig {
	ret := cfg
	ret.ClusterSize = 1
	return &ret
}

type EtcdProcessCluster struct {
	lg      *zap.Logger
	Cfg     *EtcdProcessClusterConfig
	Procs   []EtcdProcess
	nextSeq int // sequence number of the next etcd process (if it will be required)
}

type EtcdProcessClusterConfig struct {
	ServerConfig embed.Config

	// Test config

	KeepDataDir         bool
	Logger              *zap.Logger
	GoFailEnabled       bool
	GoFailClientTimeout time.Duration
	LazyFSEnabled       bool
	PeerProxy           bool

	// Process config

	EnvVars map[string]string
	Version ClusterVersion

	// Cluster setup config

	ClusterSize int
	// InitialLeaderIndex makes sure the leader is the ith proc
	// when the cluster starts if it is specified (>=0).
	InitialLeaderIndex int
	RollingStart       bool
	// BaseDataDirPath specifies the data-dir for the members. If test cases
	// do not specify `BaseDataDirPath`, then e2e framework creates a
	// temporary directory for each member; otherwise, it creates a
	// subdirectory (e.g. member-0, member-1 and member-2) under the given
	// `BaseDataDirPath` for each member.
	BaseDataDirPath string

	// Dynamic per member configuration

	BasePeerScheme     string
	BasePort           int
	BaseClientScheme   string
	MetricsURLScheme   string
	Client             ClientConfig
	ClientHTTPSeparate bool
	IsPeerTLS          bool
	IsPeerAutoTLS      bool
	CN                 bool
}

func DefaultConfig() *EtcdProcessClusterConfig {
	cfg := &EtcdProcessClusterConfig{
		ClusterSize:        3,
		CN:                 true,
		InitialLeaderIndex: -1,
		ServerConfig:       *embed.NewConfig(),
	}
	cfg.ServerConfig.InitialClusterToken = "new"
	return cfg
}

func NewConfig(opts ...EPClusterOption) *EtcdProcessClusterConfig {
	c := DefaultConfig()
	for _, opt := range opts {
		opt(c)
	}
	return c
}

type EPClusterOption func(*EtcdProcessClusterConfig)

func WithConfig(cfg *EtcdProcessClusterConfig) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { *c = *cfg }
}

func WithVersion(version ClusterVersion) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.Version = version }
}

func WithInitialLeaderIndex(i int) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.InitialLeaderIndex = i }
}

func WithDataDirPath(path string) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.BaseDataDirPath = path }
}

func WithKeepDataDir(keep bool) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.KeepDataDir = keep }
}

func WithSnapshotCount(count uint64) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.ServerConfig.SnapshotCount = count }
}

func WithSnapshotCatchUpEntries(count uint64) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) {
		c.ServerConfig.SnapshotCatchUpEntries = count
	}
}

func WithClusterSize(size int) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.ClusterSize = size }
}

func WithBasePeerScheme(scheme string) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.BasePeerScheme = scheme }
}

func WithBasePort(port int) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.BasePort = port }
}

func WithBaseClientScheme(scheme string) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.BaseClientScheme = scheme }
}

func WithClientConnType(clientConnType ClientConnType) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.Client.ConnectionType = clientConnType }
}

func WithClientCertAuthority(enabled bool) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.Client.CertAuthority = enabled }
}

func WithIsPeerTLS(isPeerTLS bool) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.IsPeerTLS = isPeerTLS }
}

func WithIsPeerAutoTLS(isPeerAutoTLS bool) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.IsPeerAutoTLS = isPeerAutoTLS }
}

func WithClientAutoTLS(isClientAutoTLS bool) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.Client.AutoTLS = isClientAutoTLS }
}

func WithClientRevokeCerts(isClientCRL bool) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.Client.RevokeCerts = isClientCRL }
}

func WithCN(cn bool) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.CN = cn }
}

func WithQuotaBackendBytes(bytes int64) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.ServerConfig.QuotaBackendBytes = bytes }
}

func WithStrictReconfigCheck(strict bool) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.ServerConfig.StrictReconfigCheck = strict }
}

func WithAuthTokenOpts(token string) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.ServerConfig.AuthToken = token }
}

func WithAuthTokenTTL(ttl uint) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.ServerConfig.AuthTokenTTL = ttl }
}

func WithRollingStart(rolling bool) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.RollingStart = rolling }
}

func WithDiscoveryEndpoints(endpoints []string) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.ServerConfig.DiscoveryCfg.Endpoints = endpoints }
}

func WithDiscoveryToken(token string) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.ServerConfig.DiscoveryCfg.Token = token }
}

func WithLogLevel(level string) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.ServerConfig.LogLevel = level }
}

func WithCorruptCheckTime(time time.Duration) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.ServerConfig.CorruptCheckTime = time }
}

func WithInitialClusterToken(token string) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.ServerConfig.InitialClusterToken = token }
}

func WithInitialCorruptCheck(enabled bool) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) {
		c.ServerConfig.ServerFeatureGate.(featuregate.MutableFeatureGate).Set(fmt.Sprintf("InitialCorruptCheck=%t", enabled))
	}
}

func WithCompactHashCheckEnabled(enabled bool) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) {
		c.ServerConfig.ServerFeatureGate.(featuregate.MutableFeatureGate).Set(fmt.Sprintf("CompactHashCheck=%t", enabled))
	}
}

func WithCompactHashCheckTime(time time.Duration) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.ServerConfig.CompactHashCheckTime = time }
}

func WithGoFailEnabled(enabled bool) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.GoFailEnabled = enabled }
}

func WithGoFailClientTimeout(dur time.Duration) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.GoFailClientTimeout = dur }
}

func WithLazyFSEnabled(enabled bool) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.LazyFSEnabled = enabled }
}

func WithWarningUnaryRequestDuration(time time.Duration) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.ServerConfig.WarningUnaryRequestDuration = time }
}

func WithServerFeatureGate(featureName string, val bool) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) {
		if err := c.ServerConfig.ServerFeatureGate.(featuregate.MutableFeatureGate).Set(fmt.Sprintf("%s=%v", featureName, val)); err != nil {
			panic(err)
		}
	}
}

func WithCompactionBatchLimit(limit int) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.ServerConfig.CompactionBatchLimit = limit }
}

func WithCompactionSleepInterval(time time.Duration) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.ServerConfig.CompactionSleepInterval = time }
}

func WithWatchProcessNotifyInterval(interval time.Duration) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.ServerConfig.WatchProgressNotifyInterval = interval }
}

func WithEnvVars(ev map[string]string) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.EnvVars = ev }
}

func WithPeerProxy(enabled bool) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.PeerProxy = enabled }
}

func WithClientHTTPSeparate(enabled bool) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.ClientHTTPSeparate = enabled }
}

func WithForceNewCluster(enabled bool) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.ServerConfig.ForceNewCluster = enabled }
}

func WithMetricsURLScheme(scheme string) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.MetricsURLScheme = scheme }
}

func WithCipherSuites(suites []string) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.ServerConfig.CipherSuites = suites }
}

func WithExtensiveMetrics() EPClusterOption {
	return func(c *EtcdProcessClusterConfig) { c.ServerConfig.Metrics = "extensive" }
}

func WithEnableDistributedTracing(addr string) EPClusterOption {
	return func(c *EtcdProcessClusterConfig) {
		c.ServerConfig.EnableDistributedTracing = true
		c.ServerConfig.DistributedTracingServiceName = "etcd"
		c.ServerConfig.DistributedTracingAddress = addr
		c.ServerConfig.DistributedTracingSamplingRatePerMillion = 1_000_000
	}
}

// NewEtcdProcessCluster launches a new cluster from etcd processes, returning
// a new EtcdProcessCluster once all nodes are ready to accept client requests.
func NewEtcdProcessCluster(ctx context.Context, tb testing.TB, opts ...EPClusterOption) (*EtcdProcessCluster, error) {
	cfg := NewConfig(opts...)
	epc, err := InitEtcdProcessCluster(tb, cfg)
	if err != nil {
		return nil, err
	}

	return StartEtcdProcessCluster(ctx, tb, epc, cfg)
}

// InitEtcdProcessCluster initializes a new cluster based on the given config.
// It doesn't start the cluster.
func InitEtcdProcessCluster(tb testing.TB, cfg *EtcdProcessClusterConfig) (*EtcdProcessCluster, error) {
	SkipInShortMode(tb)

	if cfg.Logger == nil {
		cfg.Logger = zaptest.NewLogger(tb)
	}
	if cfg.BasePort == 0 {
		cfg.BasePort = EtcdProcessBasePort
	}
	if cfg.ServerConfig.SnapshotCount == 0 {
		cfg.ServerConfig.SnapshotCount = etcdserver.DefaultSnapshotCount
	}

	// validate SnapshotCatchUpEntries could be set for at least one member
	if cfg.ServerConfig.SnapshotCatchUpEntries != etcdserver.DefaultSnapshotCatchUpEntries {
		if !CouldSetSnapshotCatchupEntries(BinPath.Etcd) {
			return nil, fmt.Errorf("cannot set SnapshotCatchUpEntries for current etcd version: %s", BinPath.Etcd)
		}
		if cfg.Version == LastVersion && !CouldSetSnapshotCatchupEntries(BinPath.EtcdLastRelease) {
			return nil, fmt.Errorf("cannot set SnapshotCatchUpEntries for last etcd version: %s", BinPath.EtcdLastRelease)
		}
	}

	etcdCfgs := cfg.EtcdAllServerProcessConfigs(tb)
	epc := &EtcdProcessCluster{
		Cfg:     cfg,
		lg:      zaptest.NewLogger(tb),
		Procs:   make([]EtcdProcess, cfg.ClusterSize),
		nextSeq: cfg.ClusterSize,
	}

	// launch etcd processes
	for i := range etcdCfgs {
		proc, err := NewEtcdProcess(tb, etcdCfgs[i])
		if err != nil {
			epc.Close()
			return nil, fmt.Errorf("cannot configure: %w", err)
		}
		epc.Procs[i] = proc
	}

	return epc, nil
}

// StartEtcdProcessCluster launches a new cluster from etcd processes.
func StartEtcdProcessCluster(ctx context.Context, tb testing.TB, epc *EtcdProcessCluster, cfg *EtcdProcessClusterConfig) (*EtcdProcessCluster, error) {
	if cfg.RollingStart {
		if err := epc.RollingStart(ctx); err != nil {
			return nil, fmt.Errorf("cannot rolling-start: %w", err)
		}
	} else {
		if err := epc.Start(ctx); err != nil {
			return nil, fmt.Errorf("cannot start: %w", err)
		}
	}

	for _, proc := range epc.Procs {
		if cfg.GoFailEnabled && !proc.Failpoints().Enabled() {
			epc.Close()
			tb.Skip("please run 'make gofail-enable && make build' before running the test")
		}
	}
	if cfg.InitialLeaderIndex >= 0 {
		if err := epc.MoveLeader(ctx, tb, cfg.InitialLeaderIndex); err != nil {
			return nil, fmt.Errorf("failed to move leader: %w", err)
		}
	}
	return epc, nil
}

func (cfg *EtcdProcessClusterConfig) ClientScheme() string {
	return setupScheme(cfg.BaseClientScheme, cfg.Client.ConnectionType == ClientTLS)
}

func (cfg *EtcdProcessClusterConfig) PeerScheme() string {
	return setupScheme(cfg.BasePeerScheme, cfg.IsPeerTLS)
}

func (cfg *EtcdProcessClusterConfig) EtcdAllServerProcessConfigs(tb testing.TB) []*EtcdServerProcessConfig {
	etcdCfgs := make([]*EtcdServerProcessConfig, cfg.ClusterSize)
	initialCluster := make([]string, cfg.ClusterSize)

	for i := 0; i < cfg.ClusterSize; i++ {
		etcdCfgs[i] = cfg.EtcdServerProcessConfig(tb, i)
		initialCluster[i] = fmt.Sprintf("%s=%s", etcdCfgs[i].Name, etcdCfgs[i].PeerURL.String())
	}

	for i := range etcdCfgs {
		cfg.SetInitialOrDiscovery(etcdCfgs[i], initialCluster, "new")
	}

	return etcdCfgs
}

func (cfg *EtcdProcessClusterConfig) SetInitialOrDiscovery(serverCfg *EtcdServerProcessConfig, initialCluster []string, initialClusterState string) {
	if len(cfg.ServerConfig.DiscoveryCfg.Endpoints) == 0 {
		serverCfg.InitialCluster = strings.Join(initialCluster, ",")
		serverCfg.Args = append(serverCfg.Args, "--initial-cluster="+serverCfg.InitialCluster)
		serverCfg.Args = append(serverCfg.Args, "--initial-cluster-state="+initialClusterState)
	}

	if len(cfg.ServerConfig.DiscoveryCfg.Endpoints) > 0 {
		serverCfg.Args = append(serverCfg.Args, fmt.Sprintf("--discovery-token=%s", cfg.ServerConfig.DiscoveryCfg.Token))
		serverCfg.Args = append(serverCfg.Args, fmt.Sprintf("--discovery-endpoints=%s", strings.Join(cfg.ServerConfig.DiscoveryCfg.Endpoints, ",")))
	}
}

func (cfg *EtcdProcessClusterConfig) EtcdServerProcessConfig(tb testing.TB, i int) *EtcdServerProcessConfig {
	var curls []string
	var curl string
	port := cfg.BasePort + 5*i
	clientPort := port
	peerPort := port + 1
	metricsPort := port + 2
	peer2Port := port + 3
	clientHTTPPort := port + 4

	if cfg.Client.ConnectionType == ClientTLSAndNonTLS {
		curl = clientURL(cfg.ClientScheme(), clientPort, ClientNonTLS)
		curls = []string{curl, clientURL(cfg.ClientScheme(), clientPort, ClientTLS)}
	} else {
		curl = clientURL(cfg.ClientScheme(), clientPort, cfg.Client.ConnectionType)
		curls = []string{curl}
	}

	peerListenURL := url.URL{Scheme: cfg.PeerScheme(), Host: fmt.Sprintf("localhost:%d", peerPort)}
	peerAdvertiseURL := url.URL{Scheme: cfg.PeerScheme(), Host: fmt.Sprintf("localhost:%d", peerPort)}
	var proxyCfg *proxy.ServerConfig
	if cfg.PeerProxy {
		if !cfg.IsPeerTLS {
			panic("Can't use peer proxy without peer TLS as it can result in malformed packets")
		}
		peerAdvertiseURL.Host = fmt.Sprintf("localhost:%d", peer2Port)
		proxyCfg = &proxy.ServerConfig{
			Logger: zap.NewNop(),
			To:     peerListenURL,
			From:   peerAdvertiseURL,
		}
	}

	name := fmt.Sprintf("%s-test-%d", testNameCleanRegex.ReplaceAllString(tb.Name(), ""), i)

	var dataDirPath string
	if cfg.BaseDataDirPath == "" {
		dataDirPath = tb.TempDir()
	} else {
		// When test cases specify the BaseDataDirPath and there are more than
		// one member in the cluster, we need to create a subdirectory for
		// each member to avoid conflict.
		// We also create a subdirectory for one-member cluster, because we
		// support dynamically adding new member.
		dataDirPath = filepath.Join(cfg.BaseDataDirPath, fmt.Sprintf("member-%d", i))
	}

	args := []string{
		"--name=" + name,
		"--listen-client-urls=" + strings.Join(curls, ","),
		"--advertise-client-urls=" + strings.Join(curls, ","),
		"--listen-peer-urls=" + peerListenURL.String(),
		"--initial-advertise-peer-urls=" + peerAdvertiseURL.String(),
		"--initial-cluster-token=" + cfg.ServerConfig.InitialClusterToken,
		"--data-dir=" + dataDirPath,
		"--snapshot-count=" + fmt.Sprintf("%d", cfg.ServerConfig.SnapshotCount),
	}
	var clientHTTPURL string
	if cfg.ClientHTTPSeparate {
		clientHTTPURL = clientURL(cfg.ClientScheme(), clientHTTPPort, cfg.Client.ConnectionType)
		args = append(args, "--listen-client-http-urls="+clientHTTPURL)
	}

	if cfg.ServerConfig.ForceNewCluster {
		args = append(args, "--force-new-cluster=true")
	}
	if cfg.ServerConfig.QuotaBackendBytes > 0 {
		args = append(args,
			"--quota-backend-bytes="+fmt.Sprintf("%d", cfg.ServerConfig.QuotaBackendBytes),
		)
	}
	if !cfg.ServerConfig.StrictReconfigCheck {
		args = append(args, "--strict-reconfig-check=false")
	}
	if cfg.ServerConfig.EnableDistributedTracing {
		args = append(args,
			"--enable-distributed-tracing",
			fmt.Sprintf("--distributed-tracing-address=%s", cfg.ServerConfig.DistributedTracingAddress),
			fmt.Sprintf("--distributed-tracing-service-name=%s", cfg.ServerConfig.DistributedTracingServiceName),
			fmt.Sprintf("--distributed-tracing-sampling-rate=%d", cfg.ServerConfig.DistributedTracingSamplingRatePerMillion),
		)
	}

	var murl string
	if cfg.MetricsURLScheme != "" {
		murl = (&url.URL{
			Scheme: cfg.MetricsURLScheme,
			Host:   fmt.Sprintf("localhost:%d", metricsPort),
		}).String()
		args = append(args, "--listen-metrics-urls="+murl)
	}

	args = append(args, cfg.TLSArgs()...)

	execPath := cfg.binaryPath(i)

	if cfg.ServerConfig.SnapshotCatchUpEntries != etcdserver.DefaultSnapshotCatchUpEntries {
		if !IsSnapshotCatchupEntriesFlagAvailable(execPath) {
			cfg.ServerConfig.SnapshotCatchUpEntries = etcdserver.DefaultSnapshotCatchUpEntries
		}
	}

	var (
		binaryVersion *semver.Version
		err           error
	)
	if execPath != "" {
		binaryVersion, err = GetVersionFromBinary(execPath)
		if err != nil {
			tb.Logf("Failed to get binary version from %s: %v", execPath, err)
		}
	}
	defaultValues := values(*embed.NewConfig())
	overrideValues := values(cfg.ServerConfig)
	for flag, value := range overrideValues {
		if defaultValue := defaultValues[flag]; value == "" || value == defaultValue {
			continue
		}
		if strings.HasSuffix(flag, "snapshot-catchup-entries") && !CouldSetSnapshotCatchupEntries(execPath) {
			continue
		}
		args = append(args, convertFlag(flag, value, binaryVersion))
	}
	envVars := map[string]string{}
	maps.Copy(envVars, cfg.EnvVars)
	var gofailPort int
	if cfg.GoFailEnabled {
		gofailPort = (i+1)*10000 + 2381
		envVars["GOFAIL_HTTP"] = fmt.Sprintf("127.0.0.1:%d", gofailPort)
	}

	return &EtcdServerProcessConfig{
		lg:                  cfg.Logger,
		ExecPath:            execPath,
		Args:                args,
		EnvVars:             envVars,
		TLSArgs:             cfg.TLSArgs(),
		Client:              cfg.Client,
		DataDirPath:         dataDirPath,
		KeepDataDir:         cfg.KeepDataDir,
		Name:                name,
		PeerURL:             peerAdvertiseURL,
		ClientURL:           curl,
		ClientHTTPURL:       clientHTTPURL,
		MetricsURL:          murl,
		InitialToken:        cfg.ServerConfig.InitialClusterToken,
		GoFailPort:          gofailPort,
		GoFailClientTimeout: cfg.GoFailClientTimeout,
		Proxy:               proxyCfg,
		LazyFSEnabled:       cfg.LazyFSEnabled,
	}
}

func (cfg *EtcdProcessClusterConfig) binaryPath(i int) string {
	var execPath string
	switch cfg.Version {
	case CurrentVersion:
		execPath = BinPath.Etcd
	case MinorityLastVersion:
		if i <= cfg.ClusterSize/2 {
			execPath = BinPath.Etcd
		} else {
			execPath = BinPath.EtcdLastRelease
		}
	case QuorumLastVersion:
		if i <= cfg.ClusterSize/2 {
			execPath = BinPath.EtcdLastRelease
		} else {
			execPath = BinPath.Etcd
		}
	case LastVersion:
		execPath = BinPath.EtcdLastRelease
	default:
		panic(fmt.Sprintf("Unknown cluster version %v", cfg.Version))
	}

	return execPath
}

func (epc *EtcdProcessCluster) MinServerVersion() (*semver.Version, error) {
	var minVersion *semver.Version
	for _, member := range epc.Procs {
		ver, err := GetVersionFromBinary(member.Config().ExecPath)
		if err != nil {
			return nil, fmt.Errorf("failed to get version from member %s binary: %w", member.Config().Name, err)
		}

		if minVersion == nil || ver.LessThan(minVersion) {
			minVersion = ver
		}
	}
	return minVersion, nil
}

func values(cfg embed.Config) map[string]string {
	// AddFlags registers many flags as fs.XxxVar(&cfg.Field, name, <constant default>, ...), which
	// resets cfg.Field to that constant, so read the configured values before calling it.
	configured := configuredScalarValues(cfg)
	fs := flag.NewFlagSet("etcd", flag.ContinueOnError)
	cfg.AddFlags(fs)
	values := map[string]string{}
	fs.VisitAll(func(f *flag.Flag) {
		value := f.Value.String()
		if v, ok := configured[f.Name]; ok {
			value = v
		}
		if value == "false" || value == "0" {
			value = ""
		}
		values[f.Name] = value
	})
	return values
}

// configuredScalarValues returns the string, bool, numeric and duration fields of cfg keyed by
// their json tag, which matches the name of the corresponding etcd flag.
func configuredScalarValues(cfg embed.Config) map[string]string {
	result := map[string]string{}
	rv := reflect.ValueOf(cfg)
	rt := rv.Type()
	for i := range rt.NumField() {
		field := rt.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if !field.IsExported() || name == "" || name == "-" {
			continue
		}
		switch rv.Field(i).Kind() {
		case reflect.String, reflect.Bool,
			reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
			reflect.Float32, reflect.Float64:
			result[name] = fmt.Sprint(rv.Field(i).Interface())
		}
	}
	return result
}

func clientURL(scheme string, port int, connType ClientConnType) string {
	curlHost := fmt.Sprintf("localhost:%d", port)
	switch connType {
	case ClientNonTLS:
		return (&url.URL{Scheme: scheme, Host: curlHost}).String()
	case ClientTLS:
		return (&url.URL{Scheme: ToTLS(scheme), Host: curlHost}).String()
	default:
		panic(fmt.Sprintf("Unsupported connection type %v", connType))
	}
}

func (cfg *EtcdProcessClusterConfig) TLSArgs() (args []string) {
	if cfg.Client.ConnectionType != ClientNonTLS {
		if cfg.Client.AutoTLS {
			args = append(args, "--auto-tls")
		} else {
			tlsClientArgs := []string{
				"--cert-file", CertPath,
				"--key-file", PrivateKeyPath,
				"--trusted-ca-file", CaPath,
			}
			args = append(args, tlsClientArgs...)

			if cfg.Client.CertAuthority {
				args = append(args, "--client-cert-auth")
			}
		}
	}

	if cfg.IsPeerTLS {
		if cfg.IsPeerAutoTLS {
			args = append(args, "--peer-auto-tls")
		} else {
			tlsPeerArgs := []string{
				"--peer-cert-file", CertPath,
				"--peer-key-file", PrivateKeyPath,
				"--peer-trusted-ca-file", CaPath,
			}
			args = append(args, tlsPeerArgs...)
		}
	}

	if cfg.Client.RevokeCerts {
		args = append(args, "--client-crl-file", CrlPath, "--client-cert-auth")
	}

	if len(cfg.ServerConfig.CipherSuites) > 0 {
		args = append(args, "--cipher-suites", strings.Join(cfg.ServerConfig.CipherSuites, ","))
	}

	return args
}

func (epc *EtcdProcessCluster) EndpointsGRPC() []string {
	return epc.Endpoints(func(ep EtcdProcess) []string { return ep.EndpointsGRPC() })
}

func (epc *EtcdProcessCluster) EndpointsHTTP() []string {
	return epc.Endpoints(func(ep EtcdProcess) []string { return ep.EndpointsHTTP() })
}

func (epc *EtcdProcessCluster) Endpoints(f func(ep EtcdProcess) []string) (ret []string) {
	for _, p := range epc.Procs {
		ret = append(ret, f(p)...)
	}
	return ret
}

func (epc *EtcdProcessCluster) CloseProc(ctx context.Context, finder func(EtcdProcess) bool, opts ...config.ClientOption) error {
	procIndex := -1
	if finder != nil {
		for i := range epc.Procs {
			if finder(epc.Procs[i]) {
				procIndex = i
				break
			}
		}
	} else {
		procIndex = len(epc.Procs) - 1
	}

	if procIndex == -1 {
		return fmt.Errorf("no process found to stop")
	}

	proc := epc.Procs[procIndex]
	epc.Procs = append(epc.Procs[:procIndex], epc.Procs[procIndex+1:]...)

	if proc == nil {
		return nil
	}

	// First remove member from the cluster

	memberCtl := epc.Etcdctl(opts...)
	memberList, err := memberCtl.MemberList(ctx, false)
	if err != nil {
		return fmt.Errorf("failed to get member list: %w", err)
	}

	memberID, err := findMemberIDByEndpoint(memberList.Members, proc.Config().ClientURL)
	if err != nil {
		return fmt.Errorf("failed to find member ID: %w", err)
	}

	sleepDuration := 500 * time.Millisecond
	maxRetries := int((2 * etcdserver.HealthInterval) / sleepDuration)
	memberRemoved := false
	for i := 0; i < maxRetries; i++ {
		_, err := memberCtl.MemberRemove(ctx, memberID)
		if err != nil && strings.Contains(err.Error(), "member not found") {
			memberRemoved = true
			break
		}

		time.Sleep(sleepDuration)
	}

	if !memberRemoved {
		return fmt.Errorf("failed to remove member after %d tries", maxRetries)
	}

	epc.lg.Info("successfully removed member", zap.String("acurl", proc.Config().ClientURL))

	// Then stop process
	return proc.Close()
}

// StartNewProc grows cluster size by one with two phases
// Phase 1 - Inform cluster of new configuration
// Phase 2 - Start new member
func (epc *EtcdProcessCluster) StartNewProc(ctx context.Context, cfg *EtcdProcessClusterConfig, tb testing.TB, addAsLearner bool, opts ...config.ClientOption) (memberID uint64, err error) {
	memberID, serverCfg, err := epc.AddMember(ctx, cfg, tb, addAsLearner, opts...)
	if err != nil {
		return 0, err
	}

	// Then start process
	if err = epc.StartNewProcFromConfig(ctx, tb, serverCfg); err != nil {
		return 0, err
	}

	return memberID, nil
}

// AddMember adds a new member to the cluster without starting it.
func (epc *EtcdProcessCluster) AddMember(ctx context.Context, cfg *EtcdProcessClusterConfig, tb testing.TB, addAsLearner bool, opts ...config.ClientOption) (memberID uint64, serverCfg *EtcdServerProcessConfig, err error) {
	if cfg != nil {
		serverCfg = cfg.EtcdServerProcessConfig(tb, epc.nextSeq)
	} else {
		serverCfg = epc.Cfg.EtcdServerProcessConfig(tb, epc.nextSeq)
	}

	epc.nextSeq++

	initialCluster := []string{
		fmt.Sprintf("%s=%s", serverCfg.Name, serverCfg.PeerURL.String()),
	}
	for _, p := range epc.Procs {
		initialCluster = append(initialCluster, fmt.Sprintf("%s=%s", p.Config().Name, p.Config().PeerURL.String()))
	}

	epc.Cfg.SetInitialOrDiscovery(serverCfg, initialCluster, "existing")

	// First add new member to cluster
	tb.Logf("add new member to cluster; member-name %s, member-peer-url %s", serverCfg.Name, serverCfg.PeerURL.String())
	memberCtl := epc.Etcdctl(opts...)
	var resp *clientv3.MemberAddResponse
	if addAsLearner {
		resp, err = memberCtl.MemberAddAsLearner(ctx, serverCfg.Name, []string{serverCfg.PeerURL.String()})
	} else {
		resp, err = memberCtl.MemberAdd(ctx, serverCfg.Name, []string{serverCfg.PeerURL.String()})
	}
	if err != nil {
		return 0, nil, fmt.Errorf("failed to add new member: %w", err)
	}

	return resp.Member.ID, serverCfg, nil
}

// StartNewProcFromConfig starts a new member process from the given config.
func (epc *EtcdProcessCluster) StartNewProcFromConfig(ctx context.Context, tb testing.TB, serverCfg *EtcdServerProcessConfig) error {
	tb.Log("start new member")
	proc, err := NewEtcdProcess(tb, serverCfg)
	if err != nil {
		epc.Close()
		return fmt.Errorf("cannot configure: %w", err)
	}

	epc.Procs = append(epc.Procs, proc)

	return proc.Start(ctx)
}

// UpdateProcOptions updates the options for a specific process. If no opt is set, then the config is identical
// to the cluster.
func (epc *EtcdProcessCluster) UpdateProcOptions(i int, tb testing.TB, opts ...EPClusterOption) error {
	if epc.Procs[i].IsRunning() {
		return fmt.Errorf("process %d is still running, please close it before updating its options", i)
	}
	cfg := *epc.Cfg
	for _, opt := range opts {
		opt(&cfg)
	}
	serverCfg := cfg.EtcdServerProcessConfig(tb, i)

	var initialCluster []string
	for _, p := range epc.Procs {
		initialCluster = append(initialCluster, fmt.Sprintf("%s=%s", p.Config().Name, p.Config().PeerURL.String()))
	}
	epc.Cfg.SetInitialOrDiscovery(serverCfg, initialCluster, "new")

	proc, err := NewEtcdProcess(tb, serverCfg)
	if err != nil {
		return err
	}
	epc.Procs[i] = proc
	return nil
}

func PatchArgs(args []string, flag, newValue string) error {
	for i, arg := range args {
		if strings.Contains(arg, flag) {
			args[i] = fmt.Sprintf("--%s=%s", flag, newValue)
			return nil
		}
	}
	return fmt.Errorf("--%s flag not found", flag)
}

func (epc *EtcdProcessCluster) Start(ctx context.Context) error {
	return epc.start(func(ep EtcdProcess) error { return ep.Start(ctx) })
}

func (epc *EtcdProcessCluster) RollingStart(ctx context.Context) error {
	return epc.rollingStart(func(ep EtcdProcess) error { return ep.Start(ctx) })
}

func (epc *EtcdProcessCluster) Restart(ctx context.Context) error {
	return epc.start(func(ep EtcdProcess) error { return ep.Restart(ctx) })
}

func (epc *EtcdProcessCluster) start(f func(ep EtcdProcess) error) error {
	readyC := make(chan error, len(epc.Procs))
	for i := range epc.Procs {
		go func(n int) { readyC <- f(epc.Procs[n]) }(i)
	}
	for range epc.Procs {
		if err := <-readyC; err != nil {
			epc.Close()
			return err
		}
	}
	return nil
}

func (epc *EtcdProcessCluster) rollingStart(f func(ep EtcdProcess) error) error {
	readyC := make(chan error, len(epc.Procs))
	for i := range epc.Procs {
		go func(n int) { readyC <- f(epc.Procs[n]) }(i)
		// make sure the servers do not start at the same time
		time.Sleep(time.Second)
	}
	for range epc.Procs {
		if err := <-readyC; err != nil {
			epc.Close()
			return err
		}
	}
	return nil
}

func (epc *EtcdProcessCluster) Kill() (err error) {
	for _, p := range epc.Procs {
		if p == nil {
			continue
		}
		if curErr := p.Kill(); curErr != nil {
			if err != nil {
				err = fmt.Errorf("%w; %w", err, curErr)
			} else {
				err = curErr
			}
		}
	}
	return err
}

func (epc *EtcdProcessCluster) Wait(ctx context.Context) error {
	closedC := make(chan error, len(epc.Procs))
	for i := range epc.Procs {
		go func(n int) {
			epc.Procs[n].Wait(ctx)
			closedC <- epc.Procs[n].Wait(ctx)
		}(i)
	}
	for range epc.Procs {
		if err := <-closedC; err != nil {
			return err
		}
	}
	return nil
}

func (epc *EtcdProcessCluster) Stop() (err error) {
	for _, p := range epc.Procs {
		if p == nil {
			continue
		}
		if curErr := p.Stop(); curErr != nil {
			if err != nil {
				err = fmt.Errorf("%w; %w", err, curErr)
			} else {
				err = curErr
			}
		}
	}
	return err
}

func (epc *EtcdProcessCluster) ConcurrentStop() (err error) {
	errCh := make(chan error, len(epc.Procs))
	for i := range epc.Procs {
		if epc.Procs[i] == nil {
			errCh <- nil
			continue
		}
		go func(n int) { errCh <- epc.Procs[n].Stop() }(i)
	}

	for range epc.Procs {
		if curErr := <-errCh; curErr != nil {
			if err != nil {
				err = fmt.Errorf("%w; %w", err, curErr)
			} else {
				err = curErr
			}
		}
	}
	close(errCh)
	return err
}

func (epc *EtcdProcessCluster) Etcdctl(opts ...config.ClientOption) *EtcdctlV3 {
	etcdctl, err := NewEtcdctl(epc.Cfg.Client, epc.EndpointsGRPC(), opts...)
	if err != nil {
		panic(err)
	}
	return etcdctl
}

func (epc *EtcdProcessCluster) Close() error {
	epc.lg.Info("closing test cluster...")
	err := epc.Stop()
	for _, p := range epc.Procs {
		// p is nil when NewEtcdProcess fails in the middle
		// Close still gets called to clean up test data
		if p == nil {
			continue
		}
		if cerr := p.Close(); cerr != nil {
			err = cerr
		}
	}
	epc.lg.Info("closed test cluster.")
	return err
}

func findMemberIDByEndpoint(members []*etcdserverpb.Member, endpoint string) (uint64, error) {
	for _, m := range members {
		if m.ClientURLs[0] == endpoint {
			return m.ID, nil
		}
	}

	return 0, fmt.Errorf("member not found")
}

// WaitLeader returns index of the member in c.Members() that is leader
// or fails the test (if not established in 30s).
func (epc *EtcdProcessCluster) WaitLeader(tb testing.TB) int {
	ctx, cancel := context.WithTimeout(tb.Context(), 30*time.Second)
	defer cancel()
	return epc.WaitMembersForLeader(ctx, tb, epc.Procs)
}

// WaitMembersForLeader waits until given members agree on the same leader,
// and returns its 'index' in the 'membs' list
func (epc *EtcdProcessCluster) WaitMembersForLeader(ctx context.Context, tb testing.TB, membs []EtcdProcess) int {
	cc := epc.Etcdctl()

	// ensure leader is up via linearizable get
	for {
		select {
		case <-ctx.Done():
			tb.Fatal("WaitMembersForLeader timeout")
		default:
		}
		_, err := cc.Get(ctx, "0", config.GetOptions{Timeout: 10*config.TickDuration + time.Second})
		if err == nil || strings.Contains(err.Error(), "Key not found") {
			break
		}
		tb.Logf("WaitMembersForLeader Get err: %v", err)
	}

	leaders := make(map[uint64]struct{})
	members := make(map[uint64]int)
	for {
		select {
		case <-ctx.Done():
			tb.Fatal("WaitMembersForLeader timeout")
		default:
		}
		for i := range membs {
			if !membs[i].IsRunning() {
				// if member[i] has stopped
				continue
			}

			resp, err := membs[i].Etcdctl().Status(ctx)
			if err != nil {
				if strings.Contains(err.Error(), "connection refused") {
					// if member[i] has stopped
					continue
				}
				tb.Fatal(err)
			}
			members[resp[0].Header.MemberId] = i
			leaders[resp[0].Leader] = struct{}{}
		}
		// members agree on the same leader
		if len(leaders) == 1 {
			break
		}
		leaders = make(map[uint64]struct{})
		members = make(map[uint64]int)
		time.Sleep(10 * config.TickDuration)
	}
	for l := range leaders {
		if index, ok := members[l]; ok {
			tb.Logf("members agree on a leader, members:%v , leader:%v", members, l)
			return index
		}
		tb.Fatalf("members agree on a leader which is not one of members, members:%v , leader:%v", members, l)
	}
	tb.Fatal("impossible path of execution")
	return -1
}

// MoveLeader moves the leader to the ith process.
func (epc *EtcdProcessCluster) MoveLeader(ctx context.Context, tb testing.TB, i int) error {
	if i < 0 || i >= len(epc.Procs) {
		return fmt.Errorf("invalid index: %d, must between 0 and %d", i, len(epc.Procs)-1)
	}
	tb.Logf("moving leader to Procs[%d]", i)
	oldLeader := epc.WaitMembersForLeader(ctx, tb, epc.Procs)
	if oldLeader == i {
		tb.Logf("Procs[%d] is already the leader", i)
		return nil
	}
	resp, err := epc.Procs[i].Etcdctl().Status(ctx)
	if err != nil {
		return err
	}
	memberID := resp[0].Header.MemberId
	err = epc.Procs[oldLeader].Etcdctl().MoveLeader(ctx, memberID)
	if err != nil {
		return err
	}
	newLeader := epc.WaitMembersForLeader(ctx, tb, epc.Procs)
	if newLeader != i {
		tb.Fatalf("expect new leader to be Procs[%d] but got Procs[%d]", i, newLeader)
	}
	tb.Logf("moved leader from Procs[%d] to Procs[%d]", oldLeader, i)
	return nil
}
