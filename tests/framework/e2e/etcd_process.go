// Copyright 2017 The etcd Authors
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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Masterminds/semver/v3"
	"go.uber.org/zap"

	"go.etcd.io/etcd/api/v3/version"
	"go.etcd.io/etcd/client/pkg/v3/fileutil"
	"go.etcd.io/etcd/pkg/v3/expect"
	"go.etcd.io/etcd/pkg/v3/proxy"
	"go.etcd.io/etcd/tests/v3/framework/config"
)

var EtcdServerReadyLines = []string{"ready to serve client requests"}

// EtcdProcess is a process that serves etcd requests.
type EtcdProcess interface {
	EndpointsGRPC() []string
	EndpointsHTTP() []string
	EndpointsMetrics() []string
	Etcdctl(opts ...config.ClientOption) *EtcdctlV3

	IsRunning() bool
	Wait(ctx context.Context) error
	Start(ctx context.Context) error
	Restart(ctx context.Context) error
	Stop() error
	Close() error
	Config() *EtcdServerProcessConfig
	PeerProxy() proxy.Server
	Failpoints() *BinaryFailpoints
	LazyFS() *LazyFS
	Logs() LogsExpect
	Kill() error
	Pause() error
	Resume() error
}

type LogsExpect interface {
	ExpectWithContext(context.Context, expect.ExpectedResponse) (string, error)
	Lines() []string
	LineCount() int
}

type EtcdServerProcess struct {
	cfg        *EtcdServerProcessConfig
	proc       *expect.ExpectProcess
	proxy      proxy.Server
	lazyfs     *LazyFS
	failpoints *BinaryFailpoints
	donec      chan struct{} // closed when Interact() terminates
}

type EtcdServerProcessConfig struct {
	lg       *zap.Logger
	ExecPath string
	Args     []string
	TLSArgs  []string
	EnvVars  map[string]string

	Client      ClientConfig
	DataDirPath string
	KeepDataDir bool

	Name string

	PeerURL       url.URL
	ClientURL     string
	ClientHTTPURL string
	MetricsURL    string

	InitialToken        string
	InitialCluster      string
	GoFailPort          int
	GoFailClientTimeout time.Duration

	LazyFSEnabled bool
	Proxy         *proxy.ServerConfig
}

func NewEtcdServerProcess(tb testing.TB, cfg *EtcdServerProcessConfig) (*EtcdServerProcess, error) {
	if !fileutil.Exist(cfg.ExecPath) {
		return nil, fmt.Errorf("could not find etcd binary: %s", cfg.ExecPath)
	}
	if !cfg.KeepDataDir {
		if err := os.RemoveAll(cfg.DataDirPath); err != nil {
			return nil, err
		}
		if err := os.Mkdir(cfg.DataDirPath, 0o700); err != nil {
			return nil, err
		}
	}
	ep := &EtcdServerProcess{cfg: cfg, donec: make(chan struct{})}
	if cfg.GoFailPort != 0 {
		ep.failpoints = &BinaryFailpoints{
			member:        ep,
			clientTimeout: cfg.GoFailClientTimeout,
		}
	}
	if cfg.LazyFSEnabled {
		ep.lazyfs = newLazyFS(cfg.lg, cfg.DataDirPath, tb)
	}
	return ep, nil
}

func (ep *EtcdServerProcess) EndpointsGRPC() []string { return []string{ep.cfg.ClientURL} }
func (ep *EtcdServerProcess) EndpointsHTTP() []string {
	if ep.cfg.ClientHTTPURL == "" {
		return []string{ep.cfg.ClientURL}
	}
	return []string{ep.cfg.ClientHTTPURL}
}
func (ep *EtcdServerProcess) EndpointsMetrics() []string { return []string{ep.cfg.MetricsURL} }

func (ep *EtcdServerProcess) Etcdctl(opts ...config.ClientOption) *EtcdctlV3 {
	etcdctl, err := NewEtcdctl(ep.Config().Client, ep.EndpointsGRPC(), opts...)
	if err != nil {
		panic(err)
	}
	return etcdctl
}

