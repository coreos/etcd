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

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.etcd.io/etcd/api/v3/authpb"
	"go.etcd.io/etcd/api/v3/etcdserverpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/etcdctl/v3/ctlv3/command"
	"go.etcd.io/etcd/pkg/v3/expect"
	"go.etcd.io/etcd/tests/v3/framework/config"
)

type EtcdctlV3 struct {
	cfg        ClientConfig
	endpoints  []string
	authConfig clientv3.AuthConfig
}

func NewEtcdctl(cfg ClientConfig, endpoints []string, opts ...config.ClientOption) (*EtcdctlV3, error) {
	ctl := &EtcdctlV3{
		cfg:       cfg,
		endpoints: endpoints,
	}

	for _, opt := range opts {
		opt(ctl)
	}

	if !ctl.authConfig.Empty() {
		client, err := clientv3.New(clientv3.Config{
			Endpoints:   ctl.endpoints,
			DialTimeout: 5 * time.Second,
			Username:    ctl.authConfig.Username,
			Password:    ctl.authConfig.Password,
			Token:       ctl.authConfig.Token,
		})
		if err != nil {
			return nil, err
		}
		client.Close()
	}

	return ctl, nil
}

func WithAuth(userName, password string) config.ClientOption {
	return func(c any) {
		ctl := c.(*EtcdctlV3)
		ctl.authConfig.Username = userName
		ctl.authConfig.Password = password
	}
}

func WithAuthToken(token string) config.ClientOption {
	return func(c any) {
		ctl := c.(*EtcdctlV3)
		ctl.authConfig.Token = token
	}
}

func WithEndpoints(endpoints []string) config.ClientOption {
	return func(c any) {
		ctl := c.(*EtcdctlV3)
		ctl.endpoints = endpoints
	}
}

func WithDialTimeout(tio time.Duration) config.ClientOption {
	return func(c any) {
		ctl := c.(*EtcdctlV3)
		ctl.cfg.DialTimeout = tio
	}
}

func (ctl *EtcdctlV3) Snapshot(ctx context.Context, outFile string) error {
	// etcdctl requires the snapshot to be requested from exactly one node.
	singleEndpointCtl := *ctl
	singleEndpointCtl.endpoints = []string{ctl.endpoints[0]}
	_, err := SpawnWithExpectLines(ctx, singleEndpointCtl.cmdArgs("snapshot", "save", outFile), nil, expect.ExpectedResponse{Value: "Snapshot saved at"})
	return err
}

func (ctl *EtcdctlV3) DowngradeEnable(ctx context.Context, version string) error {
	_, err := SpawnWithExpectLines(ctx, ctl.cmdArgs("downgrade", "enable", version), nil, expect.ExpectedResponse{Value: "Downgrade enable success"})
	return err
}

func (ctl *EtcdctlV3) Downgrade(ctx context.Context, action clientv3.DowngradeAction, version string) (*clientv3.DowngradeResponse, error) {
	var args []string
	switch action {
	case clientv3.DowngradeValidate:
		args = []string{"downgrade", "validate", version}
	case clientv3.DowngradeEnable:
		args = []string{"downgrade", "enable", version}
	case clientv3.DowngradeCancel:
		args = []string{"downgrade", "cancel"}
	default:
		return nil, fmt.Errorf("unknown downgrade action %v", action)
	}
	resp := clientv3.DowngradeResponse{}
	err := ctl.spawnJSONCmd(ctx, &resp, args...)
	return &resp, err
}

func (ctl *EtcdctlV3) DowngradeCancel(ctx context.Context) error {
	_, err := SpawnWithExpectLines(ctx, ctl.cmdArgs("downgrade", "cancel"), nil, expect.ExpectedResponse{Value: "Downgrade cancel success"})
	return err
}

