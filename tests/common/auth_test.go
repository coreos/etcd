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

package common

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/storage/mvcc/testutil"
	"go.etcd.io/etcd/tests/v3/framework/config"
	"go.etcd.io/etcd/tests/v3/framework/testutils"
)

var (
	tokenTTL          = time.Second * 3
	defaultSignMethod = "RS256"
	defaultAuthToken  = fmt.Sprintf("jwt,pub-key=%s,priv-key=%s,sign-method=%s,ttl=%s",
		mustAbsPath("../fixtures/server.crt"), mustAbsPath("../fixtures/server.key.insecure"), defaultSignMethod, tokenTTL)
	defaultKeyPath    = mustAbsPath("../fixtures/server.key.insecure")
	verifyJWTOnlyAuth = fmt.Sprintf("jwt,pub-key=%s,sign-method=%s,ttl=%s",
		mustAbsPath("../fixtures/server.crt"), defaultSignMethod, tokenTTL)
)

const (
	PermissionDenied      = "etcdserver: permission denied"
	AuthenticationFailed  = "etcdserver: authentication failed, invalid user ID or password"
	InvalidAuthManagement = "etcdserver: invalid auth management"
	InvalidAuthToken      = "etcdserver: invalid auth token"

	testPeerURL = "http://localhost:20011"
)

func TestAuthEnable(t *testing.T) {
	testRunner.BeforeTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(config.ClusterConfig{ClusterSize: 1}))
	defer clus.Close()
	cc := testutils.MustClient(clus.Client())
	testutils.ExecuteUntil(ctx, t, func() {
		require.NoErrorf(t, setupAuth(cc, []authRole{}, []authUser{rootUser}), "failed to enable auth")
	})
}

func TestAuthDisable(t *testing.T) {
	testRunner.BeforeTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(config.ClusterConfig{ClusterSize: 1}))
	defer clus.Close()
	cc := testutils.MustClient(clus.Client())
	testutils.ExecuteUntil(ctx, t, func() {
		_, err := cc.Put(ctx, "hoo", "a", config.PutOptions{})
		require.NoError(t, err)
		require.NoErrorf(t, setupAuth(cc, []authRole{testRole}, []authUser{rootUser, testUser}), "failed to enable auth")

		rootAuthClient := testutils.MustClient(clus.Client(WithAuth(rootUserName, rootPassword)))
		testUserAuthClient := testutils.MustClient(clus.Client(WithAuth(testUserName, testPassword)))

		// test-user doesn't have the permission, it must fail
		_, err = testUserAuthClient.Put(ctx, "hoo", "bar", config.PutOptions{})
		require.Error(t, err)
		require.NoErrorf(t, rootAuthClient.AuthDisable(ctx), "failed to disable auth")
		// now ErrAuthNotEnabled of Authenticate() is simply ignored
		_, err = testUserAuthClient.Put(ctx, "hoo", "bar", config.PutOptions{})
		require.NoError(t, err)
		// now the key can be accessed
		_, err = testUserAuthClient.Put(ctx, "hoo", "bar", config.PutOptions{})
		require.NoError(t, err)
		// confirm put succeeded
		resp, err := cc.Get(ctx, "hoo", config.GetOptions{})
		require.NoError(t, err)
		require.Lenf(t, resp.Kvs, 1, "want key value pair 'hoo', 'bar' but got %+v", resp.Kvs)
		require.Equalf(t, "hoo", string(resp.Kvs[0].Key), "want key value pair 'hoo', 'bar' but got %+v", resp.Kvs)
		require.Equalf(t, "bar", string(resp.Kvs[0].Value), "want key value pair 'hoo', 'bar' but got %+v", resp.Kvs)
	})
}

func TestAuthGracefulDisable(t *testing.T) {
	testRunner.BeforeTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(config.ClusterConfig{ClusterSize: 1}))
	defer clus.Close()
	cc := testutils.MustClient(clus.Client())
	testutils.ExecuteUntil(ctx, t, func() {
		require.NoErrorf(t, setupAuth(cc, []authRole{}, []authUser{rootUser}), "failed to enable auth")
		donec := make(chan struct{})
		rootAuthClient := testutils.MustClient(clus.Client(WithAuth(rootUserName, rootPassword)))

		startedC := make(chan struct{}, 1)
		go func() {
			defer close(donec)
			defer close(startedC)
			// sleep a bit to let the watcher connects while auth is still enabled
			time.Sleep(time.Second)
			// now disable auth...
			if err := rootAuthClient.AuthDisable(ctx); err != nil {
				t.Errorf("failed to auth disable %v", err)
				return
			}
			// ...and restart the node
			clus.Members()[0].Stop()
			if err := clus.Members()[0].Start(ctx); err != nil {
				t.Errorf("failed to restart member %v", err)
				return
			}
			startedC <- struct{}{}
			// the watcher should still work after reconnecting
			_, err := rootAuthClient.Put(ctx, "key", "value", config.PutOptions{})
			assert.NoErrorf(t, err, "failed to put key value")
		}()

		<-startedC

		wCtx, wCancel := context.WithCancel(ctx)
		defer wCancel()

		watchCh := rootAuthClient.Watch(wCtx, "key", config.WatchOptions{Revision: 1})
		wantedLen := 1
		watchTimeout := 10 * time.Second
		wanted := []testutils.KV{{Key: "key", Val: "value"}}
		kvs, err := testutils.KeyValuesFromWatchChan(watchCh, wantedLen, watchTimeout)
		require.NoErrorf(t, err, "failed to get key-values from watch channel %s", err)
		require.Equal(t, wanted, kvs)
		<-donec
	})
}

func TestAuthStatus(t *testing.T) {
	testRunner.BeforeTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(config.ClusterConfig{ClusterSize: 1}))
	defer clus.Close()
	cc := testutils.MustClient(clus.Client())
	testutils.ExecuteUntil(ctx, t, func() {
		resp, err := cc.AuthStatus(ctx)
		require.NoError(t, err)
		require.Falsef(t, resp.Enabled, "want auth not enabled but enabled")

		require.NoErrorf(t, setupAuth(cc, []authRole{}, []authUser{rootUser}), "failed to enable auth")
		rootAuthClient := testutils.MustClient(clus.Client(WithAuth(rootUserName, rootPassword)))
		resp, err = rootAuthClient.AuthStatus(ctx)
		require.NoError(t, err)
		require.Truef(t, resp.Enabled, "want enabled but got not enabled")
	})
}

