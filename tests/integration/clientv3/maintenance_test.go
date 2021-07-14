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
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"math"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
	"google.golang.org/grpc"

	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	"go.etcd.io/etcd/api/v3/version"
	"go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/lease"
	"go.etcd.io/etcd/server/v3/storage/backend"
	"go.etcd.io/etcd/server/v3/storage/mvcc"
	"go.etcd.io/etcd/tests/v3/integration"
)

func TestMaintenanceHashKV(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewClusterV3(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	for i := 0; i < 3; i++ {
		if _, err := clus.RandClient().Put(context.Background(), "foo", "bar"); err != nil {
			t.Fatal(err)
		}
	}

	var hv uint32
	for i := 0; i < 3; i++ {
		cli := clus.Client(i)
		// ensure writes are replicated
		if _, err := cli.Get(context.TODO(), "foo"); err != nil {
			t.Fatal(err)
		}
		hresp, err := cli.HashKV(context.Background(), clus.Members[i].GRPCAddr(), 0)
		if err != nil {
			t.Fatal(err)
		}
		if hv == 0 {
			hv = hresp.Hash
			continue
		}
		if hv != hresp.Hash {
			t.Fatalf("#%d: hash expected %d, got %d", i, hv, hresp.Hash)
		}
	}
}

func TestMaintenanceMoveLeader(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewClusterV3(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	oldLeadIdx := clus.WaitLeader(t)
	targetIdx := (oldLeadIdx + 1) % 3
	target := uint64(clus.Members[targetIdx].ID())

	cli := clus.Client(targetIdx)
	_, err := cli.MoveLeader(context.Background(), target)
	if err != rpctypes.ErrNotLeader {
		t.Fatalf("error expected %v, got %v", rpctypes.ErrNotLeader, err)
	}

	cli = clus.Client(oldLeadIdx)
	_, err = cli.MoveLeader(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}

	leadIdx := clus.WaitLeader(t)
	lead := uint64(clus.Members[leadIdx].ID())
	if target != lead {
		t.Fatalf("new leader expected %d, got %d", target, lead)
	}
}

// TestMaintenanceSnapshotCancel ensures that context cancel
// before snapshot reading returns corresponding context errors.
func TestMaintenanceSnapshotCancel(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewClusterV3(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	// reading snapshot with canceled context should error out
	ctx, cancel := context.WithCancel(context.Background())
	rc1, err := clus.RandClient().Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rc1.Close()

	cancel()
	_, err = io.Copy(ioutil.Discard, rc1)
	if err != context.Canceled {
		t.Errorf("expected %v, got %v", context.Canceled, err)
	}
}

// TestMaintenanceSnapshotWithVersionTimeout ensures that SnapshotWithVersion function
// returns corresponding context errors when context timeout happened before snapshot reading
func TestMaintenanceSnapshotWithVersionTimeout(t *testing.T) {
	testMaintenanceSnapshotTimeout(t, func(ctx context.Context, client *clientv3.Client) (io.ReadCloser, error) {
		resp, err := client.SnapshotWithVersion(ctx)
		if err != nil {
			return nil, err
		}
		return resp.Snapshot, nil
	})
}

// TestMaintenanceSnapshotTimeout ensures that Snapshot function
// returns corresponding context errors when context timeout happened before snapshot reading
func TestMaintenanceSnapshotTimeout(t *testing.T) {
	testMaintenanceSnapshotTimeout(t, func(ctx context.Context, client *clientv3.Client) (io.ReadCloser, error) {
		return client.Snapshot(ctx)
	})
}

// testMaintenanceSnapshotTimeout given snapshot function ensures that it
// returns corresponding context errors when context timeout happened before snapshot reading
func testMaintenanceSnapshotTimeout(t *testing.T, snapshot func(context.Context, *clientv3.Client) (io.ReadCloser, error)) {
	integration.BeforeTest(t)

	clus := integration.NewClusterV3(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	// reading snapshot with deadline exceeded should error out
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	rc2, err := snapshot(ctx, clus.RandClient())
	if err != nil {
		t.Fatal(err)
	}
	defer rc2.Close()

	time.Sleep(2 * time.Second)

	_, err = io.Copy(ioutil.Discard, rc2)
	if err != nil && !IsClientTimeout(err) {
		t.Errorf("expected client timeout, got %v", err)
	}
}

// TestMaintenanceSnapshotWithVersionErrorInflight ensures that ReaderCloser returned by SnapshotWithVersion function
// will fail to read with corresponding context errors on inflight context cancel timeout.
func TestMaintenanceSnapshotWithVersionErrorInflight(t *testing.T) {
	testMaintenanceSnapshotErrorInflight(t, func(ctx context.Context, client *clientv3.Client) (io.ReadCloser, error) {
		resp, err := client.SnapshotWithVersion(ctx)
		if err != nil {
			return nil, err
		}
		return resp.Snapshot, nil
	})
}

// TestMaintenanceSnapshotError ensures that ReaderCloser returned by Snapshot function
// will fail to read with corresponding context errors on inflight context cancel timeout.
func TestMaintenanceSnapshotErrorInflight(t *testing.T) {
	testMaintenanceSnapshotErrorInflight(t, func(ctx context.Context, client *clientv3.Client) (io.ReadCloser, error) {
		return client.Snapshot(ctx)
	})
}

// testMaintenanceSnapshotErrorInflight given snapshot function ensures that ReaderCloser returned by it
// will fail to read with corresponding context errors on inflight context cancel timeout.
func testMaintenanceSnapshotErrorInflight(t *testing.T, snapshot func(context.Context, *clientv3.Client) (io.ReadCloser, error)) {
	integration.BeforeTest(t)

	clus := integration.NewClusterV3(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	// take about 1-second to read snapshot
	clus.Members[0].Stop(t)
	dpath := filepath.Join(clus.Members[0].DataDir, "member", "snap", "db")
	b := backend.NewDefaultBackend(dpath)
	s := mvcc.NewStore(zaptest.NewLogger(t), b, &lease.FakeLessor{}, mvcc.StoreConfig{CompactionBatchLimit: math.MaxInt32})
	rev := 100000
	for i := 2; i <= rev; i++ {
		s.Put([]byte(fmt.Sprintf("%10d", i)), bytes.Repeat([]byte("a"), 1024), lease.NoLease)
	}
	s.Close()
	b.Close()
	clus.Members[0].Restart(t)

	// reading snapshot with canceled context should error out
	ctx, cancel := context.WithCancel(context.Background())
	rc1, err := snapshot(ctx, clus.RandClient())
	if err != nil {
		t.Fatal(err)
	}
	defer rc1.Close()

	donec := make(chan struct{})
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
		close(donec)
	}()
	_, err = io.Copy(ioutil.Discard, rc1)
	if err != nil && err != context.Canceled {
		t.Errorf("expected %v, got %v", context.Canceled, err)
	}
	<-donec

	// reading snapshot with deadline exceeded should error out
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	rc2, err := snapshot(ctx, clus.RandClient())
	if err != nil {
		t.Fatal(err)
	}
	defer rc2.Close()

	// 300ms left and expect timeout while snapshot reading is in progress
	time.Sleep(700 * time.Millisecond)
	_, err = io.Copy(ioutil.Discard, rc2)
	if err != nil && !IsClientTimeout(err) {
		t.Errorf("expected client timeout, got %v", err)
	}
}

// TestMaintenanceSnapshotWithVersionVersion ensures that SnapshotWithVersion returns correct version value.
func TestMaintenanceSnapshotWithVersionVersion(t *testing.T) {
	integration.BeforeTest(t)

	// Set SnapshotCount to 1 to force raft snapshot to ensure that storage version is set
	clus := integration.NewClusterV3(t, &integration.ClusterConfig{Size: 1, SnapshotCount: 1})
	defer clus.Terminate(t)

	// Put some keys to ensure that wal snapshot is triggered
	for i := 0; i < 10; i++ {
		clus.RandClient().Put(context.Background(), fmt.Sprintf("%d", i), "1")
	}

	// reading snapshot with canceled context should error out
	resp, err := clus.RandClient().SnapshotWithVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Snapshot.Close()
	if resp.Version != "3.6.0" {
		t.Errorf("unexpected version, expected %q, got %q", version.Version, resp.Version)
	}
}

func TestMaintenanceStatus(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewClusterV3(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	clus.WaitLeader(t)

	eps := make([]string, 3)
	for i := 0; i < 3; i++ {
		eps[i] = clus.Members[i].GRPCAddr()
	}

	cli, err := integration.NewClient(t, clientv3.Config{Endpoints: eps, DialOptions: []grpc.DialOption{grpc.WithBlock()}})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	prevID, leaderFound := uint64(0), false
	for i := 0; i < 3; i++ {
		resp, err := cli.Status(context.TODO(), eps[i])
		if err != nil {
			t.Fatal(err)
		}
		if prevID == 0 {
			prevID, leaderFound = resp.Header.MemberId, resp.Header.MemberId == resp.Leader
			continue
		}
		if prevID == resp.Header.MemberId {
			t.Errorf("#%d: status returned duplicate member ID with %016x", i, prevID)
		}
		if leaderFound && resp.Header.MemberId == resp.Leader {
			t.Errorf("#%d: leader already found, but found another %016x", i, resp.Header.MemberId)
		}
		if !leaderFound {
			leaderFound = resp.Header.MemberId == resp.Leader
		}
	}
	if !leaderFound {
		t.Fatal("no leader found")
	}
}
