// Copyright 2022 The etcd Authors
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

package integration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"go.uber.org/zap"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"go.etcd.io/etcd/client/pkg/v3/testutil"
	"go.etcd.io/etcd/client/pkg/v3/transport"
	clientv3 "go.etcd.io/etcd/client/v3"
	etcdctlcmd "go.etcd.io/etcd/etcdctl/v3/ctlv3/command"
	"go.etcd.io/etcd/tests/v3/framework/config"
	intf "go.etcd.io/etcd/tests/v3/framework/interfaces"
)

type integrationRunner struct{}

func NewIntegrationRunner() intf.TestRunner {
	return &integrationRunner{}
}

func (e integrationRunner) TestMain(m *testing.M) {
	testutil.MustTestMainWithLeakDetection(m)
}

func (e integrationRunner) BeforeTest(tb testing.TB) {
	BeforeTest(tb)
}

func (e integrationRunner) NewCluster(ctx context.Context, tb testing.TB, opts ...config.ClusterOption) intf.Cluster {
	var err error
	cfg := config.NewClusterConfig(opts...)
	integrationCfg := ClusterConfig{
		Size:                       cfg.ClusterSize,
		QuotaBackendBytes:          cfg.QuotaBackendBytes,
		DisableStrictReconfigCheck: !cfg.StrictReconfigCheck,
		AuthToken:                  cfg.AuthToken,
		SnapshotCount:              cfg.SnapshotCount,
	}
	integrationCfg.ClientTLS, err = tlsInfo(tb, cfg.ClientTLS)
	if err != nil {
		tb.Fatalf("ClientTLS: %s", err)
	}
	integrationCfg.PeerTLS, err = tlsInfo(tb, cfg.PeerTLS)
	if err != nil {
		tb.Fatalf("PeerTLS: %s", err)
	}

	if cfg.ClusterContext != nil {
		if ctx, ok := cfg.ClusterContext.(*ClusterContext); ok && ctx != nil {
			integrationCfg.UseTCP = !ctx.UseUnix
			integrationCfg.UseIP = !ctx.UseUnix
		}
	}

	return &integrationCluster{
		Cluster: NewCluster(tb, &integrationCfg),
		t:       tb,
		ctx:     ctx,
	}
}

func tlsInfo(tb testing.TB, cfg config.TLSConfig) (*transport.TLSInfo, error) {
	switch cfg {
	case config.NoTLS:
		return nil, nil
	case config.AutoTLS:
		tls, err := transport.SelfCert(zap.NewNop(), tb.TempDir(), []string{"localhost"}, 1)
		if err != nil {
			return nil, fmt.Errorf("failed to generate cert: %w", err)
		}
		return &tls, nil
	case config.ManualTLS:
		return &TestTLSInfo, nil
	default:
		return nil, fmt.Errorf("config %q not supported", cfg)
	}
}

type integrationCluster struct {
	*Cluster
	t   testing.TB
	ctx context.Context
}

func (c *integrationCluster) Members() (ms []intf.Member) {
	for _, m := range c.Cluster.Members {
		ms = append(ms, integrationMember{Member: m, t: c.t})
	}
	return ms
}

func (c *integrationCluster) TemplateEndpoints(tb testing.TB, pattern string) []string {
	tb.Helper()
	var endpoints []string
	for _, m := range c.Cluster.Members {
		ent := pattern
		ent = strings.ReplaceAll(ent, "${MEMBER_PORT}", m.GRPCPortNumber())
		ent = strings.ReplaceAll(ent, "${MEMBER_NAME}", m.Name)
		endpoints = append(endpoints, ent)
	}
	return endpoints
}

func templateAuthority(tb testing.TB, pattern string, m *Member) string {
	tb.Helper()
	authority := pattern
	authority = strings.ReplaceAll(authority, "${MEMBER_PORT}", m.GRPCPortNumber())
	authority = strings.ReplaceAll(authority, "${MEMBER_NAME}", m.Name)
	return authority
}

func (c *integrationCluster) AssertAuthority(tb testing.TB, expectedAuthorityPattern string) {
	tb.Helper()
	const filterMethod = "/etcdserverpb.KV/Put"
	for _, m := range c.Cluster.Members {
		expectedAuthority := templateAuthority(tb, expectedAuthorityPattern, m)
		requestsFound := 0
		for _, r := range m.RecordedRequests() {
			if r.FullMethod != filterMethod {
				continue
			}
			if r.Authority == expectedAuthority {
				requestsFound++
			} else {
				tb.Errorf("Got unexpected authority header, member %q, request %q, got %q, expected %q", m.Name, r.FullMethod, r.Authority, expectedAuthority)
			}
		}
		if requestsFound == 0 {
			tb.Errorf("Expect at least one request with matched authority header value was recorded by the server intercepter on member %s but got 0", m.Name)
		}
	}
}