func TestAuthRoleUpdate(t *testing.T) {
	testRunner.BeforeTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(config.ClusterConfig{ClusterSize: 1}))
	defer clus.Close()
	cc := testutils.MustClient(clus.Client())
	testutils.ExecuteUntil(ctx, t, func() {
		_, err := cc.Put(ctx, "foo", "bar", config.PutOptions{})
		require.NoError(t, err)
		require.NoErrorf(t, setupAuth(cc, []authRole{testRole}, []authUser{rootUser, testUser}), "failed to enable auth")

		rootAuthClient := testutils.MustClient(clus.Client(WithAuth(rootUserName, rootPassword)))
		testUserAuthClient := testutils.MustClient(clus.Client(WithAuth(testUserName, testPassword)))

		_, err = testUserAuthClient.Put(ctx, "hoo", "bar", config.PutOptions{})
		require.ErrorContains(t, err, PermissionDenied)
		// grant a new key
		_, err = rootAuthClient.RoleGrantPermission(ctx, testRoleName, "hoo", "", clientv3.PermissionType(clientv3.PermReadWrite))
		require.NoError(t, err)
		// try a newly granted key
		_, err = testUserAuthClient.Put(ctx, "hoo", "bar", config.PutOptions{})
		require.NoError(t, err)
		// confirm put succeeded
		resp, err := testUserAuthClient.Get(ctx, "hoo", config.GetOptions{})
		require.NoError(t, err)
		require.Lenf(t, resp.Kvs, 1, "want key value pair 'hoo' 'bar' but got %+v", resp.Kvs)
		require.Equalf(t, "hoo", string(resp.Kvs[0].Key), "want key value pair 'hoo' 'bar' but got %+v", resp.Kvs)
		require.Equalf(t, "bar", string(resp.Kvs[0].Value), "want key value pair 'hoo' 'bar' but got %+v", resp.Kvs)
		// revoke the newly granted key
		_, err = rootAuthClient.RoleRevokePermission(ctx, testRoleName, "hoo", "")
		require.NoError(t, err)
		// try put to the revoked key
		_, err = testUserAuthClient.Put(ctx, "hoo", "bar", config.PutOptions{})
		require.ErrorContains(t, err, PermissionDenied)
		// confirm a key still granted can be accessed
		resp, err = testUserAuthClient.Get(ctx, "foo", config.GetOptions{})
		require.NoError(t, err)
		require.Lenf(t, resp.Kvs, 1, "want key value pair 'foo' 'bar' but got %+v", resp.Kvs)
		require.Equalf(t, "foo", string(resp.Kvs[0].Key), "want key value pair 'foo' 'bar' but got %+v", resp.Kvs)
		require.Equalf(t, "bar", string(resp.Kvs[0].Value), "want key value pair 'foo' 'bar' but got %+v", resp.Kvs)
	})
}

func TestAuthUserDeleteDuringOps(t *testing.T) {
	testRunner.BeforeTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(config.ClusterConfig{ClusterSize: 1}))
	defer clus.Close()
	cc := testutils.MustClient(clus.Client())
	testutils.ExecuteUntil(ctx, t, func() {
		_, err := cc.Put(ctx, "foo", "bar", config.PutOptions{})
		require.NoError(t, err)
		require.NoErrorf(t, setupAuth(cc, []authRole{testRole}, []authUser{rootUser, testUser}), "failed to enable auth")

		rootAuthClient := testutils.MustClient(clus.Client(WithAuth(rootUserName, rootPassword)))
		testUserAuthClient := testutils.MustClient(clus.Client(WithAuth(testUserName, testPassword)))

		// create a key
		_, err = testUserAuthClient.Put(ctx, "foo", "bar", config.PutOptions{})
		require.NoError(t, err)
		// confirm put succeeded
		resp, err := testUserAuthClient.Get(ctx, "foo", config.GetOptions{})
		require.NoError(t, err)
		require.Lenf(t, resp.Kvs, 1, "want key value pair 'foo' 'bar' but got %+v", resp.Kvs)
		require.Equalf(t, "foo", string(resp.Kvs[0].Key), "want key value pair 'foo' 'bar' but got %+v", resp.Kvs)
		require.Equalf(t, "bar", string(resp.Kvs[0].Value), "want key value pair 'foo' 'bar' but got %+v", resp.Kvs)
		// delete the user
		_, err = rootAuthClient.UserDelete(ctx, testUserName)
		require.NoError(t, err)
		// check the user is deleted
		_, err = testUserAuthClient.Put(ctx, "foo", "baz", config.PutOptions{})
		require.ErrorContains(t, err, AuthenticationFailed)
	})
}

func TestAuthRoleRevokeDuringOps(t *testing.T) {
	testRunner.BeforeTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(config.ClusterConfig{ClusterSize: 1}))
	defer clus.Close()
	cc := testutils.MustClient(clus.Client())
	testutils.ExecuteUntil(ctx, t, func() {
		_, err := cc.Put(ctx, "foo", "bar", config.PutOptions{})
		require.NoError(t, err)
		require.NoErrorf(t, setupAuth(cc, []authRole{testRole}, []authUser{rootUser, testUser}), "failed to enable auth")

		rootAuthClient := testutils.MustClient(clus.Client(WithAuth(rootUserName, rootPassword)))
		testUserAuthClient := testutils.MustClient(clus.Client(WithAuth(testUserName, testPassword)))

		// create a key
		_, err = testUserAuthClient.Put(ctx, "foo", "bar", config.PutOptions{})
		require.NoError(t, err)
		// confirm put succeeded
		resp, err := testUserAuthClient.Get(ctx, "foo", config.GetOptions{})
		require.NoError(t, err)
		require.Lenf(t, resp.Kvs, 1, "want key value pair 'foo' 'bar' but got %+v", resp.Kvs)
		require.Equalf(t, "foo", string(resp.Kvs[0].Key), "want key value pair 'foo' 'bar' but got %+v", resp.Kvs)
		require.Equalf(t, "bar", string(resp.Kvs[0].Value), "want key value pair 'foo' 'bar' but got %+v", resp.Kvs)
		// create a new role
		_, err = rootAuthClient.RoleAdd(ctx, "test-role2")
		require.NoError(t, err)
		// grant a new key to the new role
		_, err = rootAuthClient.RoleGrantPermission(ctx, "test-role2", "hoo", "", clientv3.PermissionType(clientv3.PermReadWrite))
		require.NoError(t, err)
		// grant the new role to the user
		_, err = rootAuthClient.UserGrantRole(ctx, testUserName, "test-role2")
		require.NoError(t, err)

		// try a newly granted key
		_, err = testUserAuthClient.Put(ctx, "hoo", "bar", config.PutOptions{})
		require.NoError(t, err)
		// confirm put succeeded
		resp, err = testUserAuthClient.Get(ctx, "hoo", config.GetOptions{})
		require.NoError(t, err)
		require.Lenf(t, resp.Kvs, 1, "want key value pair 'hoo' 'bar' but got %+v", resp.Kvs)
		require.Equalf(t, "hoo", string(resp.Kvs[0].Key), "want key value pair 'hoo' 'bar' but got %+v", resp.Kvs)
		require.Equalf(t, "bar", string(resp.Kvs[0].Value), "want key value pair 'hoo' 'bar' but got %+v", resp.Kvs)
		// revoke a role from the user
		_, err = rootAuthClient.UserRevokeRole(ctx, testUserName, testRoleName)
		require.NoError(t, err)
		// check the role is revoked and permission is lost from the user
		_, err = testUserAuthClient.Put(ctx, "foo", "baz", config.PutOptions{})
		require.ErrorContains(t, err, PermissionDenied)

		// try a key that can be accessed from the remaining role
		_, err = testUserAuthClient.Put(ctx, "hoo", "bar2", config.PutOptions{})
		require.NoError(t, err)
		// confirm put succeeded
		resp, err = testUserAuthClient.Get(ctx, "hoo", config.GetOptions{})
		require.NoError(t, err)
		require.Lenf(t, resp.Kvs, 1, "want key value pair 'hoo' 'bar2' but got %+v", resp.Kvs)
		require.Equalf(t, "hoo", string(resp.Kvs[0].Key), "want key value pair 'hoo' 'bar2' but got %+v", resp.Kvs)
		require.Equalf(t, "bar2", string(resp.Kvs[0].Value), "want key value pair 'hoo' 'bar2' but got %+v", resp.Kvs)
	})
}

