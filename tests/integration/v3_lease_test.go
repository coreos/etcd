// Copyright 2016 The etcd Authors
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
	"errors"
	"fmt"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	"go.etcd.io/etcd/client/pkg/v3/testutil"
	clientv3 "go.etcd.io/etcd/client/v3"
	framecfg "go.etcd.io/etcd/tests/v3/framework/config"
	"go.etcd.io/etcd/tests/v3/framework/integration"
	gofail "go.etcd.io/gofail/runtime"
)

// TestV3LeasePromote ensures the newly elected leader can promote itself
// to the primary lessor, refresh the leases and start to manage leases.
// TODO: use customized clock to make this test go faster?
func TestV3LeasePromote(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3, UseBridge: true})
	defer clus.Terminate(t)

	// create lease
	lresp, err := integration.ToGRPC(clus.RandClient()).Lease.LeaseGrant(t.Context(), &pb.LeaseGrantRequest{TTL: 3})
	ttl := time.Duration(lresp.TTL) * time.Second
	afterGrant := time.Now()
	require.NoError(t, err)
	require.Empty(t, lresp.Error)

	// wait until the lease is going to expire.
	time.Sleep(time.Until(afterGrant.Add(ttl - time.Second)))

	// kill the current leader, all leases should be refreshed.
	toStop := clus.WaitMembersForLeader(t, clus.Members)
	beforeStop := time.Now()
	clus.Members[toStop].Stop(t)

	var toWait []*integration.Member
	for i, m := range clus.Members {
		if i != toStop {
			toWait = append(toWait, m)
		}
	}
	clus.WaitMembersForLeader(t, toWait)
	clus.Members[toStop].Restart(t)
	clus.WaitMembersForLeader(t, clus.Members)
	afterReelect := time.Now()

	// ensure lease is refreshed by waiting for a "long" time.
	// it was going to expire anyway.
	time.Sleep(time.Until(beforeStop.Add(ttl - time.Second)))

	if !leaseExist(t, clus, lresp.ID) {
		t.Error("unexpected lease not exists")
	}

	// wait until the renewed lease is expected to expire.
	time.Sleep(time.Until(afterReelect.Add(ttl)))

	// wait for up to 10 seconds for lease to expire.
	expiredCondition := func() (bool, error) {
		return !leaseExist(t, clus, lresp.ID), nil
	}
	expired, err := testutil.Poll(100*time.Millisecond, 10*time.Second, expiredCondition)
	if err != nil {
		t.Error(err)
	}

	if !expired {
		t.Error("unexpected lease exists")
	}
}

// TestV3LeaseRevoke ensures a key is deleted once its lease is revoked.
func TestV3LeaseRevoke(t *testing.T) {
	integration.BeforeTest(t)
	testLeaseRemoveLeasedKey(t, func(clus *integration.Cluster, leaseID int64) error {
		lc := integration.ToGRPC(clus.RandClient()).Lease
		_, err := lc.LeaseRevoke(t.Context(), &pb.LeaseRevokeRequest{ID: leaseID})
		return err
	})
}

// TestV3LeaseGrantByID ensures leases may be created by a given id.
func TestV3LeaseGrantByID(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	// create fixed lease
	lresp, err := integration.ToGRPC(clus.RandClient()).Lease.LeaseGrant(
		t.Context(),
		&pb.LeaseGrantRequest{ID: 1, TTL: 1})
	if err != nil {
		t.Errorf("could not create lease 1 (%v)", err)
	}
	if lresp.ID != 1 {
		t.Errorf("got id %v, wanted id %v", lresp.ID, 1)
	}

	// create duplicate fixed lease
	_, err = integration.ToGRPC(clus.RandClient()).Lease.LeaseGrant(
		t.Context(),
		&pb.LeaseGrantRequest{ID: 1, TTL: 1})
	if !eqErrGRPC(err, rpctypes.ErrGRPCLeaseExist) {
		t.Error(err)
	}

	// create fresh fixed lease
	lresp, err = integration.ToGRPC(clus.RandClient()).Lease.LeaseGrant(
		t.Context(),
		&pb.LeaseGrantRequest{ID: 2, TTL: 1})
	if err != nil {
		t.Errorf("could not create lease 2 (%v)", err)
	}
	if lresp.ID != 2 {
		t.Errorf("got id %v, wanted id %v", lresp.ID, 2)
	}
}

// TestV3LeaseNegativeID ensures restarted member lessor can recover negative leaseID from backend.
//
// When the negative leaseID is used for lease revoke, all etcd nodes will remove the lease
// and delete associated keys to ensure kv store data consistency
//
// It ensures issue 12535 is fixed by PR 13676
func TestV3LeaseNegativeID(t *testing.T) {
	tcs := []struct {
		leaseID int64
		k       []byte
		v       []byte
	}{
		{
			leaseID: -1, // int64 -1 is 2^64 -1 in uint64
			k:       []byte("foo"),
			v:       []byte("bar"),
		},
		{
			leaseID: math.MaxInt64,
			k:       []byte("bar"),
			v:       []byte("foo"),
		},
		{
			leaseID: math.MinInt64,
			k:       []byte("hello"),
			v:       []byte("world"),
		},
	}
	for _, tc := range tcs {
		t.Run(fmt.Sprintf("test with lease ID %16x", tc.leaseID), func(t *testing.T) {
			integration.BeforeTest(t)
			clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3})
			defer clus.Terminate(t)

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			cc := clus.RandClient()
			lresp, err := integration.ToGRPC(cc).Lease.LeaseGrant(ctx, &pb.LeaseGrantRequest{ID: tc.leaseID, TTL: 300})
			if err != nil {
				t.Errorf("could not create lease %d (%v)", tc.leaseID, err)
			}
			if lresp.ID != tc.leaseID {
				t.Errorf("got id %v, wanted id %v", lresp.ID, tc.leaseID)
			}
			putr := &pb.PutRequest{Key: tc.k, Value: tc.v, Lease: tc.leaseID}
			_, err = integration.ToGRPC(cc).KV.Put(ctx, putr)
			if err != nil {
				t.Errorf("couldn't put key (%v)", err)
			}

			// wait for backend Commit
			time.Sleep(100 * time.Millisecond)
			// restore lessor from db file
			clus.Members[2].Stop(t)
			err = clus.Members[2].Restart(t)
			require.NoError(t, err)

			// revoke lease should remove key
			integration.WaitClientV3(t, clus.Members[2].Client)
			_, err = integration.ToGRPC(clus.RandClient()).Lease.LeaseRevoke(ctx, &pb.LeaseRevokeRequest{ID: tc.leaseID})
			if err != nil {
				t.Errorf("could not revoke lease %d (%v)", tc.leaseID, err)
			}
			var revision int64
			for _, m := range clus.Members {
				getr := &pb.RangeRequest{Key: tc.k}
				getresp, err := integration.ToGRPC(m.Client).KV.Range(ctx, getr)
				require.NoError(t, err)
				if revision == 0 {
					revision = getresp.Header.Revision
				}
				if revision != getresp.Header.Revision {
					t.Errorf("expect revision %d, but got %d", revision, getresp.Header.Revision)
				}
				if len(getresp.Kvs) != 0 {
					t.Errorf("lease removed but key remains")
				}
			}
		})
	}
}

