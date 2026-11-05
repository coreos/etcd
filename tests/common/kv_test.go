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
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/etcdserver/txn"
	"go.etcd.io/etcd/server/v3/lease"
	"go.etcd.io/etcd/tests/v3/framework/config"
	"go.etcd.io/etcd/tests/v3/framework/interfaces"
	"go.etcd.io/etcd/tests/v3/framework/testutils"
)

func TestKVPut(t *testing.T) {
	testRunner.BeforeTest(t)
	for _, tc := range clusterTestCases() {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(tc.config))
			defer clus.Close()
			cc := testutils.MustClient(clus.Client())

			testutils.ExecuteUntil(ctx, t, func() {
				key, value := "foo", "bar"

				_, err := cc.Put(ctx, key, value, config.PutOptions{})
				require.NoErrorf(t, err, "count not put key %q", key)
				resp, err := cc.Get(ctx, key, config.GetOptions{})
				require.NoErrorf(t, err, "count not get key %q, err: %s", key, err)
				assert.Lenf(t, resp.Kvs, 1, "Unexpected length of response, got %d", len(resp.Kvs))
				assert.Equalf(t, string(resp.Kvs[0].Key), key, "Unexpected key, want %q, got %q", key, resp.Kvs[0].Key)
				assert.Equalf(t, string(resp.Kvs[0].Value), value, "Unexpected value, want %q, got %q", value, resp.Kvs[0].Value)
			})
		})
	}
}

func TestKVPutWithIgnoreValue(t *testing.T) {
	testRunner.BeforeTest(t)
	for _, tc := range clusterTestCases() {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(tc.config))
			defer clus.Close()
			cc := testutils.MustClient(clus.Client())

			testutils.ExecuteUntil(ctx, t, func() {
				_, err := cc.Put(ctx, "foo", "", config.PutOptions{IgnoreValue: true})
				require.ErrorContains(t, err, rpctypes.ErrKeyNotFound.Error())

				_, err = cc.Put(ctx, "foo", "bar", config.PutOptions{})
				require.NoError(t, err)

				_, err = cc.Put(ctx, "foo", "", config.PutOptions{IgnoreValue: true})
				require.NoError(t, err)

				resp, err := cc.Get(ctx, "foo", config.GetOptions{})
				require.NoError(t, err)
				require.Len(t, resp.Kvs, 1)
				require.Equal(t, "bar", string(resp.Kvs[0].Value))
			})
		})
	}
}

func TestKVPutWithIgnoreLease(t *testing.T) {
	testRunner.BeforeTest(t)
	for _, tc := range clusterTestCases() {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(tc.config))
			defer clus.Close()
			cc := testutils.MustClient(clus.Client())

			testutils.ExecuteUntil(ctx, t, func() {
				grant, err := cc.Grant(ctx, 10)
				require.NoError(t, err)

				_, err = cc.Put(ctx, "foo", "bar", config.PutOptions{IgnoreLease: true})
				require.ErrorContains(t, err, rpctypes.ErrKeyNotFound.Error())

				_, err = cc.Put(ctx, "foo", "bar", config.PutOptions{LeaseID: grant.ID})
				require.NoError(t, err)

				_, err = cc.Put(ctx, "foo", "bar1", config.PutOptions{IgnoreLease: true})
				require.NoError(t, err)

				resp, err := cc.Get(ctx, "foo", config.GetOptions{})
				require.NoError(t, err)
				require.Len(t, resp.Kvs, 1)
				require.Equal(t, "bar1", string(resp.Kvs[0].Value))
				require.Equal(t, int64(grant.ID), resp.Kvs[0].Lease)

				_, err = cc.Revoke(ctx, grant.ID)
				require.NoError(t, err)
			})
		})
	}
}

func TestKVGet(t *testing.T) {
	testKVGet(t, false)
}

func TestKVGetStream(t *testing.T) {
	testKVGet(t, true)
}