type integrationMember struct {
	*Member
	t testing.TB
}

func (m integrationMember) Client() intf.Client {
	return integrationClient{Client: m.Member.Client}
}

func (m integrationMember) Start(ctx context.Context) error {
	return m.Member.Restart(m.t)
}

func (m integrationMember) Stop() {
	m.Member.Stop(m.t)
}

func (c *integrationCluster) Close() error {
	c.Terminate(c.t)
	return nil
}

func (c *integrationCluster) Client(opts ...config.ClientOption) (intf.Client, error) {
	cc, err := c.ClusterClient(c.t, opts...)
	if err != nil {
		return nil, err
	}
	return integrationClient{Client: cc}, nil
}

type integrationClient struct {
	*clientv3.Client
}

func (c integrationClient) Get(ctx context.Context, key string, o config.GetOptions) (*clientv3.GetResponse, error) {
	if o.Timeout != 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	var clientOpts []clientv3.OpOption
	if o.Revision != 0 {
		clientOpts = append(clientOpts, clientv3.WithRev(int64(o.Revision)))
	}
	if o.End != "" {
		clientOpts = append(clientOpts, clientv3.WithRange(o.End))
	}
	if o.Serializable {
		clientOpts = append(clientOpts, clientv3.WithSerializable())
	}
	if o.Prefix {
		clientOpts = append(clientOpts, clientv3.WithPrefix())
	}
	if o.Limit != 0 {
		clientOpts = append(clientOpts, clientv3.WithLimit(int64(o.Limit)))
	}
	if o.FromKey {
		clientOpts = append(clientOpts, clientv3.WithFromKey())
	}
	if o.CountOnly {
		clientOpts = append(clientOpts, clientv3.WithCountOnly())
	}
	if o.KeysOnly {
		clientOpts = append(clientOpts, clientv3.WithKeysOnly())
	}
	if o.SortBy != clientv3.SortByKey || o.Order != clientv3.SortNone {
		clientOpts = append(clientOpts, clientv3.WithSort(o.SortBy, o.Order))
	}
	if o.MaxCreateRevision != 0 {
		clientOpts = append(clientOpts, clientv3.WithMaxCreateRev(int64(o.MaxCreateRevision)))
	}
	if o.MinCreateRevision != 0 {
		clientOpts = append(clientOpts, clientv3.WithMinCreateRev(int64(o.MinCreateRevision)))
	}
	if o.MaxModRevision != 0 {
		clientOpts = append(clientOpts, clientv3.WithMaxModRev(int64(o.MaxModRevision)))
	}
	if o.MinModRevision != 0 {
		clientOpts = append(clientOpts, clientv3.WithMinModRev(int64(o.MinModRevision)))
	}
	if o.Stream {
		stream, err := c.Client.GetStream(ctx, key, clientOpts...)
		if err != nil {
			return nil, err
		}
		return clientv3.GetStreamToGetResponse(stream)
	}
	return c.Client.Get(ctx, key, clientOpts...)
}

func (c integrationClient) Put(ctx context.Context, key, value string, opts config.PutOptions) (*clientv3.PutResponse, error) {
	if opts.Timeout != 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	var clientOpts []clientv3.OpOption
	if opts.LeaseID != 0 {
		clientOpts = append(clientOpts, clientv3.WithLease(opts.LeaseID))
	}
	if opts.IgnoreValue {
		clientOpts = append(clientOpts, clientv3.WithIgnoreValue())
	}
	if opts.IgnoreLease {
		clientOpts = append(clientOpts, clientv3.WithIgnoreLease())
	}
	return c.Client.Put(ctx, key, value, clientOpts...)
}

func (c integrationClient) Delete(ctx context.Context, key string, o config.DeleteOptions) (*clientv3.DeleteResponse, error) {
	var clientOpts []clientv3.OpOption
	if o.Prefix {
		clientOpts = append(clientOpts, clientv3.WithPrefix())
	}
	if o.FromKey {
		clientOpts = append(clientOpts, clientv3.WithFromKey())
	}
	if o.End != "" {
		clientOpts = append(clientOpts, clientv3.WithRange(o.End))
	}
	return c.Client.Delete(ctx, key, clientOpts...)
}

