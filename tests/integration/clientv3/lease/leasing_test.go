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

package lease_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/testing/protocmp"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
	"go.etcd.io/etcd/client/v3/leasing"
	"go.etcd.io/etcd/tests/v3/framework/integration"
)

func TestLeasingPutGet(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	lKV1, closeLKV1, err := leasing.NewKV(clus.Client(0), "foo/")
	require.NoError(t, err)
	defer closeLKV1()

	lKV2, closeLKV2, err := leasing.NewKV(clus.Client(1), "foo/")
	require.NoError(t, err)
	defer closeLKV2()

	lKV3, closeLKV3, err := leasing.NewKV(clus.Client(2), "foo/")
	require.NoError(t, err)
	defer closeLKV3()

	resp, err := lKV1.Get(t.Context(), "abc")
	require.NoError(t, err)
	if len(resp.Kvs) != 0 {
		t.Errorf("expected nil, got %q", resp.Kvs[0].Key)
	}

	_, err = lKV1.Put(t.Context(), "abc", "def")
	require.NoError(t, err)
	resp, err = lKV2.Get(t.Context(), "abc")
	require.NoError(t, err)
	if string(resp.Kvs[0].Key) != "abc" {
		t.Errorf("expected key=%q, got key=%q", "abc", resp.Kvs[0].Key)
	}
	if string(resp.Kvs[0].Value) != "def" {
		t.Errorf("expected value=%q, got value=%q", "def", resp.Kvs[0].Value)
	}

	_, err = lKV3.Get(t.Context(), "abc")
	require.NoError(t, err)
	_, err = lKV2.Put(t.Context(), "abc", "ghi")
	require.NoError(t, err)

	resp, err = lKV3.Get(t.Context(), "abc")
	require.NoError(t, err)
	if string(resp.Kvs[0].Key) != "abc" {
		t.Errorf("expected key=%q, got key=%q", "abc", resp.Kvs[0].Key)
	}

	if string(resp.Kvs[0].Value) != "ghi" {
		t.Errorf("expected value=%q, got value=%q", "ghi", resp.Kvs[0].Value)
	}
}

// TestLeasingInterval checks the leasing KV fetches key intervals.
func TestLeasingInterval(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	lkv, closeLKV, err := leasing.NewKV(clus.Client(0), "pfx/")
	require.NoError(t, err)
	defer closeLKV()

	keys := []string{"abc/a", "abc/b", "abc/a/a"}
	for _, k := range keys {
		_, err = clus.Client(0).Put(t.Context(), k, "v")
		require.NoError(t, err)
	}

	resp, err := lkv.Get(t.Context(), "abc/", clientv3.WithPrefix())
	require.NoError(t, err)
	require.Lenf(t, resp.Kvs, 3, "expected keys %+v, got response keys %+v", keys, resp.Kvs)

	// load into cache
	_, err = lkv.Get(t.Context(), "abc/a")
	require.NoError(t, err)

	// get when prefix is also a cached key
	resp, err = lkv.Get(t.Context(), "abc/a", clientv3.WithPrefix())
	require.NoError(t, err)
	require.Lenf(t, resp.Kvs, 2, "expected keys %+v, got response keys %+v", keys, resp.Kvs)
}

// TestLeasingPutInvalidateNew checks the leasing KV updates its cache on a Put to a new key.
func TestLeasingPutInvalidateNew(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	lkv, closeLKV, err := leasing.NewKV(clus.Client(0), "pfx/")
	require.NoError(t, err)
	defer closeLKV()

	_, err = lkv.Get(t.Context(), "k")
	require.NoError(t, err)
	_, err = lkv.Put(t.Context(), "k", "v")
	require.NoError(t, err)

	lkvResp, err := lkv.Get(t.Context(), "k")
	require.NoError(t, err)
	cResp, cerr := clus.Client(0).Get(t.Context(), "k")
	require.NoError(t, cerr)
	require.Emptyf(t, cmp.Diff((*pb.RangeResponse)(lkvResp), (*pb.RangeResponse)(cResp), protocmp.Transform()), "-want, +got")
}

// TestLeasingPutInvalidateExisting checks the leasing KV updates its cache on a Put to an existing key.
func TestLeasingPutInvalidateExisting(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	_, err := clus.Client(0).Put(t.Context(), "k", "abc")
	require.NoError(t, err)

	lkv, closeLKV, err := leasing.NewKV(clus.Client(0), "pfx/")
	require.NoError(t, err)
	defer closeLKV()

	_, err = lkv.Get(t.Context(), "k")
	require.NoError(t, err)
	_, err = lkv.Put(t.Context(), "k", "v")
	require.NoError(t, err)

	lkvResp, err := lkv.Get(t.Context(), "k")
	require.NoError(t, err)
	cResp, cerr := clus.Client(0).Get(t.Context(), "k")
	require.NoError(t, cerr)
	require.Emptyf(t, cmp.Diff((*pb.RangeResponse)(lkvResp), (*pb.RangeResponse)(cResp), protocmp.Transform()), "-want, +got")
}

// TestLeasingGetNoLeaseTTL checks a key with a TTL is not leased.
func TestLeasingGetNoLeaseTTL(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1, UseBridge: true})
	defer clus.Terminate(t)

	lkv, closeLKV, err := leasing.NewKV(clus.Client(0), "pfx/")
	require.NoError(t, err)
	defer closeLKV()

	lresp, err := clus.Client(0).Grant(t.Context(), 60)
	require.NoError(t, err)

	_, err = clus.Client(0).Put(t.Context(), "k", "v", clientv3.WithLease(lresp.ID))
	require.NoError(t, err)

	gresp, err := lkv.Get(t.Context(), "k")
	require.NoError(t, err)
	assert.Len(t, gresp.Kvs, 1)

	clus.Members[0].Stop(t)

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	_, err = lkv.Get(ctx, "k")
	cancel()
	assert.Equal(t, err, ctx.Err())
}

// TestLeasingGetSerializable checks the leasing KV can make serialized requests
// when the etcd cluster is partitioned.
func TestLeasingGetSerializable(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 2, UseBridge: true})
	defer clus.Terminate(t)

	lkv, closeLKV, err := leasing.NewKV(clus.Client(0), "pfx/")
	require.NoError(t, err)
	defer closeLKV()

	_, err = clus.Client(0).Put(t.Context(), "cached", "abc")
	require.NoError(t, err)
	_, err = lkv.Get(t.Context(), "cached")
	require.NoError(t, err)

	clus.Members[1].Stop(t)

	// don't necessarily try to acquire leasing key ownership for new key
	resp, err := lkv.Get(t.Context(), "uncached", clientv3.WithSerializable())
	require.NoError(t, err)
	require.Emptyf(t, resp.Kvs, `expected no keys, got response %+v`, resp)

	clus.Members[0].Stop(t)

	// leasing key ownership should have "cached" locally served
	cachedResp, err := lkv.Get(t.Context(), "cached", clientv3.WithSerializable())
	require.NoError(t, err)
	if len(cachedResp.Kvs) != 1 || string(cachedResp.Kvs[0].Value) != "abc" {
		t.Fatalf(`expected "cached"->"abc", got response %+v`, cachedResp)
	}
}

// TestLeasingPrevKey checks the cache respects WithPrevKV on puts.
func TestLeasingPrevKey(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 2})
	defer clus.Terminate(t)

	lkv, closeLKV, err := leasing.NewKV(clus.Client(0), "pfx/")
	require.NoError(t, err)
	defer closeLKV()

	_, err = clus.Client(0).Put(t.Context(), "k", "abc")
	require.NoError(t, err)
	// acquire leasing key
	_, err = lkv.Get(t.Context(), "k")
	require.NoError(t, err)
	resp, err := lkv.Put(t.Context(), "k", "def", clientv3.WithPrevKV())
	require.NoError(t, err)
	if resp.PrevKv == nil || string(resp.PrevKv.Value) != "abc" {
		t.Fatalf(`expected PrevKV.Value="abc", got response %+v`, resp)
	}
}

