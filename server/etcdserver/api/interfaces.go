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

package api

import (
	"context"
	"net/http"

	"github.com/coreos/go-semver/semver"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/client/pkg/v3/types"
	"go.etcd.io/etcd/server/v3/config"
	"go.etcd.io/etcd/server/v3/etcdserver/api/membership"
	"go.etcd.io/etcd/server/v3/etcdserver/api/v2stats"
	"go.etcd.io/etcd/server/v3/lease"
)

// Cluster is an interface representing a collection of members in one etcd cluster.
type Cluster interface {
	// ID returns the cluster ID
	ID() types.ID
	// ClientURLs returns an aggregate set of all URLs on which this
	// cluster is listening for client requests
	ClientURLs() []string
	// Members returns a slice of members sorted by their ID
	Members() []*membership.Member
	// Member retrieves a particular member based on ID, or nil if the
	// member does not exist in the cluster
	Member(id types.ID) *membership.Member
	// Version is the cluster-wide minimum major.minor version.
	Version() *semver.Version

	IsMemberExist(id types.ID) bool
	IsLocalMemberLearner() bool
}

type ServerConfig interface {
	Config() config.ServerConfig
}

type BackendStats interface {
	BackendSize() int64
}

type ServerV2 interface {
	Server
	Leader() types.ID

	// Do takes a V2 request and attempts to fulfill it, returning a Response.
	Do(ctx context.Context, r etcdserverpb.Request) (Response, error)
	v2stats.Stats
	ClientCertAuthEnabled() bool
}

type ServerV3 interface {
	Server
	RaftStatusGetter
}

type Server interface {
	// AddMember attempts to add a member into the cluster. It will return
	// ErrIDRemoved if member ID is removed from the cluster, or return
	// ErrIDExists if member ID exists in the cluster.
	AddMember(ctx context.Context, memb membership.Member) ([]*membership.Member, error)
	// RemoveMember attempts to remove a member from the cluster. It will
	// return ErrIDRemoved if member ID is removed from the cluster, or return
	// ErrIDNotFound if member ID is not in the cluster.
	RemoveMember(ctx context.Context, id uint64) ([]*membership.Member, error)
	// UpdateMember attempts to update an existing member in the cluster. It will
	// return ErrIDNotFound if the member ID does not exist.
	UpdateMember(ctx context.Context, updateMemb membership.Member) ([]*membership.Member, error)
	// PromoteMember attempts to promote a non-voting node to a voting node. It will
	// return ErrIDNotFound if the member ID does not exist.
	// return ErrLearnerNotReady if the member are not ready.
	// return ErrMemberNotLearner if the member is not a learner.
	PromoteMember(ctx context.Context, id uint64) ([]*membership.Member, error)

	// ClusterVersion is the cluster-wide minimum major.minor version.
	// Cluster version is set to the min version that an etcd member is
	// compatible with when first bootstrap.
	//
	// ClusterVersion is nil until the cluster is bootstrapped (has a quorum).
	//
	// During a rolling upgrades, the ClusterVersion will be updated
	// automatically after a sync. (5 second by default)
	//
	// The API/raft component can utilize ClusterVersion to determine if
	// it can accept a client request or a raft RPC.
	// NOTE: ClusterVersion might be nil when etcd 2.1 works with etcd 2.0 and
	// the leader is etcd 2.0. etcd 2.0 leader will not update clusterVersion since
	// this feature is introduced post 2.0.
	ClusterVersion() *semver.Version
	Cluster() Cluster
	Alarms() []*etcdserverpb.AlarmMember

	// LeaderChangedNotify returns a channel for application level code to be notified
	// when etcd leader changes, this function is intend to be used only in application
	// which embed etcd.
	// Caution:
	// 1. the returned channel is being closed when the leadership changes.
	// 2. so the new channel needs to be obtained for each raft term.
	// 3. user can loose some consecutive channel changes using this API.
	LeaderChangedNotify() <-chan struct{}
}

type ServerPeer interface {
	ServerV2
	RaftHandler() http.Handler
	LeaseHandler() http.Handler
}

type ServerPeerV2 interface {
	ServerPeer
	HashKVHandler() http.Handler
	DowngradeEnabledHandler() http.Handler
}

// RaftStatusGetter represents etcd server and Raft progress.
type RaftStatusGetter interface {
	ID() types.ID
	Leader() types.ID
	CommittedIndex() uint64
	AppliedIndex() uint64
	Term() uint64
}