func testKVGet(t *testing.T, stream bool) {
	testRunner.BeforeTest(t)
	for _, tc := range clusterTestCases() {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(tc.config))
			defer clus.Close()
			cc := testutils.MustClient(clus.Client())

			if stream && !clusterSupportsGetStream(ctx, t, clus) {
				t.Skip("RangeStream is not supported by this cluster")
			}

			const noLease = int64(lease.NoLease)
			lease1, err := cc.Grant(ctx, 100)
			require.NoError(t, err)
			lease2, err := cc.Grant(ctx, 100)
			require.NoError(t, err)

			testutils.ExecuteUntil(ctx, t, func() {
				resp, err := cc.Get(ctx, "", config.GetOptions{Prefix: true})
				require.NoError(t, err)
				firstRev := resp.Header.Revision

				kvA := createKV("a", "aa1", firstRev+1, firstRev+1, 1, noLease)
				kvB := createKV("b", "a", firstRev+2, firstRev+2, 1, noLease)
				kvCV1 := createKV("c", "ac1", firstRev+3, firstRev+3, 1, noLease)
				kvCV2 := createKV("c", "ac2", firstRev+3, firstRev+4, 2, int64(lease1.ID))
				kvC := createKV("c", "aac", firstRev+3, firstRev+5, 3, int64(lease2.ID))
				kvFoo := createKV("foo", "bar", firstRev+6, firstRev+6, 1, noLease)
				kvFooAbc := createKV("foo/abc", "0", firstRev+7, firstRev+7, 1, noLease)
				kvFop := createKV("fop", "s", firstRev+8, firstRev+8, 1, noLease)

				inputs := []*mvccpb.KeyValue{kvA, kvB, kvCV1, kvCV2, kvC, kvFoo, kvFooAbc, kvFop}
				for i := range inputs {
					_, putError := cc.Put(ctx, string(inputs[i].Key), string(inputs[i].Value), config.PutOptions{LeaseID: clientv3.LeaseID(inputs[i].Lease)})
					require.NoErrorf(t, putError, "count not put key value %q", inputs[i])
				}

				allKvs := []*mvccpb.KeyValue{kvA, kvB, kvC, kvFoo, kvFooAbc, kvFop}
				kvsByVersion := []*mvccpb.KeyValue{kvA, kvB, kvFoo, kvFooAbc, kvFop, kvC}
				reversedKvs := []*mvccpb.KeyValue{kvFop, kvFooAbc, kvFoo, kvC, kvB, kvA}
				kvsByValue := []*mvccpb.KeyValue{kvFooAbc, kvB, kvA, kvC, kvFoo, kvFop}
				kvsByValueDesc := []*mvccpb.KeyValue{kvFop, kvFoo, kvC, kvA, kvB, kvFooAbc}

				currentResp, err := cc.Get(ctx, "", config.GetOptions{Prefix: true})
				require.NoError(t, err)
				currentHeader := &etcdserverpb.ResponseHeader{
					ClusterId: currentResp.Header.ClusterId,
					Revision:  currentResp.Header.Revision,
				}

				type testcase struct {
					name    string
					begin   string
					options config.GetOptions

					wantResponse *clientv3.GetResponse
				}
				tests := []testcase{
					{name: "Get one specific key (a)", begin: "a", wantResponse: &clientv3.GetResponse{Header: currentHeader, Count: 1, Kvs: []*mvccpb.KeyValue{kvA}}},
					{name: "Get one specific key (a), serializable", begin: "a", options: config.GetOptions{Serializable: true}, wantResponse: &clientv3.GetResponse{Header: currentHeader, Count: 1, Kvs: []*mvccpb.KeyValue{kvA}}},
					{name: "Get [a, c)", begin: "a", options: config.GetOptions{End: "c"}, wantResponse: &clientv3.GetResponse{Header: currentHeader, Count: 2, Kvs: allKvs[:2]}},
					{name: "blank key with --prefix option -> all KVs", begin: "", options: config.GetOptions{Prefix: true}, wantResponse: &clientv3.GetResponse{Header: currentHeader, Count: 6, Kvs: allKvs}},
					{name: "blank key with --from-key option -> all KVs", begin: "", options: config.GetOptions{FromKey: true}, wantResponse: &clientv3.GetResponse{Header: currentHeader, Count: 6, Kvs: allKvs}},
					{name: "Range covering all keys -> all KVs", begin: "a", options: config.GetOptions{End: "x"}, wantResponse: &clientv3.GetResponse{Header: currentHeader, Count: 6, Kvs: allKvs}},
					{name: "blank key with --prefix and revision -> [first key, entry at specified revision]", begin: "", options: config.GetOptions{Prefix: true, Revision: int(firstRev + 3)}, wantResponse: &clientv3.GetResponse{Header: currentHeader, Count: 3, Kvs: []*mvccpb.KeyValue{kvA, kvB, kvCV1}}},
					{name: "--count-only for one single key", begin: "a", options: config.GetOptions{CountOnly: true}, wantResponse: &clientv3.GetResponse{Header: currentHeader, Count: 1, Kvs: nil}},
					{name: "--count-only --prefix with no matching keys", begin: "zzz", options: config.GetOptions{CountOnly: true, Prefix: true}, wantResponse: &clientv3.GetResponse{Header: currentHeader, Count: 0, Kvs: nil}},
					{name: "--count-only --prefix of a", begin: "a", options: config.GetOptions{CountOnly: true, Prefix: true}, wantResponse: &clientv3.GetResponse{Header: currentHeader, Count: 1, Kvs: nil}},
					{name: "--prefix of foo -> all entries with the prefix", begin: "foo", options: config.GetOptions{Prefix: true}, wantResponse: &clientv3.GetResponse{Header: currentHeader, Count: 2, Kvs: allKvs[3:5]}},
					{name: "--from-key of 'foo' -> <end>", begin: "foo", options: config.GetOptions{FromKey: true}, wantResponse: &clientv3.GetResponse{Header: currentHeader, Count: 3, Kvs: allKvs[3:]}},
					{name: "blank key with limit set", begin: "", options: config.GetOptions{Prefix: true, Limit: 2}, wantResponse: &clientv3.GetResponse{Header: currentHeader, Count: 6, Kvs: allKvs[:2], More: true}},
					{name: "all kvs ordered by mod revision ascending", begin: "", options: config.GetOptions{Prefix: true, Order: clientv3.SortAscend, SortBy: clientv3.SortByModRevision}, wantResponse: &clientv3.GetResponse{Header: currentHeader, Count: 6, Kvs: allKvs}},
					{name: "all KVs ordered by version ascending", begin: "", options: config.GetOptions{Prefix: true, Order: clientv3.SortAscend, SortBy: clientv3.SortByVersion}, wantResponse: &clientv3.GetResponse{Header: currentHeader, Count: 6, Kvs: kvsByVersion}},
					{name: "all KVs ordered by key ascending, limit 2", begin: "", options: config.GetOptions{Prefix: true, Order: clientv3.SortAscend, SortBy: clientv3.SortByKey, Limit: 2}, wantResponse: &clientv3.GetResponse{Header: currentHeader, Count: 6, Kvs: []*mvccpb.KeyValue{kvA, kvB}, More: true}},
					{name: "range [b, z) ordered by key descending, limit 2", begin: "b", options: config.GetOptions{End: "z", Order: clientv3.SortDescend, SortBy: clientv3.SortByKey, Limit: 2}, wantResponse: &clientv3.GetResponse{Header: currentHeader, Count: 5, Kvs: []*mvccpb.KeyValue{kvFop, kvFooAbc}, More: true}},
					{name: "all KVs ordered by create revision, unspecified sort order", begin: "", options: config.GetOptions{Prefix: true, Order: clientv3.SortNone, SortBy: clientv3.SortByCreateRevision}, wantResponse: &clientv3.GetResponse{Header: currentHeader, Count: 6, Kvs: allKvs}},
					{name: "all KVs ordered by create revision descending", begin: "", options: config.GetOptions{Prefix: true, Order: clientv3.SortDescend, SortBy: clientv3.SortByCreateRevision}, wantResponse: &clientv3.GetResponse{Header: currentHeader, Count: 6, Kvs: reversedKvs}},
					{name: "all KVs ordered by key descending", begin: "", options: config.GetOptions{Prefix: true, Order: clientv3.SortDescend, SortBy: clientv3.SortByKey}, wantResponse: &clientv3.GetResponse{Header: currentHeader, Count: 6, Kvs: reversedKvs}},
					{name: "all KVs ordered by value, unspecified sort order", begin: "", options: config.GetOptions{Prefix: true, Order: clientv3.SortNone, SortBy: clientv3.SortByValue}, wantResponse: &clientv3.GetResponse{Header: currentHeader, Count: 6, Kvs: kvsByValue}},
					{name: "all KVs ordered by value, ascending", begin: "", options: config.GetOptions{Prefix: true, Order: clientv3.SortAscend, SortBy: clientv3.SortByValue}, wantResponse: &clientv3.GetResponse{Header: currentHeader, Count: 6, Kvs: kvsByValue}},
					{name: "all KVs ordered by value descending", begin: "", options: config.GetOptions{Prefix: true, Order: clientv3.SortDescend, SortBy: clientv3.SortByValue}, wantResponse: &clientv3.GetResponse{Header: currentHeader, Count: 6, Kvs: kvsByValueDesc}},
					{name: "all KVs descending", begin: "", options: config.GetOptions{Prefix: true, Order: clientv3.SortDescend}, wantResponse: &clientv3.GetResponse{Header: currentHeader, Count: 6, Kvs: reversedKvs}},
					{name: "Get first version of 'c' by its revision", begin: "c", options: config.GetOptions{Revision: int(firstRev) + 3}, wantResponse: &clientv3.GetResponse{Header: currentHeader, Count: 1, Kvs: []*mvccpb.KeyValue{kvCV1}}},
					{name: "Get second version of 'c' by its revision", begin: "c", options: config.GetOptions{Revision: int(firstRev) + 4}, wantResponse: &clientv3.GetResponse{Header: currentHeader, Count: 1, Kvs: []*mvccpb.KeyValue{kvCV2}}},
					{name: "Get third version of 'c' by its revision", begin: "c", options: config.GetOptions{Revision: int(firstRev) + 5}, wantResponse: &clientv3.GetResponse{Header: currentHeader, Count: 1, Kvs: []*mvccpb.KeyValue{kvC}}},
					{name: "Get the latest version of 'c'", begin: "c", wantResponse: &clientv3.GetResponse{Header: currentHeader, Count: 1, Kvs: []*mvccpb.KeyValue{kvC}}},
					{name: "all KVs with mininum mod revision sorted by mod revision", begin: "", options: config.GetOptions{Prefix: true, MinModRevision: int(firstRev) + 3, SortBy: clientv3.SortByModRevision}, wantResponse: &clientv3.GetResponse{Header: currentHeader, Count: 6, Kvs: allKvs[2:]}},
					{name: "all KVs with maximum mod revision, sorted by key descending", begin: "", options: config.GetOptions{Prefix: true, MaxModRevision: int(firstRev) + 4, Order: clientv3.SortDescend, SortBy: clientv3.SortByKey}, wantResponse: &clientv3.GetResponse{Header: currentHeader, Count: 6, Kvs: reversedKvs[4:]}},
					{name: "all KVs with minimum create revision, sorted by version, descending", begin: "", options: config.GetOptions{Prefix: true, MinCreateRevision: int(firstRev) + 3, Order: clientv3.SortDescend, SortBy: clientv3.SortByVersion}, wantResponse: &clientv3.GetResponse{Header: currentHeader, Count: 6, Kvs: allKvs[2:]}},
					{name: "all KVs with maximimum create revision, sorted by value", begin: "", options: config.GetOptions{Prefix: true, MaxCreateRevision: int(firstRev) + 6, Order: clientv3.SortDescend, SortBy: clientv3.SortByValue}, wantResponse: &clientv3.GetResponse{Header: currentHeader, Count: 6, Kvs: kvsByValueDesc[1:5]}},
				}

				testsWithKeysOnly := make([]testcase, 0, len(tests))
				for _, otc := range tests {
					if otc.options.CountOnly {
						continue // can't use both --count-only and --keys-only at the same time
					}
					withKeysOnly := otc
					withKeysOnly.name = fmt.Sprintf("%s --keys-only", withKeysOnly.name)
					withKeysOnly.options.KeysOnly = true
					withKeysOnly.wantResponse = cloneGetResponseWithoutValues(otc.wantResponse)
					testsWithKeysOnly = append(testsWithKeysOnly, withKeysOnly)
				}
				for _, tt := range slices.Concat(tests, testsWithKeysOnly) {
					t.Run(tt.name, func(t *testing.T) {
						if stream && !rangeStreamSupports(tt.options) {
							t.Skip("options not supported by RangeStream")
						}
						opts := tt.options
						opts.Stream = stream
						resp, err := cc.Get(ctx, tt.begin, opts)
						require.NoErrorf(t, err, "count not get key %q, err: %s", tt.begin, err)
						resp.Header.MemberId = 0
						resp.Header.RaftTerm = 0
						resp.Header.LeaderId = 0
						assert.Emptyf(t,
							cmp.Diff(
								(*etcdserverpb.RangeResponse)(tt.wantResponse),
								(*etcdserverpb.RangeResponse)(resp),
								protocmp.Transform(),
							),
							"-want, +got")
					})
				}
			})
		})
	}
}