// TestLeasingRevGet checks the cache respects Get by Revision.
func TestLeasingRevGet(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	lkv, closeLKV, err := leasing.NewKV(clus.Client(0), "pfx/")
	require.NoError(t, err)
	defer closeLKV()

	putResp, err := clus.Client(0).Put(t.Context(), "k", "abc")
	require.NoError(t, err)
	_, err = clus.Client(0).Put(t.Context(), "k", "def")
	require.NoError(t, err)

	// check historic revision
	getResp, gerr := lkv.Get(t.Context(), "k", clientv3.WithRev(putResp.Header.Revision))
	require.NoError(t, gerr)
	if len(getResp.Kvs) != 1 || string(getResp.Kvs[0].Value) != "abc" {
		t.Fatalf(`expected "k"->"abc" at rev=%d, got response %+v`, putResp.Header.Revision, getResp)
	}
	// check current revision
	getResp, gerr = lkv.Get(t.Context(), "k")
	require.NoError(t, gerr)
	if len(getResp.Kvs) != 1 || string(getResp.Kvs[0].Value) != "def" {
		t.Fatalf(`expected "k"->"def" at rev=%d, got response %+v`, putResp.Header.Revision, getResp)
	}
}

// TestLeasingGetWithOpts checks options that can be served through the cache do not depend on the server.
func TestLeasingGetWithOpts(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1, UseBridge: true})
	defer clus.Terminate(t)

	lkv, closeLKV, err := leasing.NewKV(clus.Client(0), "pfx/")
	require.NoError(t, err)
	defer closeLKV()

	_, err = clus.Client(0).Put(t.Context(), "k", "abc")
	require.NoError(t, err)
	// in cache
	_, err = lkv.Get(t.Context(), "k", clientv3.WithKeysOnly())
	require.NoError(t, err)

	clus.Members[0].Stop(t)

	opts := []clientv3.OpOption{
		clientv3.WithKeysOnly(),
		clientv3.WithLimit(1),
		clientv3.WithMinCreateRev(1),
		clientv3.WithMinModRev(1),
		clientv3.WithSort(clientv3.SortByKey, clientv3.SortAscend),
		clientv3.WithSerializable(),
	}
	for _, opt := range opts {
		_, err = lkv.Get(t.Context(), "k", opt)
		require.NoError(t, err)
	}

	var getOpts []clientv3.OpOption
	for i := 0; i < len(opts); i++ {
		getOpts = append(getOpts, opts[rand.Intn(len(opts))])
	}
	getOpts = getOpts[:rand.Intn(len(opts))]
	_, err = lkv.Get(t.Context(), "k", getOpts...)
	require.NoError(t, err)
}

// TestLeasingConcurrentPut ensures that a get after concurrent puts returns
// the recently put data.
func TestLeasingConcurrentPut(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	lkv, closeLKV, err := leasing.NewKV(clus.Client(0), "pfx/")
	require.NoError(t, err)
	defer closeLKV()

	// force key into leasing key cache
	_, err = lkv.Get(t.Context(), "k")
	require.NoError(t, err)

	// concurrently put through leasing client
	numPuts := 16
	putc := make(chan *clientv3.PutResponse, numPuts)
	for i := 0; i < numPuts; i++ {
		go func() {
			resp, perr := lkv.Put(t.Context(), "k", "abc")
			if perr != nil {
				t.Error(perr)
			}
			putc <- resp
		}()
	}
	// record maximum revision from puts
	maxRev := int64(0)
	for i := 0; i < numPuts; i++ {
		if resp := <-putc; resp.Header.Revision > maxRev {
			maxRev = resp.Header.Revision
		}
	}

	// confirm Get gives most recently put revisions
	getResp, gerr := lkv.Get(t.Context(), "k")
	require.NoError(t, gerr)
	if mr := getResp.Kvs[0].ModRevision; mr != maxRev {
		t.Errorf("expected ModRevision %d, got %d", maxRev, mr)
	}
	if ver := getResp.Kvs[0].Version; ver != int64(numPuts) {
		t.Errorf("expected Version %d, got %d", numPuts, ver)
	}
}

func TestLeasingDisconnectedGet(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1, UseBridge: true})
	defer clus.Terminate(t)

	lkv, closeLKV, err := leasing.NewKV(clus.Client(0), "pfx/")
	require.NoError(t, err)
	defer closeLKV()

	_, err = clus.Client(0).Put(t.Context(), "cached", "abc")
	require.NoError(t, err)
	// get key so it's cached
	_, err = lkv.Get(t.Context(), "cached")
	require.NoError(t, err)

	clus.Members[0].Stop(t)

	// leasing key ownership should have "cached" locally served
	cachedResp, err := lkv.Get(t.Context(), "cached")
	require.NoError(t, err)
	if len(cachedResp.Kvs) != 1 || string(cachedResp.Kvs[0].Value) != "abc" {
		t.Fatalf(`expected "cached"->"abc", got response %+v`, cachedResp)
	}
}

func TestLeasingDeleteOwner(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	lkv, closeLKV, err := leasing.NewKV(clus.Client(0), "pfx/")
	require.NoError(t, err)
	defer closeLKV()

	_, err = clus.Client(0).Put(t.Context(), "k", "abc")
	require.NoError(t, err)

	// get+own / delete / get
	_, err = lkv.Get(t.Context(), "k")
	require.NoError(t, err)
	_, err = lkv.Delete(t.Context(), "k")
	require.NoError(t, err)
	resp, err := lkv.Get(t.Context(), "k")
	require.NoError(t, err)

	require.Emptyf(t, resp.Kvs, `expected "k" to be deleted, got response %+v`, resp)
	// try to double delete
	_, err = lkv.Delete(t.Context(), "k")
	require.NoError(t, err)
}

func TestLeasingDeleteNonOwner(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	lkv1, closeLKV1, err := leasing.NewKV(clus.Client(0), "pfx/")
	require.NoError(t, err)
	defer closeLKV1()

	lkv2, closeLKV2, err := leasing.NewKV(clus.Client(0), "pfx/")
	require.NoError(t, err)
	defer closeLKV2()

	_, err = clus.Client(0).Put(t.Context(), "k", "abc")
	require.NoError(t, err)
	// acquire ownership
	_, err = lkv1.Get(t.Context(), "k")
	require.NoError(t, err)
	// delete via non-owner
	_, err = lkv2.Delete(t.Context(), "k")
	require.NoError(t, err)

	// key should be removed from lkv1
	resp, err := lkv1.Get(t.Context(), "k")
	require.NoError(t, err)
	require.Emptyf(t, resp.Kvs, `expected "k" to be deleted, got response %+v`, resp)
}

func TestLeasingDeleteProtectsProtocolKeys(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	lkv, closeLKV, err := leasing.NewKV(clus.Client(0), "pfx/")
	require.NoError(t, err)
	defer closeLKV()

	for _, key := range []string{"a", "b"} {
		_, err = lkv.Put(t.Context(), key, key)
		require.NoError(t, err)
		_, err = lkv.Get(t.Context(), key)
		require.NoError(t, err)
	}

	deleteFromB := clientv3.OpDelete("b", clientv3.WithFromKey())
	deleteCalls := map[string]func() error{
		"Delete": func() error {
			_, callErr := lkv.Delete(t.Context(), "b", clientv3.WithFromKey())
			return callErr
		},
		"Do": func() error {
			_, callErr := lkv.Do(t.Context(), deleteFromB)
			return callErr
		},
		"Txn": func() error {
			_, callErr := lkv.Txn(t.Context()).Then(deleteFromB).Commit()
			return callErr
		},
		"nested Txn": func() error {
			nestedTxn := clientv3.OpTxn(nil, []clientv3.Op{deleteFromB}, nil)
			_, callErr := lkv.Txn(t.Context()).Then(nestedTxn).Commit()
			return callErr
		},
	}
	for name, call := range deleteCalls {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, codes.InvalidArgument, status.Code(call()))
		})
	}

	userKeys, err := clus.Client(0).Get(t.Context(), "a", clientv3.WithRange("c"))
	require.NoError(t, err)
	require.Len(t, userKeys.Kvs, 2)
	protocolKeys, err := clus.Client(0).Get(t.Context(), "pfx/", clientv3.WithPrefix())
	require.NoError(t, err)
	require.Len(t, protocolKeys.Kvs, 2)
}

