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

package integration

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/status"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/client/v3/concurrency"
	epb "go.etcd.io/etcd/server/v3/etcdserver/api/v3election/v3electionpb"
	"go.etcd.io/etcd/tests/v3/framework/integration"
)

// TestV3ElectionCampaign checks that Campaign will not give
// simultaneous leadership to multiple campaigners.
func TestV3ElectionCampaign(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	lease1, err1 := integration.ToGRPC(clus.RandClient()).Lease.LeaseGrant(t.Context(), &pb.LeaseGrantRequest{TTL: 30})
	require.NoError(t, err1)
	lease2, err2 := integration.ToGRPC(clus.RandClient()).Lease.LeaseGrant(t.Context(), &pb.LeaseGrantRequest{TTL: 30})
	require.NoError(t, err2)

	lc := integration.ToGRPC(clus.Client(0)).Election
	req1 := &epb.CampaignRequest{Name: []byte("foo"), Lease: lease1.ID, Value: []byte("abc")}
	l1, lerr1 := lc.Campaign(t.Context(), req1)
	require.NoError(t, lerr1)

	campaignc := make(chan struct{})
	go func() {
		defer close(campaignc)
		req2 := &epb.CampaignRequest{Name: []byte("foo"), Lease: lease2.ID, Value: []byte("def")}
		l2, lerr2 := lc.Campaign(t.Context(), req2)
		if lerr2 != nil {
			t.Error(lerr2)
		}
		if l1.Header.Revision >= l2.Header.Revision {
			t.Errorf("expected l1 revision < l2 revision, got %d >= %d", l1.Header.Revision, l2.Header.Revision)
		}
	}()

	select {
	case <-time.After(200 * time.Millisecond):
	case <-campaignc:
		t.Fatalf("got leadership before resign")
	}

	_, uerr := lc.Resign(t.Context(), &epb.ResignRequest{Leader: l1.Leader})
	require.NoError(t, uerr)

	select {
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("campaigner unelected after resign")
	case <-campaignc:
	}

	lval, lverr := lc.Leader(t.Context(), &epb.LeaderRequest{Name: []byte("foo")})
	require.NoError(t, lverr)

	if string(lval.Kv.Value) != "def" {
		t.Fatalf("got election value %q, expected %q", string(lval.Kv.Value), "def")
	}
}

func TestV3ElectionRequestBehavior(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	grpc := integration.ToGRPC(clus.Client(0))
	lc := grpc.Election

	campaign := func(t *testing.T, name, value string) *epb.LeaderKey {
		t.Helper()
		lease, err := grpc.Lease.LeaseGrant(t.Context(), &pb.LeaseGrantRequest{TTL: 30})
		require.NoError(t, err)
		resp, err := lc.Campaign(t.Context(), &epb.CampaignRequest{
			Name:  []byte(name),
			Lease: lease.ID,
			Value: []byte(value),
		})
		require.NoError(t, err)
		return resp.Leader
	}

	requireNotLeader := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err)
		require.Equal(t, concurrency.ErrElectionNotLeader.Error(), status.Convert(err).Message())
	}

	t.Run("ProclaimRejectsMismatchedLeader", func(t *testing.T) {
		const name = "proclaim-mismatched-leader"
		leader := campaign(t, name, "first")
		mismatchedLeader := *leader
		mismatchedLeader.Rev++

		_, err := lc.Proclaim(t.Context(), &epb.ProclaimRequest{
			Leader: &mismatchedLeader,
			Value:  []byte("second"),
		})
		requireNotLeader(t, err)

		resp, err := lc.Leader(t.Context(), &epb.LeaderRequest{Name: []byte(name)})
		require.NoError(t, err)
		require.Equal(t, "first", string(resp.Kv.Value))

		_, err = lc.Resign(t.Context(), &epb.ResignRequest{Leader: leader})
		require.NoError(t, err)
	})

	t.Run("ProclaimRejectsResignedLeader", func(t *testing.T) {
		leader := campaign(t, "proclaim-resigned-leader", "first")
		_, err := lc.Resign(t.Context(), &epb.ResignRequest{Leader: leader})
		require.NoError(t, err)

		_, err = lc.Proclaim(t.Context(), &epb.ProclaimRequest{
			Leader: leader,
			Value:  []byte("second"),
		})
		requireNotLeader(t, err)
	})

	t.Run("ResignAllowsNewCampaign", func(t *testing.T) {
		const name = "resign-new-campaign"
		leader := campaign(t, name, "first")
		_, err := lc.Resign(t.Context(), &epb.ResignRequest{Leader: leader})
		require.NoError(t, err)

		lease, err := grpc.Lease.LeaseGrant(t.Context(), &pb.LeaseGrantRequest{TTL: 30})
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		resp, err := lc.Campaign(ctx, &epb.CampaignRequest{
			Name:  []byte(name),
			Lease: lease.ID,
			Value: []byte("second"),
		})
		require.NoError(t, err)
		require.NotEqual(t, string(leader.Key), string(resp.Leader.Key))
		current, err := lc.Leader(t.Context(), &epb.LeaderRequest{Name: []byte(name)})
		require.NoError(t, err)
		require.Equal(t, "second", string(current.Kv.Value))

		_, err = lc.Resign(t.Context(), &epb.ResignRequest{Leader: resp.Leader})
		require.NoError(t, err)
	})

	t.Run("ObserveStopsOnContextCancel", func(t *testing.T) {
		metricValue := func(metricName string, extraLabels ...string) (int64, error) {
			labels := append([]string{
				`grpc_service="v3electionpb.Election"`,
				`grpc_method="Observe"`,
			}, extraLabels...)
			value, err := clus.Members[0].Metric(metricName, labels...)
			if err != nil {
				return 0, err
			}
			if value == "" {
				return 0, nil
			}
			return strconv.ParseInt(value, 10, 64)
		}

		startedCount, err := metricValue("grpc_server_started_total")
		require.NoError(t, err)
		canceledCount, err := metricValue("grpc_server_handled_total", `grpc_code="Canceled"`)
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		stream, err := lc.Observe(ctx, &epb.LeaderRequest{Name: []byte("observe-cancel")})
		require.NoError(t, err)
		require.EventuallyWithTf(t, func(c *assert.CollectT) {
			count, err := metricValue("grpc_server_started_total")
			require.NoError(c, err)
			require.Equal(c, startedCount+1, count)
		}, 3*time.Second, 100*time.Millisecond, "Observe server handler did not start")

		recvErrc := make(chan error, 1)
		go func() {
			_, err := stream.Recv()
			recvErrc <- err
		}()
		cancel()

		select {
		case err := <-recvErrc:
			require.Error(t, err)
		case <-time.After(time.Second):
			t.Fatal("Observe did not stop after its context was canceled")
		}

		require.EventuallyWithTf(t, func(c *assert.CollectT) {
			count, err := metricValue("grpc_server_handled_total", `grpc_code="Canceled"`)
			require.NoError(c, err)
			require.Equal(c, canceledCount+1, count)
		}, 3*time.Second, 100*time.Millisecond, "Observe server handler did not stop after cancellation")
	})
}

