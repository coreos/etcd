// Copyright 2026 The etcd Authors
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

// This file is a regression gate for the "unauthenticated endpoint" class of
// bugs.
//
// TestV3AuthMatrixCoverage enumerates every method of every registered gRPC
// service and fails if it has no entry in authProbes, so a new RPC cannot be
// merged without someone stating its required privilege level.
//
// TestV3AuthMatrixDenyByDefault then drives each entry with no token, a
// malformed token and a token for a user holding no relevant permission, and
// asserts the call is refused.

package integration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"

	"go.etcd.io/etcd/api/v3/authpb"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	epb "go.etcd.io/etcd/server/v3/etcdserver/api/v3election/v3electionpb"
	lockpb "go.etcd.io/etcd/server/v3/etcdserver/api/v3lock/v3lockpb"
	"go.etcd.io/etcd/tests/v3/framework/integration"
)

// authLevel is the privilege a caller must hold for a given request to be
// accepted. It is a property of the probe's request, not of the method: for
// example LeaseTimeToLive only performs an RBAC check when Keys is set.
type authLevel int

const (
	// levelPublic is callable with no credentials at all, even with auth
	// enabled. Only Authenticate and AuthStatus are meant to be public.
	levelPublic authLevel = iota
	// levelAuthenticated requires a valid token but performs no RBAC check.
	levelAuthenticated
	// levelRBAC requires a per-key permission covering the request.
	levelRBAC
	// levelAdmin requires the root role.
	levelAdmin
)

func (l authLevel) String() string {
	switch l {
	case levelPublic:
		return "public"
	case levelAuthenticated:
		return "authenticated"
	case levelRBAC:
		return "rbac"
	case levelAdmin:
		return "admin"
	default:
		return fmt.Sprintf("authLevel(%d)", int(l))
	}
}

const (
	// allowedKey is the only key authMatrixUser can read.
	allowedKey = "/allowed"
	// writeOnlyKey is writable but not readable by authMatrixUser, so
	// probes can tell a write that also reads apart from a plain write.
	writeOnlyKey = "/writeonly"
	// deniedKey is outside every permission granted to authMatrixUser.
	deniedKey = "/denied"
	// deniedName is used for the lock/election probes, whose keys are
	// derived from a caller-supplied prefix.
	deniedName = "/denied/name"

	authMatrixUser     = "matrix-user"
	authMatrixPass     = "matrix-pass"
	authMatrixRole     = "matrix-role"
	authMatrixRoot     = "root"
	authMatrixRootPass = "root-pass"
)

// authProbe is one request aimed at one method, together with the privilege
// that request is expected to require.
type authProbe struct {
	// method is the full gRPC method name, e.g. "/etcdserverpb.KV/Range".
	method string
	// name disambiguates multiple probes against the same method.
	name string
	// level is the privilege this request must require.
	level authLevel
	// req is the request message, or the first message for a stream.
	req proto.Message
	// build supplies req lazily when it depends on cluster state.
	build func(t *testing.T, e *authMatrixEnv) proto.Message

	clientStreams bool
	serverStreams bool

	// emptyStreamIsDenial accepts a clean end-of-stream as a refusal. Some
	// streaming handlers drop the authorization error and close the stream
	// instead of failing the RPC, so the caller cannot tell "not permitted"
	// apart from "nothing to report". No data escapes, but the asymmetry is
	// worth recording rather than papering over.
	emptyStreamIsDenial bool
}

func (p authProbe) id() string {
	if p.name != "" {
		return p.method + "#" + p.name
	}
	return p.method
}

// authMatrixServices is every gRPC service etcd registers on the client port.
// TestV3AuthMatrixCoverage walks these descriptors, so adding a service here
// is the only manual step needed when a new API surface appears.
var authMatrixServices = []*grpc.ServiceDesc{
	&pb.KV_ServiceDesc,
	&pb.Watch_ServiceDesc,
	&pb.Lease_ServiceDesc,
	&pb.Cluster_ServiceDesc,
	&pb.Maintenance_ServiceDesc,
	&pb.Auth_ServiceDesc,
	&lockpb.Lock_ServiceDesc,
	&epb.Election_ServiceDesc,
}