func TestLeasingOverwriteResponse(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	lkv, closeLKV, err := leasing.NewKV(clus.Client(0), "pfx/")
	require.NoError(t, err)
	defer closeLKV()

	_, err = clus.Client(0).Put(t.Context(), "k", "abc")
	require.NoError(t, err)

	resp, err := lkv.Get(t.Context(), "k")
	require.NoError(t, err)

	resp.Kvs[0].Key[0] = 'z'
	resp.Kvs[0].Value[0] = 'z'

	resp, err = lkv.Get(t.Context(), "k")
	require.NoError(t, err)

	if string(resp.Kvs[0].Key) != "k" {
		t.Errorf(`expected key "k", got %q`, string(resp.Kvs[0].Key))
	}
	if string(resp.Kvs[0].Value) != "abc" {
		t.Errorf(`expected value "abc", got %q`, string(resp.Kvs[0].Value))
	}
}

func TestLeasingOwnerPutResponse(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1, UseBridge: true})
	defer clus.Terminate(t)

	lkv, closeLKV, err := leasing.NewKV(clus.Client(0), "pfx/")
	require.NoError(t, err)
	defer closeLKV()

	_, err = clus.Client(0).Put(t.Context(), "k", "abc")
	require.NoError(t, err)
	_, gerr := lkv.Get(t.Context(), "k")
	require.NoError(t, gerr)
	presp, err := lkv.Put(t.Context(), "k", "def")
	require.NoError(t, err)
	if presp == nil {
		t.Fatal("expected put response, got nil")
	}

	clus.Members[0].Stop(t)

	gresp, gerr := lkv.Get(t.Context(), "k")
	require.NoError(t, gerr)
	if gresp.Kvs[0].ModRevision != presp.Header.Revision {
		t.Errorf("expected mod revision %d, got %d", presp.Header.Revision, gresp.Kvs[0].ModRevision)
	}
	if gresp.Kvs[0].Version != 2 {
		t.Errorf("expected version 2, got version %d", gresp.Kvs[0].Version)
	}
}

func TestLeasingTxnOwnerGetRange(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	lkv, closeLKV, err := leasing.NewKV(clus.Client(0), "pfx/")
	require.NoError(t, err)
	defer closeLKV()

	keyCount := rand.Intn(10) + 1
	for i := 0; i < keyCount; i++ {
		k := fmt.Sprintf("k-%d", i)
		_, err = clus.Client(0).Put(t.Context(), k, k+k)
		require.NoError(t, err)
	}
	_, err = lkv.Get(t.Context(), "k-")
	require.NoError(t, err)

	tresp, terr := lkv.Txn(t.Context()).Then(clientv3.OpGet("k-", clientv3.WithPrefix())).Commit()
	require.NoError(t, terr)
	resp := tresp.Responses[0].GetResponseRange()
	require.Equalf(t, len(resp.Kvs), keyCount, "expected %d keys, got response %+v", keyCount, resp.Kvs)
}

func TestLeasingTxnOwnerGet(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1, UseBridge: true})
	defer clus.Terminate(t)

	client := clus.Client(0)

	lkv, closeLKV, err := leasing.NewKV(client, "pfx/")
	require.NoError(t, err)

	defer func() {
		// In '--tags cluster_proxy' mode the client need to be closed before
		// closeLKV(). This interrupts all outstanding watches. Closing by closeLKV()
		// is not sufficient as (unfortunately) context close does not interrupts Watches.
		// See ./clientv3/watch.go:
		// >> Currently, client contexts are overwritten with "valCtx" that never closes. <<
		clus.TakeClient(0) // avoid double Close() of the client.
		client.Close()
		closeLKV()
	}()

	// TODO: Randomization in tests is a bad practice (except explicitly exploratory).
	keyCount := rand.Intn(10) + 1
	var ops []clientv3.Op
	presps := make([]*clientv3.PutResponse, keyCount)
	for i := range presps {
		k := fmt.Sprintf("k-%d", i)
		presp, err := client.Put(t.Context(), k, k+k)
		require.NoError(t, err)
		presps[i] = presp

		_, err = lkv.Get(t.Context(), k)
		require.NoError(t, err)
		ops = append(ops, clientv3.OpGet(k))
	}

	// TODO: Randomization in unit tests is a bad practice (except explicitly exploratory).
	ops = ops[:rand.Intn(len(ops))]

	// served through cache
	clus.Members[0].Stop(t)

	var thenOps, elseOps []clientv3.Op
	cmps, useThen := randCmps("k-", presps)

	if useThen {
		thenOps = ops
		elseOps = []clientv3.Op{clientv3.OpPut("k", "1")}
	} else {
		thenOps = []clientv3.Op{clientv3.OpPut("k", "1")}
		elseOps = ops
	}

	tresp, terr := lkv.Txn(t.Context()).
		If(cmps...).
		Then(thenOps...).
		Else(elseOps...).Commit()

	require.NoError(t, terr)
	require.Equalf(t, tresp.Succeeded, useThen, "expected succeeded=%v, got tresp=%+v", useThen, tresp)
	require.Lenf(t, ops, len(tresp.Responses), "expected %d responses, got %d", len(ops), len(tresp.Responses))
	wrev := presps[len(presps)-1].Header.Revision
	require.GreaterOrEqualf(t, tresp.Header.Revision, wrev, "expected header revision >= %d, got %d", wrev, tresp.Header.Revision)
	for i := range ops {
		k := fmt.Sprintf("k-%d", i)
		rr := tresp.Responses[i].GetResponseRange()
		require.NotNilf(t, rr, "expected get response, got %+v", tresp.Responses[i])
		if string(rr.Kvs[0].Key) != k || string(rr.Kvs[0].Value) != k+k {
			t.Errorf(`expected key for %q, got %+v`, k, rr.Kvs)
		}
	}
}

func TestLeasingTxnOwnerDeleteRange(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	lkv, closeLKV, err := leasing.NewKV(clus.Client(0), "pfx/")
	require.NoError(t, err)
	defer closeLKV()

	keyCount := rand.Intn(10) + 1
	for i := 0; i < keyCount; i++ {
		k := fmt.Sprintf("k-%d", i)
		_, perr := clus.Client(0).Put(t.Context(), k, k+k)
		require.NoError(t, perr)
	}

	// cache in lkv
	resp, err := lkv.Get(t.Context(), "k-", clientv3.WithPrefix())
	require.NoError(t, err)
	require.Equalf(t, len(resp.Kvs), keyCount, "expected %d keys, got %d", keyCount, len(resp.Kvs))

	_, terr := lkv.Txn(t.Context()).Then(clientv3.OpDelete("k-", clientv3.WithPrefix())).Commit()
	require.NoError(t, terr)

	resp, err = lkv.Get(t.Context(), "k-", clientv3.WithPrefix())
	require.NoError(t, err)
	require.Emptyf(t, resp.Kvs, "expected no keys, got %d", len(resp.Kvs))
}

