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

package clientv3

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	"go.etcd.io/etcd/api/v3/version"
	"go.etcd.io/etcd/client/pkg/v3/testutil"
)

func NewClient(t *testing.T, cfg Config) (*Client, error) {
	t.Helper()
	if cfg.Logger == nil {
		cfg.Logger = zaptest.NewLogger(t).Named("client")
	}
	return New(cfg)
}

func TestDialCancel(t *testing.T) {
	testutil.RegisterLeakDetection(t)

	// Start a real gRPC endpoint with health service so initial dial readiness
	// check succeeds before switching endpoints below.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	srv := grpc.NewServer()
	healthpb.RegisterHealthServer(srv, health.NewServer())
	serveDone := make(chan error)
	go func() {
		defer close(serveDone)
		srv.Serve(ln)
	}()
	defer func() {
		srv.Stop()
		<-serveDone
	}()

	ep := ln.Addr().String()
	cfg := Config{
		Endpoints:   []string{ep},
		DialTimeout: 30 * time.Second,
	}
	c, err := NewClient(t, cfg)
	require.NoError(t, err)

	// connect to ipv4 black hole so dial blocks
	c.SetEndpoints("http://254.0.0.1:12345")

	// issue Get to force redial attempts
	getc := make(chan struct{})
	go func() {
		defer close(getc)
		// Get may hang forever on grpc's Stream.Header() if its
		// context is never canceled.
		c.Get(c.Ctx(), "abc")
	}()

	// wait a little bit so client close is after dial starts
	time.Sleep(100 * time.Millisecond)

	donec := make(chan struct{})
	go func() {
		defer close(donec)
		c.Close()
	}()

	select {
	case <-time.After(5 * time.Second):
		t.Fatalf("failed to close")
	case <-donec:
	}
	select {
	case <-time.After(5 * time.Second):
		t.Fatalf("get failed to exit")
	case <-getc:
	}
}

func TestMaxUnaryRetries(t *testing.T) {
	maxUnaryRetries := uint(10)
	cfg := Config{
		Endpoints:       []string{"127.0.0.1:12345"},
		MaxUnaryRetries: maxUnaryRetries,
	}
	c, err := NewClient(t, cfg)
	require.NoError(t, err)
	require.NotNil(t, c)
	defer c.Close()

	require.Equal(t, maxUnaryRetries, c.cfg.MaxUnaryRetries)
}

func TestBackoff(t *testing.T) {
	backoffWaitBetween := 100 * time.Millisecond
	cfg := Config{
		Endpoints:          []string{"127.0.0.1:12345"},
		BackoffWaitBetween: backoffWaitBetween,
	}
	c, err := NewClient(t, cfg)
	require.NoError(t, err)
	require.NotNil(t, c)
	defer c.Close()

	require.Equal(t, backoffWaitBetween, c.cfg.BackoffWaitBetween)
}

func TestBackoffJitterFraction(t *testing.T) {
	backoffJitterFraction := float64(0.9)
	cfg := Config{
		Endpoints:             []string{"127.0.0.1:12345"},
		BackoffJitterFraction: backoffJitterFraction,
	}
	c, err := NewClient(t, cfg)
	require.NoError(t, err)
	require.NotNil(t, c)
	defer c.Close()

	require.InDelta(t, backoffJitterFraction, c.cfg.BackoffJitterFraction, 0.01)
}

func TestBackoffExponent(t *testing.T) {
	backoffExponent := float64(2.0)
	cfg := Config{
		Endpoints:       []string{"127.0.0.1:12345"},
		BackoffExponent: backoffExponent,
	}
	c, err := NewClient(t, cfg)
	require.NoError(t, err)
	require.NotNil(t, c)
	defer c.Close()

	require.InDelta(t, backoffExponent, c.cfg.BackoffExponent, 0.01)

	backoffExponent = float64(1.0)
	cfg = Config{
		Endpoints:       []string{"127.0.0.1:12345"},
		BackoffExponent: backoffExponent,
	}
	c, err = NewClient(t, cfg)
	require.NoError(t, err)
	require.NotNil(t, c)
	defer c.Close()

	require.InDelta(t, backoffExponent, c.cfg.BackoffExponent, 0.01)
}

func TestMaxBackoff(t *testing.T) {
	backoffMaxWaitBetween := 100 * time.Millisecond
	cfg := Config{
		Endpoints:             []string{"127.0.0.1:12345"},
		BackoffMaxWaitBetween: backoffMaxWaitBetween,
	}
	c, err := NewClient(t, cfg)
	require.NoError(t, err)
	require.NotNil(t, c)
	defer c.Close()

	require.Equal(t, backoffMaxWaitBetween, c.cfg.BackoffMaxWaitBetween)
}

