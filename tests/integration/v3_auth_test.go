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

package integration

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"go.etcd.io/etcd/api/v3/authpb"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/tests/v3/framework/integration"
)

// TestV3AuthEmptyUserGet ensures that a get with an empty user will return an empty user error.
func TestV3AuthEmptyUserGet(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	api := integration.ToGRPC(clus.Client(0))
	authSetupRoot(t, api.Auth)

	_, err := api.KV.Range(ctx, &pb.RangeRequest{Key: []byte("abc")})
	require.Truef(t, eqErrGRPC(err, rpctypes.ErrUserEmpty), "got %v, expected %v", err, rpctypes.ErrUserEmpty)
}

// TestV3AuthEmptyUserPut ensures that a put with an empty user will return an empty user error,
// and the consistent_index should be moved forward even the apply-->Put fails.
func TestV3AuthEmptyUserPut(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{
		Size:          1,
		SnapshotCount: 3,
	})
	defer clus.Terminate(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	api := integration.ToGRPC(clus.Client(0))
	authSetupRoot(t, api.Auth)

	// The SnapshotCount is 3, so there must be at least 3 new snapshot files being created.
	// The VERIFY logic will check whether the consistent_index >= last snapshot index on
	// cluster terminating.
	for i := 0; i < 10; i++ {
		_, err := api.KV.Put(ctx, &pb.PutRequest{Key: []byte("foo"), Value: []byte("bar")})
		require.Truef(t, eqErrGRPC(err, rpctypes.ErrUserEmpty), "got %v, expected %v", err, rpctypes.ErrUserEmpty)
	}
}

// TestV3AuthTokenWithDisable tests that auth won't crash if
// given a valid token when authentication is disabled
func TestV3AuthTokenWithDisable(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	authSetupRoot(t, integration.ToGRPC(clus.Client(0)).Auth)

	c, cerr := integration.NewClient(t, clientv3.Config{Endpoints: clus.Client(0).Endpoints(), Username: "root", Password: "123"})
	require.NoError(t, cerr)
	defer c.Close()

	rctx, cancel := context.WithCancel(t.Context())
	donec := make(chan struct{})
	go func() {
		defer close(donec)
		for rctx.Err() == nil {
			c.Put(rctx, "abc", "def")
		}
	}()

	time.Sleep(10 * time.Millisecond)
	_, err := c.AuthDisable(t.Context())
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)

	cancel()
	<-donec
}

func TestV3AuthRevision(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	api := integration.ToGRPC(clus.Client(0))

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	presp, perr := api.KV.Put(ctx, &pb.PutRequest{Key: []byte("foo"), Value: []byte("bar")})
	cancel()
	require.NoError(t, perr)
	rev := presp.Header.Revision

	ctx, cancel = context.WithTimeout(t.Context(), 5*time.Second)
	aresp, aerr := api.Auth.UserAdd(ctx, &pb.AuthUserAddRequest{Name: "root", Password: "123", Options: &authpb.UserAddOptions{NoPassword: false}})
	cancel()
	require.NoError(t, aerr)
	require.Equalf(t, aresp.Header.Revision, rev, "revision expected %d, got %d", rev, aresp.Header.Revision)
}

// TestV3AuthWithLeaseRevokeWithRoot ensures that granted leases
// with root user be revoked after TTL.
func TestV3AuthWithLeaseRevokeWithRoot(t *testing.T) {
	testV3AuthWithLeaseRevokeWithRoot(t, integration.ClusterConfig{Size: 1})
}

// TestV3AuthWithLeaseRevokeWithRootJWT creates a lease with a JWT-token enabled cluster.
// And tests if server is able to revoke expiry lease item.
func TestV3AuthWithLeaseRevokeWithRootJWT(t *testing.T) {
	testV3AuthWithLeaseRevokeWithRoot(t, integration.ClusterConfig{Size: 1, AuthToken: integration.DefaultTokenJWT})
}

