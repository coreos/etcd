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

package clientv3test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/ordering"
	"go.etcd.io/etcd/tests/v3/framework/integration"
)

func TestDetectKvOrderViolation(t *testing.T) {
	errOrderViolation := errors.New("DetectedOrderViolation")

	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3, UseBridge: true})
	defer clus.Terminate(t)

	cfg := clientv3.Config{
		Endpoints: []string{
			clus.Members[0].GRPCURL,
			clus.Members[1].GRPCURL,
			clus.Members[2].GRPCURL,
		},
	}
	cli, err := integration.NewClient(t, cfg)
	require.NoError(t, err)
	defer func() { assert.NoError(t, cli.Close()) }()
	ctx := t.Context()

	_, err = clus.Client(0).Put(ctx, "foo", "bar")
	require.NoError(t, err)
	// ensure that the second member has the current revision for the key foo
	_, err = clus.Client(1).Get(ctx, "foo")
	require.NoError(t, err)

	// stop third member in order to force the member to have an outdated revision
	clus.Members[2].Stop(t)
	require.Eventuallyf(t, func() bool {
		timeoutCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		_, err = cli.Put(timeoutCtx, "foo", "buzz")
		return err == nil
	}, 5*time.Second, 50*time.Millisecond, "failed to execute Put after member stop")

	// perform get request against the first member, in order to
	// set up kvOrdering to expect "foo" revisions greater than that of
	// the third member.
	orderingKv := ordering.NewKV(cli.KV,
		func(op clientv3.Op, resp clientv3.OpResponse, prevRev int64) error {
			return errOrderViolation
		})
	v, err := orderingKv.Get(ctx, "foo")
	require.NoError(t, err)
	t.Logf("Read from the first member: v:%v err:%v", v, err)
	assert.Equal(t, []byte("buzz"), v.Kvs[0].Value)

	// ensure that only the third member is queried during requests
	clus.Members[0].Stop(t)
	clus.Members[1].Stop(t)
	require.NoError(t, clus.Members[2].Restart(t))
	// force OrderingKv to query the third member
	cli.SetEndpoints(clus.Members[2].GRPCURL)
	require.Eventuallyf(t, func() bool {
		timeoutCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		_, err = cli.Get(timeoutCtx, "foo", clientv3.WithSerializable())
		return err == nil
	}, 5*time.Second, 50*time.Millisecond, "failed to establish gRPC connection to restarted member")

	t.Logf("Quering m2 after restart")
	v, err = orderingKv.Get(ctx, "foo", clientv3.WithSerializable())
	t.Logf("Quering m2 returned: v:%v err:%v ", v, err)
	if !errors.Is(err, errOrderViolation) {
		t.Fatalf("expected %v, got err:%v v:%v", errOrderViolation, err, v)
	}
}

func TestDetectTxnOrderViolation(t *testing.T) {
	errOrderViolation := errors.New("DetectedOrderViolation")

	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3, UseBridge: true})
	defer clus.Terminate(t)

	cfg := clientv3.Config{
		Endpoints: []string{
			clus.Members[0].GRPCURL,
			clus.Members[1].GRPCURL,
			clus.Members[2].GRPCURL,
		},
	}
	cli, err := integration.NewClient(t, cfg)
	require.NoError(t, err)
	defer func() { assert.NoError(t, cli.Close()) }()
	ctx := t.Context()

	_, err = clus.Client(0).Put(ctx, "foo", "bar")
	require.NoError(t, err)
	// ensure that the second member has the current revision for the key foo
	_, err = clus.Client(1).Get(ctx, "foo")
	require.NoError(t, err)

	// stop third member in order to force the member to have an outdated revision
	clus.Members[2].Stop(t)
	require.Eventuallyf(t, func() bool {
		timeoutCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		_, err = clus.Client(1).Put(timeoutCtx, "foo", "buzz")
		return err == nil
	}, 5*time.Second, 50*time.Millisecond, "failed to execute Put after member stop")

	// perform get request against the first member, in order to
	// set up kvOrdering to expect "foo" revisions greater than that of
	// the third member.
	orderingKv := ordering.NewKV(cli.KV,
		func(op clientv3.Op, resp clientv3.OpResponse, prevRev int64) error {
			return errOrderViolation
		})
	orderingTxn := orderingKv.Txn(ctx)
	_, err = orderingTxn.If(
		clientv3.Compare(clientv3.Value("b"), ">", "a"),
	).Then(
		clientv3.OpGet("foo"),
	).Commit()
	require.NoError(t, err)

	// ensure that only the third member is queried during requests
	clus.Members[0].Stop(t)
	clus.Members[1].Stop(t)
	require.NoError(t, clus.Members[2].Restart(t))
	// force OrderingKv to query the third member
	cli.SetEndpoints(clus.Members[2].GRPCURL)
	require.Eventuallyf(t, func() bool {
		timeoutCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		_, err = cli.Get(timeoutCtx, "foo", clientv3.WithSerializable())
		return err == nil
	}, 5*time.Second, 50*time.Millisecond, "failed to establish gRPC connection to restarted member")

	_, err = orderingKv.Get(ctx, "foo", clientv3.WithSerializable())
	if !errors.Is(err, errOrderViolation) {
		t.Fatalf("expected %v, got %v", errOrderViolation, err)
	}
	orderingTxn = orderingKv.Txn(ctx)
	_, err = orderingTxn.If(
		clientv3.Compare(clientv3.Value("b"), ">", "a"),
	).Then(
		clientv3.OpGet("foo", clientv3.WithSerializable()),
	).Commit()
	if !errors.Is(err, errOrderViolation) {
		t.Fatalf("expected %v, got %v", errOrderViolation, err)
	}
}