// TestV3LeaseExpire ensures a key is deleted once a key expires.
func TestV3LeaseExpire(t *testing.T) {
	integration.BeforeTest(t)
	testLeaseRemoveLeasedKey(t, func(clus *integration.Cluster, leaseID int64) error {
		// let lease lapse; wait for deleted key

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		wStream, err := integration.ToGRPC(clus.RandClient()).Watch.Watch(ctx)
		if err != nil {
			return err
		}

		wreq := &pb.WatchRequest{RequestUnion: &pb.WatchRequest_CreateRequest{
			CreateRequest: &pb.WatchCreateRequest{
				Key: []byte("foo"), StartRevision: 1,
			},
		}}
		if err := wStream.Send(wreq); err != nil {
			return err
		}
		if _, err := wStream.Recv(); err != nil {
			// the 'created' message
			return err
		}
		if _, err := wStream.Recv(); err != nil {
			// the 'put' message
			return err
		}

		errc := make(chan error, 1)
		go func() {
			resp, err := wStream.Recv()
			switch {
			case err != nil:
				errc <- err
			case len(resp.Events) != 1:
				fallthrough
			case resp.Events[0].Type != mvccpb.Event_DELETE:
				errc <- fmt.Errorf("expected key delete, got %v", resp)
			default:
				errc <- nil
			}
		}()

		select {
		case <-time.After(15 * time.Second):
			return fmt.Errorf("lease expiration too slow")
		case err := <-errc:
			return err
		}
	})
}

// TestV3LeaseKeepAlive ensures keepalive keeps the lease alive.
func TestV3LeaseKeepAlive(t *testing.T) {
	integration.BeforeTest(t)
	testLeaseRemoveLeasedKey(t, func(clus *integration.Cluster, leaseID int64) error {
		lc := integration.ToGRPC(clus.RandClient()).Lease
		lreq := &pb.LeaseKeepAliveRequest{ID: leaseID}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		lac, err := lc.LeaseKeepAlive(ctx)
		if err != nil {
			return err
		}
		defer lac.CloseSend()

		// renew long enough so lease would've expired otherwise
		for i := 0; i < 3; i++ {
			if err = lac.Send(lreq); err != nil {
				return err
			}
			lresp, rxerr := lac.Recv()
			if rxerr != nil {
				return rxerr
			}
			if lresp.ID != leaseID {
				return fmt.Errorf("expected lease ID %v, got %v", leaseID, lresp.ID)
			}
			time.Sleep(time.Duration(lresp.TTL/2) * time.Second)
		}
		_, err = lc.LeaseRevoke(t.Context(), &pb.LeaseRevokeRequest{ID: leaseID})
		return err
	})
}

