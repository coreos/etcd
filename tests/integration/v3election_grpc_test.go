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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
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

// TestV3ElectionProclaimNotLeader checks that Proclaim fails when given
// leader credentials that do not match the current leader (e.g. a stale
// revision), rather than silently overwriting the election value.
func TestV3ElectionProclaimNotLeader(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	lease, err := integration.ToGRPC(clus.RandClient()).Lease.LeaseGrant(t.Context(), &pb.LeaseGrantRequest{TTL: 30})
	require.NoError(t, err)

	lc := integration.ToGRPC(clus.Client(0)).Election
	c, cerr := lc.Campaign(t.Context(), &epb.CampaignRequest{Name: []byte("foo"), Lease: lease.ID, Value: []byte("abc")})
	require.NoError(t, cerr)

	// forge a stale LeaderKey with the wrong revision; the compare against
	// the actual CreateRevision should fail and reject the Proclaim.
	staleLeader := &epb.LeaderKey{
		Name:  c.Leader.Name,
		Key:   c.Leader.Key,
		Rev:   c.Leader.Rev + 1,
		Lease: c.Leader.Lease,
	}
	_, perr := lc.Proclaim(t.Context(), &epb.ProclaimRequest{Leader: staleLeader, Value: []byte("def")})
	require.ErrorContains(t, perr, "election: not leader")

	// the value must be unchanged
	lval, lverr := lc.Leader(t.Context(), &epb.LeaderRequest{Name: []byte("foo")})
	require.NoError(t, lverr)
	require.Equal(t, "abc", string(lval.Kv.Value))
}

// TestV3ElectionProclaimAfterResign checks that Proclaim fails once the
// leader has resigned, since resignation deletes the backing leader key.
func TestV3ElectionProclaimAfterResign(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	lease, err := integration.ToGRPC(clus.RandClient()).Lease.LeaseGrant(t.Context(), &pb.LeaseGrantRequest{TTL: 30})
	require.NoError(t, err)

	lc := integration.ToGRPC(clus.Client(0)).Election
	c, cerr := lc.Campaign(t.Context(), &epb.CampaignRequest{Name: []byte("foo"), Lease: lease.ID, Value: []byte("abc")})
	require.NoError(t, cerr)

	_, rerr := lc.Resign(t.Context(), &epb.ResignRequest{Leader: c.Leader})
	require.NoError(t, rerr)

	_, perr := lc.Proclaim(t.Context(), &epb.ProclaimRequest{Leader: c.Leader, Value: []byte("def")})
	require.ErrorContains(t, perr, "election: not leader")
}

// TestV3ElectionResignThenCampaignAgain checks that Resign fully releases
// the election so a brand new Campaign (not one already waiting in line)
// succeeds promptly rather than blocking on the resigned leader's key.
func TestV3ElectionResignThenCampaignAgain(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	lc := integration.ToGRPC(clus.Client(0)).Election

	lease1, err1 := integration.ToGRPC(clus.RandClient()).Lease.LeaseGrant(t.Context(), &pb.LeaseGrantRequest{TTL: 30})
	require.NoError(t, err1)
	c1, cerr1 := lc.Campaign(t.Context(), &epb.CampaignRequest{Name: []byte("foo"), Lease: lease1.ID, Value: []byte("abc")})
	require.NoError(t, cerr1)

	_, rerr := lc.Resign(t.Context(), &epb.ResignRequest{Leader: c1.Leader})
	require.NoError(t, rerr)

	lease2, err2 := integration.ToGRPC(clus.RandClient()).Lease.LeaseGrant(t.Context(), &pb.LeaseGrantRequest{TTL: 30})
	require.NoError(t, err2)

	campaignc := make(chan error, 1)
	go func() {
		_, cerr2 := lc.Campaign(t.Context(), &epb.CampaignRequest{Name: []byte("foo"), Lease: lease2.ID, Value: []byte("def")})
		campaignc <- cerr2
	}()

	select {
	case cerr2 := <-campaignc:
		require.NoError(t, cerr2)
	case <-time.After(time.Second):
		t.Fatalf("new campaign did not acquire leadership promptly after resign")
	}

	lval, lverr := lc.Leader(t.Context(), &epb.LeaderRequest{Name: []byte("foo")})
	require.NoError(t, lverr)
	require.Equal(t, "def", string(lval.Kv.Value))
}

// TestV3ElectionObserveCancellation checks that an Observe stream unblocks
// promptly when the client cancels its context, rather than hanging the
// server-side handler goroutine indefinitely.
func TestV3ElectionObserveCancellation(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	lc := integration.ToGRPC(clus.Client(0)).Election

	lease, err := integration.ToGRPC(clus.RandClient()).Lease.LeaseGrant(t.Context(), &pb.LeaseGrantRequest{TTL: 30})
	require.NoError(t, err)
	_, cerr := lc.Campaign(t.Context(), &epb.CampaignRequest{Name: []byte("foo"), Lease: lease.ID, Value: []byte("abc")})
	require.NoError(t, cerr)

	octx, ocancel := context.WithCancel(t.Context())
	s, operr := lc.Observe(octx, &epb.LeaderRequest{Name: []byte("foo")})
	require.NoError(t, operr)

	// consume the initial leadership notification before cancelling
	_, rerr := s.Recv()
	require.NoError(t, rerr)

	ocancel()

	recvc := make(chan error, 1)
	go func() {
		_, err := s.Recv()
		recvc <- err
	}()

	select {
	case err := <-recvc:
		require.Errorf(t, err, "expected Recv to fail after context cancellation")
	case <-time.After(time.Second):
		t.Fatalf("Observe stream did not unblock within 1s of client cancellation")
	}
}