func TestLeasingTxnOwnerDelete(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	lkv, closeLKV, err := leasing.NewKV(clus.Client(0), "pfx/")
	require.NoError(t, err)
	defer closeLKV()

	_, err = clus.Client(0).Put(t.Context(), "k", "abc")
	require.NoError(t, err)

	// cache in lkv
	_, gerr := lkv.Get(t.Context(), "k")
	require.NoError(t, gerr)

	_, terr := lkv.Txn(t.Context()).Then(clientv3.OpDelete("k")).Commit()
	require.NoError(t, terr)

	resp, err := lkv.Get(t.Context(), "k")
	require.NoError(t, err)
	require.Emptyf(t, resp.Kvs, "expected no keys, got %d", len(resp.Kvs))
}

func TestLeasingTxnOwnerIf(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1, UseBridge: true})
	defer clus.Terminate(t)

	lkv, closeLKV, err := leasing.NewKV(clus.Client(0), "pfx/")
	require.NoError(t, err)
	defer closeLKV()

	_, err = clus.Client(0).Put(t.Context(), "k", "abc")
	require.NoError(t, err)
	_, err = lkv.Get(t.Context(), "k")
	require.NoError(t, err)

	// served through cache
	clus.Members[0].Stop(t)

	tests := []struct {
		cmps       []clientv3.Cmp
		wSucceeded bool
		wResponses int
	}{
		// success
		{
			cmps:       []clientv3.Cmp{clientv3.Compare(clientv3.Value("k"), "=", "abc")},
			wSucceeded: true,
			wResponses: 1,
		},
		{
			cmps:       []clientv3.Cmp{clientv3.Compare(clientv3.CreateRevision("k"), "=", 2)},
			wSucceeded: true,
			wResponses: 1,
		},
		{
			cmps:       []clientv3.Cmp{clientv3.Compare(clientv3.ModRevision("k"), "=", 2)},
			wSucceeded: true,
			wResponses: 1,
		},
		{
			cmps:       []clientv3.Cmp{clientv3.Compare(clientv3.Version("k"), "=", 1)},
			wSucceeded: true,
			wResponses: 1,
		},
		// failure
		{
			cmps: []clientv3.Cmp{clientv3.Compare(clientv3.Value("k"), ">", "abc")},
		},
		{
			cmps: []clientv3.Cmp{clientv3.Compare(clientv3.CreateRevision("k"), ">", 2)},
		},
		{
			cmps:       []clientv3.Cmp{clientv3.Compare(clientv3.ModRevision("k"), "=", 2)},
			wSucceeded: true,
			wResponses: 1,
		},
		{
			cmps: []clientv3.Cmp{clientv3.Compare(clientv3.Version("k"), ">", 1)},
		},
		{
			cmps: []clientv3.Cmp{clientv3.Compare(clientv3.Value("k"), "<", "abc")},
		},
		{
			cmps: []clientv3.Cmp{clientv3.Compare(clientv3.CreateRevision("k"), "<", 2)},
		},
		{
			cmps: []clientv3.Cmp{clientv3.Compare(clientv3.ModRevision("k"), "<", 2)},
		},
		{
			cmps: []clientv3.Cmp{clientv3.Compare(clientv3.Version("k"), "<", 1)},
		},
		{
			cmps: []clientv3.Cmp{
				clientv3.Compare(clientv3.Version("k"), "=", 1),
				clientv3.Compare(clientv3.Version("k"), "<", 1),
			},
		},
	}

	for i, tt := range tests {
		tresp, terr := lkv.Txn(t.Context()).If(tt.cmps...).Then(clientv3.OpGet("k")).Commit()
		require.NoError(t, terr)
		if tresp.Succeeded != tt.wSucceeded {
			t.Errorf("#%d: expected succeeded %v, got %v", i, tt.wSucceeded, tresp.Succeeded)
		}
		if len(tresp.Responses) != tt.wResponses {
			t.Errorf("#%d: expected %d responses, got %d", i, tt.wResponses, len(tresp.Responses))
		}
	}
}

func TestLeasingTxnCancel(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3, UseBridge: true})
	defer clus.Terminate(t)

	lkv1, closeLKV1, err := leasing.NewKV(clus.Client(0), "pfx/")
	require.NoError(t, err)
	defer closeLKV1()

	lkv2, closeLKV2, err := leasing.NewKV(clus.Client(1), "pfx/")
	require.NoError(t, err)
	defer closeLKV2()

	// acquire lease but disconnect so no revoke in time
	_, err = lkv1.Get(t.Context(), "k")
	require.NoError(t, err)
	clus.Members[0].Stop(t)

	// wait for leader election, if any
	_, err = clus.Client(1).Get(t.Context(), "abc")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	_, err = lkv2.Txn(ctx).Then(clientv3.OpPut("k", "v")).Commit()
	require.ErrorIsf(t, err, context.Canceled, "expected %v, got %v", context.Canceled, err)
}

func TestLeasingTxnNonOwnerPut(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	lkv, closeLKV, err := leasing.NewKV(clus.Client(0), "pfx/")
	require.NoError(t, err)
	defer closeLKV()

	lkv2, closeLKV2, err := leasing.NewKV(clus.Client(0), "pfx/")
	require.NoError(t, err)
	defer closeLKV2()

	_, err = clus.Client(0).Put(t.Context(), "k", "abc")
	require.NoError(t, err)
	_, err = clus.Client(0).Put(t.Context(), "k2", "123")
	require.NoError(t, err)
	// cache in lkv
	_, err = lkv.Get(t.Context(), "k")
	require.NoError(t, err)
	_, err = lkv.Get(t.Context(), "k2")
	require.NoError(t, err)
	// invalidate via lkv2 txn
	opArray := make([]clientv3.Op, 0)
	opArray = append(opArray, clientv3.OpPut("k2", "456"))
	tresp, terr := lkv2.Txn(t.Context()).Then(
		clientv3.OpTxn(nil, opArray, nil),
		clientv3.OpPut("k", "def"),
		clientv3.OpPut("k3", "999"), // + a key not in any cache
	).Commit()
	require.NoError(t, terr)
	if !tresp.Succeeded || len(tresp.Responses) != 3 {
		t.Fatalf("expected txn success, got %+v", tresp)
	}
	// check cache was invalidated
	gresp, gerr := lkv.Get(t.Context(), "k")
	require.NoError(t, gerr)
	if len(gresp.Kvs) != 1 || string(gresp.Kvs[0].Value) != "def" {
		t.Errorf(`expected value "def", got %+v`, gresp)
	}
	gresp, gerr = lkv.Get(t.Context(), "k2")
	require.NoError(t, gerr)
	if len(gresp.Kvs) != 1 || string(gresp.Kvs[0].Value) != "456" {
		t.Errorf(`expected value "def", got %+v`, gresp)
	}

	// check puts were applied and are all in the same revision
	w := clus.Client(0).Watch(
		clus.Client(0).Ctx(),
		"k",
		clientv3.WithRev(tresp.Header.Revision),
		clientv3.WithPrefix())
	wresp := <-w
	c := 0
	var evsInStr []string
	for _, ev := range wresp.Events {
		evsInStr = append(evsInStr, ev.String())
		if ev.Kv.ModRevision == tresp.Header.Revision {
			c++
		}
	}
	require.Equalf(t, 3, c, "expected 3 put events, got %+v", evsInStr)
}