// TestV3LeaseKeepAliveForwardingCatchError ensures the server properly generates error
// codes while the follower server is forwarding LeaseKeepAlive request to the leader.
func TestV3LeaseKeepAliveForwardingCatchError(t *testing.T) {
	integration.BeforeTest(t)
	// Longer than leaseHandler.ServeHTTP()'s default timeout duration
	sleepDuration := 8 * time.Second

	t.Run("forwarding succeeds", func(t *testing.T) {
		leader, follower, _ := setupLeaseForwardingCluster(t)
		leaderClient := integration.ToGRPC(leader.Client).Lease

		grantResp, err := leaderClient.LeaseGrant(t.Context(), &pb.LeaseGrantRequest{TTL: 30})
		require.NoError(t, err)
		leaseID := grantResp.ID

		keepAliveClient, err := integration.ToGRPC(follower.Client).Lease.LeaseKeepAlive(t.Context())
		require.NoError(t, err)
		defer keepAliveClient.CloseSend()

		require.NoError(t, keepAliveClient.Send(&pb.LeaseKeepAliveRequest{ID: leaseID}))
		resp, err := keepAliveClient.Recv()
		require.NoError(t, err)
		require.Equal(t, leaseID, resp.ID)
		require.Positive(t, resp.TTL)
	})

	t.Run("client cancels while forwarding", func(t *testing.T) {
		integration.SkipIfNoGoFail(t)
		leader, follower, _ := setupLeaseForwardingCluster(t)
		leaderClient := integration.ToGRPC(leader.Client).Lease

		grantResp, err := leaderClient.LeaseGrant(t.Context(), &pb.LeaseGrantRequest{TTL: 30})
		require.NoError(t, err)
		leaseID := grantResp.ID

		ctx, cancel := context.WithCancel(t.Context())
		keepAliveClient, err := integration.ToGRPC(follower.Client).Lease.LeaseKeepAlive(ctx)
		require.NoError(t, err)
		defer keepAliveClient.CloseSend()

		require.NoError(t, keepAliveClient.Send(&pb.LeaseKeepAliveRequest{ID: leaseID}))
		_, err = keepAliveClient.Recv()
		require.NoError(t, err)

		sleepBeforeServingLeaseRenew(t, sleepDuration)

		// Use server metrics to verify behavior since client.Recv() always returns Canceled
		// after cancel() regardless of the actual server response.
		prevCanceledCount := getLeaseKeepAliveMetric(t, follower, "Canceled")
		prevUnavailableCount := getLeaseKeepAliveMetric(t, follower, "Unavailable")

		require.NoError(t, keepAliveClient.Send(&pb.LeaseKeepAliveRequest{ID: leaseID}))
		time.Sleep(50 * time.Millisecond)
		cancel()

		// Client sees Canceled (gRPC returns this immediately after cancel())
		_, err = keepAliveClient.Recv()
		require.Equal(t, codes.Canceled, status.Code(err))

		require.Eventually(t, func() bool {
			return getLeaseKeepAliveMetric(t, follower, "Canceled") == prevCanceledCount+1
		}, 3*time.Second, 100*time.Millisecond)
		require.Equal(t, prevUnavailableCount, getLeaseKeepAliveMetric(t, follower, "Unavailable"))
	})

	t.Run("forwarding times out", func(t *testing.T) {
		integration.SkipIfNoGoFail(t)
		leader, follower, _ := setupLeaseForwardingCluster(t)
		leaderClient := integration.ToGRPC(leader.Client).Lease

		grantResp, err := leaderClient.LeaseGrant(t.Context(), &pb.LeaseGrantRequest{TTL: 30})
		require.NoError(t, err)
		leaseID := grantResp.ID

		keepAliveClient, err := integration.ToGRPC(follower.Client).Lease.LeaseKeepAlive(t.Context())
		require.NoError(t, err)
		defer keepAliveClient.CloseSend()

		require.NoError(t, keepAliveClient.Send(&pb.LeaseKeepAliveRequest{ID: leaseID}))
		_, err = keepAliveClient.Recv()
		require.NoError(t, err)

		sleepBeforeServingLeaseRenew(t, sleepDuration)

		prevUnavailableCount := getLeaseKeepAliveMetric(t, follower, "Unavailable")
		require.NoError(t, keepAliveClient.Send(&pb.LeaseKeepAliveRequest{ID: leaseID}))

		_, err = keepAliveClient.Recv()
		require.Equal(t, rpctypes.ErrGRPCTimeout, err)

		require.Eventually(t, func() bool {
			return getLeaseKeepAliveMetric(t, follower, "Unavailable") == prevUnavailableCount+1
		}, 3*time.Second, 100*time.Millisecond)
	})

	// Client set up with WithRequireLeader() will receive NoLeader error right after
	// monitorLeader() detects leader missing and cancels the server stream with ErrGRPCNoLeader.
	t.Run("catches NoLeader error with WithRequireLeader", func(t *testing.T) {
		leader, follower, anotherFollower := setupLeaseForwardingCluster(t)
		followerClient := integration.ToGRPC(follower.Client).Lease

		prevUnavailableCount := getLeaseKeepAliveMetric(t, follower, "Unavailable")
		leader.Stop(t)
		anotherFollower.Stop(t)

		keepAliveClient, err := followerClient.LeaseKeepAlive(clientv3.WithRequireLeader(t.Context()))
		require.NoError(t, err)

		_, err = keepAliveClient.Recv()
		require.Equal(t, rpctypes.ErrNoLeader.Error(), rpctypes.ErrorDesc(err))
		// Skip metric check in proxy mode - metrics are recorded on the proxy, not the etcd server.
		if !integration.ThroughProxy {
			require.Equal(t, prevUnavailableCount+1, getLeaseKeepAliveMetric(t, follower, "Unavailable"))
		}
	})

	// Client receives NoLeader error after the waitLeader() timed out in LeaseRenew().
	t.Run("catches NoLeader error without WithRequireLeader", func(t *testing.T) {
		leader, follower, anotherFollower := setupLeaseForwardingCluster(t)
		leaderClient := integration.ToGRPC(leader.Client).Lease
		followerClient := integration.ToGRPC(follower.Client).Lease

		grantResp, err := leaderClient.LeaseGrant(t.Context(), &pb.LeaseGrantRequest{TTL: 30})
		require.NoError(t, err)
		leaseID := grantResp.ID

		keepAliveClient, err := followerClient.LeaseKeepAlive(t.Context())
		require.NoError(t, err)
		defer keepAliveClient.CloseSend()

		require.NoError(t, keepAliveClient.Send(&pb.LeaseKeepAliveRequest{ID: leaseID}))
		_, err = keepAliveClient.Recv()
		require.NoError(t, err)
		prevUnavailableCount := getLeaseKeepAliveMetric(t, follower, "Unavailable")

		leader.Stop(t)
		anotherFollower.Stop(t)
		require.NoError(t, keepAliveClient.Send(&pb.LeaseKeepAliveRequest{ID: leaseID}))
		_, err = keepAliveClient.Recv()
		if integration.ThroughProxy {
			// Known limitation: grpcproxy doesn't propagate NoLeader error without
			// WithRequireLeader. The keepAliveLoop in server/proxy/grpcproxy/lease.go
			// discards errors and only calls cancel(), resulting in context.Canceled.
			// TODO: Consider fixing grpcproxy to properly propagate errors.
			require.ErrorIs(t, err, context.Canceled)
		} else {
			require.Equal(t, rpctypes.ErrNoLeader.Error(), rpctypes.ErrorDesc(err))
			require.Equal(t, prevUnavailableCount+1, getLeaseKeepAliveMetric(t, follower, "Unavailable"))
		}
	})
}

func setupLeaseForwardingCluster(t *testing.T) (*integration.Member, *integration.Member, *integration.Member) {
	t.Helper()
	cluster := integration.NewCluster(t, &integration.ClusterConfig{Size: 3})
	t.Cleanup(func() { cluster.Terminate(t) })

	leaderIdx := cluster.WaitLeader(t)
	return cluster.Members[leaderIdx], cluster.Members[(leaderIdx+1)%3], cluster.Members[(leaderIdx+2)%3]
}