func (c integrationClient) Compact(ctx context.Context, rev int64, o config.CompactOption) (*clientv3.CompactResponse, error) {
	if o.Timeout != 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	var clientOpts []clientv3.CompactOption
	if o.Physical {
		clientOpts = append(clientOpts, clientv3.WithCompactPhysical())
	}
	return c.Client.Compact(ctx, rev, clientOpts...)
}

func (c integrationClient) Status(ctx context.Context) ([]*clientv3.StatusResponse, error) {
	endpoints := c.Client.Endpoints()
	var resp []*clientv3.StatusResponse
	for _, ep := range endpoints {
		status, err := c.Client.Status(ctx, ep)
		if err != nil {
			return nil, err
		}
		resp = append(resp, status)
	}
	return resp, nil
}

func (c integrationClient) HashKV(ctx context.Context, rev int64) ([]*clientv3.HashKVResponse, error) {
	endpoints := c.Client.Endpoints()
	var resp []*clientv3.HashKVResponse
	for _, ep := range endpoints {
		hashKV, err := c.Client.HashKV(ctx, ep, rev)
		if err != nil {
			return nil, err
		}
		resp = append(resp, hashKV)
	}
	return resp, nil
}

func (c integrationClient) Health(ctx context.Context) error {
	cli := healthpb.NewHealthClient(c.Client.ActiveConnection())
	resp, err := cli.Check(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		return err
	}
	if resp.Status != healthpb.HealthCheckResponse_SERVING {
		return fmt.Errorf("status expected %s, got %s", healthpb.HealthCheckResponse_SERVING, resp.Status)
	}
	return nil
}

func (c integrationClient) Defragment(ctx context.Context, o config.DefragOption) error {
	if o.Timeout != 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	for _, ep := range c.Endpoints() {
		_, err := c.Client.Defragment(ctx, ep)
		if err != nil {
			return err
		}
	}
	return nil
}

func (c integrationClient) Snapshot(ctx context.Context, outFile string) error {
	rc, err := c.Client.Snapshot(ctx)
	if err != nil {
		return err
	}
	defer rc.Close()
	f, err := os.Create(outFile)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, rc)
	return err
}

func (c integrationClient) Downgrade(ctx context.Context, action clientv3.DowngradeAction, version string) (*clientv3.DowngradeResponse, error) {
	return c.Client.Downgrade(ctx, action, version)
}

func (c integrationClient) TimeToLive(ctx context.Context, id clientv3.LeaseID, o config.LeaseOption) (*clientv3.LeaseTimeToLiveResponse, error) {
	var leaseOpts []clientv3.LeaseOption
	if o.WithAttachedKeys {
		leaseOpts = append(leaseOpts, clientv3.WithAttachedKeys())
	}

	return c.Client.TimeToLive(ctx, id, leaseOpts...)
}

func (c integrationClient) Leases(ctx context.Context) (*clientv3.LeaseLeasesResponse, error) {
	return c.Client.Leases(ctx)
}

func (c integrationClient) KeepAliveOnce(ctx context.Context, id clientv3.LeaseID) (*clientv3.LeaseKeepAliveResponse, error) {
	return c.Client.KeepAliveOnce(ctx, id)
}

func (c integrationClient) Revoke(ctx context.Context, id clientv3.LeaseID) (*clientv3.LeaseRevokeResponse, error) {
	return c.Client.Revoke(ctx, id)
}

func (c integrationClient) AuthEnable(ctx context.Context) error {
	_, err := c.Client.AuthEnable(ctx)
	return err
}

func (c integrationClient) AuthDisable(ctx context.Context) error {
	_, err := c.Client.AuthDisable(ctx)
	return err
}

func (c integrationClient) AuthStatus(ctx context.Context) (*clientv3.AuthStatusResponse, error) {
	return c.Client.AuthStatus(ctx)
}

func (c integrationClient) UserAdd(ctx context.Context, name, password string, opts config.UserAddOptions) (*clientv3.AuthUserAddResponse, error) {
	return c.Client.UserAddWithOptions(ctx, name, password, &clientv3.UserAddOptions{
		NoPassword: opts.NoPassword,
	})
}

func (c integrationClient) UserGet(ctx context.Context, name string) (*clientv3.AuthUserGetResponse, error) {
	return c.Client.UserGet(ctx, name)
}

func (c integrationClient) UserList(ctx context.Context) (*clientv3.AuthUserListResponse, error) {
	return c.Client.UserList(ctx)
}

func (c integrationClient) UserDelete(ctx context.Context, name string) (*clientv3.AuthUserDeleteResponse, error) {
	return c.Client.UserDelete(ctx, name)
}