func testV3AuthWithLeaseRevokeWithRoot(t *testing.T, ccfg integration.ClusterConfig) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &ccfg)
	defer clus.Terminate(t)

	api := integration.ToGRPC(clus.Client(0))
	authSetupRoot(t, api.Auth)

	rootc, cerr := integration.NewClient(t, clientv3.Config{
		Endpoints: clus.Client(0).Endpoints(),
		Username:  "root",
		Password:  "123",
	})
	require.NoError(t, cerr)
	defer rootc.Close()

	leaseResp, err := rootc.Grant(t.Context(), 2)
	require.NoError(t, err)
	leaseID := leaseResp.ID

	_, err = rootc.Put(t.Context(), "foo", "bar", clientv3.WithLease(leaseID))
	require.NoError(t, err)

	// wait for lease expire
	time.Sleep(3 * time.Second)

	tresp, terr := rootc.TimeToLive(
		t.Context(),
		leaseID,
		clientv3.WithAttachedKeys(),
	)
	if terr != nil {
		t.Error(terr)
	}
	if len(tresp.Keys) > 0 || tresp.GrantedTTL != 0 {
		t.Errorf("lease %016x should have been revoked, got %+v", leaseID, tresp)
	}
	if tresp.TTL != -1 {
		t.Errorf("lease %016x should have been expired, got %+v", leaseID, tresp)
	}
}

type user struct {
	name     string
	password string
	role     string
	perm     string
	key      string
	end      string
}

func TestV3AuthWithLeaseRevoke(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	users := []user{
		{
			name:     "user1",
			password: "user1-123",
			role:     "role1",
			key:      "k1",
			end:      "k2",
		},
	}
	authSetupUsers(t, integration.ToGRPC(clus.Client(0)).Auth, users)

	authSetupRoot(t, integration.ToGRPC(clus.Client(0)).Auth)

	rootc, cerr := integration.NewClient(t, clientv3.Config{Endpoints: clus.Client(0).Endpoints(), Username: "root", Password: "123"})
	require.NoError(t, cerr)
	defer rootc.Close()

	leaseResp, err := rootc.Grant(t.Context(), 90)
	require.NoError(t, err)
	leaseID := leaseResp.ID
	// permission of k3 isn't granted to user1
	_, err = rootc.Put(t.Context(), "k3", "val", clientv3.WithLease(leaseID))
	require.NoError(t, err)

	userc, cerr := integration.NewClient(t, clientv3.Config{Endpoints: clus.Client(0).Endpoints(), Username: "user1", Password: "user1-123"})
	require.NoError(t, cerr)
	defer userc.Close()
	_, err = userc.Revoke(t.Context(), leaseID)
	if err == nil {
		t.Fatal("revoking from user1 should be failed with permission denied")
	}
}

func TestV3AuthWithLeaseRenew(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	users := []user{
		{
			name:     "test-user",
			password: "test-user-123",
			role:     "test-role",
			// test-user can only write keys in [k1, k3), i.e. k1 and k2.
			key: "k1",
			end: "k3",
		},
	}
	authSetupUsers(t, integration.ToGRPC(clus.Client(0)).Auth, users)
	authSetupRoot(t, integration.ToGRPC(clus.Client(0)).Auth)

	rootCli, cerr := integration.NewClient(t, clientv3.Config{
		Endpoints: clus.Client(0).Endpoints(),
		Username:  "root",
		Password:  "123",
	})
	require.NoError(t, cerr)
	defer rootCli.Close()

	testUserClis := []*clientv3.Client{}
	for i := 0; i < len(clus.Members); i++ {
		testUserCli, err := integration.NewClient(t, clientv3.Config{
			Endpoints: clus.Client(i).Endpoints(),
			Username:  "test-user",
			Password:  "test-user-123",
		})
		require.NoError(t, err)
		defer testUserCli.Close()

		testUserClis = append(testUserClis, testUserCli)
	}

	anonCli, cerr := integration.NewClient(t, clientv3.Config{
		Endpoints: clus.Client(0).Endpoints(),
	})
	require.NoError(t, cerr)
	defer anonCli.Close()

	leaseResp, err := rootCli.Grant(t.Context(), 90)
	require.NoError(t, err)
	leaseID := leaseResp.ID

	_, err = rootCli.Put(t.Context(), "k1", "val", clientv3.WithLease(leaseID))
	require.NoError(t, err)
	_, err = rootCli.Put(t.Context(), "k3", "val", clientv3.WithLease(leaseID))
	require.NoError(t, err)

	_, err = anonCli.KeepAliveOnce(t.Context(), leaseID)
	require.ErrorContainsf(t, err, "etcdserver: user name is empty", "should reject renew")

	_, err = rootCli.KeepAliveOnce(t.Context(), leaseID)
	require.NoError(t, err)

	for _, testUserCli := range testUserClis {
		_, err = testUserCli.KeepAliveOnce(t.Context(), leaseID)
		require.ErrorContainsf(t, err, "etcdserver: permission denied", "[%v] should reject renew", testUserCli.Endpoints())
	}

	leaseResp, err = rootCli.Grant(t.Context(), 90)
	require.NoError(t, err)
	leaseID = leaseResp.ID

	_, err = rootCli.Put(t.Context(), "k1", "val", clientv3.WithLease(leaseID))
	require.NoError(t, err)
	_, err = rootCli.Put(t.Context(), "k2", "val", clientv3.WithLease(leaseID))
	require.NoError(t, err)

	for _, testUserCli := range testUserClis {
		_, err = testUserCli.KeepAliveOnce(t.Context(), leaseID)
		require.NoErrorf(t, err, "[%v] should accept renew", testUserCli.Endpoints())
	}
}