func getLeaseKeepAliveMetric(t *testing.T, member *integration.Member, grpcCode string) int64 {
	t.Helper()
	metricVal, err := member.Metric(
		"grpc_server_handled_total",
		`grpc_method="LeaseKeepAlive"`,
		fmt.Sprintf(`grpc_code="%v"`, grpcCode),
	)
	require.NoError(t, err)
	count, err := strconv.ParseInt(metricVal, 10, 32)
	require.NoError(t, err)
	return count
}

func sleepBeforeServingLeaseRenew(t *testing.T, duration time.Duration) {
	t.Helper()
	failpointName := "beforeServeHTTPLeaseRenew"
	require.NoError(t, gofail.Enable(failpointName, fmt.Sprintf(`sleep("%s")`, duration)))
	t.Cleanup(func() {
		terr := gofail.Disable(failpointName)
		if terr != nil && !errors.Is(terr, gofail.ErrDisabled) {
			t.Fatalf("Failed to disable failpoint %v, got error: %v", failpointName, terr)
		}
	})
}

// TestV3LeaseCheckpoint ensures a lease checkpoint results in a remaining TTL being persisted
// across leader elections.
func TestV3LeaseCheckpoint(t *testing.T) {
	tcs := []struct {
		name                  string
		checkpointingEnabled  bool
		ttl                   time.Duration
		checkpointingInterval time.Duration
		leaderChanges         int
		clusterSize           int
		expectTTLIsGT         time.Duration
		expectTTLIsLT         time.Duration
	}{
		{
			name:          "Checkpointing disabled, lease TTL is reset",
			ttl:           300 * time.Second,
			leaderChanges: 1,
			clusterSize:   3,
			expectTTLIsGT: 298 * time.Second,
		},
		{
			name:                  "Checkpointing enabled 10s, lease TTL is preserved after leader change",
			ttl:                   300 * time.Second,
			checkpointingEnabled:  true,
			checkpointingInterval: 10 * time.Second,
			leaderChanges:         1,
			clusterSize:           3,
			expectTTLIsLT:         290 * time.Second,
		},
		{
			name:                  "Checkpointing enabled 10s, lease TTL is preserved after cluster restart",
			ttl:                   300 * time.Second,
			checkpointingEnabled:  true,
			checkpointingInterval: 10 * time.Second,
			leaderChanges:         1,
			clusterSize:           1,
			expectTTLIsLT:         290 * time.Second,
		},
		{
			// Checking if checkpointing continues after the first leader change.
			name:                  "Checkpointing enabled 10s, lease TTL is preserved after 2 leader changes",
			ttl:                   300 * time.Second,
			checkpointingEnabled:  true,
			checkpointingInterval: 10 * time.Second,
			leaderChanges:         2,
			clusterSize:           3,
			expectTTLIsLT:         280 * time.Second,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			integration.BeforeTest(t)
			config := &integration.ClusterConfig{
				Size:                    tc.clusterSize,
				EnableLeaseCheckpoint:   tc.checkpointingEnabled,
				LeaseCheckpointInterval: tc.checkpointingInterval,
			}
			clus := integration.NewCluster(t, config)
			defer clus.Terminate(t)

			// create lease
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			c := integration.ToGRPC(clus.RandClient())
			lresp, err := c.Lease.LeaseGrant(ctx, &pb.LeaseGrantRequest{TTL: int64(tc.ttl.Seconds())})
			require.NoError(t, err)

			for i := 0; i < tc.leaderChanges; i++ {
				// wait for a checkpoint to occur
				time.Sleep(tc.checkpointingInterval + 1*time.Second)

				// Force a leader election
				leaderID := clus.WaitLeader(t)
				leader := clus.Members[leaderID]
				leader.Stop(t)
				time.Sleep(time.Duration(3*integration.ElectionTicks) * framecfg.TickDuration)
				leader.Restart(t)
			}

			newLeaderID := clus.WaitLeader(t)
			c2 := integration.ToGRPC(clus.Client(newLeaderID))

			time.Sleep(250 * time.Millisecond)

			// Check the TTL of the new leader
			var ttlresp *pb.LeaseTimeToLiveResponse
			for i := 0; i < 10; i++ {
				if ttlresp, err = c2.Lease.LeaseTimeToLive(ctx, &pb.LeaseTimeToLiveRequest{ID: lresp.ID}); err != nil {
					if status, ok := status.FromError(err); ok && status.Code() == codes.Unavailable {
						time.Sleep(time.Millisecond * 250)
					} else {
						t.Fatal(err)
					}
				}
			}

			if tc.expectTTLIsGT != 0 && time.Duration(ttlresp.TTL)*time.Second < tc.expectTTLIsGT {
				t.Errorf("Expected lease ttl (%v) to be >= than (%v)", time.Duration(ttlresp.TTL)*time.Second, tc.expectTTLIsGT)
			}

			if tc.expectTTLIsLT != 0 && time.Duration(ttlresp.TTL)*time.Second > tc.expectTTLIsLT {
				t.Errorf("Expected lease ttl (%v) to be lower than (%v)", time.Duration(ttlresp.TTL)*time.Second, tc.expectTTLIsLT)
			}
		})
	}
}

// TestV3LeaseExists creates a lease on a random client and confirms it exists in the cluster.
func TestV3LeaseExists(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	// create lease
	ctx0, cancel0 := context.WithCancel(t.Context())
	defer cancel0()
	lresp, err := integration.ToGRPC(clus.RandClient()).Lease.LeaseGrant(
		ctx0,
		&pb.LeaseGrantRequest{TTL: 30})
	require.NoError(t, err)
	require.Empty(t, lresp.Error)

	if !leaseExist(t, clus, lresp.ID) {
		t.Error("unexpected lease not exists")
	}
}

// TestV3LeaseLeases creates leases and confirms list RPC fetches created ones.
func TestV3LeaseLeases(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	ctx0, cancel0 := context.WithCancel(t.Context())
	defer cancel0()

	// create leases
	var ids []int64
	for i := 0; i < 5; i++ {
		lresp, err := integration.ToGRPC(clus.RandClient()).Lease.LeaseGrant(
			ctx0,
			&pb.LeaseGrantRequest{TTL: 30})
		require.NoError(t, err)
		require.Empty(t, lresp.Error)
		ids = append(ids, lresp.ID)
	}

	lresp, err := integration.ToGRPC(clus.RandClient()).Lease.LeaseLeases(
		t.Context(),
		&pb.LeaseLeasesRequest{})
	require.NoError(t, err)
	for i := range lresp.Leases {
		if lresp.Leases[i].ID != ids[i] {
			t.Fatalf("#%d: lease ID expected %d, got %d", i, ids[i], lresp.Leases[i].ID)
		}
	}
}