func (ep *EtcdServerProcess) Start(ctx context.Context) error {
	ep.donec = make(chan struct{})
	if ep.proc != nil {
		panic("already started")
	}
	if ep.cfg.Proxy != nil && ep.proxy == nil {
		ep.cfg.lg.Info("starting proxy...", zap.String("name", ep.cfg.Name), zap.String("from", ep.cfg.Proxy.From.String()), zap.String("to", ep.cfg.Proxy.To.String()))
		ep.proxy = proxy.NewServer(*ep.cfg.Proxy)
		select {
		case <-ep.proxy.Ready():
		case err := <-ep.proxy.Error():
			return err
		}
	}
	if ep.lazyfs != nil {
		ep.cfg.lg.Info("starting lazyfs...", zap.String("name", ep.cfg.Name))
		err := ep.lazyfs.Start(ctx)
		if err != nil {
			return err
		}
	}

	ep.cfg.lg.Info("starting server...", zap.String("name", ep.cfg.Name))
	proc, err := SpawnCmdWithLogger(ep.cfg.lg, append([]string{ep.cfg.ExecPath}, ep.cfg.Args...), ep.cfg.EnvVars, ep.cfg.Name)
	if err != nil {
		return err
	}
	ep.proc = proc
	err = ep.waitReady(ctx)
	if err == nil {
		ep.cfg.lg.Info("started server.", zap.String("name", ep.cfg.Name), zap.Int("pid", ep.proc.Pid()))
	}
	return err
}

func (ep *EtcdServerProcess) Restart(ctx context.Context) error {
	ep.cfg.lg.Info("restarting server...", zap.String("name", ep.cfg.Name))
	if err := ep.Stop(); err != nil {
		return err
	}
	err := ep.Start(ctx)
	if err == nil {
		ep.cfg.lg.Info("restarted server", zap.String("name", ep.cfg.Name))
	}
	return err
}