// TestLeasingTxnRandIfThenOrElse randomly leases keys two separate clients, then
// issues a random If/{Then,Else} transaction on those keys to one client.
func TestLeasingTxnRandIfThenOrElse(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	lkv1, closeLKV1, err1 := leasing.NewKV(clus.Client(0), "pfx/")
	require.NoError(t, err1)
	defer closeLKV1()

	lkv2, closeLKV2, err2 := leasing.NewKV(clus.Client(0), "pfx/")
	require.NoError(t, err2)
	defer closeLKV2()

	keyCount := 16
	dat := make([]*clientv3.PutResponse, keyCount)
	for i := 0; i < keyCount; i++ {
		k, v := fmt.Sprintf("k-%d", i), fmt.Sprintf("%d", i)
		dat[i], err1 = clus.Client(0).Put(t.Context(), k, v)
		require.NoError(t, err1)
	}

	// nondeterministically populate leasing caches
	var wg sync.WaitGroup
	getc := make(chan struct{}, keyCount)
	getRandom := func(kv clientv3.KV) {
		defer wg.Done()
		for i := 0; i < keyCount/2; i++ {
			k := fmt.Sprintf("k-%d", rand.Intn(keyCount))
			if _, err := kv.Get(t.Context(), k); err != nil {
				t.Error(err)
			}
			getc <- struct{}{}
		}
	}
	wg.Add(2)
	defer wg.Wait()
	go getRandom(lkv1)
	go getRandom(lkv2)

	// random list of comparisons, all true
	cmps, useThen := randCmps("k-", dat)
	// random list of puts/gets; unique keys
	var ops []clientv3.Op
	usedIdx := make(map[int]struct{})
	for i := 0; i < keyCount; i++ {
		idx := rand.Intn(keyCount)
		if _, ok := usedIdx[idx]; ok {
			continue
		}
		usedIdx[idx] = struct{}{}
		k := fmt.Sprintf("k-%d", idx)
		switch rand.Intn(2) {
		case 0:
			ops = append(ops, clientv3.OpGet(k))
		case 1:
			ops = append(ops, clientv3.OpPut(k, "a"))
			// TODO: add delete
		}
	}
	// random lengths
	ops = ops[:rand.Intn(len(ops))]

	// wait for some gets to populate the leasing caches before committing
	for i := 0; i < keyCount/2; i++ {
		<-getc
	}

	// randomly choose between then and else blocks
	var thenOps, elseOps []clientv3.Op
	if useThen {
		thenOps = ops
	} else {
		// force failure
		elseOps = ops
	}

	tresp, terr := lkv1.Txn(t.Context()).If(cmps...).Then(thenOps...).Else(elseOps...).Commit()
	require.NoError(t, terr)
	// cmps always succeed
	require.Equalf(t, tresp.Succeeded, useThen, "expected succeeded=%v, got tresp=%+v", useThen, tresp)
	// get should match what was put
	checkPuts := func(s string, kv clientv3.KV) {
		for _, op := range ops {
			if !op.IsPut() {
				continue
			}
			resp, rerr := kv.Get(t.Context(), string(op.KeyBytes()))
			require.NoError(t, rerr)
			if len(resp.Kvs) != 1 || string(resp.Kvs[0].Value) != "a" {
				t.Fatalf(`%s: expected value="a", got %+v`, s, resp.Kvs)
			}
		}
	}
	checkPuts("client(0)", clus.Client(0))
	checkPuts("lkv1", lkv1)
	checkPuts("lkv2", lkv2)
}

func TestLeasingOwnerPutError(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1, UseBridge: true})
	defer clus.Terminate(t)

	lkv, closeLKV, err := leasing.NewKV(clus.Client(0), "pfx/")
	require.NoError(t, err)
	defer closeLKV()

	_, err = lkv.Get(t.Context(), "k")
	require.NoError(t, err)

	clus.Members[0].Stop(t)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	resp, err := lkv.Put(ctx, "k", "v")
	require.Errorf(t, err, "expected error, got response %+v", resp)
}

func TestLeasingOwnerDeleteError(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1, UseBridge: true})
	defer clus.Terminate(t)

	lkv, closeLKV, err := leasing.NewKV(clus.Client(0), "pfx/")
	require.NoError(t, err)
	defer closeLKV()

	_, err = lkv.Get(t.Context(), "k")
	require.NoError(t, err)

	clus.Members[0].Stop(t)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	resp, err := lkv.Delete(ctx, "k")
	require.Errorf(t, err, "expected error, got response %+v", resp)
}

func TestLeasingNonOwnerPutError(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1, UseBridge: true})
	defer clus.Terminate(t)

	lkv, closeLKV, err := leasing.NewKV(clus.Client(0), "pfx/")
	require.NoError(t, err)
	defer closeLKV()

	clus.Members[0].Stop(t)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	resp, err := lkv.Put(ctx, "k", "v")
	require.Errorf(t, err, "expected error, got response %+v", resp)
}

func TestLeasingOwnerDeletePrefix(t *testing.T) {
	testLeasingOwnerDelete(t, clientv3.OpDelete("key/", clientv3.WithPrefix()))
}

func TestLeasingOwnerDeleteFrom(t *testing.T) {
	testLeasingOwnerDelete(t, clientv3.OpDelete("kd", clientv3.WithFromKey()))
}

func testLeasingOwnerDelete(t *testing.T, del clientv3.Op) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	lkv, closeLKV, err := leasing.NewKV(clus.Client(0), "0/")
	require.NoError(t, err)
	defer closeLKV()

	for i := 0; i < 8; i++ {
		_, err = clus.Client(0).Put(t.Context(), fmt.Sprintf("key/%d", i), "123")
		require.NoError(t, err)
	}

	_, err = lkv.Get(t.Context(), "key/1")
	require.NoError(t, err)

	opResp, delErr := lkv.Do(t.Context(), del)
	require.NoError(t, delErr)
	delResp := opResp.Del()

	// confirm keys are invalidated from cache and deleted on etcd
	for i := 0; i < 8; i++ {
		resp, err := lkv.Get(t.Context(), fmt.Sprintf("key/%d", i))
		require.NoError(t, err)
		require.Emptyf(t, resp.Kvs, "expected no keys on key/%d, got %+v", i, resp)
	}

	// confirm keys were deleted atomically

	w := clus.Client(0).Watch(
		clus.Client(0).Ctx(),
		"key/",
		clientv3.WithRev(delResp.Header.Revision),
		clientv3.WithPrefix())

	wresp := <-w
	require.Lenf(t, wresp.Events, 8, "expected %d delete events,got %d", 8, len(wresp.Events))
}

func TestLeasingDeleteRangeBounds(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1, UseBridge: true})
	defer clus.Terminate(t)

	delkv, closeDelKV, err := leasing.NewKV(clus.Client(0), "0/")
	require.NoError(t, err)
	defer closeDelKV()

	getkv, closeGetKv, err := leasing.NewKV(clus.Client(0), "0/")
	require.NoError(t, err)
	defer closeGetKv()

	for _, k := range []string{"j", "m"} {
		_, err = clus.Client(0).Put(t.Context(), k, "123")
		require.NoError(t, err)
		_, err = getkv.Get(t.Context(), k)
		require.NoError(t, err)
	}

	_, err = delkv.Delete(t.Context(), "k", clientv3.WithPrefix())
	require.NoError(t, err)

	// leases still on server?
	for _, k := range []string{"j", "m"} {
		resp, geterr := clus.Client(0).Get(t.Context(), "0/"+k, clientv3.WithPrefix())
		require.NoError(t, geterr)
		require.Lenf(t, resp.Kvs, 1, "expected leasing key, got %+v", resp)
	}

	// j and m should still have leases registered since not under k*
	clus.Members[0].Stop(t)

	_, err = getkv.Get(t.Context(), "j")
	require.NoError(t, err)
	_, err = getkv.Get(t.Context(), "m")
	require.NoError(t, err)
}

func TestLeasingDeleteRangeContendTxn(t *testing.T) {
	then := []clientv3.Op{clientv3.OpDelete("key/", clientv3.WithPrefix())}
	testLeasingDeleteRangeContend(t, clientv3.OpTxn(nil, then, nil))
}

func TestLeaseDeleteRangeContendDel(t *testing.T) {
	op := clientv3.OpDelete("key/", clientv3.WithPrefix())
	testLeasingDeleteRangeContend(t, op)
}

