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

// +build !cluster_proxy

package integration

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/coreos/etcd/clientv3"
	"github.com/coreos/etcd/integration"
	"github.com/coreos/etcd/pkg/testutil"
)

// TestBalancerUnderBlackholeWatch ensures that watch client
// switch its endpoints when the member of the pinned endpoint is blackholed.
func TestBalancerUnderBlackholeWatch(t *testing.T) {
	defer testutil.AfterTest(t)

	clus := integration.NewClusterV3(t, &integration.ClusterConfig{
		Size:               2,
		SkipCreatingClient: true,
	})
	defer clus.Terminate(t)

	eps := []string{clus.Members[0].GRPCAddr(), clus.Members[1].GRPCAddr()}

	ccfg := clientv3.Config{
		Endpoints:            []string{eps[0]},
		DialTimeout:          1 * time.Second,
		DialKeepAliveTime:    1 * time.Second,
		DialKeepAliveTimeout: 500 * time.Millisecond,
	}
	watchCli, err := clientv3.New(ccfg)
	if err != nil {
		t.Fatal(err)
	}
	defer watchCli.Close()

	// wait for eps[0] to be pinned
	mustWaitPinReady(t, watchCli)

	// add all eps to list, so that when the original pined one fails
	// the client can switch to other available eps
	watchCli.SetEndpoints(eps...)

	key, val := "foo", "bar"
	wch := watchCli.Watch(context.Background(), key, clientv3.WithCreatedNotify())
	select {
	case <-wch:
	case <-time.After(3 * time.Second):
		t.Fatal("took too long to create watch")
	}

	donec := make(chan struct{})
	go func() {
		defer close(donec)

		// switch to others when eps[lead] is shut down
		select {
		case ev := <-wch:
			if werr := ev.Err(); werr != nil {
				t.Fatal(werr)
			}
			if len(ev.Events) != 1 {
				t.Fatalf("expected one event, got %+v", ev)
			}
			if !bytes.Equal(ev.Events[0].Kv.Value, []byte(val)) {
				t.Fatalf("expected %q, got %+v", val, ev.Events[0].Kv)
			}
		case <-time.After(7 * time.Second):
			t.Fatal("took too long to receive events")
		}
	}()

	// blackhole eps[0]
	clus.Members[0].Blackhole()

	// writes to eps[1]
	putCli, err := clientv3.New(clientv3.Config{Endpoints: []string{eps[1]}})
	if err != nil {
		t.Fatal(err)
	}
	defer putCli.Close()
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err = putCli.Put(ctx, key, val)
		cancel()
		if err == nil {
			break
		}
		if err == context.DeadlineExceeded {
			continue
		}
		t.Fatal(err)
	}

	select {
	case <-donec:
	case <-time.After(5 * time.Second): // enough time for balancer switch
		t.Fatal("took too long to receive events")
	}
}

func TestBalancerUnderBlackholeNoKeepAlivePut(t *testing.T) {
	testBalancerUnderBlackholeNoKeepAliveMutable(t, func(cli *clientv3.Client, ctx context.Context) error {
		_, err := cli.Put(ctx, "foo", "bar")
		return err
	})
}

func TestBalancerUnderBlackholeNoKeepAliveDelete(t *testing.T) {
	testBalancerUnderBlackholeNoKeepAliveMutable(t, func(cli *clientv3.Client, ctx context.Context) error {
		_, err := cli.Delete(ctx, "foo")
		return err
	})
}

func TestBalancerUnderBlackholeNoKeepAliveTxn(t *testing.T) {
	testBalancerUnderBlackholeNoKeepAliveMutable(t, func(cli *clientv3.Client, ctx context.Context) error {
		_, err := cli.Txn(ctx).
			If(clientv3.Compare(clientv3.Version("foo"), "=", 0)).
			Then(clientv3.OpPut("foo", "bar")).
			Else(clientv3.OpPut("foo", "baz")).Commit()
		return err
	})
}

func testBalancerUnderBlackholeNoKeepAliveMutable(t *testing.T, op func(*clientv3.Client, context.Context) error) {
	defer testutil.AfterTest(t)

	clus := integration.NewClusterV3(t, &integration.ClusterConfig{
		Size:               2,
		SkipCreatingClient: true,
	})
	defer clus.Terminate(t)

	eps := []string{clus.Members[0].GRPCAddr(), clus.Members[1].GRPCAddr()}

	ccfg := clientv3.Config{
		Endpoints:   []string{eps[0]},
		DialTimeout: 1 * time.Second,
	}
	cli, err := clientv3.New(ccfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	// wait for eps[0] to be pinned
	mustWaitPinReady(t, cli)

	// add all eps to list, so that when the original pined one fails
	// the client can switch to other available eps
	cli.SetEndpoints(eps...)

	// blackhole eps[0]
	clus.Members[0].Blackhole()

	// fail first due to blackhole, retry should succeed
	// TODO: first mutable operation can succeed
	// when gRPC supports better retry on non-delivered request
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err = op(cli, ctx)
		cancel()
		if i == 0 {
			if err != context.DeadlineExceeded {
				t.Fatalf("#%d: err = %v, want %v", i, err, context.DeadlineExceeded)
			}
		} else if err != nil {
			t.Errorf("#%d: mutable operation failed with error %v", i, err)
		}
	}
}