func (c integrationClient) UserChangePass(ctx context.Context, user, newPass string) error {
	_, err := c.Client.UserChangePassword(ctx, user, newPass)
	return err
}

func (c integrationClient) UserGrantRole(ctx context.Context, user string, role string) (*clientv3.AuthUserGrantRoleResponse, error) {
	return c.Client.UserGrantRole(ctx, user, role)
}

func (c integrationClient) UserRevokeRole(ctx context.Context, user string, role string) (*clientv3.AuthUserRevokeRoleResponse, error) {
	return c.Client.UserRevokeRole(ctx, user, role)
}

func (c integrationClient) RoleAdd(ctx context.Context, name string) (*clientv3.AuthRoleAddResponse, error) {
	return c.Client.RoleAdd(ctx, name)
}

func (c integrationClient) RoleGrantPermission(ctx context.Context, name string, key, rangeEnd string, permType clientv3.PermissionType) (*clientv3.AuthRoleGrantPermissionResponse, error) {
	return c.Client.RoleGrantPermission(ctx, name, key, rangeEnd, permType)
}

func (c integrationClient) RoleGet(ctx context.Context, role string) (*clientv3.AuthRoleGetResponse, error) {
	return c.Client.RoleGet(ctx, role)
}

func (c integrationClient) RoleList(ctx context.Context) (*clientv3.AuthRoleListResponse, error) {
	return c.Client.RoleList(ctx)
}

func (c integrationClient) RoleRevokePermission(ctx context.Context, role string, key, rangeEnd string) (*clientv3.AuthRoleRevokePermissionResponse, error) {
	return c.Client.RoleRevokePermission(ctx, role, key, rangeEnd)
}

func (c integrationClient) RoleDelete(ctx context.Context, role string) (*clientv3.AuthRoleDeleteResponse, error) {
	return c.Client.RoleDelete(ctx, role)
}

func (c integrationClient) Txn(ctx context.Context, compares, ifSucess, ifFail []string, o config.TxnOptions) (*clientv3.TxnResponse, error) {
	txn := c.Client.Txn(ctx)
	var cmps []clientv3.Cmp
	for _, c := range compares {
		cmp, err := etcdctlcmd.ParseCompare(c)
		if err != nil {
			return nil, err
		}
		cmps = append(cmps, *cmp)
	}

	succOps := getOps(ifSucess)

	failOps := getOps(ifFail)

	txnrsp, err := txn.
		If(cmps...).
		Then(succOps...).
		Else(failOps...).
		Commit()
	return txnrsp, err
}

func getOps(ss []string) []clientv3.Op {
	var ops []clientv3.Op
	for _, s := range ss {
		s = strings.TrimSpace(s)
		args := etcdctlcmd.Argify(s)
		switch args[0] {
		case "get":
			ops = append(ops, clientv3.OpGet(args[1]))
		case "put":
			ops = append(ops, clientv3.OpPut(args[1], args[2]))
		case "del":
			ops = append(ops, clientv3.OpDelete(args[1]))
		}
	}
	return ops
}

func (c integrationClient) Watch(ctx context.Context, key string, opts config.WatchOptions) clientv3.WatchChan {
	var opOpts []clientv3.OpOption
	if opts.Prefix {
		opOpts = append(opOpts, clientv3.WithPrefix())
	}
	if opts.Revision != 0 {
		opOpts = append(opOpts, clientv3.WithRev(opts.Revision))
	}
	if opts.RangeEnd != "" {
		opOpts = append(opOpts, clientv3.WithRange(opts.RangeEnd))
	}

	return c.Client.Watch(ctx, key, opOpts...)
}

func (c integrationClient) MemberAdd(ctx context.Context, _ string, peerAddrs []string) (*clientv3.MemberAddResponse, error) {
	return c.Client.MemberAdd(ctx, peerAddrs)
}

func (c integrationClient) MemberAddAsLearner(ctx context.Context, _ string, peerAddrs []string) (*clientv3.MemberAddResponse, error) {
	return c.Client.MemberAddAsLearner(ctx, peerAddrs)
}

func (c integrationClient) MemberRemove(ctx context.Context, id uint64) (*clientv3.MemberRemoveResponse, error) {
	return c.Client.MemberRemove(ctx, id)
}

func (c integrationClient) MemberList(ctx context.Context, serializable bool) (*clientv3.MemberListResponse, error) {
	if serializable {
		return c.Client.MemberList(ctx, clientv3.WithSerializable())
	}
	return c.Client.MemberList(ctx)
}

func (c integrationClient) MakeMirror(ctx context.Context, destEndpoints string, opts config.MakeMirrorOptions) error {
	return errors.New("errors")
}