func testLeasingDeleteRangeContend(t *testing.T, op clientv3.Op) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	delkv, closeDelKV, err := leasing.NewKV(clus.Client(0), "0/")
	require.NoError(t, err)
	defer closeDelKV()

	putkv, closePutKV, err := leasing.NewKV(clus.Client(0), "0/")
	require.NoError(t, err)
	defer closePutKV()

	const maxKey = 8
	for i := 0; i < maxKey; i++ {
		key := fmt.Sprintf("key/%d", i)
		_, err = clus.Client(0).Put(t.Context(), key, "123")
		require.NoError(t, err)
		_, err = putkv.Get(t.Context(), key)
		require.NoError(t, err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	donec := make(chan struct{})
	go func(t *testing.T) {
		defer close(donec)
		for i := 0; ctx.Err() == nil; i++ {
			key := fmt.Sprintf("key/%d", i%maxKey)
			if _, err = putkv.Put(t.Context(), key, "123"); err != nil {
				t.Errorf("fail putting key %s: %v", key, err)
			}
			if _, err = putkv.Get(t.Context(), key); err != nil {
				t.Errorf("fail getting key %s: %v", key, err)
			}
		}
	}(t)

	_, delErr := delkv.Do(t.Context(), op)
	cancel()
	<-donec
	require.NoError(t, delErr)

	// confirm keys on non-deleter match etcd
	for i := 0; i < maxKey; i++ {
		key := fmt.Sprintf("key/%d", i)
		resp, err := putkv.Get(t.Context(), key)
		require.NoError(t, err)
		servResp, err := clus.Client(0).Get(t.Context(), key)
		require.NoError(t, err)
		if !reflect.DeepEqual(resp.Kvs, servResp.Kvs) {
			t.Errorf("#%d: expected %+v, got %+v", i, servResp.Kvs, resp.Kvs)
		}
	}
}

func TestLeasingPutGetDeleteConcurrent(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	lkvs := make([]clientv3.KV, 16)
	for i := range lkvs {
		lkv, closeLKV, err := leasing.NewKV(clus.Client(0), "pfx/")
		require.NoError(t, err)
		defer closeLKV()
		lkvs[i] = lkv
	}

	getdel := func(kv clientv3.KV) {
		_, err := kv.Put(t.Context(), "k", "abc")
		require.NoError(t, err)
		time.Sleep(time.Millisecond)
		_, err = kv.Get(t.Context(), "k")
		require.NoError(t, err)
		_, err = kv.Delete(t.Context(), "k")
		require.NoError(t, err)
		time.Sleep(2 * time.Millisecond)
	}

	var wg sync.WaitGroup
	wg.Add(16)
	for i := 0; i < 16; i++ {
		go func() {
			defer wg.Done()
			for _, kv := range lkvs {
				getdel(kv)
			}
		}()
	}
	wg.Wait()

	resp, err := lkvs[0].Get(t.Context(), "k")
	require.NoError(t, err)

	require.Emptyf(t, resp.Kvs, "expected no kvs, got %+v", resp.Kvs)

	resp, err = clus.Client(0).Get(t.Context(), "k")
	require.NoError(t, err)
	require.Emptyf(t, resp.Kvs, "expected no kvs, got %+v", resp.Kvs)
}

// TestLeasingReconnectOwnerRevoke checks that revocation works if
// disconnected when trying to submit revoke txn.
func TestLeasingReconnectOwnerRevoke(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3, UseBridge: true})
	defer clus.Terminate(t)

	lkv1, closeLKV1, err1 := leasing.NewKV(clus.Client(0), "foo/")
	require.NoError(t, err1)
	defer closeLKV1()

	lkv2, closeLKV2, err2 := leasing.NewKV(clus.Client(1), "foo/")
	require.NoError(t, err2)
	defer closeLKV2()

	_, err := lkv1.Get(t.Context(), "k")
	require.NoError(t, err)

	// force leader away from member 0
	clus.Members[0].Stop(t)
	clus.WaitLeader(t)
	clus.Members[0].Restart(t)

	cctx, cancel := context.WithCancel(t.Context())
	sdonec, pdonec := make(chan struct{}), make(chan struct{})
	// make lkv1 connection choppy so Txn fails
	go func() {
		defer close(sdonec)
		for i := 0; i < 3 && cctx.Err() == nil; i++ {
			clus.Members[0].Stop(t)
			time.Sleep(10 * time.Millisecond)
			clus.Members[0].Restart(t)
		}
	}()
	go func() {
		defer close(pdonec)
		if _, err := lkv2.Put(cctx, "k", "v"); err != nil {
			t.Log(err)
		}
		// blocks until lkv1 connection comes back
		resp, err := lkv1.Get(cctx, "k")
		if err != nil {
			t.Error(err)
		}
		if string(resp.Kvs[0].Value) != "v" {
			t.Errorf(`expected "v" value, got %+v`, resp)
		}
	}()
	select {
	case <-pdonec:
		cancel()
		<-sdonec
	case <-time.After(15 * time.Second):
		cancel()
		<-sdonec
		<-pdonec
		t.Fatal("took too long to revoke and put")
	}
}

// TestLeasingReconnectOwnerRevokeCompact checks that revocation works if
// disconnected and the watch is compacted.
func TestLeasingReconnectOwnerRevokeCompact(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3, UseBridge: true})
	defer clus.Terminate(t)

	lkv1, closeLKV1, err1 := leasing.NewKV(clus.Client(0), "foo/")
	require.NoError(t, err1)
	defer closeLKV1()

	lkv2, closeLKV2, err2 := leasing.NewKV(clus.Client(1), "foo/")
	require.NoError(t, err2)
	defer closeLKV2()

	_, err := lkv1.Get(t.Context(), "k")
	require.NoError(t, err)

	clus.Members[0].Stop(t)
	clus.WaitLeader(t)

	// put some more revisions for compaction
	_, err = clus.Client(1).Put(t.Context(), "a", "123")
	require.NoError(t, err)
	presp, err := clus.Client(1).Put(t.Context(), "a", "123")
	require.NoError(t, err)
	// compact while lkv1 is disconnected
	rev := presp.Header.Revision
	_, err = clus.Client(1).Compact(t.Context(), rev)
	require.NoError(t, err)

	clus.Members[0].Restart(t)

	cctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err = lkv2.Put(cctx, "k", "v")
	require.NoError(t, err)
	resp, err := lkv1.Get(cctx, "k")
	require.NoError(t, err)
	require.Equalf(t, "v", string(resp.Kvs[0].Value), `expected "v" value, got %+v`, resp)
}

// TestLeasingReconnectOwnerConsistency checks a write error on an owner will
// not cause inconsistency between the server and the client.
func TestLeasingReconnectOwnerConsistency(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1, UseBridge: true})
	defer clus.Terminate(t)

	lkv, closeLKV, err := leasing.NewKV(clus.Client(0), "foo/")
	defer closeLKV()
	require.NoError(t, err)

	_, err = lkv.Put(t.Context(), "k", "x")
	require.NoError(t, err)
	_, err = lkv.Put(t.Context(), "kk", "y")
	require.NoError(t, err)
	_, err = lkv.Get(t.Context(), "k")
	require.NoError(t, err)

	for i := 0; i < 10; i++ {
		v := fmt.Sprintf("%d", i)
		donec := make(chan struct{})
		clus.Members[0].Bridge().DropConnections()
		go func() {
			defer close(donec)
			for i := 0; i < 20; i++ {
				clus.Members[0].Bridge().DropConnections()
				time.Sleep(time.Millisecond)
			}
		}()
		switch rand.Intn(7) {
		case 0:
			_, err = lkv.Put(t.Context(), "k", v)
		case 1:
			_, err = lkv.Delete(t.Context(), "k")
		case 2:
			txn := lkv.Txn(t.Context()).Then(
				clientv3.OpGet("k"),
				clientv3.OpDelete("k"),
			)
			_, err = txn.Commit()
		case 3:
			txn := lkv.Txn(t.Context()).Then(
				clientv3.OpGet("k"),
				clientv3.OpPut("k", v),
			)
			_, err = txn.Commit()
		case 4:
			_, err = lkv.Do(t.Context(), clientv3.OpPut("k", v))
		case 5:
			_, err = lkv.Do(t.Context(), clientv3.OpDelete("k"))
		case 6:
			_, err = lkv.Delete(t.Context(), "k", clientv3.WithPrefix())
		}
		<-donec
		if err != nil {
			// TODO wrap input client to generate errors
			break
		}
	}

	lresp, lerr := lkv.Get(t.Context(), "k")
	require.NoError(t, lerr)
	cresp, cerr := clus.Client(0).Get(t.Context(), "k")
	require.NoError(t, cerr)
	require.Truef(t, reflect.DeepEqual(lresp.Kvs, cresp.Kvs), "expected %+v, got %+v", cresp, lresp)
}