func (ctl *EtcdctlV3) Get(ctx context.Context, key string, o config.GetOptions) (*clientv3.GetResponse, error) {
	var args []string
	if o.Timeout != 0 {
		args = append(args, fmt.Sprintf("--command-timeout=%s", o.Timeout))
	}
	if o.Serializable {
		args = append(args, "--consistency", "s")
	}
	args = append(args, "get", key, "-w", "json")
	if o.End != "" {
		args = append(args, o.End)
	}
	if o.Revision != 0 {
		args = append(args, fmt.Sprintf("--rev=%d", o.Revision))
	}
	if o.Prefix {
		args = append(args, "--prefix")
	}
	if o.Limit != 0 {
		args = append(args, fmt.Sprintf("--limit=%d", o.Limit))
	}
	if o.FromKey {
		args = append(args, "--from-key")
	}
	writeOut := "json"
	if o.CountOnly || o.KeysOnly {
		writeOut = "fields"
	}
	args = append(args, "-w", writeOut)
	if o.CountOnly {
		args = append(args, "--count-only")
	}
	if o.KeysOnly {
		args = append(args, "--keys-only")
	}
	if o.MaxCreateRevision != 0 {
		args = append(args, fmt.Sprintf("--max-create-rev=%d", o.MaxCreateRevision))
	}
	if o.MinCreateRevision != 0 {
		args = append(args, fmt.Sprintf("--min-create-rev=%d", o.MinCreateRevision))
	}
	if o.MaxModRevision != 0 {
		args = append(args, fmt.Sprintf("--max-mod-rev=%d", o.MaxModRevision))
	}
	if o.MinModRevision != 0 {
		args = append(args, fmt.Sprintf("--min-mod-rev=%d", o.MinModRevision))
	}
	if o.Stream {
		args = append(args, "--stream")
	}
	switch o.SortBy {
	case clientv3.SortByCreateRevision:
		args = append(args, "--sort-by=CREATE")
	case clientv3.SortByModRevision:
		args = append(args, "--sort-by=MODIFY")
	case clientv3.SortByValue:
		args = append(args, "--sort-by=VALUE")
	case clientv3.SortByVersion:
		args = append(args, "--sort-by=VERSION")
	case clientv3.SortByKey:
		// nothing
	default:
		return nil, fmt.Errorf("bad sort target %v", o.SortBy)
	}
	switch o.Order {
	case clientv3.SortAscend:
		args = append(args, "--order=ASCEND")
	case clientv3.SortDescend:
		args = append(args, "--order=DESCEND")
	case clientv3.SortNone:
		// nothing
	default:
		return nil, fmt.Errorf("bad sort order %v", o.Order)
	}
	if o.CountOnly {
		cmd, err := SpawnCmd(ctl.cmdArgs(args...), nil)
		if err != nil {
			return nil, err
		}
		defer cmd.Close()
		// Relying on finding 'Count' as the last line of the output to get all the lines from cmd.Lines()
		_, err = cmd.ExpectWithContext(ctx, expect.ExpectedResponse{Value: "Count"})
		if err != nil {
			return nil, err
		}
		return parseFieldsGetResponse(cmd.Lines())
	}
	resp := clientv3.GetResponse{}
	err := ctl.spawnJSONCmd(ctx, &resp, args...)
	return &resp, err
}

func parseFieldsGetResponse(lines []string) (*clientv3.GetResponse, error) {
	resp := &clientv3.GetResponse{Header: &etcdserverpb.ResponseHeader{}}
	for _, l := range lines {
		fields := strings.Split(l, ":")
		key, value := strings.TrimSpace(fields[0]), strings.TrimSpace(fields[1])
		var err error
		if key, err = strconv.Unquote(key); err != nil {
			return resp, err
		}
		switch key {
		case "ClusterID":
			resp.Header.ClusterId, err = strconv.ParseUint(value, 10, 64)
		case "MemberID":
			resp.Header.MemberId, err = strconv.ParseUint(value, 10, 64)
		case "Revision":
			resp.Header.Revision, err = strconv.ParseInt(value, 10, 64)
		case "RaftTerm":
			resp.Header.RaftTerm, err = strconv.ParseUint(value, 10, 64)
		case "More":
			resp.More, err = strconv.ParseBool(value)
		case "Count":
			resp.Count, err = strconv.ParseInt(value, 10, 64)
		default:
			return resp, fmt.Errorf("unexpected field %q:%s", key, value)
		}
		if err != nil {
			return resp, err
		}
	}
	return resp, nil
}