// TestV3ElectionObserve checks that an Observe stream receives
// proclamations from different leaders uninterrupted.
func TestV3ElectionObserve(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	lc := integration.ToGRPC(clus.Client(0)).Election

	// observe leadership events
	observec := make(chan struct{}, 1)
	go func() {
		defer close(observec)
		s, err := lc.Observe(t.Context(), &epb.LeaderRequest{Name: []byte("foo")})
		observec <- struct{}{}
		if err != nil {
			t.Error(err)
		}
		for i := 0; i < 10; i++ {
			resp, rerr := s.Recv()
			if rerr != nil {
				t.Error(rerr)
			}
			respV := 0
			fmt.Sscanf(string(resp.Kv.Value), "%d", &respV)
			// leader transitions should not go backwards
			if respV < i {
				t.Errorf(`got observe value %q, expected >= "%d"`, string(resp.Kv.Value), i)
			}
			i = respV
		}
	}()

	select {
	case <-observec:
	case <-time.After(time.Second):
		t.Fatalf("observe stream took too long to start")
	}

	lease1, err1 := integration.ToGRPC(clus.RandClient()).Lease.LeaseGrant(t.Context(), &pb.LeaseGrantRequest{TTL: 30})
	require.NoError(t, err1)
	c1, cerr1 := lc.Campaign(t.Context(), &epb.CampaignRequest{Name: []byte("foo"), Lease: lease1.ID, Value: []byte("0")})
	require.NoError(t, cerr1)

	// overlap other leader so it waits on resign
	leader2c := make(chan struct{})
	go func() {
		defer close(leader2c)

		lease2, err2 := integration.ToGRPC(clus.RandClient()).Lease.LeaseGrant(t.Context(), &pb.LeaseGrantRequest{TTL: 30})
		if err2 != nil {
			t.Error(err2)
		}
		c2, cerr2 := lc.Campaign(t.Context(), &epb.CampaignRequest{Name: []byte("foo"), Lease: lease2.ID, Value: []byte("5")})
		if cerr2 != nil {
			t.Error(cerr2)
		}
		for i := 6; i < 10; i++ {
			v := []byte(fmt.Sprintf("%d", i))
			req := &epb.ProclaimRequest{Leader: c2.Leader, Value: v}
			if _, err := lc.Proclaim(t.Context(), req); err != nil {
				t.Error(err)
			}
		}
	}()

	for i := 1; i < 5; i++ {
		v := []byte(fmt.Sprintf("%d", i))
		req := &epb.ProclaimRequest{Leader: c1.Leader, Value: v}
		_, err := lc.Proclaim(t.Context(), req)
		require.NoError(t, err)
	}
	// start second leader
	lc.Resign(t.Context(), &epb.ResignRequest{Leader: c1.Leader})

	select {
	case <-observec:
	case <-time.After(time.Second):
		t.Fatalf("observe did not observe all events in time")
	}

	<-leader2c
}