func TestLeasingTxnAtomicCache(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	lkv, closeLKV, err := leasing.NewKV(clus.Client(0), "foo/")
	require.NoError(t, err)
	defer closeLKV()

	puts, gets := make([]clientv3.Op, 16), make([]clientv3.Op, 16)
	for i := range puts {
		k := fmt.Sprintf("k-%d", i)
		puts[i], gets[i] = clientv3.OpPut(k, k), clientv3.OpGet(k)
	}
	_, err = clus.Client(0).Txn(t.Context()).Then(puts...).Commit()
	require.NoError(t, err)
	for i := range gets {
		_, err = lkv.Do(t.Context(), gets[i])
		require.NoError(t, err)
	}

	numPutters, numGetters := 16, 16

	var wgPutters, wgGetters sync.WaitGroup
	wgPutters.Add(numPutters)
	wgGetters.Add(numGetters)
	txnerrCh := make(chan error, 1)

	f := func() {
		defer wgPutters.Done()
		for i := 0; i < 10; i++ {
			if _, txnerr := lkv.Txn(t.Context()).Then(puts...).Commit(); txnerr != nil {
				select {
				case txnerrCh <- txnerr:
				default:
				}
			}
		}
	}

	donec := make(chan struct{}, numPutters)
	g := func() {
		defer wgGetters.Done()
		for {
			select {
			case <-donec:
				return
			default:
			}
			tresp, err := lkv.Txn(t.Context()).Then(gets...).Commit()
			if err != nil {
				t.Error(err)
			}
			revs := make([]int64, len(gets))
			for i, resp := range tresp.Responses {
				rr := resp.GetResponseRange()
				revs[i] = rr.Kvs[0].ModRevision
			}
			for i := 1; i < len(revs); i++ {
				if revs[i] != revs[i-1] {
					t.Errorf("expected matching revisions, got %+v", revs)
				}
			}
		}
	}

	for i := 0; i < numGetters; i++ {
		go g()
	}
	for i := 0; i < numPutters; i++ {
		go f()
	}

	wgPutters.Wait()
	select {
	case txnerr := <-txnerrCh:
		t.Fatal(txnerr)
	default:
	}
	close(donec)
	wgGetters.Wait()
}

// TestLeasingReconnectTxn checks that Txn is resilient to disconnects.
func TestLeasingReconnectTxn(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1, UseBridge: true})
	defer clus.Terminate(t)

	lkv, closeLKV, err := leasing.NewKV(clus.Client(0), "foo/")
	require.NoError(t, err)
	defer closeLKV()

	_, err = lkv.Get(t.Context(), "k")
	require.NoError(t, err)

	donec := make(chan struct{})
	go func() {
		defer close(donec)
		clus.Members[0].Bridge().DropConnections()
		for i := 0; i < 10; i++ {
			clus.Members[0].Bridge().DropConnections()
			time.Sleep(time.Millisecond)
		}
		time.Sleep(10 * time.Millisecond)
	}()

	_, lerr := lkv.Txn(t.Context()).
		If(clientv3.Compare(clientv3.Version("k"), "=", 0)).
		Then(clientv3.OpGet("k")).
		Commit()
	<-donec
	require.NoError(t, lerr)
}

// TestLeasingReconnectNonOwnerGet checks a get error on an owner will
// not cause inconsistency between the server and the client.
func TestLeasingReconnectNonOwnerGet(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1, UseBridge: true})
	defer clus.Terminate(t)

	lkv, closeLKV, err := leasing.NewKV(clus.Client(0), "foo/")
	require.NoError(t, err)
	defer closeLKV()

	// populate a few keys so some leasing gets have keys
	for i := 0; i < 4; i++ {
		k := fmt.Sprintf("k-%d", i*2)
		_, err = lkv.Put(t.Context(), k, k[2:])
		require.NoError(t, err)
	}

	n := 0
	for i := 0; i < 10; i++ {
		donec := make(chan struct{})
		clus.Members[0].Bridge().DropConnections()
		go func() {
			defer close(donec)
			for j := 0; j < 10; j++ {
				clus.Members[0].Bridge().DropConnections()
				time.Sleep(time.Millisecond)
			}
		}()
		_, err = lkv.Get(t.Context(), fmt.Sprintf("k-%d", i))
		<-donec
		n++
		if err != nil {
			break
		}
	}
	for i := 0; i < n; i++ {
		k := fmt.Sprintf("k-%d", i)
		lresp, lerr := lkv.Get(t.Context(), k)
		require.NoError(t, lerr)
		cresp, cerr := clus.Client(0).Get(t.Context(), k)
		require.NoError(t, cerr)
		require.Truef(t, reflect.DeepEqual(lresp.Kvs, cresp.Kvs), "expected %+v, got %+v", cresp, lresp)
	}
}

func TestLeasingTxnRangeCmp(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	lkv, closeLKV, err := leasing.NewKV(clus.Client(0), "foo/")
	require.NoError(t, err)
	defer closeLKV()

	_, err = clus.Client(0).Put(t.Context(), "k", "a")
	require.NoError(t, err)
	// k2 version = 2
	_, err = clus.Client(0).Put(t.Context(), "k2", "a")
	require.NoError(t, err)
	_, err = clus.Client(0).Put(t.Context(), "k2", "a")
	require.NoError(t, err)

	// cache k
	_, err = lkv.Get(t.Context(), "k")
	require.NoError(t, err)

	cmp := clientv3.Compare(clientv3.Version("k").WithPrefix(), "=", 1)
	tresp, terr := lkv.Txn(t.Context()).If(cmp).Commit()
	require.NoError(t, terr)
	require.Falsef(t, tresp.Succeeded, "expected Succeeded=false, got %+v", tresp)
}

func TestLeasingDo(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	lkv, closeLKV, err := leasing.NewKV(clus.Client(0), "foo/")
	require.NoError(t, err)
	defer closeLKV()

	ops := []clientv3.Op{
		clientv3.OpTxn(nil, nil, nil),
		clientv3.OpGet("a"),
		clientv3.OpPut("a/abc", "v"),
		clientv3.OpDelete("a", clientv3.WithPrefix()),
		clientv3.OpTxn(nil, nil, nil),
	}
	for i, op := range ops {
		resp, resperr := lkv.Do(t.Context(), op)
		if resperr != nil {
			t.Errorf("#%d: failed (%v)", i, resperr)
		}
		switch {
		case op.IsGet() && resp.Get() == nil:
			t.Errorf("#%d: get but nil get response", i)
		case op.IsPut() && resp.Put() == nil:
			t.Errorf("#%d: put op but nil get response", i)
		case op.IsDelete() && resp.Del() == nil:
			t.Errorf("#%d: delete op but nil delete response", i)
		case op.IsTxn() && resp.Txn() == nil:
			t.Errorf("#%d: txn op but nil txn response", i)
		}
	}

	gresp, err := clus.Client(0).Get(t.Context(), "a", clientv3.WithPrefix())
	require.NoError(t, err)
	require.Emptyf(t, gresp.Kvs, "expected no keys, got %+v", gresp.Kvs)
}