func (ctl *EtcdctlV3) Put(ctx context.Context, key, value string, opts config.PutOptions) (*clientv3.PutResponse, error) {
	resp := clientv3.PutResponse{}
	args := []string{"put", key}
	if !opts.IgnoreValue {
		args = append(args, value)
	}
	if opts.LeaseID != 0 && !opts.IgnoreLease {
		args = append(args, "--lease", strconv.FormatInt(int64(opts.LeaseID), 16))
	}
	if opts.IgnoreValue {
		args = append(args, "--ignore-value")
	}
	if opts.IgnoreLease {
		args = append(args, "--ignore-lease")
	}
	if opts.Timeout != 0 {
		args = append(args, fmt.Sprintf("--command-timeout=%s", opts.Timeout))
	}
	err := ctl.spawnJSONCmd(ctx, &resp, args...)
	return &resp, err
}

func (ctl *EtcdctlV3) Delete(ctx context.Context, key string, o config.DeleteOptions) (*clientv3.DeleteResponse, error) {
	args := []string{"del", key}
	if o.End != "" {
		args = append(args, o.End)
	}
	if o.Prefix {
		args = append(args, "--prefix")
	}
	if o.FromKey {
		args = append(args, "--from-key")
	}
	var resp clientv3.DeleteResponse
	err := ctl.spawnJSONCmd(ctx, &resp, args...)
	return &resp, err
}

func (ctl *EtcdctlV3) Txn(ctx context.Context, compares, ifSucess, ifFail []string, o config.TxnOptions) (*clientv3.TxnResponse, error) {
	args := ctl.cmdArgs()
	args = append(args, "txn")
	if o.Interactive {
		args = append(args, "--interactive")
	}
	args = append(args, "-w", "json", "--hex=true")
	cmd, err := SpawnCmd(args, nil)
	if err != nil {
		return nil, err
	}
	defer cmd.Close()
	_, err = cmd.ExpectWithContext(ctx, expect.ExpectedResponse{Value: "compares:"})
	if err != nil {
		return nil, err
	}
	for _, cmp := range compares {
		if err = cmd.Send(cmp + "\r"); err != nil {
			return nil, err
		}
	}
	if err = cmd.Send("\r"); err != nil {
		return nil, err
	}
	_, err = cmd.ExpectWithContext(ctx, expect.ExpectedResponse{Value: "success requests (get, put, del):"})
	if err != nil {
		return nil, err
	}
	for _, req := range ifSucess {
		if err = cmd.Send(req + "\r"); err != nil {
			return nil, err
		}
	}
	if err = cmd.Send("\r"); err != nil {
		return nil, err
	}

	_, err = cmd.ExpectWithContext(ctx, expect.ExpectedResponse{Value: "failure requests (get, put, del):"})
	if err != nil {
		return nil, err
	}
	for _, req := range ifFail {
		if err = cmd.Send(req + "\r"); err != nil {
			return nil, err
		}
	}
	if err = cmd.Send("\r"); err != nil {
		return nil, err
	}
	var line string
	line, err = cmd.ExpectWithContext(ctx, expect.ExpectedResponse{Value: "header"})
	if err != nil {
		return nil, err
	}
	var jsonResp command.TxnResponseJSON
	if err := json.Unmarshal([]byte(line), &jsonResp); err != nil {
		return nil, err
	}
	return (*clientv3.TxnResponse)(jsonResp.ToProto()), nil
}

func (ctl *EtcdctlV3) MemberList(ctx context.Context, serializable bool) (*clientv3.MemberListResponse, error) {
	var resp clientv3.MemberListResponse
	args := []string{"member", "list"}
	if serializable {
		args = append(args, "--consistency", "s")
	}
	err := ctl.spawnJSONCmd(ctx, &resp, args...)
	return &resp, err
}

func (ctl *EtcdctlV3) MemberAdd(ctx context.Context, name string, peerAddrs []string) (*clientv3.MemberAddResponse, error) {
	var resp clientv3.MemberAddResponse
	err := ctl.spawnJSONCmd(ctx, &resp, "member", "add", name, "--peer-urls", strings.Join(peerAddrs, ","))
	return &resp, err
}

func (ctl *EtcdctlV3) MemberAddAsLearner(ctx context.Context, name string, peerAddrs []string) (*clientv3.MemberAddResponse, error) {
	var resp clientv3.MemberAddResponse
	err := ctl.spawnJSONCmd(ctx, &resp, "member", "add", name, "--learner", "--peer-urls", strings.Join(peerAddrs, ","))
	return &resp, err
}