func TestAuthWriteKey(t *testing.T) {
	testRunner.BeforeTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(config.ClusterConfig{ClusterSize: 1}))
	defer clus.Close()
	cc := testutils.MustClient(clus.Client())
	testutils.ExecuteUntil(ctx, t, func() {
		_, err := cc.Put(ctx, "foo", "a", config.PutOptions{})
		require.NoError(t, err)
		require.NoErrorf(t, setupAuth(cc, []authRole{testRole}, []authUser{rootUser, testUser}), "failed to enable auth")

		rootAuthClient := testutils.MustClient(clus.Client(WithAuth(rootUserName, rootPassword)))
		testUserAuthClient := testutils.MustClient(clus.Client(WithAuth(testUserName, testPassword)))

		// confirm root role can access to all keys
		_, err = rootAuthClient.Put(ctx, "foo", "bar", config.PutOptions{})
		require.NoError(t, err)
		resp, err := rootAuthClient.Get(ctx, "foo", config.GetOptions{})
		require.NoError(t, err)
		require.Lenf(t, resp.Kvs, 1, "want key value pair 'foo' 'bar' but got %+v", resp.Kvs)
		require.Equalf(t, "foo", string(resp.Kvs[0].Key), "want key value pair 'foo' 'bar' but got %+v", resp.Kvs)
		require.Equalf(t, "bar", string(resp.Kvs[0].Value), "want key value pair 'foo' 'bar' but got %+v", resp.Kvs)
		// try invalid user
		_, err = clus.Client(WithAuth("a", "b"))
		require.ErrorContains(t, err, AuthenticationFailed)

		// try good user
		_, err = testUserAuthClient.Put(ctx, "foo", "bar2", config.PutOptions{})
		require.NoError(t, err)
		// confirm put succeeded
		resp, err = testUserAuthClient.Get(ctx, "foo", config.GetOptions{})
		require.NoError(t, err)
		require.Lenf(t, resp.Kvs, 1, "want key value pair 'foo' 'bar2' but got %+v", resp.Kvs)
		require.Equalf(t, "foo", string(resp.Kvs[0].Key), "want key value pair 'foo' 'bar2' but got %+v", resp.Kvs)
		require.Equalf(t, "bar2", string(resp.Kvs[0].Value), "want key value pair 'foo' 'bar2' but got %+v", resp.Kvs)

		// try bad password
		_, err = clus.Client(WithAuth(testUserName, "badpass"))
		require.ErrorContains(t, err, AuthenticationFailed)
	})
}

func TestAuthTxn(t *testing.T) {
	tcs := []struct {
		name string
		cfg  config.ClusterConfig
	}{
		{
			"NoJWT",
			config.ClusterConfig{ClusterSize: 1},
		},
		{
			"JWT",
			config.ClusterConfig{ClusterSize: 1, AuthToken: defaultAuthToken},
		},
	}

	reqs := []txnReq{
		{
			compare:       []string{`version("c2") = "1"`},
			ifSuccess:     []string{"get s2"},
			ifFail:        []string{"get f2"},
			expectResults: []string{"SUCCESS", "s2", "v"},
			expectError:   false,
		},
		// a key of compare case isn't granted
		{
			compare:       []string{`version("c1") = "1"`},
			ifSuccess:     []string{"get s2"},
			ifFail:        []string{"get f2"},
			expectResults: []string{PermissionDenied},
			expectError:   true,
		},
		// a key of success case isn't granted
		{
			compare:       []string{`version("c2") = "1"`},
			ifSuccess:     []string{"get s1"},
			ifFail:        []string{"get f2"},
			expectResults: []string{PermissionDenied},
			expectError:   true,
		},
		// a key of failure case isn't granted
		{
			compare:       []string{`version("c2") = "1"`},
			ifSuccess:     []string{"get s2"},
			ifFail:        []string{"get f1"},
			expectResults: []string{PermissionDenied},
			expectError:   true,
		},
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			testRunner.BeforeTest(t)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(tc.cfg))
			defer clus.Close()
			cc := testutils.MustClient(clus.Client())
			testutils.ExecuteUntil(ctx, t, func() {
				// keys with 1 suffix aren't granted to test-user
				keys := []string{"c1", "s1", "f1"}
				// keys with 2 suffix are granted to test-user, see Line 399
				grantedKeys := []string{"c2", "s2", "f2"}
				for _, key := range keys {
					_, err := cc.Put(ctx, key, "v", config.PutOptions{})
					require.NoError(t, err)
				}
				for _, key := range grantedKeys {
					_, err := cc.Put(ctx, key, "v", config.PutOptions{})
					require.NoError(t, err)
				}

				require.NoErrorf(t, setupAuth(cc, []authRole{testRole}, []authUser{rootUser, testUser}), "failed to enable auth")
				rootAuthClient := testutils.MustClient(clus.Client(WithAuth(rootUserName, rootPassword)))
				testUserAuthClient := testutils.MustClient(clus.Client(WithAuth(testUserName, testPassword)))

				// grant keys to test-user
				for _, key := range grantedKeys {
					_, err := rootAuthClient.RoleGrantPermission(ctx, testRoleName, key, "", clientv3.PermissionType(clientv3.PermReadWrite))
					require.NoError(t, err)
				}
				for _, req := range reqs {
					resp, err := testUserAuthClient.Txn(ctx, req.compare, req.ifSuccess, req.ifFail, config.TxnOptions{
						Interactive: true,
					})
					if req.expectError {
						require.ErrorContains(t, err, req.expectResults[0])
					} else {
						require.NoError(t, err)
						require.Equal(t, req.expectResults, getRespValues(resp))
					}
				}
			})
		})
	}
}