func TestV3AuthWithLeaseAttach(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	users := []user{
		{
			name:     "user1",
			password: "user1-123",
			role:     "role1",
			key:      "k1",
			end:      "k3",
		},
		{
			name:     "user2",
			password: "user2-123",
			role:     "role2",
			key:      "k2",
			end:      "k4",
		},
	}
	authSetupUsers(t, integration.ToGRPC(clus.Client(0)).Auth, users)

	authSetupRoot(t, integration.ToGRPC(clus.Client(0)).Auth)

	user1c, cerr := integration.NewClient(t, clientv3.Config{Endpoints: clus.Client(0).Endpoints(), Username: "user1", Password: "user1-123"})
	require.NoError(t, cerr)
	defer user1c.Close()

	user2c, cerr := integration.NewClient(t, clientv3.Config{Endpoints: clus.Client(0).Endpoints(), Username: "user2", Password: "user2-123"})
	require.NoError(t, cerr)
	defer user2c.Close()

	leaseResp, err := user1c.Grant(t.Context(), 90)
	require.NoError(t, err)
	leaseID := leaseResp.ID
	// permission of k2 is also granted to user2
	_, err = user1c.Put(t.Context(), "k2", "val", clientv3.WithLease(leaseID))
	require.NoError(t, err)

	_, err = user2c.Revoke(t.Context(), leaseID)
	require.NoError(t, err)

	leaseResp, err = user1c.Grant(t.Context(), 90)
	require.NoError(t, err)
	leaseID = leaseResp.ID
	// permission of k1 isn't granted to user2
	_, err = user1c.Put(t.Context(), "k1", "val", clientv3.WithLease(leaseID))
	require.NoError(t, err)

	_, err = user2c.Revoke(t.Context(), leaseID)
	if err == nil {
		t.Fatal("revoking from user2 should be failed with permission denied")
	}
}

func authSetupUsers(t *testing.T, auth pb.AuthClient, users []user) {
	for _, user := range users {
		_, err := auth.UserAdd(t.Context(), &pb.AuthUserAddRequest{Name: user.name, Password: user.password, Options: &authpb.UserAddOptions{NoPassword: false}})
		require.NoError(t, err)
		_, err = auth.RoleAdd(t.Context(), &pb.AuthRoleAddRequest{Name: user.role})
		require.NoError(t, err)
		_, err = auth.UserGrantRole(t.Context(), &pb.AuthUserGrantRoleRequest{User: user.name, Role: user.role})
		require.NoError(t, err)

		if len(user.key) == 0 {
			continue
		}

		permType := authpb.Permission_READWRITE
		if len(user.perm) > 0 {
			val, ok := authpb.Permission_Type_value[strings.ToUpper(user.perm)]
			if ok {
				permType = authpb.Permission_Type(val)
			}
		}
		perm := &authpb.Permission{
			PermType: permType,
			Key:      []byte(user.key),
			RangeEnd: []byte(user.end),
		}
		_, err = auth.RoleGrantPermission(t.Context(), &pb.AuthRoleGrantPermissionRequest{Name: user.role, Perm: perm})
		require.NoError(t, err)
	}
}