func (ep *EtcdServerProcess) Stop() (err error) {
	if ep == nil || ep.proc == nil {
		return nil
	}

	ep.cfg.lg.Info("stopping server...", zap.String("name", ep.cfg.Name))

	defer func() {
		ep.proc = nil
	}()

	err = ep.proc.Stop()
	if err != nil {
		return err
	}
	err = ep.proc.Close()
	if err != nil && !strings.Contains(err.Error(), "unexpected exit code") {
		return err
	}
	<-ep.donec
	ep.donec = make(chan struct{})
	if ep.cfg.PeerURL.Scheme == "unix" || ep.cfg.PeerURL.Scheme == "unixs" {
		err = os.Remove(ep.cfg.PeerURL.Host + ep.cfg.PeerURL.Path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	ep.cfg.lg.Info("stopped server.", zap.String("name", ep.cfg.Name))
	if ep.proxy != nil {
		ep.cfg.lg.Info("stopping proxy...", zap.String("name", ep.cfg.Name))
		err = ep.proxy.Close()
		ep.proxy = nil
		if err != nil {
			return err
		}
	}
	if ep.lazyfs != nil {
		ep.cfg.lg.Info("stopping lazyfs...", zap.String("name", ep.cfg.Name))
		err = ep.lazyfs.Stop()
		ep.lazyfs = nil
		if err != nil {
			return err
		}
	}
	return nil
}

func (ep *EtcdServerProcess) Close() error {
	ep.cfg.lg.Info("closing server...", zap.String("name", ep.cfg.Name))
	if err := ep.Stop(); err != nil {
		return err
	}

	if !ep.cfg.KeepDataDir {
		ep.cfg.lg.Info("removing directory", zap.String("data-dir", ep.cfg.DataDirPath))
		return os.RemoveAll(ep.cfg.DataDirPath)
	}
	return nil
}

func (ep *EtcdServerProcess) waitReady(ctx context.Context) error {
	defer close(ep.donec)
	err := WaitReadyExpectProc(ctx, ep.proc, EtcdServerReadyLines)
	if err != nil {
		return fmt.Errorf("failed to find etcd ready lines %q, err: %w", EtcdServerReadyLines, err)
	}
	return nil
}

func (ep *EtcdServerProcess) Config() *EtcdServerProcessConfig { return ep.cfg }

func (ep *EtcdServerProcess) Logs() LogsExpect {
	if ep.proc == nil {
		ep.cfg.lg.Panic("Please grab logs before process is stopped")
	}
	return ep.proc
}

func (ep *EtcdServerProcess) Kill() error {
	ep.cfg.lg.Info("killing server...", zap.String("name", ep.cfg.Name))
	if ep.proc == nil {
		return nil
	}
	return ep.proc.Signal(syscall.SIGKILL)
}

func (ep *EtcdServerProcess) Pause() error {
	ep.cfg.lg.Info("Pausing server...", zap.String("name", ep.cfg.Name))
	return ep.proc.Signal(syscall.SIGSTOP)
}

func (ep *EtcdServerProcess) Resume() error {
	ep.cfg.lg.Info("Resuming server...", zap.String("name", ep.cfg.Name))
	return ep.proc.Signal(syscall.SIGCONT)
}

func (ep *EtcdServerProcess) Wait(ctx context.Context) error {
	ch := make(chan struct{})
	go func() {
		defer close(ch)
		if ep.proc != nil {
			ep.proc.Wait()

			exitCode, exitErr := ep.proc.ExitCode()

			ep.cfg.lg.Info("server exited",
				zap.String("name", ep.cfg.Name),
				zap.Int("code", exitCode),
				zap.Error(exitErr),
			)
		}
	}()
	select {
	case <-ch:
		ep.proc = nil
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (ep *EtcdServerProcess) IsRunning() bool {
	if ep.proc == nil {
		return false
	}

	exitCode, err := ep.proc.ExitCode()
	if errors.Is(err, expect.ErrProcessRunning) {
		return true
	}

	ep.cfg.lg.Info("server exited",
		zap.String("name", ep.cfg.Name),
		zap.Int("code", exitCode),
		zap.Error(err))
	ep.proc = nil
	return false
}

func AssertProcessLogs(t *testing.T, ep EtcdProcess, expectLog string) {
	t.Helper()
	var err error
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	_, err = ep.Logs().ExpectWithContext(ctx, expect.ExpectedResponse{Value: expectLog})
	if err != nil {
		t.Fatal(err)
	}
}

func (ep *EtcdServerProcess) PeerProxy() proxy.Server {
	return ep.proxy
}

func (ep *EtcdServerProcess) LazyFS() *LazyFS {
	return ep.lazyfs
}

func (ep *EtcdServerProcess) Failpoints() *BinaryFailpoints {
	return ep.failpoints
}

type BinaryFailpoints struct {
	member         EtcdProcess
	availableCache map[string]string
	clientTimeout  time.Duration
}

func (f *BinaryFailpoints) SetupEnv(failpoint, payload string) error {
	if f.member.IsRunning() {
		return errors.New("cannot setup environment variable while process is running")
	}
	f.member.Config().EnvVars["GOFAIL_FAILPOINTS"] = fmt.Sprintf("%s=%s", failpoint, payload)
	return nil
}

func (f *BinaryFailpoints) SetupHTTP(ctx context.Context, failpoint, payload string) error {
	host := fmt.Sprintf("127.0.0.1:%d", f.member.Config().GoFailPort)
	failpointURL := url.URL{
		Scheme: "http",
		Host:   host,
		Path:   failpoint,
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPut, failpointURL.String(), bytes.NewBuffer([]byte(payload)))
	if err != nil {
		return err
	}
	httpClient := http.Client{
		Timeout: 1 * time.Second,
	}
	if f.clientTimeout != 0 {
		httpClient.Timeout = f.clientTimeout
	}
	resp, err := httpClient.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		errMsg, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("bad status code: %d, err: %w", resp.StatusCode, err)
		}
		return fmt.Errorf("bad status code: %d, err: %s", resp.StatusCode, errMsg)
	}
	return nil
}

func (f *BinaryFailpoints) DeactivateHTTP(ctx context.Context, failpoint string) error {
	host := fmt.Sprintf("127.0.0.1:%d", f.member.Config().GoFailPort)
	failpointURL := url.URL{
		Scheme: "http",
		Host:   host,
		Path:   failpoint,
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodDelete, failpointURL.String(), nil)
	if err != nil {
		return err
	}
	httpClient := http.Client{
		Timeout: time.Second,
	}
	if f.clientTimeout != 0 {
		httpClient.Timeout = f.clientTimeout
	}
	resp, err := httpClient.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		errMsg, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("bad status code: %d, err: %w", resp.StatusCode, err)
		}
		return fmt.Errorf("bad status code: %d, err: %s", resp.StatusCode, errMsg)
	}
	return nil
}

func (f *BinaryFailpoints) Enabled() bool {
	_, err := failpoints(f.member)
	return err == nil
}