func createKV(key, val string, createRev, modRev, ver, lease int64) *mvccpb.KeyValue {
	return &mvccpb.KeyValue{
		Key:            []byte(key),
		Value:          []byte(val),
		CreateRevision: createRev,
		ModRevision:    modRev,
		Version:        ver,
		Lease:          lease,
	}
}

// clusterSupportsGetStream probes every cluster member with a RangeStream RPC and returns false if any member rejects it.
func clusterSupportsGetStream(ctx context.Context, t *testing.T, clus interfaces.Cluster) bool {
	for _, m := range clus.Members() {
		_, err := m.Client().Get(ctx, "probe", config.GetOptions{Stream: true})
		if err != nil {
			t.Logf("member does not support RangeStream: %v", err)
			return false
		}
	}
	return true
}

// rangeStreamSupports reports whether the server's RangeStream RPC accepts a
// request with these options, mirroring v3rpc.checkRangeStreamRequest.
func rangeStreamSupports(o config.GetOptions) bool {
	if !txn.IsDefaultOrdering(
		etcdserverpb.RangeRequest_SortTarget(o.SortBy),
		etcdserverpb.RangeRequest_SortOrder(o.Order),
	) {
		return false
	}
	return !txn.HasRevisionFilters(&etcdserverpb.RangeRequest{
		MinModRevision:    int64(o.MinModRevision),
		MaxModRevision:    int64(o.MaxModRevision),
		MinCreateRevision: int64(o.MinCreateRevision),
		MaxCreateRevision: int64(o.MaxCreateRevision),
	})
}