func TestAuthPrefixPerm(t *testing.T) {
	testRunner.BeforeTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(config.ClusterConfig{ClusterSize: 1}))
	defer clus.Close()
	cc := testutils.MustClient(clus.Client())
	testutils.ExecuteUntil(ctx, t, func() {
		require.NoErrorf(t, setupAuth(cc, []authRole{testRole}, []authUser{rootUser, testUser}), "failed to enable auth")
		rootAuthClient := testutils.MustClient(clus.Client(WithAuth(rootUserName, rootPassword)))
		testUserAuthClient := testutils.MustClient(clus.Client(WithAuth(testUserName, testPassword)))
		prefix := "/prefix/" // directory like prefix
		// grant keys to test-user
		_, err := rootAuthClient.RoleGrantPermission(ctx, "test-role", prefix, clientv3.GetPrefixRangeEnd(prefix), clientv3.PermissionType(clientv3.PermReadWrite))
		require.NoError(t, err)
		// try a prefix granted permission
		for i := 0; i < 10; i++ {
			key := fmt.Sprintf("%s%d", prefix, i)
			_, err = testUserAuthClient.Put(ctx, key, "val", config.PutOptions{})
			require.NoError(t, err)
		}
		// expect put 'key with prefix end "/prefix0"' value failed
		_, err = testUserAuthClient.Put(ctx, clientv3.GetPrefixRangeEnd(prefix), "baz", config.PutOptions{})
		require.ErrorContains(t, err, PermissionDenied)

		// grant the prefix2 keys to test-user
		prefix2 := "/prefix2/"
		_, err = rootAuthClient.RoleGrantPermission(ctx, "test-role", prefix2, clientv3.GetPrefixRangeEnd(prefix2), clientv3.PermissionType(clientv3.PermReadWrite))
		require.NoError(t, err)
		for i := 0; i < 10; i++ {
			key := fmt.Sprintf("%s%d", prefix2, i)
			_, err = testUserAuthClient.Put(ctx, key, "val", config.PutOptions{})
			require.NoError(t, err)
		}
	})
}

func TestAuthLeaseKeepAlive(t *testing.T) {
	testRunner.BeforeTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(config.ClusterConfig{ClusterSize: 1}))
	defer clus.Close()
	cc := testutils.MustClient(clus.Client())
	testutils.ExecuteUntil(ctx, t, func() {
		require.NoErrorf(t, setupAuth(cc, []authRole{}, []authUser{rootUser}), "failed to enable auth")
		rootAuthClient := testutils.MustClient(clus.Client(WithAuth(rootUserName, rootPassword)))

		resp, err := rootAuthClient.Grant(ctx, 10)
		require.NoError(t, err)
		leaseID := resp.ID
		_, err = rootAuthClient.Put(ctx, "key", "value", config.PutOptions{LeaseID: leaseID})
		require.NoError(t, err)
		_, err = rootAuthClient.KeepAliveOnce(ctx, leaseID)
		require.NoError(t, err)

		gresp, err := rootAuthClient.Get(ctx, "key", config.GetOptions{})
		require.NoError(t, err)
		require.Lenf(t, gresp.Kvs, 1, "want kv pair ('key', 'value') but got %v", gresp.Kvs)
		require.Equalf(t, "key", string(gresp.Kvs[0].Key), "want kv pair ('key', 'value') but got %v", gresp.Kvs)
		require.Equalf(t, "value", string(gresp.Kvs[0].Value), "want kv pair ('key', 'value') but got %v", gresp.Kvs)
	})
}

func TestAuthRevokeWithDelete(t *testing.T) {
	testRunner.BeforeTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(config.ClusterConfig{ClusterSize: 1}))
	defer clus.Close()
	cc := testutils.MustClient(clus.Client())
	testutils.ExecuteUntil(ctx, t, func() {
		require.NoErrorf(t, setupAuth(cc, []authRole{testRole}, []authUser{rootUser, testUser}), "failed to enable auth")
		rootAuthClient := testutils.MustClient(clus.Client(WithAuth(rootUserName, rootPassword)))
		// create a new role
		newTestRoleName := "test-role2"
		_, err := rootAuthClient.RoleAdd(ctx, newTestRoleName)
		require.NoError(t, err)
		// grant the new role to the user
		_, err = rootAuthClient.UserGrantRole(ctx, testUserName, newTestRoleName)
		require.NoError(t, err)
		// check the result
		resp, err := rootAuthClient.UserGet(ctx, testUserName)
		require.NoError(t, err)
		require.ElementsMatch(t, resp.Roles, []string{testRoleName, newTestRoleName})
		// delete the role, test-role2 must be revoked from test-user
		_, err = rootAuthClient.RoleDelete(ctx, newTestRoleName)
		require.NoError(t, err)
		// check the result
		resp, err = rootAuthClient.UserGet(ctx, testUserName)
		require.NoError(t, err)
		require.ElementsMatch(t, resp.Roles, []string{testRoleName})
	})
}

func TestAuthLeaseTimeToLiveExpired(t *testing.T) {
	testRunner.BeforeTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(config.ClusterConfig{ClusterSize: 1}))
	defer clus.Close()
	cc := testutils.MustClient(clus.Client())
	testutils.ExecuteUntil(ctx, t, func() {
		require.NoErrorf(t, setupAuth(cc, []authRole{}, []authUser{rootUser}), "failed to enable auth")
		rootAuthClient := testutils.MustClient(clus.Client(WithAuth(rootUserName, rootPassword)))
		resp, err := rootAuthClient.Grant(ctx, 2)
		require.NoError(t, err)
		leaseID := resp.ID
		_, err = rootAuthClient.Put(ctx, "key", "val", config.PutOptions{LeaseID: leaseID})
		require.NoError(t, err)
		// eliminate false positive
		time.Sleep(3 * time.Second)
		tresp, err := rootAuthClient.TimeToLive(ctx, leaseID, config.LeaseOption{})
		require.NoError(t, err)
		require.Equal(t, int64(-1), tresp.TTL)

		gresp, err := rootAuthClient.Get(ctx, "key", config.GetOptions{})
		require.NoError(t, err)
		require.Empty(t, gresp.Kvs)
	})
}

func TestAuthLeaseGrantLeases(t *testing.T) {
	testRunner.BeforeTest(t)
	tcs := []testCase{
		{
			name:   "NoJWT",
			config: config.ClusterConfig{ClusterSize: 1},
		},
		{
			name:   "JWT",
			config: config.ClusterConfig{ClusterSize: 1, AuthToken: defaultAuthToken},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(tc.config))
			defer clus.Close()
			cc := testutils.MustClient(clus.Client())

			testutils.ExecuteUntil(ctx, t, func() {
				require.NoErrorf(t, setupAuth(cc, []authRole{testRole}, []authUser{rootUser, testUser}), "failed to enable auth")
				rootAuthClient := testutils.MustClient(clus.Client(WithAuth(rootUserName, rootPassword)))

				resp, err := rootAuthClient.Grant(ctx, 10)
				require.NoError(t, err)

				leaseID := resp.ID
				lresp, err := rootAuthClient.Leases(ctx)
				require.NoError(t, err)
				require.Lenf(t, lresp.Leases, 1, "want %v leaseID but got %v leases", leaseID, lresp.Leases)
				require.Equalf(t, lresp.Leases[0].ID, leaseID, "want %v leaseID but got %v leases", leaseID, lresp.Leases)

				anonAuthClient := testutils.MustClient(clus.Client())
				_, err = anonAuthClient.Grant(ctx, 10)
				require.ErrorContains(t, err, "etcdserver: user name is empty")

				testUserAuthClient := testutils.MustClient(clus.Client(WithAuth(testUserName, testPassword)))
				_, err = testUserAuthClient.Grant(ctx, 10)
				require.NoError(t, err)
			})
		})
	}
}