// authProbes records the privilege every reachable request requires, with the
// enforcement site that provides it. Every method in authMatrixServices must
// appear at least once.
func authProbes() []authProbe {
	return []authProbe{
		// ---- etcdserverpb.KV ----------------------------------------
		// v3_server.go Range -> doSerialize for both read modes;
		// Serializable only skips LinearizableReadNotify.
		{
			method: "/etcdserverpb.KV/Range", level: levelRBAC,
			req: &pb.RangeRequest{Key: []byte(deniedKey)},
		},
		{
			// Guards against a future fast path for local reads that
			// skips the check.
			method: "/etcdserverpb.KV/Range", name: "serializable", level: levelRBAC,
			req: &pb.RangeRequest{Key: []byte(deniedKey), Serializable: true},
		},
		{
			// A range starting inside the permitted key but running past
			// it must not leak the tail. This is the shape of the watch
			// bug fixed in 7cf71ec9e.
			method: "/etcdserverpb.KV/Range", name: "range-end-escapes-permission", level: levelRBAC,
			req: &pb.RangeRequest{Key: []byte(allowedKey), RangeEnd: []byte{0}},
		},
		{
			method: "/etcdserverpb.KV/RangeStream", level: levelRBAC,
			req:           &pb.RangeRequest{Key: []byte(deniedKey)},
			serverStreams: true,
		},
		{
			method: "/etcdserverpb.KV/RangeStream", name: "range-end-escapes-permission", level: levelRBAC,
			req:           &pb.RangeRequest{Key: []byte(allowedKey), RangeEnd: []byte{0}},
			serverStreams: true,
		},
		// apply/auth.go authApplierV3.Put.
		{
			method: "/etcdserverpb.KV/Put", level: levelRBAC,
			req: &pb.PutRequest{Key: []byte(deniedKey), Value: []byte("v")},
		},
		{
			// PrevKv upgrades a write into a read of the same key;
			// checkPutAuth must demand both. writeOnlyKey passes the
			// write check, so only the read check can refuse this.
			method: "/etcdserverpb.KV/Put", name: "prev-kv-needs-read", level: levelRBAC,
			req: &pb.PutRequest{Key: []byte(writeOnlyKey), Value: []byte("v"), PrevKv: true},
		},
		{
			// Attaching a lease that holds a key the caller cannot write
			// would let it revoke that key; checkLeasePuts must refuse it
			// even though the target key itself is writable.
			method: "/etcdserverpb.KV/Put", name: "lease-holding-denied-key", level: levelRBAC,
			build: func(_ *testing.T, e *authMatrixEnv) proto.Message {
				return &pb.PutRequest{Key: []byte(allowedKey), Value: []byte("v"), Lease: e.deniedLease}
			},
		},
		// apply/auth.go authApplierV3.DeleteRange.
		{
			method: "/etcdserverpb.KV/DeleteRange", level: levelRBAC,
			req: &pb.DeleteRangeRequest{Key: []byte(deniedKey)},
		},
		{
			method: "/etcdserverpb.KV/DeleteRange", name: "range-end-escapes-permission", level: levelRBAC,
			req: &pb.DeleteRangeRequest{Key: []byte(allowedKey), RangeEnd: []byte{0}},
		},
		// apply/auth.go CheckTxnAuth.
		{
			method: "/etcdserverpb.KV/Txn", name: "compare", level: levelRBAC,
			req: &pb.TxnRequest{
				Compare: []*pb.Compare{{
					Key:         []byte(deniedKey),
					Target:      pb.Compare_VERSION,
					Result:      pb.Compare_EQUAL,
					TargetUnion: &pb.Compare_Version{Version: 0},
				}},
			},
		},
		{
			method: "/etcdserverpb.KV/Txn", name: "success-op", level: levelRBAC,
			req: &pb.TxnRequest{Success: []*pb.RequestOp{{
				Request: &pb.RequestOp_RequestPut{
					RequestPut: &pb.PutRequest{Key: []byte(deniedKey), Value: []byte("v")},
				},
			}}},
		},
		{
			method: "/etcdserverpb.KV/Txn", name: "failure-op", level: levelRBAC,
			req: &pb.TxnRequest{Failure: []*pb.RequestOp{{
				Request: &pb.RequestOp_RequestDeleteRange{
					RequestDeleteRange: &pb.DeleteRangeRequest{Key: []byte(deniedKey)},
				},
			}}},
		},
		{
			// The txn applier rewrites "\x00" via mkGteRange after the
			// check runs; each op must be checked against the wire form.
			method: "/etcdserverpb.KV/Txn", name: "compare-range-end-escapes-permission", level: levelRBAC,
			req: &pb.TxnRequest{
				Compare: []*pb.Compare{{
					Key:         []byte(allowedKey),
					RangeEnd:    []byte{0},
					Target:      pb.Compare_VERSION,
					Result:      pb.Compare_EQUAL,
					TargetUnion: &pb.Compare_Version{Version: 0},
				}},
			},
		},
		{
			method: "/etcdserverpb.KV/Txn", name: "range-op-range-end-escapes-permission", level: levelRBAC,
			req: &pb.TxnRequest{Success: []*pb.RequestOp{{
				Request: &pb.RequestOp_RequestRange{
					RequestRange: &pb.RangeRequest{Key: []byte(allowedKey), RangeEnd: []byte{0}},
				},
			}}},
		},
		{
			method: "/etcdserverpb.KV/Txn", name: "delete-op-range-end-escapes-permission", level: levelRBAC,
			req: &pb.TxnRequest{Success: []*pb.RequestOp{{
				Request: &pb.RequestOp_RequestDeleteRange{
					RequestDeleteRange: &pb.DeleteRangeRequest{Key: []byte(allowedKey), RangeEnd: []byte{0}},
				},
			}}},
		},
		{
			// The same PrevKv read inside a txn, which checkTxnReqsPermission
			// once checked with IsPutPermitted alone (fixed in 70a2b4871).
			method: "/etcdserverpb.KV/Txn", name: "put-op-prev-kv-needs-read", level: levelRBAC,
			req: &pb.TxnRequest{Success: []*pb.RequestOp{{
				Request: &pb.RequestOp_RequestPut{
					RequestPut: &pb.PutRequest{Key: []byte(writeOnlyKey), Value: []byte("v"), PrevKv: true},
				},
			}}},
		},
		{
			// The same lease attachment inside a txn, also fixed in 70a2b4871.
			method: "/etcdserverpb.KV/Txn", name: "put-op-lease-holding-denied-key", level: levelRBAC,
			build: func(_ *testing.T, e *authMatrixEnv) proto.Message {
				return &pb.TxnRequest{Success: []*pb.RequestOp{{
					Request: &pb.RequestOp_RequestPut{
						RequestPut: &pb.PutRequest{Key: []byte(allowedKey), Value: []byte("v"), Lease: e.deniedLease},
					},
				}}}
			},
		},
		{
			// checkTxnPermission recurses; a nested txn must not escape it.
			method: "/etcdserverpb.KV/Txn", name: "nested-txn", level: levelRBAC,
			req: &pb.TxnRequest{Success: []*pb.RequestOp{{
				Request: &pb.RequestOp_RequestTxn{
					RequestTxn: &pb.TxnRequest{Success: []*pb.RequestOp{{
						Request: &pb.RequestOp_RequestRange{
							RequestRange: &pb.RangeRequest{Key: []byte(deniedKey)},
						},
					}}},
				},
			}}},
		},
		// v3rpc/key.go kvServer.Compact -> AuthAdmin.isPermitted.
		{
			method: "/etcdserverpb.KV/Compact", level: levelAdmin,
			req: &pb.CompactionRequest{Revision: 1},
		},

		// ---- etcdserverpb.Watch -------------------------------------
		// v3rpc/watch.go isWatchPermitted. Denials arrive in-band as a
		// WatchResponse with Canceled set, not as a gRPC status.
		{
			method: "/etcdserverpb.Watch/Watch", level: levelRBAC,
			req: &pb.WatchRequest{RequestUnion: &pb.WatchRequest_CreateRequest{
				CreateRequest: &pb.WatchCreateRequest{Key: []byte(deniedKey)},
			}},
			clientStreams: true, serverStreams: true,
		},
		{
			method: "/etcdserverpb.Watch/Watch", name: "range-end-escapes-permission", level: levelRBAC,
			req: &pb.WatchRequest{RequestUnion: &pb.WatchRequest_CreateRequest{
				CreateRequest: &pb.WatchCreateRequest{Key: []byte(allowedKey), RangeEnd: []byte{0}},
			}},
			clientStreams: true, serverStreams: true,
		},

		// ---- etcdserverpb.Lease -------------------------------------
		// v3_server.go LeaseGrant -> requireAuthInfo only.
		{
			method: "/etcdserverpb.Lease/LeaseGrant", level: levelAuthenticated,
			req: &pb.LeaseGrantRequest{TTL: 60},
		},
		// v3_server.go LeaseRevoke -> requireAuthInfo, then
		// apply/auth.go checkLeasePuts over the attached keys.
		{
			method: "/etcdserverpb.Lease/LeaseRevoke", name: "unknown-lease", level: levelAuthenticated,
			req: &pb.LeaseRevokeRequest{ID: 1},
		},
		{
			method: "/etcdserverpb.Lease/LeaseRevoke", name: "lease-holding-denied-key", level: levelRBAC,
			build: func(_ *testing.T, e *authMatrixEnv) proto.Message {
				return &pb.LeaseRevokeRequest{ID: e.deniedLease}
			},
		},
		// v3_server.go checkLeaseRenew.
		{
			method: "/etcdserverpb.Lease/LeaseKeepAlive", name: "lease-holding-denied-key", level: levelRBAC,
			build: func(_ *testing.T, e *authMatrixEnv) proto.Message {
				return &pb.LeaseKeepAliveRequest{ID: e.deniedLease}
			},
			clientStreams: true, serverStreams: true,
		},
		// v3_server.go LeaseTimeToLive: RBAC only when Keys is set.
		{
			method: "/etcdserverpb.Lease/LeaseTimeToLive", name: "no-keys", level: levelAuthenticated,
			build: func(_ *testing.T, e *authMatrixEnv) proto.Message {
				return &pb.LeaseTimeToLiveRequest{ID: e.deniedLease}
			},
		},
		{
			method: "/etcdserverpb.Lease/LeaseTimeToLive", name: "keys", level: levelRBAC,
			build: func(_ *testing.T, e *authMatrixEnv) proto.Message {
				return &pb.LeaseTimeToLiveRequest{ID: e.deniedLease, Keys: true}
			},
		},
		// v3_server.go checkLeaseLeases walks every lease in the cluster,
		// and deniedLease holds a key this user cannot read.
		{
			method: "/etcdserverpb.Lease/LeaseLeases", level: levelRBAC,
			req: &pb.LeaseLeasesRequest{},
		},

		// ---- etcdserverpb.Cluster -----------------------------------
		// server.go checkMembershipOperationPermission.
		{
			method: "/etcdserverpb.Cluster/MemberAdd", level: levelAdmin,
			req: &pb.MemberAddRequest{PeerURLs: []string{"http://127.0.0.1:1"}},
		},
		{
			method: "/etcdserverpb.Cluster/MemberRemove", level: levelAdmin,
			req: &pb.MemberRemoveRequest{ID: 1},
		},
		{
			method: "/etcdserverpb.Cluster/MemberUpdate", level: levelAdmin,
			req: &pb.MemberUpdateRequest{ID: 1, PeerURLs: []string{"http://127.0.0.1:1"}},
		},
		{
			method: "/etcdserverpb.Cluster/MemberPromote", level: levelAdmin,
			req: &pb.MemberPromoteRequest{ID: 1},
		},
		// server.go MemberList -> requireAuthInfo. Relaxed from admin in
		// a2987fdee so non-root clients can discover endpoints.
		{
			method: "/etcdserverpb.Cluster/MemberList", level: levelAuthenticated,
			req: &pb.MemberListRequest{},
		},

		// ---- etcdserverpb.Maintenance -------------------------------
		// v3rpc/maintenance.go authMaintenanceServer.
		{
			method: "/etcdserverpb.Maintenance/Alarm", name: "get", level: levelAuthenticated,
			req: &pb.AlarmRequest{Action: pb.AlarmRequest_GET},
		},
		{
			method: "/etcdserverpb.Maintenance/Alarm", name: "activate", level: levelAdmin,
			req: &pb.AlarmRequest{Action: pb.AlarmRequest_ACTIVATE, Alarm: pb.AlarmType_NOSPACE},
		},
		// Relaxed from admin in 114f6ad80.
		{
			method: "/etcdserverpb.Maintenance/Status", level: levelAuthenticated,
			req: &pb.StatusRequest{},
		},
		{
			method: "/etcdserverpb.Maintenance/Defragment", level: levelAdmin,
			req: &pb.DefragmentRequest{},
		},
		{
			method: "/etcdserverpb.Maintenance/Hash", level: levelAdmin,
			req: &pb.HashRequest{},
		},
		{
			method: "/etcdserverpb.Maintenance/HashKV", level: levelAdmin,
			req: &pb.HashKVRequest{Revision: 0},
		},
		{
			method: "/etcdserverpb.Maintenance/Snapshot", level: levelAdmin,
			req:           &pb.SnapshotRequest{},
			serverStreams: true,
		},
		{
			method: "/etcdserverpb.Maintenance/MoveLeader", level: levelAdmin,
			req: &pb.MoveLeaderRequest{TargetID: 1},
		},
		{
			method: "/etcdserverpb.Maintenance/Downgrade", level: levelAdmin,
			req: &pb.DowngradeRequest{Action: pb.DowngradeRequest_VALIDATE, Version: "3.5"},
		},

		// ---- etcdserverpb.Auth --------------------------------------
		// Mutations are gated post-raft by apply/auth.go needAdminPermission.
		{
			method: "/etcdserverpb.Auth/AuthEnable", level: levelAdmin,
			req: &pb.AuthEnableRequest{},
		},
		{
			method: "/etcdserverpb.Auth/AuthDisable", level: levelAdmin,
			req: &pb.AuthDisableRequest{},
		},
		{
			// Relaxed from admin to public in 61cffd5e2 (#20802) so clients
			// that mint their own JWTs can read the auth revision without
			// holding a valid token.
			method: "/etcdserverpb.Auth/AuthStatus", level: levelPublic,
			req: &pb.AuthStatusRequest{},
		},
		{
			// Must stay reachable without a token: it is how tokens are issued.
			method: "/etcdserverpb.Auth/Authenticate", level: levelPublic,
			req: &pb.AuthenticateRequest{Name: authMatrixUser, Password: authMatrixPass},
		},
		{
			method: "/etcdserverpb.Auth/UserAdd", level: levelAdmin,
			req: &pb.AuthUserAddRequest{Name: "intruder", Password: "p", Options: &authpb.UserAddOptions{NoPassword: false}},
		},
		{
			method: "/etcdserverpb.Auth/UserDelete", level: levelAdmin,
			req: &pb.AuthUserDeleteRequest{Name: authMatrixRoot},
		},
		{
			method: "/etcdserverpb.Auth/UserChangePassword", level: levelAdmin,
			req: &pb.AuthUserChangePasswordRequest{Name: authMatrixRoot, Password: "owned"},
		},
		{
			method: "/etcdserverpb.Auth/UserGrantRole", level: levelAdmin,
			req: &pb.AuthUserGrantRoleRequest{User: authMatrixUser, Role: authMatrixRoot},
		},
		{
			method: "/etcdserverpb.Auth/UserRevokeRole", level: levelAdmin,
			req: &pb.AuthUserRevokeRoleRequest{Name: authMatrixRoot, Role: authMatrixRoot},
		},
		{
			method: "/etcdserverpb.Auth/UserList", level: levelAdmin,
			req: &pb.AuthUserListRequest{},
		},
		// apply/auth.go UserGet allows a non-root user to read only itself.
		{
			method: "/etcdserverpb.Auth/UserGet", name: "other-user", level: levelAdmin,
			req: &pb.AuthUserGetRequest{Name: authMatrixRoot},
		},
		{
			method: "/etcdserverpb.Auth/RoleAdd", level: levelAdmin,
			req: &pb.AuthRoleAddRequest{Name: "intruder-role"},
		},
		{
			method: "/etcdserverpb.Auth/RoleDelete", level: levelAdmin,
			req: &pb.AuthRoleDeleteRequest{Role: authMatrixRoot},
		},
		{
			method: "/etcdserverpb.Auth/RoleList", level: levelAdmin,
			req: &pb.AuthRoleListRequest{},
		},
		// apply/auth.go RoleGet allows reading a role the caller holds.
		{
			method: "/etcdserverpb.Auth/RoleGet", name: "other-role", level: levelAdmin,
			req: &pb.AuthRoleGetRequest{Role: authMatrixRoot},
		},
		{
			method: "/etcdserverpb.Auth/RoleGrantPermission", level: levelAdmin,
			req: &pb.AuthRoleGrantPermissionRequest{
				Name: authMatrixRole,
				Perm: &authpb.Permission{PermType: authpb.Permission_READWRITE, Key: []byte{0}, RangeEnd: []byte{0}},
			},
		},
		{
			method: "/etcdserverpb.Auth/RoleRevokePermission", level: levelAdmin,
			req: &pb.AuthRoleRevokePermissionRequest{Role: authMatrixRole, Key: []byte(allowedKey)},
		},

		// ---- v3lockpb.Lock ------------------------------------------
		// These servers contain no auth code. They are protected only
		// because v3client's in-process adapters forward the incoming
		// gRPC context, so the token is still visible to
		// AuthInfoFromCtx when the call reaches the KV layer.
		{
			method: "/v3lockpb.Lock/Lock", level: levelRBAC,
			req: &lockpb.LockRequest{Name: []byte(deniedName)},
		},
		{
			method: "/v3lockpb.Lock/Unlock", level: levelRBAC,
			req: &lockpb.UnlockRequest{Key: []byte(deniedName + "/1")},
		},

		// ---- v3electionpb.Election ----------------------------------
		{
			method: "/v3electionpb.Election/Campaign", level: levelRBAC,
			req: &epb.CampaignRequest{Name: []byte(deniedName), Value: []byte("v")},
		},
		{
			method: "/v3electionpb.Election/Proclaim", level: levelRBAC,
			req: &epb.ProclaimRequest{
				Leader: &epb.LeaderKey{Name: []byte(deniedName), Key: []byte(deniedName + "/1"), Rev: 1, Lease: 1},
				Value:  []byte("v"),
			},
		},
		{
			method: "/v3electionpb.Election/Leader", level: levelRBAC,
			req: &epb.LeaderRequest{Name: []byte(deniedName)},
		},
		{
			method: "/v3electionpb.Election/Resign", level: levelRBAC,
			req: &epb.ResignRequest{
				Leader: &epb.LeaderKey{Name: []byte(deniedName), Key: []byte(deniedName + "/1"), Rev: 1, Lease: 1},
			},
		},
		{
			// concurrency.Election.observe discards any error from its
			// initial Get and closes the channel (client/v3/concurrency/
			// election.go), so v3election.Observe returns nil and the
			// caller sees a successful, empty stream instead of a refusal.
			// A permitted caller instead blocks waiting for a leader, so a
			// clean close still means the Get was refused.
			method: "/v3electionpb.Election/Observe", level: levelRBAC,
			req:                 &epb.LeaderRequest{Name: []byte(deniedName)},
			serverStreams:       true,
			emptyStreamIsDenial: true,
		},
	}
}