func TestIsHaltErr(t *testing.T) {
	assert.Truef(t,
		isHaltErr(t.Context(), errors.New("etcdserver: some etcdserver error")),
		"error created by errors.New should be unavailable error",
	)
	assert.Falsef(t,
		isHaltErr(t.Context(), rpctypes.ErrGRPCStopped),
		`error "%v" should not be halt error`, rpctypes.ErrGRPCStopped,
	)
	assert.Falsef(t,
		isHaltErr(t.Context(), rpctypes.ErrGRPCNoLeader),
		`error "%v" should not be halt error`, rpctypes.ErrGRPCNoLeader,
	)
	ctx, cancel := context.WithCancel(t.Context())
	assert.Falsef(t,
		isHaltErr(ctx, nil),
		"no error and active context should be halt error",
	)
	cancel()
	assert.Truef(t,
		isHaltErr(ctx, nil),
		"cancel on context should be halt error",
	)
}

func TestIsUnavailableErr(t *testing.T) {
	assert.Falsef(t,
		isUnavailableErr(t.Context(), errors.New("etcdserver: some etcdserver error")),
		"error created by errors.New should not be unavailable error",
	)
	assert.Truef(t,
		isUnavailableErr(t.Context(), rpctypes.ErrGRPCStopped),
		`error "%v" should be unavailable error`, rpctypes.ErrGRPCStopped,
	)
	assert.Falsef(t,
		isUnavailableErr(t.Context(), rpctypes.ErrGRPCNotCapable),
		"error %v should not be unavailable error", rpctypes.ErrGRPCNotCapable,
	)
	ctx, cancel := context.WithCancel(t.Context())
	assert.Falsef(t,
		isUnavailableErr(ctx, nil),
		"no error and active context should not be unavailable error",
	)
	cancel()
	assert.Falsef(t,
		isUnavailableErr(ctx, nil),
		"cancel on context should not be unavailable error",
	)
}

func TestCloseCtxClient(t *testing.T) {
	ctx := t.Context()
	c := NewCtxClient(ctx)
	err := c.Close()
	// Close returns ctx.toErr, a nil error means an open Done channel
	if err == nil {
		t.Errorf("failed to Close the client. %v", err)
	}
}

func TestWithLogger(t *testing.T) {
	ctx := t.Context()
	c := NewCtxClient(ctx)
	if c.lg.Load() == nil {
		t.Errorf("unexpected nil in *zap.Logger")
	}

	c.WithLogger(nil)
	if c.GetLogger() != nil {
		t.Errorf("WithLogger should modify *zap.Logger")
	}
}

func TestZapWithLogger(t *testing.T) {
	ctx := t.Context()
	lg := zap.NewNop()
	c := NewCtxClient(ctx, WithZapLogger(lg))

	if c.GetLogger() != lg {
		t.Errorf("WithZapLogger should modify *zap.Logger")
	}
}

func TestAuthTokenBundleNoOverwrite(t *testing.T) {
	// This call in particular changes working directory to the tmp dir of
	// the test. The `etcd-auth-test:0` can be created in local directory,
	// not exceeding the longest allowed path on OsX.
	testutil.BeforeTest(t)

	// Create a mock AuthServer to handle Authenticate RPCs.
	lis, err := net.Listen("unix", "etcd-auth-test:0")
	require.NoError(t, err)
	defer lis.Close()
	addr := "unix://" + lis.Addr().String()
	srv := grpc.NewServer()
	etcdserverpb.RegisterAuthServer(srv, mockAuthServer{})
	go srv.Serve(lis)
	defer srv.Stop()

	// Create a client, which should call Authenticate on the mock server to
	// exchange username/password for an auth token.
	c, err := NewClient(t, Config{
		DialTimeout: 5 * time.Second,
		Endpoints:   []string{addr},
		Username:    "foo",
		Password:    "bar",
	})
	require.NoError(t, err)
	defer c.Close()
	oldTokenBundle := c.authTokenBundle

	// Call the public Dial again, which should preserve the original
	// authTokenBundle.
	gc, err := c.Dial(addr)
	require.NoError(t, err)
	defer gc.Close()
	newTokenBundle := c.authTokenBundle

	if oldTokenBundle != newTokenBundle {
		t.Error("Client.authTokenBundle has been overwritten during Client.Dial")
	}
}