// TestV3LeaseRenewStress keeps creating lease and renewing it immediately to ensure the renewal goes through.
// it was oberserved that the immediate lease renewal after granting a lease from follower resulted lease not found.
// related issue https://github.com/etcd-io/etcd/issues/6978
func TestV3LeaseRenewStress(t *testing.T) {
	testLeaseStress(t, stressLeaseRenew, false)
}

// TestV3LeaseRenewStressWithClusterClient is similar to TestV3LeaseRenewStress,
// but it uses a cluster client instead of a specific member's client.
// The related issue is https://github.com/etcd-io/etcd/issues/13675.
func TestV3LeaseRenewStressWithClusterClient(t *testing.T) {
	testLeaseStress(t, stressLeaseRenew, true)
}

// TestV3LeaseTimeToLiveStress keeps creating lease and retrieving it immediately to ensure the lease can be retrieved.
// it was oberserved that the immediate lease retrieval after granting a lease from follower resulted lease not found.
// related issue https://github.com/etcd-io/etcd/issues/6978
func TestV3LeaseTimeToLiveStress(t *testing.T) {
	testLeaseStress(t, stressLeaseTimeToLive, false)
}

// TestV3LeaseTimeToLiveStressWithClusterClient is similar to TestV3LeaseTimeToLiveStress,
// but it uses a cluster client instead of a specific member's client.
// The related issue is https://github.com/etcd-io/etcd/issues/13675.
func TestV3LeaseTimeToLiveStressWithClusterClient(t *testing.T) {
	testLeaseStress(t, stressLeaseTimeToLive, true)
}

func testLeaseStress(t *testing.T, stresser func(context.Context, pb.LeaseClient) error, useClusterClient bool) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	errc := make(chan error)

	if useClusterClient {
		clusterClient, err := clus.ClusterClient(t)
		require.NoError(t, err)
		for i := 0; i < 300; i++ {
			go func() { errc <- stresser(ctx, integration.ToGRPC(clusterClient).Lease) }()
		}
	} else {
		for i := 0; i < 100; i++ {
			for j := 0; j < 3; j++ {
				go func(i int) { errc <- stresser(ctx, integration.ToGRPC(clus.Client(i)).Lease) }(j)
			}
		}
	}

	for i := 0; i < 300; i++ {
		err := <-errc
		require.NoError(t, err)
	}
}

func stressLeaseRenew(tctx context.Context, lc pb.LeaseClient) (reterr error) {
	defer func() {
		if tctx.Err() != nil {
			reterr = nil
		}
	}()
	lac, err := lc.LeaseKeepAlive(tctx)
	if err != nil {
		return err
	}
	for tctx.Err() == nil {
		resp, gerr := lc.LeaseGrant(tctx, &pb.LeaseGrantRequest{TTL: 60})
		if gerr != nil {
			continue
		}
		err = lac.Send(&pb.LeaseKeepAliveRequest{ID: resp.ID})
		if err != nil {
			continue
		}
		rresp, rxerr := lac.Recv()
		if rxerr != nil {
			continue
		}
		if rresp.TTL == 0 {
			return errors.New("TTL shouldn't be 0 so soon")
		}
	}
	return nil
}

func stressLeaseTimeToLive(tctx context.Context, lc pb.LeaseClient) (reterr error) {
	defer func() {
		if tctx.Err() != nil {
			reterr = nil
		}
	}()
	for tctx.Err() == nil {
		resp, gerr := lc.LeaseGrant(tctx, &pb.LeaseGrantRequest{TTL: 60})
		if gerr != nil {
			continue
		}
		_, kerr := lc.LeaseTimeToLive(tctx, &pb.LeaseTimeToLiveRequest{ID: resp.ID})
		if errors.Is(rpctypes.Error(kerr), rpctypes.ErrLeaseNotFound) {
			return kerr
		}
	}
	return nil
}

func TestV3PutOnNonExistLease(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	badLeaseID := int64(0x12345678)
	putr := &pb.PutRequest{Key: []byte("foo"), Value: []byte("bar"), Lease: badLeaseID}
	_, err := integration.ToGRPC(clus.RandClient()).KV.Put(ctx, putr)
	if !eqErrGRPC(err, rpctypes.ErrGRPCLeaseNotFound) {
		t.Errorf("err = %v, want %v", err, rpctypes.ErrGRPCLeaseNotFound)
	}
}

// TestV3GetNonExistLease ensures client retrieving nonexistent lease on a follower doesn't result node panic
// related issue https://github.com/etcd-io/etcd/issues/6537
func TestV3GetNonExistLease(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	lc := integration.ToGRPC(clus.RandClient()).Lease
	lresp, err := lc.LeaseGrant(ctx, &pb.LeaseGrantRequest{TTL: 10})
	if err != nil {
		t.Errorf("failed to create lease %v", err)
	}
	_, err = lc.LeaseRevoke(t.Context(), &pb.LeaseRevokeRequest{ID: lresp.ID})
	require.NoError(t, err)

	leaseTTLr := &pb.LeaseTimeToLiveRequest{
		ID:   lresp.ID,
		Keys: true,
	}

	for _, m := range clus.Members {
		// quorum-read to ensure revoke completes before TimeToLive
		_, err := integration.ToGRPC(m.Client).KV.Range(ctx, &pb.RangeRequest{Key: []byte("_")})
		require.NoError(t, err)
		resp, err := integration.ToGRPC(m.Client).Lease.LeaseTimeToLive(ctx, leaseTTLr)
		if err != nil {
			t.Fatalf("expected non nil error, but go %v", err)
		}
		if resp.TTL != -1 {
			t.Fatalf("expected TTL to be -1, but got %v", resp.TTL)
		}
	}
}