func TestAuthMemberAdd(t *testing.T) {
	testRunner.BeforeTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(config.ClusterConfig{ClusterSize: 1}))
	defer clus.Close()
	cc := testutils.MustClient(clus.Client())
	testutils.ExecuteUntil(ctx, t, func() {
		require.NoErrorf(t, setupAuth(cc, []authRole{testRole}, []authUser{rootUser, testUser}), "failed to enable auth")
		rootAuthClient := testutils.MustClient(clus.Client(WithAuth(rootUserName, rootPassword)))
		testUserAuthClient := testutils.MustClient(clus.Client(WithAuth(testUserName, testPassword)))
		_, err := testUserAuthClient.MemberAdd(ctx, "newmember", []string{testPeerURL})
		require.ErrorContains(t, err, PermissionDenied)
		_, err = rootAuthClient.MemberAdd(ctx, "newmember", []string{testPeerURL})
		require.NoError(t, err)
	})
}

func TestAuthCompact(t *testing.T) {
	testRunner.BeforeTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(config.ClusterConfig{ClusterSize: 1}))
	defer clus.Close()

	cc := testutils.MustClient(clus.Client())
	testutils.ExecuteUntil(ctx, t, func() {
		require.NoErrorf(t, setupAuth(cc, []authRole{testRole}, []authUser{rootUser, testUser}), "failed to enable auth")
		rootAuthClient := testutils.MustClient(clus.Client(WithAuth(rootUserName, rootPassword)))
		testUserAuthClient := testutils.MustClient(clus.Client(WithAuth(testUserName, testPassword)))

		_, err := rootAuthClient.Put(ctx, "key", "value", config.PutOptions{})
		require.NoError(t, err)

		_, err = rootAuthClient.Put(ctx, "key", "value", config.PutOptions{})
		require.NoError(t, err)

		_, err = testUserAuthClient.Compact(ctx, 1, config.CompactOption{Physical: true})
		require.ErrorContains(t, err, PermissionDenied)

		_, err = rootAuthClient.Compact(ctx, 1, config.CompactOption{Physical: true})
		require.NoError(t, err)
	})
}

func TestAuthLeaseLeases(t *testing.T) {
	testRunner.BeforeTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(config.ClusterConfig{ClusterSize: 1}))
	defer clus.Close()

	cc := testutils.MustClient(clus.Client())
	testutils.ExecuteUntil(ctx, t, func() {
		require.NoErrorf(t, setupAuth(cc, []authRole{testRole}, []authUser{rootUser, testUser}), "failed to enable auth")
		rootAuthClient := testutils.MustClient(clus.Client(WithAuth(rootUserName, rootPassword)))
		testUserAuthClient := testutils.MustClient(clus.Client(WithAuth(testUserName, testPassword)))
		anonAuthClient := testutils.MustClient(clus.Client())

		lresp, err := rootAuthClient.Grant(ctx, 90)
		require.NoError(t, err)
		firstLeaseID := lresp.ID

		_, err = rootAuthClient.Put(ctx, "foo", "value", config.PutOptions{LeaseID: firstLeaseID})
		require.NoError(t, err)

		lresp, err = rootAuthClient.Grant(ctx, 90)
		require.NoError(t, err)
		secondLeaseID := lresp.ID

		_, err = rootAuthClient.Put(ctx, "foo1", "value", config.PutOptions{LeaseID: secondLeaseID})
		require.NoError(t, err)

		_, err = testUserAuthClient.Leases(ctx)
		require.ErrorContains(t, err, PermissionDenied)

		_, err = anonAuthClient.Leases(ctx)
		require.ErrorContains(t, err, "etcdserver: user name is empty")

		resp, err := rootAuthClient.Leases(ctx)
		require.NoError(t, err)
		require.Lenf(t, resp.Leases, 2, "want 2 leases but got %v", resp.Leases)

		leaseIDs := []clientv3.LeaseID{firstLeaseID, secondLeaseID}
		for _, lease := range resp.Leases {
			require.Containsf(t, leaseIDs, lease.ID, "unexpected lease ID %v, want one of %v", lease.ID, leaseIDs)
		}

		_, err = rootAuthClient.Revoke(ctx, secondLeaseID)
		require.NoError(t, err)

		_, err = testUserAuthClient.Leases(ctx)
		require.NoError(t, err)
	})
}

func TestAuthMemberList(t *testing.T) {
	testRunner.BeforeTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(config.ClusterConfig{ClusterSize: 1}))
	defer clus.Close()
	cc := testutils.MustClient(clus.Client())
	testutils.ExecuteUntil(ctx, t, func() {
		require.NoErrorf(t, setupAuth(cc, []authRole{testRole}, []authUser{rootUser, testUser}), "failed to enable auth")
		rootAuthClient := testutils.MustClient(clus.Client(WithAuth(rootUserName, rootPassword)))
		testUserAuthClient := testutils.MustClient(clus.Client(WithAuth(testUserName, testPassword)))
		anonAuthClient := testutils.MustClient(clus.Client())

		_, err := testUserAuthClient.MemberList(ctx, false)
		require.NoError(t, err)

		_, err = anonAuthClient.MemberList(ctx, false)
		require.Error(t, err)
		require.ErrorContains(t, err, "etcdserver: user name is empty")

		_, err = rootAuthClient.MemberList(ctx, false)
		require.NoError(t, err)
	})
}

func TestAuthMemberRemove(t *testing.T) {
	testRunner.BeforeTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	clusterSize := 3
	clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(config.ClusterConfig{ClusterSize: clusterSize}))
	defer clus.Close()
	cc := testutils.MustClient(clus.Client())
	testutils.ExecuteUntil(ctx, t, func() {
		memberIDToEndpoints := getMemberIDToEndpoints(ctx, t, clus)
		require.NoErrorf(t, setupAuth(cc, []authRole{testRole}, []authUser{rootUser, testUser}), "failed to enable auth")
		rootAuthClient := testutils.MustClient(clus.Client(WithAuth(rootUserName, rootPassword)))

		memberID, clusterID := memberToRemove(ctx, t, rootAuthClient, clusterSize)
		delete(memberIDToEndpoints, memberID)
		endpoints := make([]string, 0, len(memberIDToEndpoints))
		for _, ep := range memberIDToEndpoints {
			endpoints = append(endpoints, ep)
		}
		testUserAuthClient := testutils.MustClient(clus.Client(WithAuth(testUserName, testPassword)))
		// ordinary user cannot remove a member
		_, err := testUserAuthClient.MemberRemove(ctx, memberID)
		require.ErrorContains(t, err, PermissionDenied)

		// root can remove a member, building a client excluding removed member endpoint
		rootAuthClient2 := testutils.MustClient(clus.Client(WithAuth(rootUserName, rootPassword), WithEndpoints(endpoints)))
		resp, err := rootAuthClient2.MemberRemove(ctx, memberID)
		require.NoError(t, err)
		require.Equal(t, resp.Header.ClusterId, clusterID)
		found := false
		for _, member := range resp.Members {
			if member.ID == memberID {
				found = true
				break
			}
		}
		require.Falsef(t, found, "expect removed member not found in member remove response")
	})
}