// TestV3AuthMatrixCoverage fails when a registered gRPC method has no entry in
// authProbes. It needs no cluster, so it runs in milliseconds and is the gate
// that stops an unguarded endpoint from being merged.
func TestV3AuthMatrixCoverage(t *testing.T) {
	registered := map[string]struct{}{}
	for _, sd := range authMatrixServices {
		for _, m := range sd.Methods {
			registered["/"+sd.ServiceName+"/"+m.MethodName] = struct{}{}
		}
		for _, s := range sd.Streams {
			registered["/"+sd.ServiceName+"/"+s.StreamName] = struct{}{}
		}
	}

	probed := map[string]struct{}{}
	for _, p := range authProbes() {
		probed[p.method] = struct{}{}
	}

	for m := range registered {
		if _, ok := probed[m]; !ok {
			t.Errorf("gRPC method %s has no entry in authProbes. Every RPC must "+
				"declare the privilege it requires; add a probe stating its "+
				"authLevel and the enforcement site that provides it.", m)
		}
	}
	for m := range probed {
		if _, ok := registered[m]; !ok {
			t.Errorf("authProbes references %s, which is not a registered gRPC method", m)
		}
	}
}

// TestV3AuthMatrixDenyByDefault drives every probe with credentials that must
// not satisfy it.
func TestV3AuthMatrixDenyByDefault(t *testing.T) {
	integration.BeforeTest(t)
	e := newAuthMatrixEnv(t)

	callers := []struct {
		name string
		// token is resolved lazily because it depends on env setup.
		token func() string
		// grants is the highest level this caller satisfies.
		grants authLevel
		// checkAccepted also asserts the converse: that a probe this caller
		// does clear is not refused. Only the real principal does this. A
		// garbage token is legitimately rejected by anything that parses
		// tokens, yet accepted by Authenticate, which never looks at one, so
		// neither outcome would mean anything.
		checkAccepted bool
	}{
		{name: "no-token", token: func() string { return "" }, grants: levelPublic},
		{name: "invalid-token", token: func() string { return "not-a-real-token" }, grants: levelPublic},
		{name: "unprivileged-user", token: func() string { return e.userToken }, grants: levelAuthenticated, checkAccepted: true},
	}

	for _, p := range authProbes() {
		for _, c := range callers {
			t.Run(p.id()+"/"+c.name, func(t *testing.T) {
				req := p.req
				if p.build != nil {
					req = p.build(t, e)
				}
				require.NotNilf(t, req, "probe %s has neither req nor build", p.id())

				ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
				defer cancel()

				err := e.call(ctx, p, req, c.token())
				denied := isAuthDenial(err) ||
					(p.emptyStreamIsDenial && errors.Is(err, errEmptyStream))

				if p.level > c.grants {
					require.Truef(t, denied,
						"%s must be refused for caller %q (requires %s), got err=%v",
						p.id(), c.name, p.level, err)
					return
				}
				if !c.checkAccepted {
					return
				}
				// The caller clears the bar, so the request must fail for
				// some other reason or not at all. An auth error here means
				// the probe's declared level is wrong, or the server got
				// stricter and the table needs updating.
				require.Falsef(t, denied,
					"%s was refused for caller %q even though it only requires %s: %v",
					p.id(), c.name, p.level, err)
			})
		}
	}
}