func TestNewWithOnlyJWT(t *testing.T) {
	// This call in particular changes working directory to the tmp dir of
	// the test. The `etcd-auth-test:1` can be created in local directory,
	// not exceeding the longest allowed path on OsX.
	testutil.BeforeTest(t)

	// Create a mock AuthServer to handle Authenticate RPCs.
	lis, err := net.Listen("unix", "etcd-auth-test:1")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	addr := "unix://" + lis.Addr().String()
	srv := grpc.NewServer()
	// Having a token removes the need to ever call Authenticate on the
	// server. If that happens then this will cause a connection failure.
	etcdserverpb.RegisterAuthServer(srv, mockFailingAuthServer{})
	go srv.Serve(lis)
	defer srv.Stop()

	c, err := NewClient(t, Config{
		DialTimeout: 5 * time.Second,
		Endpoints:   []string{addr},
		Token:       "foo",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	meta, err := c.authTokenBundle.PerRPCCredentials().GetRequestMetadata(t.Context(), "")
	if err != nil {
		t.Errorf("Error building request metadata: %s", err)
	}

	if tok, ok := meta[rpctypes.TokenFieldNameGRPC]; !ok {
		t.Error("Token was not successfully set in the auth bundle")
	} else if tok != "foo" {
		t.Errorf("Incorrect token set in auth bundle, got '%s', expected 'foo'", tok)
	}
}

func TestNewOnlyJWTExclusivity(t *testing.T) {
	testutil.BeforeTest(t)

	// Create a mock AuthServer to handle Authenticate RPCs.
	lis, err := net.Listen("unix", "etcd-auth-test:1")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	addr := "unix://" + lis.Addr().String()
	srv := grpc.NewServer()
	// Having a token removes the need to ever call Authenticate on the
	// server. If that happens then this will cause a connection failure.
	etcdserverpb.RegisterAuthServer(srv, mockFailingAuthServer{})
	go srv.Serve(lis)
	defer srv.Stop()

	_, err = NewClient(t, Config{
		DialTimeout: 5 * time.Second,
		Endpoints:   []string{addr},
		Token:       "foo",
		Username:    "user",
		Password:    "pass",
	})
	require.ErrorIs(t, ErrMutuallyExclusiveCfg, err)
}

func TestSyncFiltersMembers(t *testing.T) {
	c, _ := NewClient(t, Config{Endpoints: []string{"http://254.0.0.1:12345"}})
	defer c.Close()
	c.Cluster = &mockCluster{
		[]*etcdserverpb.Member{
			{ID: 0, Name: "", ClientURLs: []string{"http://254.0.0.1:12345"}, IsLearner: false},
			{ID: 1, Name: "isStarted", ClientURLs: []string{"http://254.0.0.2:12345"}, IsLearner: true},
			{ID: 2, Name: "isStartedAndNotLearner", ClientURLs: []string{"http://254.0.0.3:12345"}, IsLearner: false},
		},
	}
	c.Sync(t.Context())

	endpoints := c.Endpoints()
	if len(endpoints) != 1 || endpoints[0] != "http://254.0.0.3:12345" {
		t.Error("Client.Sync uses learner and/or non-started member client URLs")
	}
}

func TestMinSupportedVersion(t *testing.T) {
	testutil.BeforeTest(t)
	tests := []struct {
		name                string
		currentVersion      semver.Version
		minSupportedVersion semver.Version
	}{
		{
			name:                "v3.6 client should accept v3.5",
			currentVersion:      version.V3_6,
			minSupportedVersion: version.V3_5,
		},
		{
			name:                "v3.7 client should accept v3.6",
			currentVersion:      version.V3_7,
			minSupportedVersion: version.V3_6,
		},
		{
			name:                "v3.8 client should accept v3.6",
			currentVersion:      version.V3_8,
			minSupportedVersion: version.V3_7,
		},
		{
			name:                "first minor version should accept its previous version",
			currentVersion:      version.V4_0,
			minSupportedVersion: version.V3_8,
		},
		{
			name:                "first version in list should not accept previous versions",
			currentVersion:      version.V3_0,
			minSupportedVersion: version.V3_0,
		},
	}

	versionBackup := version.Version
	t.Cleanup(func() {
		version.Version = versionBackup
	})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			version.Version = tt.currentVersion.String()
			require.True(t, minSupportedVersion().Equal(&tt.minSupportedVersion))
		})
	}
}