func TestAuthTestInvalidMgmt(t *testing.T) {
	testRunner.BeforeTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(config.ClusterConfig{ClusterSize: 1}))
	defer clus.Close()
	cc := testutils.MustClient(clus.Client())
	testutils.ExecuteUntil(ctx, t, func() {
		require.NoErrorf(t, setupAuth(cc, []authRole{}, []authUser{rootUser}), "failed to enable auth")
		rootAuthClient := testutils.MustClient(clus.Client(WithAuth(rootUserName, rootPassword)))
		_, err := rootAuthClient.UserDelete(ctx, rootUserName)
		require.ErrorContains(t, err, InvalidAuthManagement)
		_, err = rootAuthClient.UserRevokeRole(ctx, rootUserName, rootRoleName)
		require.ErrorContains(t, err, InvalidAuthManagement)
	})
}

func TestAuthLeaseRevoke(t *testing.T) {
	testRunner.BeforeTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(config.ClusterConfig{ClusterSize: 1}))
	defer clus.Close()
	cc := testutils.MustClient(clus.Client())
	testutils.ExecuteUntil(ctx, t, func() {
		require.NoErrorf(t, setupAuth(cc, []authRole{testRole}, []authUser{rootUser, testUser}), "failed to enable auth")
		rootAuthClient := testutils.MustClient(clus.Client(WithAuth(rootUserName, rootPassword)))

		lresp, err := rootAuthClient.Grant(ctx, 10)
		require.NoError(t, err)
		_, err = rootAuthClient.Put(ctx, "key", "value", config.PutOptions{LeaseID: lresp.ID})
		require.NoError(t, err)

		_, err = rootAuthClient.Revoke(ctx, lresp.ID)
		require.NoError(t, err)

		_, err = rootAuthClient.Get(ctx, "key", config.GetOptions{})
		require.NoError(t, err)

		lresp, err = rootAuthClient.Grant(ctx, 10)
		require.NoError(t, err)

		annoAuthClient := testutils.MustClient(clus.Client())
		_, err = annoAuthClient.Revoke(ctx, lresp.ID)
		require.ErrorContainsf(t, err, "etcdserver: user name is empty", "should fail to revoke lease with unauthenticated client")
	})
}

func TestAuthRoleGet(t *testing.T) {
	testRunner.BeforeTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(config.ClusterConfig{ClusterSize: 1}))
	defer clus.Close()
	cc := testutils.MustClient(clus.Client())
	testutils.ExecuteUntil(ctx, t, func() {
		require.NoErrorf(t, setupAuth(cc, []authRole{testRole}, []authUser{rootUser, testUser}), "failed to enable auth")
		rootAuthClient := testutils.MustClient(clus.Client(WithAuth(rootUserName, rootPassword)))
		testUserAuthClient := testutils.MustClient(clus.Client(WithAuth(testUserName, testPassword)))

		resp, err := rootAuthClient.RoleGet(ctx, testRoleName)
		require.NoError(t, err)
		requireRolePermissionEqual(t, testRole, resp.Perm)

		// test-user can get the information of test-role because it belongs to the role
		resp, err = testUserAuthClient.RoleGet(ctx, testRoleName)
		require.NoError(t, err)
		requireRolePermissionEqual(t, testRole, resp.Perm)
		// test-user cannot get the information of root because it doesn't belong to the role
		_, err = testUserAuthClient.RoleGet(ctx, rootRoleName)
		require.ErrorContains(t, err, PermissionDenied)
	})
}

func TestAuthUserGet(t *testing.T) {
	testRunner.BeforeTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(config.ClusterConfig{ClusterSize: 1}))
	defer clus.Close()
	cc := testutils.MustClient(clus.Client())
	testutils.ExecuteUntil(ctx, t, func() {
		require.NoErrorf(t, setupAuth(cc, []authRole{testRole}, []authUser{rootUser, testUser}), "failed to enable auth")
		rootAuthClient := testutils.MustClient(clus.Client(WithAuth(rootUserName, rootPassword)))
		testUserAuthClient := testutils.MustClient(clus.Client(WithAuth(testUserName, testPassword)))

		resp, err := rootAuthClient.UserGet(ctx, testUserName)
		require.NoError(t, err)
		requireUserRolesEqual(t, testUser, resp.Roles)

		// test-user can get the information of test-user itself
		resp, err = testUserAuthClient.UserGet(ctx, testUserName)
		require.NoError(t, err)
		requireUserRolesEqual(t, testUser, resp.Roles)
		// test-user cannot get the information of root
		_, err = testUserAuthClient.UserGet(ctx, rootUserName)
		require.ErrorContains(t, err, PermissionDenied)
	})
}

func TestAuthRoleList(t *testing.T) {
	testRunner.BeforeTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(config.ClusterConfig{ClusterSize: 1}))
	defer clus.Close()
	cc := testutils.MustClient(clus.Client())
	testutils.ExecuteUntil(ctx, t, func() {
		require.NoErrorf(t, setupAuth(cc, []authRole{testRole}, []authUser{rootUser, testUser}), "failed to enable auth")
		rootAuthClient := testutils.MustClient(clus.Client(WithAuth(rootUserName, rootPassword)))

		resp, err := rootAuthClient.RoleList(ctx)
		require.NoError(t, err)
		requireUserRolesEqual(t, testUser, resp.Roles)
	})
}

func TestAuthJWTExpire(t *testing.T) {
	testRunner.BeforeTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(config.ClusterConfig{ClusterSize: 1, AuthToken: defaultAuthToken}))
	defer clus.Close()
	cc := testutils.MustClient(clus.Client())
	testutils.ExecuteUntil(ctx, t, func() {
		require.NoErrorf(t, setupAuth(cc, []authRole{testRole}, []authUser{rootUser, testUser}), "failed to enable auth")
		testUserAuthClient := testutils.MustClient(clus.Client(WithAuth(testUserName, testPassword)))

		_, err := testUserAuthClient.Put(ctx, "foo", "bar", config.PutOptions{})
		require.NoError(t, err)
		// wait an expiration of my JWT token
		<-time.After(3 * tokenTTL)

		// e2e test will generate a new token while
		// integration test that re-uses the same etcd client will refresh the token on server failure.
		_, err = testUserAuthClient.Put(ctx, "foo", "bar", config.PutOptions{})
		require.NoError(t, err)
	})
}