// --- harness ---------------------------------------------------------------

type authMatrixEnv struct {
	conn *grpc.ClientConn

	rootToken string
	userToken string

	// deniedLease is a lease held by root that is attached to deniedKey, so
	// the lease RBAC paths have something the unprivileged user cannot touch.
	deniedLease int64
}

func newAuthMatrixEnv(t *testing.T) *authMatrixEnv {
	t.Helper()

	// UseTCP so the member has a plain host:port gRPC address we can dial
	// directly. The probes must not go through clientv3's connection: its
	// retry interceptor treats ErrInvalidAuthToken and ErrAuthOldRevision as
	// a cue to refresh the token and retry (client/v3/retry_interceptor.go
	// shouldRefreshToken), which would rewrite the very errors under test.
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1, UseTCP: true})
	t.Cleanup(func() { clus.Terminate(t) })

	target := strings.TrimPrefix(clus.Members[0].GRPCURL, "http://")
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	e := &authMatrixEnv{conn: conn}

	authc := pb.NewAuthClient(conn)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	// Unprivileged user: read-write on allowedKey and write-only on
	// writeOnlyKey, so it is a real authenticated principal rather than a
	// user with an empty role.
	authSetupUsers(t, authc, []user{{
		name:     authMatrixUser,
		password: authMatrixPass,
		role:     authMatrixRole,
		perm:     "readwrite",
		key:      allowedKey,
	}})
	_, err = authc.RoleGrantPermission(ctx, &pb.AuthRoleGrantPermissionRequest{
		Name: authMatrixRole,
		Perm: &authpb.Permission{PermType: authpb.Permission_WRITE, Key: []byte(writeOnlyKey)},
	})
	require.NoError(t, err)
	authSetupUsers(t, authc, []user{{
		name:     authMatrixRoot,
		password: authMatrixRootPass,
		role:     "root",
	}})

	kvc := pb.NewKVClient(conn)

	// A previous value for the PrevKv probes to leak.
	_, err = kvc.Put(ctx, &pb.PutRequest{Key: []byte(writeOnlyKey), Value: []byte("secret")})
	require.NoError(t, err)

	// A lease attached to deniedKey, created before auth is enabled.
	lgr, err := pb.NewLeaseClient(conn).LeaseGrant(ctx, &pb.LeaseGrantRequest{TTL: 3600})
	require.NoError(t, err)
	_, err = kvc.Put(ctx, &pb.PutRequest{
		Key: []byte(deniedKey), Value: []byte("secret"), Lease: lgr.ID,
	})
	require.NoError(t, err)
	e.deniedLease = lgr.ID

	_, err = authc.AuthEnable(ctx, &pb.AuthEnableRequest{})
	require.NoError(t, err)

	rootResp, err := authc.Authenticate(ctx, &pb.AuthenticateRequest{Name: authMatrixRoot, Password: authMatrixRootPass})
	require.NoError(t, err)
	e.rootToken = rootResp.Token

	userResp, err := authc.Authenticate(ctx, &pb.AuthenticateRequest{Name: authMatrixUser, Password: authMatrixPass})
	require.NoError(t, err)
	e.userToken = userResp.Token

	// The PrevKv probes are only meaningful if the plain write succeeds and
	// the plain read does not; otherwise they pass for the wrong reason.
	userCtx := metadata.AppendToOutgoingContext(ctx, rpctypes.TokenFieldNameGRPC, e.userToken)
	_, err = kvc.Put(userCtx, &pb.PutRequest{Key: []byte(writeOnlyKey), Value: []byte("secret")})
	require.NoErrorf(t, err, "%s must be writable by %s", writeOnlyKey, authMatrixUser)
	_, err = kvc.Range(userCtx, &pb.RangeRequest{Key: []byte(writeOnlyKey)})
	require.Truef(t, isAuthDenial(err), "%s must not be readable by %s, got err=%v", writeOnlyKey, authMatrixUser, err)

	return e
}