func (ctl *EtcdctlV3) MemberRemove(ctx context.Context, id uint64) (*clientv3.MemberRemoveResponse, error) {
	var resp clientv3.MemberRemoveResponse
	err := ctl.spawnJSONCmd(ctx, &resp, "member", "remove", fmt.Sprintf("%x", id))
	return &resp, err
}

func (ctl *EtcdctlV3) MemberPromote(ctx context.Context, id uint64) (*clientv3.MemberPromoteResponse, error) {
	var resp clientv3.MemberPromoteResponse
	err := ctl.spawnJSONCmd(ctx, &resp, "member", "promote", fmt.Sprintf("%x", id))
	return &resp, err
}

// MoveLeader requests current leader to transfer its leadership to the transferee.
// Request must be made to the leader.
func (ctl *EtcdctlV3) MoveLeader(ctx context.Context, transfereeID uint64) error {
	_, err := SpawnWithExpectLines(ctx, ctl.cmdArgs("move-leader", fmt.Sprintf("%x", transfereeID)), nil, expect.ExpectedResponse{Value: "Leadership transferred"})
	return err
}

func (ctl *EtcdctlV3) cmdArgs(args ...string) []string {
	cmdArgs := []string{BinPath.Etcdctl}
	for k, v := range ctl.flags() {
		cmdArgs = append(cmdArgs, fmt.Sprintf("--%s=%s", k, v))
	}
	return append(cmdArgs, args...)
}

func (ctl *EtcdctlV3) flags() map[string]string {
	fmap := make(map[string]string)
	if ctl.cfg.ConnectionType == ClientTLS {
		if ctl.cfg.AutoTLS {
			fmap["insecure-transport"] = "false"
			fmap["insecure-skip-tls-verify"] = "true"
		} else if ctl.cfg.RevokeCerts {
			fmap["cacert"] = CaPath
			fmap["cert"] = RevokedCertPath
			fmap["key"] = RevokedPrivateKeyPath
		} else {
			fmap["cacert"] = CaPath
			fmap["cert"] = CertPath
			fmap["key"] = PrivateKeyPath
		}
	}
	fmap["endpoints"] = strings.Join(ctl.endpoints, ",")
	if ctl.authConfig.Token != "" {
		fmap["auth-jwt-token"] = ctl.authConfig.Token
	} else if !ctl.authConfig.Empty() {
		fmap["user"] = ctl.authConfig.Username + ":" + ctl.authConfig.Password
	}
	if ctl.cfg.DialTimeout != 0 {
		fmap["dial-timeout"] = ctl.cfg.DialTimeout.String()
	}
	return fmap
}

func (ctl *EtcdctlV3) Compact(ctx context.Context, rev int64, o config.CompactOption) (*clientv3.CompactResponse, error) {
	args := ctl.cmdArgs("compact", fmt.Sprint(rev))
	if o.Timeout != 0 {
		args = append(args, fmt.Sprintf("--command-timeout=%s", o.Timeout))
	}
	if o.Physical {
		args = append(args, "--physical")
	}

	_, err := SpawnWithExpectLines(ctx, args, nil, expect.ExpectedResponse{Value: fmt.Sprintf("compacted revision %v", rev)})
	return nil, err
}

func (ctl *EtcdctlV3) Status(ctx context.Context) ([]*clientv3.StatusResponse, error) {
	var epStatus []*struct {
		Endpoint string
		Status   *clientv3.StatusResponse
	}
	err := ctl.spawnJSONCmd(ctx, &epStatus, "endpoint", "status")
	if err != nil {
		return nil, err
	}
	resp := make([]*clientv3.StatusResponse, len(epStatus))
	for i, e := range epStatus {
		resp[i] = e.Status
	}
	return resp, err
}

func (ctl *EtcdctlV3) HashKV(ctx context.Context, rev int64) ([]*clientv3.HashKVResponse, error) {
	var epHashKVs []*struct {
		Endpoint string
		HashKV   *clientv3.HashKVResponse
	}
	err := ctl.spawnJSONCmd(ctx, &epHashKVs, "endpoint", "hashkv", "--rev", fmt.Sprint(rev))
	if err != nil {
		return nil, err
	}
	resp := make([]*clientv3.HashKVResponse, len(epHashKVs))
	for i, e := range epHashKVs {
		resp[i] = e.HashKV
	}
	return resp, err
}