func TestLeasingTxnOwnerPutBranch(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3, UseBridge: true})
	defer clus.Terminate(t)

	lkv, closeLKV, err := leasing.NewKV(clus.Client(0), "foo/")
	require.NoError(t, err)
	defer closeLKV()

	n := 0
	treeOp := makePutTreeOp("tree", &n, 4)
	for i := 0; i < n; i++ {
		k := fmt.Sprintf("tree/%d", i)
		_, err = clus.Client(0).Put(t.Context(), k, "a")
		require.NoError(t, err)
		_, err = lkv.Get(t.Context(), k)
		require.NoError(t, err)
	}

	_, err = lkv.Do(t.Context(), treeOp)
	require.NoError(t, err)

	// lkv shouldn't need to call out to server for updated leased keys
	clus.Members[0].Stop(t)

	for i := 0; i < n; i++ {
		k := fmt.Sprintf("tree/%d", i)
		lkvResp, err := lkv.Get(t.Context(), k)
		require.NoError(t, err)
		clusResp, err := clus.Client(1).Get(t.Context(), k)
		require.NoError(t, err)
		require.Truef(t, reflect.DeepEqual(clusResp.Kvs, lkvResp.Kvs), "expected %+v, got %+v", clusResp.Kvs, lkvResp.Kvs)
	}
}

func makePutTreeOp(pfx string, v *int, depth int) clientv3.Op {
	key := fmt.Sprintf("%s/%d", pfx, *v)
	*v = *v + 1
	if depth == 0 {
		return clientv3.OpPut(key, "leaf")
	}

	t, e := makePutTreeOp(pfx, v, depth-1), makePutTreeOp(pfx, v, depth-1)
	tPut, ePut := clientv3.OpPut(key, "then"), clientv3.OpPut(key, "else")

	cmps := make([]clientv3.Cmp, 1)
	if rand.Intn(2) == 0 {
		// follow then path
		cmps[0] = clientv3.Compare(clientv3.Version("nokey"), "=", 0)
	} else {
		// follow else path
		cmps[0] = clientv3.Compare(clientv3.Version("nokey"), ">", 0)
	}

	return clientv3.OpTxn(cmps, []clientv3.Op{t, tPut}, []clientv3.Op{e, ePut})
}

func randCmps(pfx string, dat []*clientv3.PutResponse) (cmps []clientv3.Cmp, then bool) {
	for i := 0; i < len(dat); i++ {
		idx := rand.Intn(len(dat))
		k := fmt.Sprintf("%s%d", pfx, idx)
		rev := dat[idx].Header.Revision
		var cmp clientv3.Cmp
		switch rand.Intn(4) {
		case 0:
			cmp = clientv3.Compare(clientv3.CreateRevision(k), ">", rev-1)
		case 1:
			cmp = clientv3.Compare(clientv3.Version(k), "=", 1)
		case 2:
			cmp = clientv3.Compare(clientv3.CreateRevision(k), "=", rev)
		case 3:
			cmp = clientv3.Compare(clientv3.CreateRevision(k), "!=", rev+1)
		}
		cmps = append(cmps, cmp)
	}
	cmps = cmps[:rand.Intn(len(dat))]
	if rand.Intn(2) == 0 {
		return cmps, true
	}
	i := rand.Intn(len(dat))
	cmps = append(cmps, clientv3.Compare(clientv3.Version(fmt.Sprintf("k-%d", i)), "=", 0))
	return cmps, false
}

func TestLeasingSessionExpire(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3, UseBridge: true})
	defer clus.Terminate(t)

	lkv, closeLKV, err := leasing.NewKV(clus.Client(0), "foo/", concurrency.WithTTL(1))
	require.NoError(t, err)
	defer closeLKV()

	lkv2, closeLKV2, err := leasing.NewKV(clus.Client(0), "foo/")
	require.NoError(t, err)
	defer closeLKV2()

	// acquire lease on abc
	_, err = lkv.Get(t.Context(), "abc")
	require.NoError(t, err)

	// down endpoint lkv uses for keepalives
	clus.Members[0].Stop(t)
	err = waitForLeasingExpire(clus.Client(1), "foo/abc")
	require.NoError(t, err)
	waitForExpireAck(t, lkv)
	clus.Members[0].Restart(t)
	integration.WaitClientV3(t, lkv2)
	_, err = lkv2.Put(t.Context(), "abc", "def")
	require.NoError(t, err)

	resp, err := lkv.Get(t.Context(), "abc")
	require.NoError(t, err)
	require.Equal(t, "def", string(resp.Kvs[0].Value))
}

func TestLeasingSessionExpireCancel(t *testing.T) {
	tests := []func(context.Context, clientv3.KV) error{
		func(ctx context.Context, kv clientv3.KV) error {
			_, err := kv.Get(ctx, "abc")
			return err
		},
		func(ctx context.Context, kv clientv3.KV) error {
			_, err := kv.Delete(ctx, "abc")
			return err
		},
		func(ctx context.Context, kv clientv3.KV) error {
			_, err := kv.Put(ctx, "abc", "v")
			return err
		},
		func(ctx context.Context, kv clientv3.KV) error {
			_, err := kv.Txn(ctx).Then(clientv3.OpGet("abc")).Commit()
			return err
		},
		func(ctx context.Context, kv clientv3.KV) error {
			_, err := kv.Do(ctx, clientv3.OpPut("abc", "v"))
			return err
		},
		func(ctx context.Context, kv clientv3.KV) error {
			_, err := kv.Do(ctx, clientv3.OpDelete("abc"))
			return err
		},
		func(ctx context.Context, kv clientv3.KV) error {
			_, err := kv.Do(ctx, clientv3.OpGet("abc"))
			return err
		},
		func(ctx context.Context, kv clientv3.KV) error {
			op := clientv3.OpTxn(nil, []clientv3.Op{clientv3.OpGet("abc")}, nil)
			_, err := kv.Do(ctx, op)
			return err
		},
	}
	for i := range tests {
		t.Run(fmt.Sprintf("test %d", i), func(t *testing.T) {
			integration.BeforeTest(t)
			clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3, UseBridge: true})
			defer clus.Terminate(t)

			lkv, closeLKV, err := leasing.NewKV(clus.Client(0), "foo/", concurrency.WithTTL(1))
			require.NoError(t, err)
			defer closeLKV()

			_, err = lkv.Get(t.Context(), "abc")
			require.NoError(t, err)

			// down endpoint lkv uses for keepalives
			clus.Members[0].Stop(t)
			err = waitForLeasingExpire(clus.Client(1), "foo/abc")
			require.NoError(t, err)
			waitForExpireAck(t, lkv)

			ctx, cancel := context.WithCancel(t.Context())
			errc := make(chan error, 1)
			go func() { errc <- tests[i](ctx, lkv) }()
			// some delay to get past for ctx.Err() != nil {} loops
			time.Sleep(100 * time.Millisecond)
			cancel()

			select {
			case err := <-errc:
				if !errors.Is(err, ctx.Err()) {
					t.Errorf("#%d: expected %v of server unavailable, got %v", i, ctx.Err(), err)
				}
			case <-time.After(5 * time.Second):
				t.Errorf("#%d: timed out waiting for cancel", i)
			}
			clus.Members[0].Restart(t)
		})
	}
}

func waitForLeasingExpire(kv clientv3.KV, lkey string) error {
	for {
		time.Sleep(1 * time.Second)
		resp, err := kv.Get(context.TODO(), lkey, clientv3.WithPrefix())
		if err != nil {
			return err
		}
		if len(resp.Kvs) == 0 {
			// server expired the leasing key
			return nil
		}
	}
}

func waitForExpireAck(t *testing.T, kv clientv3.KV) {
	// wait for leasing client to acknowledge lost lease
	for i := 0; i < 10; i++ {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		_, err := kv.Get(ctx, "abc")
		cancel()
		if errors.Is(err, ctx.Err()) {
			return
		} else if err != nil {
			t.Logf("current error: %v", err)
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("waited too long to acknlowedge lease expiration")
}