// call issues one probe and returns the error the caller observes, normalising
// the in-band cancellation that Watch uses instead of a gRPC status.
func (e *authMatrixEnv) call(ctx context.Context, p authProbe, req proto.Message, token string) error {
	if token != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, rpctypes.TokenFieldNameGRPC, token)
	}

	reply, err := newReplyFor(p.method)
	if err != nil {
		return err
	}

	if !p.clientStreams && !p.serverStreams {
		if err := e.conn.Invoke(ctx, p.method, req, reply); err != nil {
			return err
		}
		return inbandCancelError(reply)
	}

	desc := &grpc.StreamDesc{
		StreamName:    p.method[strings.LastIndex(p.method, "/")+1:],
		ClientStreams: p.clientStreams,
		ServerStreams: p.serverStreams,
	}
	cs, err := e.conn.NewStream(ctx, desc, p.method)
	if err != nil {
		return err
	}
	if err := cs.SendMsg(req); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if err := cs.CloseSend(); err != nil {
		return err
	}
	if err := cs.RecvMsg(reply); err != nil {
		if errors.Is(err, io.EOF) {
			// The handler returned nil without sending anything.
			return errEmptyStream
		}
		return err
	}
	return inbandCancelError(reply)
}

// errEmptyStream marks a stream the server closed cleanly without sending a
// message. See authProbe.emptyStreamIsDenial.
var errEmptyStream = errors.New("server closed the stream without sending a message")

