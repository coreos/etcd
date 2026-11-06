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

func TestWatchCreatedNotify(t *testing.T) {
	testRunner.BeforeTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(config.ClusterConfig{ClusterSize: 1}))
	defer clus.Close()
	cc := testutils.MustClient(clus.Client())

	testutils.ExecuteUntil(ctx, t, func() {
		wch := cc.Watch(ctx, "foo", config.WatchOptions{CreatedNotify: true})
		require.NotNil(t, wch)

		select {
		case resp := <-wch:
			require.Truef(t, resp.Created, "expected a created notification, got %+v", resp)
			require.Empty(t, resp.Events)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for the watch created notification")
		}

		_, err := cc.Put(ctx, "foo", "bar", config.PutOptions{})
		require.NoError(t, err)

		kvs, err := testutils.KeyValuesFromWatchChan(wch, 1, 5*time.Second)
		require.NoError(t, err)
		assert.Equal(t, []testutils.KV{{Key: "foo", Val: "bar"}}, kvs)
	})
}