func (ctl *EtcdctlV3) Health(ctx context.Context) error {
	args := ctl.cmdArgs()
	args = append(args, "endpoint", "health")
	lines := make([]expect.ExpectedResponse, len(ctl.endpoints))
	for i := range lines {
		lines[i] = expect.ExpectedResponse{Value: "is healthy"}
	}
	_, err := SpawnWithExpectLines(ctx, args, nil, lines...)
	return err
}

func (ctl *EtcdctlV3) Grant(ctx context.Context, ttl int64) (*clientv3.LeaseGrantResponse, error) {
	args := ctl.cmdArgs()
	args = append(args, "lease", "grant", strconv.FormatInt(ttl, 10), "-w", "json")
	cmd, err := SpawnCmd(args, nil)
	if err != nil {
		return nil, err
	}
	defer cmd.Close()
	var resp clientv3.LeaseGrantResponse
	line, err := cmd.ExpectWithContext(ctx, expect.ExpectedResponse{Value: "ID"})
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal([]byte(line), &resp)
	return &resp, err
}

func (ctl *EtcdctlV3) TimeToLive(ctx context.Context, id clientv3.LeaseID, o config.LeaseOption) (*clientv3.LeaseTimeToLiveResponse, error) {
	args := ctl.cmdArgs()
	args = append(args, "lease", "timetolive", strconv.FormatInt(int64(id), 16), "-w", "json")
	if o.WithAttachedKeys {
		args = append(args, "--keys")
	}
	cmd, err := SpawnCmd(args, nil)
	if err != nil {
		return nil, err
	}
	defer cmd.Close()
	var resp clientv3.LeaseTimeToLiveResponse
	line, err := cmd.ExpectWithContext(ctx, expect.ExpectedResponse{Value: "member_id"})
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal([]byte(line), &resp)
	return &resp, err
}

func (ctl *EtcdctlV3) Defragment(ctx context.Context, o config.DefragOption) error {
	args := append(ctl.cmdArgs(), "defrag")
	if o.Timeout != 0 {
		args = append(args, fmt.Sprintf("--command-timeout=%s", o.Timeout))
	}
	lines := make([]expect.ExpectedResponse, len(ctl.endpoints))
	for i := range lines {
		lines[i] = expect.ExpectedResponse{Value: "Finished defragmenting etcd member"}
	}
	_, err := SpawnWithExpectLines(ctx, args, map[string]string{}, lines...)
	return err
}

func (ctl *EtcdctlV3) Leases(ctx context.Context) (*clientv3.LeaseLeasesResponse, error) {
	args := ctl.cmdArgs("lease", "list", "-w", "json")
	cmd, err := SpawnCmd(args, nil)
	if err != nil {
		return nil, err
	}
	defer cmd.Close()
	var resp clientv3.LeaseLeasesResponse
	line, err := cmd.ExpectWithContext(ctx, expect.ExpectedResponse{Value: "member_id"})
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal([]byte(line), &resp)
	return &resp, err
}

func (ctl *EtcdctlV3) KeepAliveOnce(ctx context.Context, id clientv3.LeaseID) (*clientv3.LeaseKeepAliveResponse, error) {
	args := ctl.cmdArgs("lease", "keep-alive", strconv.FormatInt(int64(id), 16), "--once", "-w", "json")
	cmd, err := SpawnCmd(args, nil)
	if err != nil {
		return nil, err
	}
	defer cmd.Close()
	var resp clientv3.LeaseKeepAliveResponse
	line, err := cmd.ExpectWithContext(ctx, expect.ExpectedResponse{Value: "ID"})
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal([]byte(line), &resp)
	return &resp, err
}

func (ctl *EtcdctlV3) Revoke(ctx context.Context, id clientv3.LeaseID) (*clientv3.LeaseRevokeResponse, error) {
	var resp clientv3.LeaseRevokeResponse
	err := ctl.spawnJSONCmd(ctx, &resp, "lease", "revoke", strconv.FormatInt(int64(id), 16))
	return &resp, err
}

func (ctl *EtcdctlV3) AlarmList(ctx context.Context) (*clientv3.AlarmResponse, error) {
	var resp clientv3.AlarmResponse
	err := ctl.spawnJSONCmd(ctx, &resp, "alarm", "list")
	return &resp, err
}