func authSetupRoot(t *testing.T, auth pb.AuthClient) {
	root := []user{
		{
			name:     "root",
			password: "123",
			role:     "root",
			key:      "",
		},
	}
	authSetupUsers(t, auth, root)
	_, err := auth.AuthEnable(t.Context(), &pb.AuthEnableRequest{})
	require.NoError(t, err)
}

func TestV3AuthNonAuthorizedRPCs(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	nonAuthedKV := clus.Client(0).KV

	key := "foo"
	val := "bar"
	_, err := nonAuthedKV.Put(t.Context(), key, val)
	require.NoErrorf(t, err, "couldn't put key (%v)", err)

	authSetupRoot(t, integration.ToGRPC(clus.Client(0)).Auth)

	respput, err := nonAuthedKV.Put(t.Context(), key, val)
	require.Truef(t, eqErrGRPC(err, rpctypes.ErrGRPCUserEmpty), "could put key (%v), it should cause an error of permission denied", respput)
}

func TestV3AuthNestedTxnPermissionDenied(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	users := []user{
		{
			name:     "user1",
			password: "user1-123",
			role:     "role1",
			key:      "foo",
			end:      "zoo",
		},
	}
	anonCli := integration.ToGRPC(clus.Client(0))
	authSetupUsers(t, anonCli.Auth, users)
	authSetupRoot(t, anonCli.Auth)

	rootc, err := integration.NewClient(t, clientv3.Config{
		Endpoints: clus.Client(0).Endpoints(),
		Username:  "root",
		Password:  "123",
	})
	require.NoError(t, err)
	defer rootc.Close()

	userc, err := integration.NewClient(t, clientv3.Config{
		Endpoints: clus.Client(0).Endpoints(),
		Username:  "user1",
		Password:  "user1-123",
	})
	require.NoError(t, err)
	defer userc.Close()

	_, err = rootc.Put(t.Context(), "boo", "bar")
	require.NoError(t, err)

	_, err = userc.Txn(t.Context()).
		Then(clientv3.OpTxn(
			nil,
			[]clientv3.Op{
				clientv3.OpTxn(
					nil,
					[]clientv3.Op{
						clientv3.OpDelete("boo"),
					},
					nil,
				),
			},
			nil,
		)).Commit()

	require.Error(t, err)
	require.Truef(t, eqErrGRPC(err, rpctypes.ErrGRPCPermissionDenied), "got %v, expected %v", err, rpctypes.ErrGRPCPermissionDenied)

	resp, err := rootc.Get(t.Context(), "boo")
	require.NoError(t, err)
	require.Len(t, resp.Kvs, 1)
	require.Equal(t, resp.Kvs[0].Value, []byte("bar"))
}

func TestReadWithPrevKvInTXN(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	users := []user{
		{
			name:     "user1",
			password: "user1-123",
			role:     "role1",
			perm:     "write",
			key:      "foo",
			end:      "zoo",
		},
	}
	anonCli := integration.ToGRPC(clus.Client(0))
	authSetupUsers(t, anonCli.Auth, users)
	authSetupRoot(t, anonCli.Auth)

	rootc, err := integration.NewClient(t, clientv3.Config{
		Endpoints: clus.Client(0).Endpoints(),
		Username:  "root",
		Password:  "123",
	})
	require.NoError(t, err)
	defer rootc.Close()

	userc, err := integration.NewClient(t, clientv3.Config{
		Endpoints: clus.Client(0).Endpoints(),
		Username:  "user1",
		Password:  "user1-123",
	})
	require.NoError(t, err)
	defer userc.Close()

	_, err = rootc.Put(t.Context(), "foo", "bar")
	require.NoError(t, err)

	_, err = userc.Txn(t.Context()).
		Then(clientv3.OpPut("foo", "new", clientv3.WithPrevKV())).
		Commit()

	require.Error(t, err)
	require.Truef(t, eqErrGRPC(err, rpctypes.ErrGRPCPermissionDenied), "got %v, expected %v", err, rpctypes.ErrGRPCPermissionDenied)
}