func TestAuthJWTOnly(t *testing.T) {
	testRunner.BeforeTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(config.ClusterConfig{ClusterSize: 1, AuthToken: verifyJWTOnlyAuth}))
	defer clus.Close()
	cc := testutils.MustClient(clus.Client())
	testutils.ExecuteUntil(ctx, t, func() {
		authRev, err := setupAuthAndGetRevision(cc, []authRole{testRole}, []authUser{rootUser, testUser})
		require.NoErrorf(t, err, "failed to enable auth")

		token, err := createSignedJWT(defaultKeyPath, defaultSignMethod, testUserName, authRev, 0)
		require.NoErrorf(t, err, "failed to create test user JWT")

		testUserAuthClient := testutils.MustClient(clus.Client(WithAuthToken(token)))
		_, err = testUserAuthClient.Put(ctx, "foo", "bar", config.PutOptions{})
		require.NoError(t, err)
	})
}

func TestAuthJWTOnlyUpdateExpired(t *testing.T) {
	testRunner.BeforeTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(config.ClusterConfig{ClusterSize: 1, AuthToken: verifyJWTOnlyAuth}))
	defer clus.Close()
	cc := testutils.MustClient(clus.Client())
	testutils.ExecuteUntil(ctx, t, func() {
		authRev, err := setupAuthAndGetRevision(cc, []authRole{testRole}, []authUser{rootUser, testUser})
		require.NoErrorf(t, err, "failed to enable auth")

		// create token expiring in 5 seconds
		tokenExpires := time.Now().Add(5 * time.Second).Unix()
		token, err := createSignedJWT(defaultKeyPath, defaultSignMethod, testUserName, authRev, tokenExpires)
		require.NoErrorf(t, err, "failed to create test user JWT")

		// use token before it expires
		testUserAuthClient := testutils.MustClient(clus.Client(WithAuthToken(token)))
		_, err = testUserAuthClient.Put(ctx, "foo", "bar", config.PutOptions{})
		require.NoError(t, err)

		// let token expire
		<-time.After(6 * time.Second)

		// token is expired now, the next request should fail with InvalidAuthToken error
		_, err = testUserAuthClient.Put(ctx, "foo", "bar", config.PutOptions{})
		require.ErrorContains(t, err, InvalidAuthToken)

		// refresh token
		tokenExpires = time.Now().Add(5 * time.Second).Unix()
		token, err = createSignedJWT(defaultKeyPath, defaultSignMethod, testUserName, authRev, tokenExpires)
		require.NoErrorf(t, err, "failed to create new test user JWT")

		// update client to use the new token
		testUserAuthClient.UpdateAuthToken(token)
		_, err = testUserAuthClient.Put(ctx, "foo", "bar", config.PutOptions{})
		require.NoError(t, err)
	})
}

func TestAuthJWTOnlySkipRevision(t *testing.T) {
	// a verify-only provider must ignore a (stale) revision claim 1 and use the current auth store revision instead
	testRunner.BeforeTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(config.ClusterConfig{ClusterSize: 1, AuthToken: verifyJWTOnlyAuth}))
	defer clus.Close()
	cc := testutils.MustClient(clus.Client())
	testutils.ExecuteUntil(ctx, t, func() {
		authRev, err := setupAuthAndGetRevision(cc, []authRole{testRole}, []authUser{rootUser, testUser})
		require.NoErrorf(t, err, "failed to enable auth")
		require.Greaterf(t, authRev, uint64(1), "expected current auth revision %d to be greater than 1", authRev)

		// get root token with (stale) auth revision 1
		rootToken, err := createSignedJWT(defaultKeyPath, defaultSignMethod, rootUserName, 1, 0)
		require.NoErrorf(t, err, "failed to create root user JWT")
		rootAuthClient := testutils.MustClient(clus.Client(WithAuthToken(rootToken)))

		fetchAuthRevision := func(rev uint64) uint64 {
			aresp, aerr := rootAuthClient.AuthStatus(ctx)
			require.NoError(t, aerr)
			require.Greaterf(t, aresp.AuthRevision, rev, "expected current auth revision to be greater than revision %d", rev)
			return aresp.AuthRevision
		}

		// grant a new key, this bumps the auth revision, the new revision must be greater than authRev
		_, err = rootAuthClient.RoleGrantPermission(ctx, testRoleName, "hoo", "", clientv3.PermissionType(clientv3.PermReadWrite))
		require.NoError(t, err)
		authRev = fetchAuthRevision(authRev)

		// get testUser token with (stale) auth revision 1
		testUserToken, err := createSignedJWT(defaultKeyPath, defaultSignMethod, testUserName, 1, 0)
		require.NoErrorf(t, err, "failed to create test user JWT")
		testUserAuthClient := testutils.MustClient(clus.Client(WithAuthToken(testUserToken)))

		// try a newly granted key using the "stale" token revision
		_, err = testUserAuthClient.Put(ctx, "hoo", "bar", config.PutOptions{})
		require.NoError(t, err)
		// confirm put succeeded
		resp, err := testUserAuthClient.Get(ctx, "hoo", config.GetOptions{})
		require.NoError(t, err)
		require.Lenf(t, resp.Kvs, 1, "want key value pair 'hoo', 'bar' but got %+v", resp.Kvs)
		require.Equalf(t, "hoo", string(resp.Kvs[0].Key), "want key value pair 'hoo', 'bar' but got %+v", resp.Kvs)
		require.Equalf(t, "bar", string(resp.Kvs[0].Value), "want key value pair 'hoo', 'bar' but got %+v", resp.Kvs)

		// retry to get hoo after revoking testRole: PermissionDenied
		_, err = rootAuthClient.UserRevokeRole(ctx, testUserName, testRoleName)
		require.NoError(t, err)
		authRev = fetchAuthRevision(authRev)
		_, err = testUserAuthClient.Get(ctx, "hoo", config.GetOptions{})
		require.ErrorContains(t, err, PermissionDenied)

		// re-grant testRole
		_, err = rootAuthClient.UserGrantRole(ctx, testUserName, testRoleName)
		require.NoError(t, err)
		authRev = fetchAuthRevision(authRev)
		_, err = testUserAuthClient.Get(ctx, "hoo", config.GetOptions{})
		require.NoError(t, err)

		// testUser token must be invalid after user deletion
		_, err = rootAuthClient.UserDelete(ctx, testUserName)
		require.NoError(t, err)
		_ = fetchAuthRevision(authRev)
		_, err = testUserAuthClient.Get(ctx, "hoo", config.GetOptions{})
		require.ErrorContains(t, err, PermissionDenied)
	})
}