func (ctl *EtcdctlV3) AlarmDisarm(ctx context.Context, _ *clientv3.AlarmMember) (*clientv3.AlarmResponse, error) {
	args := ctl.cmdArgs()
	args = append(args, "alarm", "disarm", "-w", "json")
	ep, err := SpawnCmd(args, nil)
	if err != nil {
		return nil, err
	}
	defer ep.Close()
	var resp clientv3.AlarmResponse
	line, err := ep.ExpectWithContext(ctx, expect.ExpectedResponse{Value: "alarm"})
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal([]byte(line), &resp)
	return &resp, err
}

func (ctl *EtcdctlV3) AuthEnable(ctx context.Context) error {
	args := []string{"auth", "enable"}
	cmd, err := SpawnCmd(ctl.cmdArgs(args...), nil)
	if err != nil {
		return err
	}
	defer cmd.Close()

	_, err = cmd.ExpectWithContext(ctx, expect.ExpectedResponse{Value: "Authentication Enabled"})
	return err
}

func (ctl *EtcdctlV3) AuthDisable(ctx context.Context) error {
	args := []string{"auth", "disable"}
	cmd, err := SpawnCmd(ctl.cmdArgs(args...), nil)
	if err != nil {
		return err
	}
	defer cmd.Close()

	_, err = cmd.ExpectWithContext(ctx, expect.ExpectedResponse{Value: "Authentication Disabled"})
	return err
}

func (ctl *EtcdctlV3) AuthStatus(ctx context.Context) (*clientv3.AuthStatusResponse, error) {
	var resp clientv3.AuthStatusResponse
	err := ctl.spawnJSONCmd(ctx, &resp, "auth", "status")
	return &resp, err
}

func (ctl *EtcdctlV3) UserAdd(ctx context.Context, name, password string, opts config.UserAddOptions) (*clientv3.AuthUserAddResponse, error) {
	args := ctl.cmdArgs()
	args = append(args, "user", "add")
	if password == "" {
		args = append(args, name)
	} else {
		args = append(args, fmt.Sprintf("%s:%s", name, password))
	}

	if opts.NoPassword {
		args = append(args, "--no-password")
	}

	args = append(args, "--interactive=false", "-w", "json")

	cmd, err := SpawnCmd(args, nil)
	if err != nil {
		return nil, err
	}
	defer cmd.Close()

	// If no password is provided, and NoPassword isn't set, the CLI will always
	// wait for a password, send an enter in this case for an "empty" password.
	if !opts.NoPassword && password == "" {
		err = cmd.Send("\n")
		if err != nil {
			return nil, err
		}
	}

	var resp clientv3.AuthUserAddResponse
	line, err := cmd.ExpectWithContext(ctx, expect.ExpectedResponse{Value: "header"})
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal([]byte(line), &resp)
	return &resp, err
}

func (ctl *EtcdctlV3) UserGet(ctx context.Context, name string) (*clientv3.AuthUserGetResponse, error) {
	var resp clientv3.AuthUserGetResponse
	err := ctl.spawnJSONCmd(ctx, &resp, "user", "get", name)
	return &resp, err
}

func (ctl *EtcdctlV3) UserList(ctx context.Context) (*clientv3.AuthUserListResponse, error) {
	var resp clientv3.AuthUserListResponse
	err := ctl.spawnJSONCmd(ctx, &resp, "user", "list")
	return &resp, err
}

func (ctl *EtcdctlV3) UserDelete(ctx context.Context, name string) (*clientv3.AuthUserDeleteResponse, error) {
	var resp clientv3.AuthUserDeleteResponse
	err := ctl.spawnJSONCmd(ctx, &resp, "user", "delete", name)
	return &resp, err
}

func (ctl *EtcdctlV3) UserChangePass(ctx context.Context, user, newPass string) error {
	args := ctl.cmdArgs()
	args = append(args, "user", "passwd", user, "--interactive=false")
	cmd, err := SpawnCmd(args, nil)
	if err != nil {
		return err
	}
	defer cmd.Close()
	err = cmd.Send(newPass + "\n")
	if err != nil {
		return err
	}

	_, err = cmd.ExpectWithContext(ctx, expect.ExpectedResponse{Value: "Password updated"})
	return err
}