// TestV3LeaseSwitch tests a key can be switched from one lease to another.
func TestV3LeaseSwitch(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	key := "foo"

	// create lease
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	lresp1, err1 := integration.ToGRPC(clus.RandClient()).Lease.LeaseGrant(ctx, &pb.LeaseGrantRequest{TTL: 30})
	require.NoError(t, err1)
	lresp2, err2 := integration.ToGRPC(clus.RandClient()).Lease.LeaseGrant(ctx, &pb.LeaseGrantRequest{TTL: 30})
	require.NoError(t, err2)

	// attach key on lease1 then switch it to lease2
	put1 := &pb.PutRequest{Key: []byte(key), Lease: lresp1.ID}
	_, err := integration.ToGRPC(clus.RandClient()).KV.Put(ctx, put1)
	require.NoError(t, err)
	put2 := &pb.PutRequest{Key: []byte(key), Lease: lresp2.ID}
	_, err = integration.ToGRPC(clus.RandClient()).KV.Put(ctx, put2)
	require.NoError(t, err)

	// revoke lease1 should not remove key
	_, err = integration.ToGRPC(clus.RandClient()).Lease.LeaseRevoke(ctx, &pb.LeaseRevokeRequest{ID: lresp1.ID})
	require.NoError(t, err)
	rreq := &pb.RangeRequest{Key: []byte("foo")}
	rresp, err := integration.ToGRPC(clus.RandClient()).KV.Range(t.Context(), rreq)
	require.NoError(t, err)
	if len(rresp.Kvs) != 1 {
		t.Fatalf("unexpected removal of key")
	}

	// revoke lease2 should remove key
	_, err = integration.ToGRPC(clus.RandClient()).Lease.LeaseRevoke(ctx, &pb.LeaseRevokeRequest{ID: lresp2.ID})
	require.NoError(t, err)
	rresp, err = integration.ToGRPC(clus.RandClient()).KV.Range(t.Context(), rreq)
	require.NoError(t, err)
	if len(rresp.Kvs) != 0 {
		t.Fatalf("lease removed but key remains")
	}
}

// TestV3LeaseFailover ensures the old leader drops lease keepalive requests within
// election timeout after it loses its quorum. And the new leader extends the TTL of
// the lease to at least TTL + election timeout.
func TestV3LeaseFailover(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	toIsolate := clus.WaitMembersForLeader(t, clus.Members)

	lc := integration.ToGRPC(clus.Client(toIsolate)).Lease

	// create lease
	lresp, err := lc.LeaseGrant(t.Context(), &pb.LeaseGrantRequest{TTL: 5})
	require.NoError(t, err)
	require.Empty(t, lresp.Error)

	// isolate the current leader with its followers.
	clus.Members[toIsolate].Pause()

	lreq := &pb.LeaseKeepAliveRequest{ID: lresp.ID}

	md := metadata.Pairs(rpctypes.MetadataRequireLeaderKey, rpctypes.MetadataHasLeader)
	mctx := metadata.NewOutgoingContext(t.Context(), md)
	ctx, cancel := context.WithCancel(mctx)
	defer cancel()
	lac, err := lc.LeaseKeepAlive(ctx)
	require.NoError(t, err)

	// send keep alive to old leader until the old leader starts
	// to drop lease request.
	expectedExp := time.Now().Add(5 * time.Second)
	for {
		if err = lac.Send(lreq); err != nil {
			break
		}
		lkresp, rxerr := lac.Recv()
		if rxerr != nil {
			break
		}
		expectedExp = time.Now().Add(time.Duration(lkresp.TTL) * time.Second)
		time.Sleep(time.Duration(lkresp.TTL/2) * time.Second)
	}

	clus.Members[toIsolate].Resume()
	clus.WaitMembersForLeader(t, clus.Members)

	// lease should not expire at the last received expire deadline.
	time.Sleep(time.Until(expectedExp) - 500*time.Millisecond)

	if !leaseExist(t, clus, lresp.ID) {
		t.Error("unexpected lease not exists")
	}
}

const fiveMinTTL int64 = 300

// TestV3LeaseRecoverAndRevoke ensures that revoking a lease after restart deletes the attached key.
func TestV3LeaseRecoverAndRevoke(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1, UseBridge: true})
	defer clus.Terminate(t)

	kvc := integration.ToGRPC(clus.Client(0)).KV
	lsc := integration.ToGRPC(clus.Client(0)).Lease

	lresp, err := lsc.LeaseGrant(t.Context(), &pb.LeaseGrantRequest{TTL: fiveMinTTL})
	require.NoError(t, err)
	require.Empty(t, lresp.Error)
	_, err = kvc.Put(t.Context(), &pb.PutRequest{Key: []byte("foo"), Value: []byte("bar"), Lease: lresp.ID})
	require.NoError(t, err)

	// restart server and ensure lease still exists
	clus.Members[0].Stop(t)
	clus.Members[0].Restart(t)
	clus.WaitMembersForLeader(t, clus.Members)

	// overwrite old client with newly dialed connection
	// otherwise, error with "grpc: RPC failed fast due to transport failure"
	nc, err := integration.NewClientV3(clus.Members[0])
	require.NoError(t, err)
	kvc = integration.ToGRPC(nc).KV
	lsc = integration.ToGRPC(nc).Lease
	defer nc.Close()

	// revoke should delete the key
	_, err = lsc.LeaseRevoke(t.Context(), &pb.LeaseRevokeRequest{ID: lresp.ID})
	require.NoError(t, err)
	rresp, err := kvc.Range(t.Context(), &pb.RangeRequest{Key: []byte("foo")})
	require.NoError(t, err)
	if len(rresp.Kvs) != 0 {
		t.Fatalf("lease removed but key remains")
	}
}