// newReplyFor builds an empty reply of the right type straight from the
// descriptor registry, so the table never has to name response types.
func newReplyFor(fullMethod string) (proto.Message, error) {
	name, err := methodFullName(fullMethod)
	if err != nil {
		return nil, err
	}
	d, err := protoregistry.GlobalFiles.FindDescriptorByName(name)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", name, err)
	}
	md, ok := d.(protoreflect.MethodDescriptor)
	if !ok {
		return nil, fmt.Errorf("%s is not a method descriptor", name)
	}
	return dynamicpb.NewMessage(md.Output()), nil
}

func methodFullName(fullMethod string) (protoreflect.FullName, error) {
	parts := strings.SplitN(strings.TrimPrefix(fullMethod, "/"), "/", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("malformed gRPC method %q", fullMethod)
	}
	return protoreflect.FullName(parts[0] + "." + parts[1]), nil
}

// inbandCancelError turns a response carrying canceled/cancel_reason into an
// error. v3rpc/watch.go reports permission failures this way instead of
// failing the RPC, which would otherwise read as success here.
func inbandCancelError(reply proto.Message) error {
	m := reply.ProtoReflect()
	fields := m.Descriptor().Fields()

	canceled := fields.ByName("canceled")
	if canceled == nil || canceled.Kind() != protoreflect.BoolKind || !m.Get(canceled).Bool() {
		return nil
	}
	reason := "canceled"
	if rf := fields.ByName("cancel_reason"); rf != nil && rf.Kind() == protoreflect.StringKind {
		if s := m.Get(rf).String(); s != "" {
			reason = s
		}
	}
	return errors.New(reason)
}

// authDenialMessages are the error descriptions that mean "refused for
// authorization reasons", as opposed to any other failure.
var authDenialMessages = func() []string {
	var msgs []string
	for _, err := range []error{
		rpctypes.ErrGRPCPermissionDenied,
		rpctypes.ErrGRPCUserEmpty,
		rpctypes.ErrGRPCUserNotFound,
		rpctypes.ErrGRPCInvalidAuthToken,
		rpctypes.ErrGRPCAuthOldRevision,
		rpctypes.ErrGRPCAuthNotEnabled,
	} {
		msgs = append(msgs, status.Convert(err).Message())
	}
	return msgs
}()

// isAuthDenial reports whether err means "refused for authorization reasons"
// rather than any other failure. Matching is by substring because Watch puts
// the whole formatted status ("rpc error: code = ... desc = ...") into
// WatchResponse.CancelReason rather than returning a status.
func isAuthDenial(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	if s, ok := status.FromError(err); ok {
		msg = s.Message()
	}
	for _, denial := range authDenialMessages {
		if strings.Contains(msg, denial) {
			return true
		}
	}
	return false
}