type RaftKV interface {
	Range(ctx context.Context, r *etcdserverpb.RangeRequest) (*etcdserverpb.RangeResponse, error)
	Put(ctx context.Context, r *etcdserverpb.PutRequest) (*etcdserverpb.PutResponse, error)
	DeleteRange(ctx context.Context, r *etcdserverpb.DeleteRangeRequest) (*etcdserverpb.DeleteRangeResponse, error)
	Txn(ctx context.Context, r *etcdserverpb.TxnRequest) (*etcdserverpb.TxnResponse, error)
	Compact(ctx context.Context, r *etcdserverpb.CompactionRequest) (*etcdserverpb.CompactionResponse, error)
}

type Lessor interface {
	// LeaseGrant sends LeaseGrant request to raft and apply it after committed.
	LeaseGrant(ctx context.Context, r *etcdserverpb.LeaseGrantRequest) (*etcdserverpb.LeaseGrantResponse, error)
	// LeaseRevoke sends LeaseRevoke request to raft and apply it after committed.
	LeaseRevoke(ctx context.Context, r *etcdserverpb.LeaseRevokeRequest) (*etcdserverpb.LeaseRevokeResponse, error)

	// LeaseRenew renews the lease with given ID. The renewed TTL is returned. Or an error
	// is returned.
	LeaseRenew(ctx context.Context, id lease.LeaseID) (int64, error)

	// LeaseTimeToLive retrieves lease information.
	LeaseTimeToLive(ctx context.Context, r *etcdserverpb.LeaseTimeToLiveRequest) (*etcdserverpb.LeaseTimeToLiveResponse, error)

	// LeaseLeases lists all leases.
	LeaseLeases(ctx context.Context, r *etcdserverpb.LeaseLeasesRequest) (*etcdserverpb.LeaseLeasesResponse, error)
}

type Authenticator interface {
	AuthEnable(ctx context.Context, r *etcdserverpb.AuthEnableRequest) (*etcdserverpb.AuthEnableResponse, error)
	AuthDisable(ctx context.Context, r *etcdserverpb.AuthDisableRequest) (*etcdserverpb.AuthDisableResponse, error)
	AuthStatus(ctx context.Context, r *etcdserverpb.AuthStatusRequest) (*etcdserverpb.AuthStatusResponse, error)
	Authenticate(ctx context.Context, r *etcdserverpb.AuthenticateRequest) (*etcdserverpb.AuthenticateResponse, error)
	UserAdd(ctx context.Context, r *etcdserverpb.AuthUserAddRequest) (*etcdserverpb.AuthUserAddResponse, error)
	UserDelete(ctx context.Context, r *etcdserverpb.AuthUserDeleteRequest) (*etcdserverpb.AuthUserDeleteResponse, error)
	UserChangePassword(ctx context.Context, r *etcdserverpb.AuthUserChangePasswordRequest) (*etcdserverpb.AuthUserChangePasswordResponse, error)
	UserGrantRole(ctx context.Context, r *etcdserverpb.AuthUserGrantRoleRequest) (*etcdserverpb.AuthUserGrantRoleResponse, error)
	UserGet(ctx context.Context, r *etcdserverpb.AuthUserGetRequest) (*etcdserverpb.AuthUserGetResponse, error)
	UserRevokeRole(ctx context.Context, r *etcdserverpb.AuthUserRevokeRoleRequest) (*etcdserverpb.AuthUserRevokeRoleResponse, error)
	RoleAdd(ctx context.Context, r *etcdserverpb.AuthRoleAddRequest) (*etcdserverpb.AuthRoleAddResponse, error)
	RoleGrantPermission(ctx context.Context, r *etcdserverpb.AuthRoleGrantPermissionRequest) (*etcdserverpb.AuthRoleGrantPermissionResponse, error)
	RoleGet(ctx context.Context, r *etcdserverpb.AuthRoleGetRequest) (*etcdserverpb.AuthRoleGetResponse, error)
	RoleRevokePermission(ctx context.Context, r *etcdserverpb.AuthRoleRevokePermissionRequest) (*etcdserverpb.AuthRoleRevokePermissionResponse, error)
	RoleDelete(ctx context.Context, r *etcdserverpb.AuthRoleDeleteRequest) (*etcdserverpb.AuthRoleDeleteResponse, error)
	UserList(ctx context.Context, r *etcdserverpb.AuthUserListRequest) (*etcdserverpb.AuthUserListResponse, error)
	RoleList(ctx context.Context, r *etcdserverpb.AuthRoleListRequest) (*etcdserverpb.AuthRoleListResponse, error)
}

// Quota represents an arbitrary quota against arbitrary requests. Each request
// costs some charge; if there is not enough remaining charge, then there are
// too few resources available within the quota to apply the request.
type Quota interface {
	// Available judges whether the given request fits within the quota.
	Available(req interface{}) bool
	// Cost computes the charge against the quota for a given request.
	Cost(req interface{}) int
	// Remaining is the amount of charge left for the quota.
	Remaining() int64
}

type GoAttacher interface {
	GoAttach(f func())
}