// TestV3LeaseRevokeAndRecover ensures that revoked key stays deleted after restart.
func TestV3LeaseRevokeAndRecover(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1, UseBridge: true})
	defer clus.Terminate(t)

	kvc := integration.ToGRPC(clus.Client(0)).KV
	lsc := integration.ToGRPC(clus.Client(0)).Lease

	lresp, err := lsc.LeaseGrant(t.Context(), &pb.LeaseGrantRequest{TTL: fiveMinTTL})
	require.NoError(t, err)
	require.Empty(t, lresp.Error)
	_, err = kvc.Put(t.Context(), &pb.PutRequest{Key: []byte("foo"), Value: []byte("bar"), Lease: lresp.ID})
	require.NoError(t, err)

	// revoke should delete the key
	_, err = lsc.LeaseRevoke(t.Context(), &pb.LeaseRevokeRequest{ID: lresp.ID})
	require.NoError(t, err)

	// restart server and ensure revoked key doesn't exist
	clus.Members[0].Stop(t)
	clus.Members[0].Restart(t)
	clus.WaitMembersForLeader(t, clus.Members)

	// overwrite old client with newly dialed connection
	// otherwise, error with "grpc: RPC failed fast due to transport failure"
	nc, err := integration.NewClientV3(clus.Members[0])
	require.NoError(t, err)
	kvc = integration.ToGRPC(nc).KV
	defer nc.Close()

	rresp, err := kvc.Range(t.Context(), &pb.RangeRequest{Key: []byte("foo")})
	require.NoError(t, err)
	if len(rresp.Kvs) != 0 {
		t.Fatalf("lease removed but key remains")
	}
}

// TestV3LeaseRecoverKeyWithDetachedLease ensures that revoking a detached lease after restart
// does not delete the key.
func TestV3LeaseRecoverKeyWithDetachedLease(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1, UseBridge: true})
	defer clus.Terminate(t)

	kvc := integration.ToGRPC(clus.Client(0)).KV
	lsc := integration.ToGRPC(clus.Client(0)).Lease

	lresp, err := lsc.LeaseGrant(t.Context(), &pb.LeaseGrantRequest{TTL: fiveMinTTL})
	require.NoError(t, err)
	require.Empty(t, lresp.Error)
	_, err = kvc.Put(t.Context(), &pb.PutRequest{Key: []byte("foo"), Value: []byte("bar"), Lease: lresp.ID})
	require.NoError(t, err)

	// overwrite lease with none
	_, err = kvc.Put(t.Context(), &pb.PutRequest{Key: []byte("foo"), Value: []byte("bar")})
	require.NoError(t, err)

	// restart server and ensure lease still exists
	clus.Members[0].Stop(t)
	clus.Members[0].Restart(t)
	clus.WaitMembersForLeader(t, clus.Members)

	// overwrite old client with newly dialed connection
	// otherwise, error with "grpc: RPC failed fast due to transport failure"
	nc, err := integration.NewClientV3(clus.Members[0])
	require.NoError(t, err)
	kvc = integration.ToGRPC(nc).KV
	lsc = integration.ToGRPC(nc).Lease
	defer nc.Close()

	// revoke the detached lease
	_, err = lsc.LeaseRevoke(t.Context(), &pb.LeaseRevokeRequest{ID: lresp.ID})
	require.NoError(t, err)
	rresp, err := kvc.Range(t.Context(), &pb.RangeRequest{Key: []byte("foo")})
	require.NoError(t, err)
	if len(rresp.Kvs) != 1 {
		t.Fatalf("only detached lease removed, key should remain")
	}
}

func TestV3LeaseRecoverKeyWithMultipleLease(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1, UseBridge: true})
	defer clus.Terminate(t)

	kvc := integration.ToGRPC(clus.Client(0)).KV
	lsc := integration.ToGRPC(clus.Client(0)).Lease

	var leaseIDs []int64
	for i := 0; i < 2; i++ {
		lresp, err := lsc.LeaseGrant(t.Context(), &pb.LeaseGrantRequest{TTL: fiveMinTTL})
		require.NoError(t, err)
		require.Empty(t, lresp.Error)
		leaseIDs = append(leaseIDs, lresp.ID)

		_, err = kvc.Put(t.Context(), &pb.PutRequest{Key: []byte("foo"), Value: []byte("bar"), Lease: lresp.ID})
		require.NoError(t, err)
	}

	// restart server and ensure lease still exists
	clus.Members[0].Stop(t)
	clus.Members[0].Restart(t)
	clus.WaitMembersForLeader(t, clus.Members)
	clus.Members[0].WaitStarted(t)
	for i, leaseID := range leaseIDs {
		if !leaseExist(t, clus, leaseID) {
			t.Errorf("#%d: unexpected lease not exists", i)
		}
	}

	// overwrite old client with newly dialed connection
	// otherwise, error with "grpc: RPC failed fast due to transport failure"
	nc, err := integration.NewClientV3(clus.Members[0])
	require.NoError(t, err)
	kvc = integration.ToGRPC(nc).KV
	lsc = integration.ToGRPC(nc).Lease
	defer nc.Close()

	// revoke the old lease
	_, err = lsc.LeaseRevoke(t.Context(), &pb.LeaseRevokeRequest{ID: leaseIDs[0]})
	require.NoError(t, err)
	// key should still exist
	rresp, err := kvc.Range(t.Context(), &pb.RangeRequest{Key: []byte("foo")})
	require.NoError(t, err)
	if len(rresp.Kvs) != 1 {
		t.Fatalf("only detached lease removed, key should remain")
	}

	// revoke the latest lease
	_, err = lsc.LeaseRevoke(t.Context(), &pb.LeaseRevokeRequest{ID: leaseIDs[1]})
	require.NoError(t, err)
	rresp, err = kvc.Range(t.Context(), &pb.RangeRequest{Key: []byte("foo")})
	require.NoError(t, err)
	if len(rresp.Kvs) != 0 {
		t.Fatalf("lease removed but key remains")
	}
}

func TestV3LeaseTimeToLiveWithLeaderChanged(t *testing.T) {
	t.Run("normal", func(subT *testing.T) {
		testV3LeaseTimeToLiveWithLeaderChanged(subT, "beforeLookupWhenLeaseTimeToLive")
	})

	t.Run("forward", func(subT *testing.T) {
		testV3LeaseTimeToLiveWithLeaderChanged(subT, "beforeLookupWhenForwardLeaseTimeToLive")
	})
}