func (ctl *EtcdctlV3) UserGrantRole(ctx context.Context, user string, role string) (*clientv3.AuthUserGrantRoleResponse, error) {
	var resp clientv3.AuthUserGrantRoleResponse
	err := ctl.spawnJSONCmd(ctx, &resp, "user", "grant-role", user, role)
	return &resp, err
}

func (ctl *EtcdctlV3) UserRevokeRole(ctx context.Context, user string, role string) (*clientv3.AuthUserRevokeRoleResponse, error) {
	var resp clientv3.AuthUserRevokeRoleResponse
	err := ctl.spawnJSONCmd(ctx, &resp, "user", "revoke-role", user, role)
	return &resp, err
}

func (ctl *EtcdctlV3) RoleAdd(ctx context.Context, name string) (*clientv3.AuthRoleAddResponse, error) {
	var resp clientv3.AuthRoleAddResponse
	err := ctl.spawnJSONCmd(ctx, &resp, "role", "add", name)
	return &resp, err
}

func (ctl *EtcdctlV3) RoleGrantPermission(ctx context.Context, name string, key, rangeEnd string, permType clientv3.PermissionType) (*clientv3.AuthRoleGrantPermissionResponse, error) {
	permissionType := authpb.Permission_Type_name[int32(permType)]
	var resp clientv3.AuthRoleGrantPermissionResponse
	err := ctl.spawnJSONCmd(ctx, &resp, "role", "grant-permission", name, permissionType, key, rangeEnd)
	return &resp, err
}

func (ctl *EtcdctlV3) RoleGet(ctx context.Context, role string) (*clientv3.AuthRoleGetResponse, error) {
	var resp clientv3.AuthRoleGetResponse
	err := ctl.spawnJSONCmd(ctx, &resp, "role", "get", role)
	return &resp, err
}

func (ctl *EtcdctlV3) RoleList(ctx context.Context) (*clientv3.AuthRoleListResponse, error) {
	var resp clientv3.AuthRoleListResponse
	err := ctl.spawnJSONCmd(ctx, &resp, "role", "list")
	return &resp, err
}

func (ctl *EtcdctlV3) RoleRevokePermission(ctx context.Context, role string, key, rangeEnd string) (*clientv3.AuthRoleRevokePermissionResponse, error) {
	var resp clientv3.AuthRoleRevokePermissionResponse
	err := ctl.spawnJSONCmd(ctx, &resp, "role", "revoke-permission", role, key, rangeEnd)
	return &resp, err
}

func (ctl *EtcdctlV3) RoleDelete(ctx context.Context, role string) (*clientv3.AuthRoleDeleteResponse, error) {
	var resp clientv3.AuthRoleDeleteResponse
	err := ctl.spawnJSONCmd(ctx, &resp, "role", "delete", role)
	return &resp, err
}

func (ctl *EtcdctlV3) spawnJSONCmd(ctx context.Context, output any, args ...string) error {
	args = append(args, "-w", "json")
	cmd, err := SpawnCmd(append(ctl.cmdArgs(), args...), nil)
	if err != nil {
		return err
	}
	defer cmd.Close()
	line, err := cmd.ExpectWithContext(ctx, expect.ExpectedResponse{Value: "header"})
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(line), output)
}

func (ctl *EtcdctlV3) Watch(ctx context.Context, key string, opts config.WatchOptions) clientv3.WatchChan {
	args := ctl.cmdArgs()
	args = append(args, "watch", key)
	if opts.RangeEnd != "" {
		args = append(args, opts.RangeEnd)
	}
	args = append(args, "-w", "json")
	if opts.Prefix {
		args = append(args, "--prefix")
	}
	if opts.Revision != 0 {
		args = append(args, "--rev", fmt.Sprint(opts.Revision))
	}
	if opts.CreatedNotify {
		args = append(args, "--created-notify")
	}
	proc, err := SpawnCmd(args, nil)
	if err != nil {
		return nil
	}

	ch := make(chan clientv3.WatchResponse)
	go func() {
		defer proc.Stop()
		for {
			select {
			case <-ctx.Done():
				close(ch)
				return
			default:
				if line := proc.ReadLine(); line != "" {
					var resp clientv3.WatchResponse
					json.Unmarshal([]byte(line), &resp)
					if resp.Canceled {
						ch <- resp
						close(ch)
						return
					}
					if resp.Created || len(resp.Events) > 0 {
						ch <- resp
					}
				}
			}
		}
	}()

	return ch
}