func cloneGetResponseWithoutValues(resp *clientv3.GetResponse) *clientv3.GetResponse {
	clone := cloneGetResponse(resp)
	if clone == nil {
		return nil
	}
	for _, kv := range clone.Kvs {
		if kv != nil {
			kv.Value = nil
		}
	}
	return clone
}

func cloneGetResponse(resp *clientv3.GetResponse) *clientv3.GetResponse {
	if resp == nil {
		return nil
	}
	return (*clientv3.GetResponse)(
		proto.Clone(
			(*etcdserverpb.RangeResponse)(resp),
		).(*etcdserverpb.RangeResponse),
	)
}

func TestKVDelete(t *testing.T) {
	testRunner.BeforeTest(t)
	for _, tc := range clusterTestCases() {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(tc.config))
			defer clus.Close()
			cc := testutils.MustClient(clus.Client())
			testutils.ExecuteUntil(ctx, t, func() {
				kvs := []string{"a", "b", "c", "c/abc", "d"}
				tests := []struct {
					deleteKey string
					options   config.DeleteOptions

					wantDeleted int
					wantKeys    []string
				}{
					{ // delete all keys
						deleteKey:   "",
						options:     config.DeleteOptions{Prefix: true},
						wantDeleted: 5,
					},
					{ // delete all keys
						deleteKey:   "",
						options:     config.DeleteOptions{FromKey: true},
						wantDeleted: 5,
					},
					{
						deleteKey:   "a",
						options:     config.DeleteOptions{End: "c"},
						wantDeleted: 2,
						wantKeys:    []string{"c", "c/abc", "d"},
					},
					{
						deleteKey:   "c",
						wantDeleted: 1,
						wantKeys:    []string{"a", "b", "c/abc", "d"},
					},
					{
						deleteKey:   "c",
						options:     config.DeleteOptions{Prefix: true},
						wantDeleted: 2,
						wantKeys:    []string{"a", "b", "d"},
					},
					{
						deleteKey:   "c",
						options:     config.DeleteOptions{FromKey: true},
						wantDeleted: 3,
						wantKeys:    []string{"a", "b"},
					},
					{
						deleteKey:   "e",
						wantDeleted: 0,
						wantKeys:    kvs,
					},
				}
				for _, tt := range tests {
					for i := range kvs {
						_, err := cc.Put(ctx, kvs[i], "bar", config.PutOptions{})
						require.NoErrorf(t, err, "count not put key %q", kvs[i])
					}
					del, err := cc.Delete(ctx, tt.deleteKey, tt.options)
					require.NoErrorf(t, err, "count not get key %q, err", tt.deleteKey)
					assert.Equal(t, tt.wantDeleted, int(del.Deleted))
					get, err := cc.Get(ctx, "", config.GetOptions{Prefix: true})
					require.NoErrorf(t, err, "count not get key")
					kvs := testutils.KeysFromGetResponse(get)
					assert.Equal(t, tt.wantKeys, kvs)
				}
			})
		})
	}
}

func TestKVGetNoQuorum(t *testing.T) {
	testRunner.BeforeTest(t)
	tcs := []struct {
		name    string
		options config.GetOptions

		wantError bool
	}{
		{
			name:    "Serializable",
			options: config.GetOptions{Serializable: true},
		},
		{
			name:      "Linearizable",
			options:   config.GetOptions{Serializable: false, Timeout: time.Second},
			wantError: true,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			clus := testRunner.NewCluster(ctx, t)
			defer clus.Close()

			clus.Members()[0].Stop()
			clus.Members()[1].Stop()

			cc := clus.Members()[2].Client()
			testutils.ExecuteUntil(ctx, t, func() {
				key := "foo"
				_, err := cc.Get(ctx, key, tc.options)
				if tc.wantError {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
			})
		})
	}
}