func TestClientRejectOldCluster(t *testing.T) {
	testutil.BeforeTest(t)
	tests := []struct {
		name          string
		endpoints     []string
		versions      []string
		expectedError error
	}{
		{
			name:          "all new versions with the same value",
			endpoints:     []string{"192.168.3.41:22379", "192.168.3.41:22479", "192.168.3.41:22579"},
			versions:      []string{version.Version, version.Version, version.Version},
			expectedError: nil,
		},
		{
			name:          "all new versions with different values",
			endpoints:     []string{"192.168.3.41:22379", "192.168.3.41:22479", "192.168.3.41:22579"},
			versions:      []string{version.Version, minSupportedVersion().String(), minSupportedVersion().String()},
			expectedError: nil,
		},
		{
			name:          "all old versions with different values",
			endpoints:     []string{"192.168.3.41:22379", "192.168.3.41:22479", "192.168.3.41:22579"},
			versions:      []string{"3.3.0", "3.3.0", "3.4.0"},
			expectedError: ErrOldCluster,
		},
		{
			name:          "all old versions with the same value",
			endpoints:     []string{"192.168.3.41:22379", "192.168.3.41:22479", "192.168.3.41:22579"},
			versions:      []string{"3.3.0", "3.3.0", "3.3.0"},
			expectedError: ErrOldCluster,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if len(tt.endpoints) != len(tt.versions) || len(tt.endpoints) == 0 {
				t.Errorf("Unexpected endpoints and versions length, len(endpoints):%d, len(versions):%d", len(tt.endpoints), len(tt.versions))
				return
			}
			endpointToVersion := make(map[string]string)
			for j := range tt.endpoints {
				endpointToVersion[tt.endpoints[j]] = tt.versions[j]
			}
			c := &Client{
				ctx:       t.Context(),
				endpoints: tt.endpoints,
				epMu:      new(sync.RWMutex),
				Maintenance: &mockMaintenance{
					Version: endpointToVersion,
				},
			}

			if err := c.checkVersion(); !errors.Is(err, tt.expectedError) {
				t.Errorf("checkVersion err:%v", err)
			}
		})
	}
}

type mockMaintenance struct {
	Version map[string]string
}

func (mm mockMaintenance) Status(ctx context.Context, endpoint string) (*StatusResponse, error) {
	return &StatusResponse{Version: mm.Version[endpoint]}, nil
}

func (mm mockMaintenance) AlarmList(ctx context.Context) (*AlarmResponse, error) {
	return nil, nil
}

func (mm mockMaintenance) AlarmDisarm(ctx context.Context, m *AlarmMember) (*AlarmResponse, error) {
	return nil, nil
}

func (mm mockMaintenance) Defragment(ctx context.Context, endpoint string) (*DefragmentResponse, error) {
	return nil, nil
}

func (mm mockMaintenance) HashKV(ctx context.Context, endpoint string, rev int64) (*HashKVResponse, error) {
	return nil, nil
}

func (mm mockMaintenance) SnapshotWithVersion(ctx context.Context) (*SnapshotResponse, error) {
	return nil, nil
}

func (mm mockMaintenance) Snapshot(ctx context.Context) (io.ReadCloser, error) {
	return nil, nil
}

func (mm mockMaintenance) MoveLeader(ctx context.Context, transfereeID uint64) (*MoveLeaderResponse, error) {
	return nil, nil
}

func (mm mockMaintenance) Downgrade(ctx context.Context, action DowngradeAction, version string) (*DowngradeResponse, error) {
	return nil, nil
}

type mockFailingAuthServer struct {
	etcdserverpb.UnimplementedAuthServer
}

func (mockFailingAuthServer) Authenticate(context.Context, *etcdserverpb.AuthenticateRequest) (*etcdserverpb.AuthenticateResponse, error) {
	return nil, errors.New("this auth server always fails")
}

type mockAuthServer struct {
	etcdserverpb.UnimplementedAuthServer
}

func (mockAuthServer) Authenticate(context.Context, *etcdserverpb.AuthenticateRequest) (*etcdserverpb.AuthenticateResponse, error) {
	return &etcdserverpb.AuthenticateResponse{Token: "mock-token"}, nil
}

type mockCluster struct {
	members []*etcdserverpb.Member
}

func (mc *mockCluster) MemberList(ctx context.Context, opts ...OpOption) (*MemberListResponse, error) {
	return &MemberListResponse{Members: mc.members}, nil
}

func (mc *mockCluster) MemberAdd(ctx context.Context, peerAddrs []string) (*MemberAddResponse, error) {
	return nil, nil
}

func (mc *mockCluster) MemberAddAsLearner(ctx context.Context, peerAddrs []string) (*MemberAddResponse, error) {
	return nil, nil
}

func (mc *mockCluster) MemberRemove(ctx context.Context, id uint64) (*MemberRemoveResponse, error) {
	return nil, nil
}

func (mc *mockCluster) MemberUpdate(ctx context.Context, id uint64, peerAddrs []string) (*MemberUpdateResponse, error) {
	return nil, nil
}

func (mc *mockCluster) MemberPromote(ctx context.Context, id uint64) (*MemberPromoteResponse, error) {
	return nil, nil
}