func (f *BinaryFailpoints) Available(failpoint string) bool {
	if f.availableCache == nil {
		fs, err := failpoints(f.member)
		if err != nil {
			panic(err)
		}
		f.availableCache = fs
	}
	_, found := f.availableCache[failpoint]
	return found
}

func failpoints(member EtcdProcess) (map[string]string, error) {
	body, err := fetchFailpointsBody(member)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return parseFailpointsBody(body)
}

func fetchFailpointsBody(member EtcdProcess) (io.ReadCloser, error) {
	address := fmt.Sprintf("127.0.0.1:%d", member.Config().GoFailPort)
	failpointURL := url.URL{
		Scheme: "http",
		Host:   address,
	}
	resp, err := http.Get(failpointURL.String())
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		errMsg, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("invalid status code: %d, err: %w", resp.StatusCode, err)
		}
		return nil, fmt.Errorf("invalid status code: %d, err:%s", resp.StatusCode, errMsg)
	}
	return resp.Body, nil
}

func parseFailpointsBody(body io.Reader) (map[string]string, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	failpoints := map[string]string{}
	for _, line := range lines {
		// Format:
		// failpoint=value
		parts := strings.SplitN(line, "=", 2)
		failpoint := parts[0]
		var value string
		if len(parts) == 2 {
			value = parts[1]
		}
		failpoints[failpoint] = value
	}
	return failpoints, nil
}

var GetVersionFromBinary = func(binaryPath string) (*semver.Version, error) {
	versionString, err := GetFullVersionFromBinary(binaryPath)
	if err != nil {
		return nil, err
	}
	version, err := semver.NewVersion(versionString)
	if err != nil {
		return nil, err
	}
	return semver.New(version.Major(), version.Minor(), version.Patch(), "", ""), nil
}

// GetFullVersionFromBinary returns the version string reported by the binary
// verbatim, including any pre-release and build metadata. Unlike
// GetVersionFromBinary it preserves values injected at build time through the
// `-X .../api/v3/version.Version` ldflag.
func GetFullVersionFromBinary(binaryPath string) (string, error) {
	if !fileutil.Exist(binaryPath) {
		return "", fmt.Errorf("binary path does not exist: %s", binaryPath)
	}
	lines, err := RunUtilCompletion([]string{binaryPath, "--version"}, nil)
	if err != nil {
		return "", fmt.Errorf("could not find binary version from %s, err: %w", binaryPath, err)
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "etcd Version:") {
			return strings.TrimSpace(strings.SplitAfter(line, ":")[1]), nil
		}
	}

	return "", fmt.Errorf("could not find version in binary output of %s, lines outputted were %v", binaryPath, lines)
}

// serverVersion is resolved lazily because BinPath is only populated once
// InitFlags has run.
var serverVersion = sync.OnceValue(func() string {
	v, err := GetFullVersionFromBinary(BinPath.Etcd)
	if err != nil {
		return version.Version
	}
	return v
})

// ServerVersion returns the version string reported by the etcd binary under
// test. It differs from version.Version compiled into the test binary whenever
// etcd is built with a custom version symbol, so e2e assertions about versions
// reported by the server must use this instead.
func ServerVersion() string {
	return serverVersion()
}

// ServerClusterVersion returns the major.minor cluster version of the etcd
// binary under test.
func ServerClusterVersion() string {
	return version.Cluster(ServerVersion())
}

// setGetVersionFromBinary changes the GetVersionFromBinary function to a mock in testing.
func setGetVersionFromBinary(tb testing.TB, f func(binaryPath string) (*semver.Version, error)) {
	origGetVersionFromBinary := GetVersionFromBinary
	GetVersionFromBinary = f
	tb.Cleanup(func() {
		GetVersionFromBinary = origGetVersionFromBinary
	})
}

func CouldSetSnapshotCatchupEntries(execPath string) bool {
	v, err := GetVersionFromBinary(execPath)
	if err != nil {
		return false
	}
	// snapshot-catchup-entries flag was backported in https://github.com/etcd-io/etcd/pull/17808
	v3_5_14 := *semver.New(3, 5, 14, "", "")
	return v.Compare(&v3_5_14) >= 0
}

func IsSnapshotCatchupEntriesFlagAvailable(execPath string) bool {
	v, err := GetVersionFromBinary(execPath)
	if err != nil {
		return false
	}
	return !v.LessThan(&version.V3_6)
}