func TestPutWithLeaseInTXN(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	users := []user{
		{
			name:     "user1",
			password: "user1-123",
			role:     "role1",
			perm:     "write",
			key:      "foo",
			end:      "fop",
		},
	}
	anonCli := integration.ToGRPC(clus.Client(0))
	authSetupUsers(t, anonCli.Auth, users)
	authSetupRoot(t, anonCli.Auth)

	rootc, err := integration.NewClient(t, clientv3.Config{
		Endpoints: clus.Client(0).Endpoints(),
		Username:  "root",
		Password:  "123",
	})
	require.NoError(t, err)
	defer rootc.Close()

	userc, err := integration.NewClient(t, clientv3.Config{
		Endpoints: clus.Client(0).Endpoints(),
		Username:  "user1",
		Password:  "user1-123",
	})
	require.NoError(t, err)
	defer userc.Close()

	t.Log("Create a lease and attach it to a key which the user1 doesn't have permission to write")
	leaseResp, err := rootc.Grant(t.Context(), 90)
	require.NoError(t, err)
	leaseID := leaseResp.ID
	_, err = rootc.Put(t.Context(), "eoo", "bar", clientv3.WithLease(leaseID))
	require.NoError(t, err)

	_, err = userc.Txn(t.Context()).
		Then(clientv3.OpPut("foo", "new", clientv3.WithLease(leaseID))).
		Commit()

	require.Error(t, err)
	require.Truef(t, eqErrGRPC(err, rpctypes.ErrGRPCPermissionDenied), "got %v, expected %v", err, rpctypes.ErrGRPCPermissionDenied)
}

func TestV3AuthOldRevConcurrent(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	authSetupRoot(t, integration.ToGRPC(clus.Client(0)).Auth)

	c, cerr := integration.NewClient(t, clientv3.Config{
		Endpoints:   clus.Client(0).Endpoints(),
		DialTimeout: 5 * time.Second,
		Username:    "root",
		Password:    "123",
	})
	require.NoError(t, cerr)
	defer c.Close()

	var wg sync.WaitGroup
	f := func(i int) {
		defer wg.Done()
		role, user := fmt.Sprintf("test-role-%d", i), fmt.Sprintf("test-user-%d", i)
		_, err := c.RoleAdd(t.Context(), role)
		require.NoError(t, err)
		_, err = c.RoleGrantPermission(t.Context(), role, "\x00", clientv3.GetPrefixRangeEnd(""), clientv3.PermissionType(clientv3.PermReadWrite))
		require.NoError(t, err)
		_, err = c.UserAdd(t.Context(), user, "123")
		require.NoError(t, err)
		_, err = c.Put(t.Context(), "a", "b")
		assert.NoError(t, err)
	}
	// needs concurrency to trigger
	numRoles := 2
	wg.Add(numRoles)
	for i := 0; i < numRoles; i++ {
		go f(i)
	}
	wg.Wait()
}

func TestV3AuthWatchErrorAndWatchId0(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	users := []user{
		{
			name:     "user1",
			password: "user1-123",
			role:     "role1",
			key:      "k1",
			end:      "k2",
		},
	}
	authSetupUsers(t, integration.ToGRPC(clus.Client(0)).Auth, users)

	authSetupRoot(t, integration.ToGRPC(clus.Client(0)).Auth)

	c, cerr := integration.NewClient(t, clientv3.Config{Endpoints: clus.Client(0).Endpoints(), Username: "user1", Password: "user1-123"})
	require.NoError(t, cerr)
	defer c.Close()

	watchStartCh, watchEndCh := make(chan any), make(chan any)

	go func() {
		wChan := c.Watch(ctx, "k1", clientv3.WithRev(1))
		watchStartCh <- struct{}{}
		watchResponse := <-wChan
		t.Logf("watch response from k1: %v", watchResponse)
		assert.NotEmpty(t, watchResponse.Events)
		watchEndCh <- struct{}{}
	}()

	// Chan for making sure that the above goroutine invokes Watch()
	// So the above Watch() can get watch ID = 0
	<-watchStartCh

	wChan := c.Watch(ctx, "non-allowed-key", clientv3.WithRev(1))
	watchResponse := <-wChan
	require.Error(t, watchResponse.Err()) // permission denied

	_, err := c.Put(ctx, "k1", "val")
	require.NoErrorf(t, err, "Unexpected error from Put: %v", err)

	<-watchEndCh
}