func testV3LeaseTimeToLiveWithLeaderChanged(t *testing.T, fpName string) {
	integration.SkipIfNoGoFail(t)
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	oldLeadIdx := clus.WaitLeader(t)
	followerIdx := (oldLeadIdx + 1) % 3

	followerMemberID := clus.Members[followerIdx].ID()

	oldLeadC := clus.Client(oldLeadIdx)

	leaseResp, err := oldLeadC.Grant(ctx, 100)
	require.NoError(t, err)

	require.NoError(t, gofail.Enable(fpName, `sleep("3s")`))
	t.Cleanup(func() {
		terr := gofail.Disable(fpName)
		if terr != nil && !errors.Is(terr, gofail.ErrDisabled) {
			t.Fatalf("failed to disable %s: %v", fpName, terr)
		}
	})

	readyCh := make(chan struct{})
	errCh := make(chan error, 1)

	var targetC *clientv3.Client
	switch fpName {
	case "beforeLookupWhenLeaseTimeToLive":
		targetC = oldLeadC
	case "beforeLookupWhenForwardLeaseTimeToLive":
		targetC = clus.Client((oldLeadIdx + 2) % 3)
	default:
		t.Fatalf("unsupported %s failpoint", fpName)
	}

	go func() {
		<-readyCh
		time.Sleep(1 * time.Second)

		_, merr := oldLeadC.MoveLeader(ctx, uint64(followerMemberID))
		assert.NoError(t, gofail.Disable(fpName))
		errCh <- merr
	}()

	close(readyCh)

	ttlResp, err := targetC.TimeToLive(ctx, leaseResp.ID)
	require.NoError(t, err)
	require.GreaterOrEqual(t, int64(100), ttlResp.TTL)

	require.NoError(t, <-errCh)
}

// acquireLeaseAndKey creates a new lease and creates an attached key.
func acquireLeaseAndKey(clus *integration.Cluster, key string) (int64, error) {
	// create lease
	lresp, err := integration.ToGRPC(clus.RandClient()).Lease.LeaseGrant(
		context.TODO(),
		&pb.LeaseGrantRequest{TTL: 1})
	if err != nil {
		return 0, err
	}
	if lresp.Error != "" {
		return 0, errors.New(lresp.Error)
	}
	// attach to key
	put := &pb.PutRequest{Key: []byte(key), Lease: lresp.ID}
	if _, err := integration.ToGRPC(clus.RandClient()).KV.Put(context.TODO(), put); err != nil {
		return 0, err
	}
	return lresp.ID, nil
}

// testLeaseRemoveLeasedKey performs some action while holding a lease with an
// attached key "foo", then confirms the key is gone.
func testLeaseRemoveLeasedKey(t *testing.T, act func(*integration.Cluster, int64) error) {
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	leaseID, err := acquireLeaseAndKey(clus, "foo")
	require.NoError(t, err)

	err = act(clus, leaseID)
	require.NoError(t, err)

	// confirm no key
	rreq := &pb.RangeRequest{Key: []byte("foo")}
	rresp, err := integration.ToGRPC(clus.RandClient()).KV.Range(t.Context(), rreq)
	require.NoError(t, err)
	if len(rresp.Kvs) != 0 {
		t.Fatalf("lease removed but key remains")
	}
}

func leaseExist(t *testing.T, clus *integration.Cluster, leaseID int64) bool {
	l := integration.ToGRPC(clus.RandClient()).Lease

	_, err := l.LeaseGrant(t.Context(), &pb.LeaseGrantRequest{ID: leaseID, TTL: 5})
	if err == nil {
		_, err = l.LeaseRevoke(t.Context(), &pb.LeaseRevokeRequest{ID: leaseID})
		if err != nil {
			t.Fatalf("failed to check lease %v", err)
		}
		return false
	}

	if eqErrGRPC(err, rpctypes.ErrGRPCLeaseExist) {
		return true
	}
	t.Fatalf("unexpecter error %v", err)

	return true
}

// TestV3LeaseKeepAliveReturnsUnavailableOnLeaderLoss verifies that when a
// lease keepalive fails because the leader is lost, the client receives
// codes.Unavailable so it can distinguish this transient error from permanent
// failures and retry on another member.
//
// This exercises the gRPC error mapping for leadership-related lease errors
// (ErrNotPrimary, ErrNoLeader) which should all surface as Unavailable.
func TestV3LeaseKeepAliveReturnsUnavailableOnLeaderLoss(t *testing.T) {
	integration.BeforeTest(t)
	leader, follower, anotherFollower := setupLeaseForwardingCluster(t)
	leaderClient := integration.ToGRPC(leader.Client).Lease
	followerClient := integration.ToGRPC(follower.Client).Lease

	grantResp, err := leaderClient.LeaseGrant(t.Context(), &pb.LeaseGrantRequest{TTL: 30})
	require.NoError(t, err)
	leaseID := grantResp.ID

	// Verify keepalive works while leader is up.
	keepAliveClient, err := followerClient.LeaseKeepAlive(t.Context())
	require.NoError(t, err)
	defer keepAliveClient.CloseSend()

	require.NoError(t, keepAliveClient.Send(&pb.LeaseKeepAliveRequest{ID: leaseID}))
	resp, err := keepAliveClient.Recv()
	require.NoError(t, err)
	require.Positive(t, resp.TTL)

	// Stop leader and other follower so the remaining follower cannot
	// forward the renewal.
	leader.Stop(t)
	anotherFollower.Stop(t)

	// Send another keepalive. The follower should return Unavailable.
	require.NoError(t, keepAliveClient.Send(&pb.LeaseKeepAliveRequest{ID: leaseID}))
	_, err = keepAliveClient.Recv()
	require.Error(t, err)

	if integration.ThroughProxy {
		// grpcproxy doesn't propagate the specific error code.
		require.ErrorIs(t, err, context.Canceled)
	} else {
		require.Equalf(t, codes.Unavailable, status.Code(err),
			"expected Unavailable on leader loss, got %v: %v", status.Code(err), err)
	}
}
