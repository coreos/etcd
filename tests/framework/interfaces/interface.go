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

package interfaces

import (
	"context"
	"testing"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/tests/v3/framework/config"
)

type TestRunner interface {
	TestMain(m *testing.M)
	BeforeTest(testing.TB)
	NewCluster(context.Context, testing.TB, ...config.ClusterOption) Cluster
}

type Cluster interface {
	Members() []Member
	Client(opts ...config.ClientOption) (Client, error)
	WaitLeader(t testing.TB) int
	Close() error
	Endpoints() []string
}

type Member interface {
	Client() Client
	Start(ctx context.Context) error
	Stop()
}

type Client interface {
	Put(context context.Context, key, value string, opts config.PutOptions) (*clientv3.PutResponse, error)
	Get(context context.Context, key string, opts config.GetOptions) (*clientv3.GetResponse, error)
	Delete(context context.Context, key string, opts config.DeleteOptions) (*clientv3.DeleteResponse, error)
	Compact(context context.Context, rev int64, opts config.CompactOption) (*clientv3.CompactResponse, error)
	Status(context context.Context) ([]*clientv3.StatusResponse, error)
	HashKV(context context.Context, rev int64) ([]*clientv3.HashKVResponse, error)
	Health(context context.Context) error
	Defragment(context context.Context, opts config.DefragOption) error
	Snapshot(context context.Context, outFile string) error
	Downgrade(ctx context.Context, action clientv3.DowngradeAction, version string) (*clientv3.DowngradeResponse, error)
	AlarmList(context context.Context) (*clientv3.AlarmResponse, error)
	AlarmDisarm(context context.Context, alarmMember *clientv3.AlarmMember) (*clientv3.AlarmResponse, error)
	Grant(context context.Context, ttl int64) (*clientv3.LeaseGrantResponse, error)
	TimeToLive(context context.Context, id clientv3.LeaseID, opts config.LeaseOption) (*clientv3.LeaseTimeToLiveResponse, error)
	Leases(context context.Context) (*clientv3.LeaseLeasesResponse, error)
	KeepAliveOnce(context context.Context, id clientv3.LeaseID) (*clientv3.LeaseKeepAliveResponse, error)
	Revoke(context context.Context, id clientv3.LeaseID) (*clientv3.LeaseRevokeResponse, error)

	AuthEnable(context context.Context) error
	AuthDisable(context context.Context) error
	AuthStatus(context context.Context) (*clientv3.AuthStatusResponse, error)
	UserAdd(context context.Context, name, password string, opts config.UserAddOptions) (*clientv3.AuthUserAddResponse, error)
	UserGet(context context.Context, name string) (*clientv3.AuthUserGetResponse, error)
	UserList(context context.Context) (*clientv3.AuthUserListResponse, error)
	UserDelete(context context.Context, name string) (*clientv3.AuthUserDeleteResponse, error)
	UserChangePass(context context.Context, user, newPass string) error
	UserGrantRole(context context.Context, user string, role string) (*clientv3.AuthUserGrantRoleResponse, error)
	UserRevokeRole(context context.Context, user string, role string) (*clientv3.AuthUserRevokeRoleResponse, error)
	RoleAdd(context context.Context, name string) (*clientv3.AuthRoleAddResponse, error)
	RoleGrantPermission(context context.Context, name string, key, rangeEnd string, permType clientv3.PermissionType) (*clientv3.AuthRoleGrantPermissionResponse, error)
	RoleGet(context context.Context, role string) (*clientv3.AuthRoleGetResponse, error)
	RoleList(context context.Context) (*clientv3.AuthRoleListResponse, error)
	RoleRevokePermission(context context.Context, role string, key, rangeEnd string) (*clientv3.AuthRoleRevokePermissionResponse, error)
	RoleDelete(context context.Context, role string) (*clientv3.AuthRoleDeleteResponse, error)

	Txn(context context.Context, compares, ifSucess, ifFail []string, o config.TxnOptions) (*clientv3.TxnResponse, error)

	MemberList(context context.Context, serializable bool) (*clientv3.MemberListResponse, error)
	MemberAdd(context context.Context, name string, peerAddrs []string) (*clientv3.MemberAddResponse, error)
	MemberAddAsLearner(context context.Context, name string, peerAddrs []string) (*clientv3.MemberAddResponse, error)
	MemberRemove(ctx context.Context, id uint64) (*clientv3.MemberRemoveResponse, error)

	Watch(ctx context.Context, key string, opts config.WatchOptions) clientv3.WatchChan

	UpdateAuthToken(token string)
}

type TemplateEndpoints interface {
	TemplateEndpoints(tb testing.TB, pattern string) []string
}

type AssertAuthority interface {
	AssertAuthority(tb testing.TB, expectedAuthorityPattern string)
}