// TestAuthRevisionConsistency ensures auth revision is the same after member restarts
func TestAuthRevisionConsistency(t *testing.T) {
	testRunner.BeforeTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(config.ClusterConfig{ClusterSize: 1, AuthToken: defaultAuthToken}))
	defer clus.Close()
	cc := testutils.MustClient(clus.Client())
	testutils.ExecuteUntil(ctx, t, func() {
		require.NoErrorf(t, setupAuth(cc, []authRole{}, []authUser{rootUser}), "failed to enable auth")
		rootAuthClient := testutils.MustClient(clus.Client(WithAuth(rootUserName, rootPassword)))

		// add user
		_, err := rootAuthClient.UserAdd(ctx, testUserName, testPassword, config.UserAddOptions{})
		require.NoError(t, err)
		// delete the same user
		_, err = rootAuthClient.UserDelete(ctx, testUserName)
		require.NoError(t, err)

		// get node0 auth revision
		aresp, err := rootAuthClient.AuthStatus(ctx)
		require.NoError(t, err)
		oldAuthRevision := aresp.AuthRevision

		// restart the node
		clus.Members()[0].Stop()
		require.NoError(t, clus.Members()[0].Start(ctx))

		aresp2, err := rootAuthClient.AuthStatus(ctx)
		require.NoError(t, err)
		newAuthRevision := aresp2.AuthRevision

		require.Equal(t, oldAuthRevision, newAuthRevision)
	})
}

// TestAuthTestCacheReload ensures permissions are persisted and will be reloaded after member restarts
func TestAuthTestCacheReload(t *testing.T) {
	testRunner.BeforeTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(config.ClusterConfig{ClusterSize: 1, AuthToken: defaultAuthToken}))
	defer clus.Close()
	cc := testutils.MustClient(clus.Client())
	testutils.ExecuteUntil(ctx, t, func() {
		require.NoErrorf(t, setupAuth(cc, []authRole{testRole}, []authUser{rootUser, testUser}), "failed to enable auth")
		testUserAuthClient := testutils.MustClient(clus.Client(WithAuth(testUserName, testPassword)))

		// create foo since that is within the permission set
		// expectation is to succeed
		_, err := testUserAuthClient.Put(ctx, "foo", "bar", config.PutOptions{})
		require.NoError(t, err)

		// restart the node
		clus.Members()[0].Stop()
		require.NoError(t, clus.Members()[0].Start(ctx))

		// nothing has changed, but it fails without refreshing cache after restart
		_, err = testUserAuthClient.Put(ctx, "foo", "bar2", config.PutOptions{})
		require.NoError(t, err)
	})
}

// TestAuthLeaseTimeToLive gated lease time to live with RBAC control
func TestAuthLeaseTimeToLive(t *testing.T) {
	testRunner.BeforeTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(config.ClusterConfig{ClusterSize: 1, AuthToken: defaultAuthToken}))
	defer clus.Close()
	cc := testutils.MustClient(clus.Client())
	testutils.ExecuteUntil(ctx, t, func() {
		require.NoErrorf(t, setupAuth(cc, []authRole{testRole}, []authUser{rootUser, testUser}), "failed to enable auth")
		testUserAuthClient := testutils.MustClient(clus.Client(WithAuth(testUserName, testPassword)))
		anonAuthClient := testutils.MustClient(clus.Client())

		gresp, err := testUserAuthClient.Grant(ctx, 10)
		require.NoError(t, err)
		leaseID := gresp.ID

		_, err = testUserAuthClient.Put(ctx, "foo", "bar", config.PutOptions{LeaseID: leaseID})
		require.NoError(t, err)
		_, err = testUserAuthClient.TimeToLive(ctx, leaseID, config.LeaseOption{WithAttachedKeys: true})
		require.NoError(t, err)

		_, err = anonAuthClient.TimeToLive(ctx, leaseID, config.LeaseOption{WithAttachedKeys: false})
		require.ErrorContains(t, err, "etcdserver: user name is empty")

		_, err = anonAuthClient.TimeToLive(ctx, leaseID, config.LeaseOption{WithAttachedKeys: true})
		require.ErrorContains(t, err, "etcdserver: user name is empty")

		rootAuthClient := testutils.MustClient(clus.Client(WithAuth(rootUserName, rootPassword)))
		_, err = rootAuthClient.Put(ctx, "bar", "foo", config.PutOptions{LeaseID: leaseID})
		require.NoError(t, err)

		// the lease is attached to bar, which test-user cannot access
		_, err = testUserAuthClient.TimeToLive(ctx, leaseID, config.LeaseOption{WithAttachedKeys: true})
		require.Errorf(t, err, "test-user must not be able to access to the lease, because it's attached to the key bar")

		// without --keys, access should be allowed
		_, err = testUserAuthClient.TimeToLive(ctx, leaseID, config.LeaseOption{WithAttachedKeys: false})
		require.NoError(t, err)
	})
}

func TestAuthAlarm(t *testing.T) {
	testRunner.BeforeTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(config.ClusterConfig{
		ClusterSize:       1,
		QuotaBackendBytes: 1024 * 5,
	}))
	defer clus.Close()

	cc := testutils.MustClient(clus.Client())
	testutils.ExecuteUntil(ctx, t, func() {
		require.NoErrorf(t, setupAuth(cc, []authRole{testRole}, []authUser{rootUser, testUser}), "failed to enable auth")
		rootAuthClient := testutils.MustClient(clus.Client(WithAuth(rootUserName, rootPassword)))
		testUserAuthClient := testutils.MustClient(clus.Client(WithAuth(testUserName, testPassword)))
		anonAuthClient := testutils.MustClient(clus.Client())

		for i := 0; ; i++ {
			_, err := rootAuthClient.Put(ctx,
				testutil.PickKey(int64(i)), strings.Repeat("A", 1024), config.PutOptions{})
			if err == nil {
				continue
			}

			require.ErrorContains(t, err, "etcdserver: mvcc: database space exceeded")
			break
		}

		memberID := uint64(0)

		for i := 0; i < 10; i++ {
			resp, rerr := rootAuthClient.AlarmList(ctx)
			require.NoError(t, rerr)

			if len(resp.Alarms) > 0 {
				memberID = resp.Header.MemberId
				break
			}
			time.Sleep(1 * time.Second)
		}
		require.NotEqualf(t, uint64(0), memberID, "expect to find alarm with non-zero member ID")

		_, err := anonAuthClient.AlarmList(ctx)
		require.ErrorContains(t, err, "etcdserver: user name is empty")

		_, err = testUserAuthClient.AlarmList(ctx)
		require.NoError(t, err)

		_, err = testUserAuthClient.AlarmDisarm(ctx, &clientv3.AlarmMember{
			MemberID: memberID,
			Alarm:    pb.AlarmType_NOSPACE,
		})
		require.ErrorContains(t, err, PermissionDenied)

		resp, err := rootAuthClient.AlarmDisarm(ctx, &clientv3.AlarmMember{
			MemberID: memberID,
			Alarm:    pb.AlarmType_NOSPACE,
		})
		require.NoError(t, err)
		require.Lenf(t, resp.Alarms, 1, "expect 1 alarm from disarm but got %v", resp.Alarms)

		resp, err = rootAuthClient.AlarmList(ctx)
		require.NoError(t, err)
		require.Emptyf(t, resp.Alarms, "expect no alarm after disarm but got %v", resp.Alarms)
	})
}

func mustAbsPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		panic(err)
	}
	return abs
}
