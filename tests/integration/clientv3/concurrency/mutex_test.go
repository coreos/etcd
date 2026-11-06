// Copyright 2019 The etcd Authors
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

package concurrency_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
	"go.etcd.io/etcd/tests/v3/framework/integration"
)

func TestMutexLockSessionExpired(t *testing.T) {
	cli, err := integration.NewClient(t, clientv3.Config{Endpoints: exampleEndpoints()})
	require.NoError(t, err)
	defer cli.Close()

	// create two separate sessions for lock competition
	s1, err := concurrency.NewSession(cli)
	require.NoError(t, err)
	defer s1.Close()
	m1 := concurrency.NewMutex(s1, "/my-lock/")

	s2, err := concurrency.NewSession(cli)
	require.NoError(t, err)
	m2 := concurrency.NewMutex(s2, "/my-lock/")

	// acquire lock for s1
	require.NoError(t, m1.Lock(t.Context()))

	m2Locked := make(chan struct{})
	var err2 error
	go func() {
		defer close(m2Locked)
		// m2 blocks since m1 already acquired lock /my-lock/
		if err2 = m2.Lock(t.Context()); err2 == nil {
			t.Error("expect session expired error")
		}
	}()

	// revoke the session of m2 before unlock m1
	require.NoError(t, s2.Close())
	require.NoError(t, m1.Unlock(t.Context()))

	<-m2Locked
}

func TestMutexUnlock(t *testing.T) {
	cli, err := integration.NewClient(t, clientv3.Config{Endpoints: exampleEndpoints()})
	require.NoError(t, err)
	defer cli.Close()

	s1, err := concurrency.NewSession(cli)
	require.NoError(t, err)
	defer s1.Close()

	m1 := concurrency.NewMutex(s1, "/my-lock/")
	err = m1.Unlock(t.Context())
	require.Errorf(t, err, "expect lock released error")
	if !errors.Is(err, concurrency.ErrLockReleased) {
		t.Fatal(err)
	}

	require.NoError(t, m1.Lock(t.Context()))

	require.NoError(t, m1.Unlock(t.Context()))

	err = m1.Unlock(t.Context())
	if err == nil {
		t.Fatal("expect lock released error")
	}
	if !errors.Is(err, concurrency.ErrLockReleased) {
		t.Fatal(err)
	}
}

func TestMutexStaleUnlockDoesNotDeleteSameSessionRelock(t *testing.T) {
	var loseFirstDeleteReply atomic.Bool
	loseFirstDeleteReply.Store(true)
	loseDeleteReply := func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		err := invoker(ctx, method, req, reply, cc, opts...)
		deletesKey := method == pb.KV_DeleteRange_FullMethodName
		if txn, ok := req.(*pb.TxnRequest); ok {
			for _, op := range txn.Success {
				deletesKey = deletesKey || op.GetRequestDeleteRange() != nil
			}
		}
		if err == nil && deletesKey && loseFirstDeleteReply.CompareAndSwap(true, false) {
			return status.Error(codes.DeadlineExceeded, "simulated lost delete response")
		}
		return err
	}

	cli, err := integration.NewClient(t, clientv3.Config{
		Endpoints: exampleEndpoints(),
		// Keep one total attempt so the injected post-commit error is returned instead of retried.
		MaxUnaryRetries: 1,
		DialOptions: []grpc.DialOption{
			grpc.WithChainUnaryInterceptor(loseDeleteReply),
		},
	})
	require.NoError(t, err)
	defer cli.Close()

	session, err := concurrency.NewSession(cli)
	require.NoError(t, err)
	defer session.Close()

	ctx := t.Context()
	oldMutex := concurrency.NewMutex(session, "/my-lock/")
	require.NoError(t, oldMutex.Lock(ctx))
	oldKey := oldMutex.Key()
	oldGet, err := cli.Get(ctx, oldKey)
	require.NoError(t, err)
	require.Len(t, oldGet.Kvs, 1)
	oldRevision := oldGet.Kvs[0].CreateRevision

	require.Error(t, oldMutex.Unlock(ctx))
	afterLostReply, err := cli.Get(ctx, oldKey)
	require.NoError(t, err)
	require.Empty(t, afterLostReply.Kvs)

	currentMutex := concurrency.NewMutex(session, "/my-lock/")
	require.NoError(t, currentMutex.Lock(ctx))
	require.Equal(t, oldKey, currentMutex.Key())
	currentGet, err := cli.Get(ctx, currentMutex.Key())
	require.NoError(t, err)
	require.Len(t, currentGet.Kvs, 1)
	require.Greater(t, currentGet.Kvs[0].CreateRevision, oldRevision)

	require.NoError(t, oldMutex.Unlock(ctx))
	afterStaleUnlock, err := cli.Get(ctx, currentMutex.Key())
	require.NoError(t, err)
	require.Len(t, afterStaleUnlock.Kvs, 1)
	require.Equal(t, currentGet.Kvs[0].CreateRevision, afterStaleUnlock.Kvs[0].CreateRevision)
	require.NoError(t, currentMutex.Unlock(ctx))
}