// TestV3AuthRecoverFromSnapshotInvalidatesToken verifies that a simple token
// revoked by a password change is rejected on a member that caught up through a
// raft snapshot. The member keeps the token in memory across the snapshot, so
// authStore.Recover must drop it; otherwise the revoked token stays valid there.
func TestV3AuthRecoverFromSnapshotInvalidatesToken(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{
		Size:                   3,
		SnapshotCount:          10,
		SnapshotCatchUpEntries: 5,
	})
	defer clus.Terminate(t)

	// grant alice readwrite on ["/a/", "/a0") and enable auth
	api0 := integration.ToGRPC(clus.Client(0))
	authSetupUsers(t, api0.Auth, []user{{
		name: "alice", password: "alice-pass", role: "alice-role",
		perm: "readwrite", key: "/a/", end: "/a0",
	}})
	authSetupRoot(t, api0.Auth)

	// mint alice's token on member 0; it stays in that member's memory
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	aresp, err := api0.Auth.Authenticate(ctx, &pb.AuthenticateRequest{Name: "alice", Password: "alice-pass"})
	require.NoError(t, err)

	md := metadata.Pairs(rpctypes.TokenFieldNameGRPC, aresp.Token)
	tokenRange := func(kv pb.KVClient) error {
		rctx, rcancel := context.WithTimeout(metadata.NewOutgoingContext(t.Context(), md), 10*time.Second)
		defer rcancel()
		_, rerr := kv.Range(rctx, &pb.RangeRequest{Key: []byte("/a/x"), Serializable: true})
		return rerr
	}
	// the token works on member 0 before the password change
	require.NoError(t, tokenRange(rawKVClient(t, clus.Members[0])))

	// authenticate root while the cluster is healthy, before the partition
	rootc, err := integration.NewClient(t, clientv3.Config{
		Endpoints: clus.Client(1).Endpoints(), Username: "root", Password: "123"})
	require.NoError(t, err)
	defer rootc.Close()

	// isolate member 0 so it misses the password change and catches up by snapshot
	clus.Members[0].InjectPartition(t, clus.Members[1], clus.Members[2])
	survivors := []*integration.Member{clus.Members[1], clus.Members[2]}
	leaderAmong(t, survivors) // wait for the survivors to re-elect before writing

	_, err = rootc.UserChangePassword(t.Context(), "alice", "alice-new")
	require.NoError(t, err)

	// drive writes so the leader snapshots and compacts its log past member 0
	for i := 0; i < 15; i++ {
		_, err = rootc.Put(t.Context(), fmt.Sprintf("/a/fill%d", i), "v")
		require.NoError(t, err)
	}
	expectMemberLog(t, leaderAmong(t, survivors), 5*time.Second, "saved snapshot to disk", 2)

	// member 0 rejoins and restores from the leader's snapshot. Wait for the
	// auth store restore, which runs authStore.Recover; the "received ... snapshot"
	// log fires earlier, at download time, before Recover drops the tokens.
	clus.Members[0].RecoverPartition(t, clus.Members[1], clus.Members[2])
	expectMemberLog(t, clus.Members[0], 5*time.Second, "restored auth store", 1)

	// the revoked token must now be rejected on member 0. Dial member 0
	// directly: the clientv3 interceptor would loop refreshing the token on an
	// invalid-token reply, because the cluster client carries no credentials.
	err = tokenRange(rawKVClient(t, clus.Members[0]))
	require.Truef(t, eqErrGRPC(err, rpctypes.ErrGRPCInvalidAuthToken), "got %v, want %v", err, rpctypes.ErrGRPCInvalidAuthToken)
}

// rawKVClient dials the member's gRPC socket without the clientv3 interceptors.
func rawKVClient(t *testing.T, m *integration.Member) pb.KVClient {
	t.Helper()
	sock := strings.TrimPrefix(m.GRPCURL, "unix://")
	conn, err := grpc.NewClient("passthrough:///"+m.Name,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		}),
	)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	return pb.NewKVClient(conn)
}

func expectMemberLog(t *testing.T, m *integration.Member, timeout time.Duration, s string, count int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	_, err := m.LogObserver.Expect(ctx, s, count)
	require.NoErrorf(t, err, "failed to expect (log:%s, count:%v)", s, count)
}

// leaderAmong returns the member in membs that currently believes it is leader.
func leaderAmong(t *testing.T, membs []*integration.Member) *integration.Member {
	t.Helper()
	for i := 0; i < 100; i++ {
		for _, m := range membs {
			if m.Server.Lead() == uint64(m.Server.MemberID()) {
				return m
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("no leader found among members")
	return nil
}
