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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/tests/v3/framework/config"
	"go.etcd.io/etcd/tests/v3/framework/testutils"
)

func TestWatch(t *testing.T) {
	testRunner.BeforeTest(t)
	watchTimeout := 1 * time.Second
	for _, tc := range clusterTestCases() {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(tc.config))

			defer clus.Close()
			cc := testutils.MustClient(clus.Client())
			testutils.ExecuteUntil(ctx, t, func() {
				tests := []struct {
					puts     []testutils.KV
					watchKey string
					opts     config.WatchOptions
					wanted   []testutils.KV
				}{
					{ // watch by revision
						puts:     []testutils.KV{{Key: "bar", Val: "revision_1"}, {Key: "bar", Val: "revision_2"}, {Key: "bar", Val: "revision_3"}},
						watchKey: "bar",
						opts:     config.WatchOptions{Revision: 3},
						wanted:   []testutils.KV{{Key: "bar", Val: "revision_2"}, {Key: "bar", Val: "revision_3"}},
					},
					{ // watch 1 key
						puts:     []testutils.KV{{Key: "sample", Val: "value"}},
						watchKey: "sample",
						opts:     config.WatchOptions{Revision: 1},
						wanted:   []testutils.KV{{Key: "sample", Val: "value"}},
					},
					{ // watch 3 keys by prefix
						puts:     []testutils.KV{{Key: "foo1", Val: "val1"}, {Key: "foo2", Val: "val2"}, {Key: "foo3", Val: "val3"}},
						watchKey: "foo",
						opts:     config.WatchOptions{Revision: 1, Prefix: true},
						wanted:   []testutils.KV{{Key: "foo1", Val: "val1"}, {Key: "foo2", Val: "val2"}, {Key: "foo3", Val: "val3"}},
					},
					{ // watch 3 keys by range
						puts:     []testutils.KV{{Key: "key1", Val: "val1"}, {Key: "key3", Val: "val3"}, {Key: "key2", Val: "val2"}},
						watchKey: "key",
						opts:     config.WatchOptions{Revision: 1, RangeEnd: "key3"},
						wanted:   []testutils.KV{{Key: "key1", Val: "val1"}, {Key: "key2", Val: "val2"}},
					},
				}

				for _, tt := range tests {
					wCtx, wCancel := context.WithCancel(ctx)
					wch := cc.Watch(wCtx, tt.watchKey, tt.opts)
					require.NotNilf(t, wch, "failed to watch %s", tt.watchKey)

					for j := range tt.puts {
						_, err := cc.Put(ctx, tt.puts[j].Key, tt.puts[j].Val, config.PutOptions{})
						require.NoErrorf(t, err, "can't not put key %q, err: %s", tt.puts[j].Key, err)
					}

					kvs, err := testutils.KeyValuesFromWatchChan(wch, len(tt.wanted), watchTimeout)
					if err != nil {
						wCancel()
						require.NoErrorf(t, err, "failed to get key-values from watch channel %s", err)
					}

					wCancel()
					assert.Equal(t, tt.wanted, kvs)
				}
			})
		})
	}
}

// TestWatchWithLowPermissionUser guards against a security issue where a
// user granted READ permission on exactly one key could still receive watch
// responses for every key starting from (and beyond) that key.
//
// Root cause was in server/etcdserver/api/v3rpc/watch.go: it rewrote the
// wire sentinel for an open-ended ">= key" watch (RangeEnd == a single 0x00
// byte, which is what clientv3.WithFromKey() sends) into []byte{} *before*
// the permission check ran. server/auth/range_perm_cache.go's
// isRangeOpPermitted does `if len(rangeEnd) == 0`, and len([]byte{}) is 0
// just like len(nil) - so the open-ended range request was checked as if it
// were an exact single-key point query. A single-key grant satisfied that
// point check, so the watch was created covering [key, +inf) instead of
// being denied. Fixed by running the permission check against the
// unmodified RangeEnd, before the []byte{}/nil rewrite for watchStream.Watch.
func TestWatchWithLowPermissionUser(t *testing.T) {
	testRunner.BeforeTest(t)
	watchOpts := config.WatchOptions{FromKey: true, CreatedNotify: true}
	if !WatchOptionsSupported(watchOpts) {
		t.Skip("backend does not support required watch options")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(config.ClusterConfig{ClusterSize: 1}))
	defer clus.Close()
	cc := testutils.MustClient(clus.Client())

	testutils.ExecuteUntil(ctx, t, func() {
		const (
			watchTestUserName = "watch-test-user"
			watchTestRoleName = "watch-test-role"
			watchTestPassword = "watchPass"
		)

		watchRole := authRole{
			role:       watchTestRoleName,
			permission: clientv3.PermissionType(clientv3.PermRead),
			key:        "foo",
			keyEnd:     "",
		}
		watchUser := authUser{user: watchTestUserName, pass: watchTestPassword, role: watchTestRoleName}

		require.NoError(t, setupAuth(cc, []authRole{watchRole}, []authUser{rootUser, watchUser}))

		rootAuthClient := testutils.MustClient(clus.Client(WithAuth(rootUserName, rootPassword)))
		testAuthClient := testutils.MustClient(clus.Client(WithAuth(watchTestUserName, watchTestPassword)))

		wch := testAuthClient.Watch(ctx, "foo", watchOpts)

		select {
		case wresp := <-wch:
			require.Error(t, wresp.Err())
			require.Contains(t, wresp.Err().Error(), PermissionDenied)
			require.True(t, wresp.Canceled)
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for permission denied watch response")
		}

		t.Log("Confirm root can still write to a sibling key sharing the 'foo' prefix without any leaked event reaching 'test'")
		_, err := rootAuthClient.Put(ctx, "foo1", "leaked-value", config.PutOptions{})
		require.NoError(t, err)

		select {
		case wresp, ok := <-wch:
			if ok {
				t.Fatalf("expected watch channel closed after denial, got: %+v", wresp)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for watch channel to close")
		}
	})
}
