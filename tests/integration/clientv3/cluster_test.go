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

package clientv3test

import (
	"context"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/client/pkg/v3/types"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/tests/v3/framework/integration"
)

func TestMemberList(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	capi := clus.RandClient()

	resp, err := capi.MemberList(t.Context())
	if err != nil {
		t.Fatalf("failed to list member %v", err)
	}

	if len(resp.Members) != 3 {
		t.Errorf("number of members = %d, want %d", len(resp.Members), 3)
	}
}

func TestMemberAdd(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3, DisableStrictReconfigCheck: true})
	defer clus.Terminate(t)

	capi := clus.RandClient()

	urls := []string{"http://127.0.0.1:1234"}
	resp, err := capi.MemberAdd(t.Context(), urls)
	if err != nil {
		t.Fatalf("failed to add member %v", err)
	}

	if !reflect.DeepEqual(resp.Member.PeerURLs, urls) {
		t.Errorf("urls = %v, want %v", urls, resp.Member.PeerURLs)
	}
}

func TestMemberAddWithExistingURLs(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3, DisableStrictReconfigCheck: true})
	defer clus.Terminate(t)

	capi := clus.RandClient()

	resp, err := capi.MemberList(t.Context())
	if err != nil {
		t.Fatalf("failed to list member %v", err)
	}

	existingURL := resp.Members[0].PeerURLs[0]
	_, err = capi.MemberAdd(t.Context(), []string{existingURL})
	expectedErrKeywords := "Peer URLs already exists"
	if err == nil {
		t.Fatalf("expecting add member to fail, got no error")
	}
	if !strings.Contains(err.Error(), expectedErrKeywords) {
		t.Errorf("expecting error to contain %s, got %s", expectedErrKeywords, err.Error())
	}
}

func TestMemberRemove(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3, DisableStrictReconfigCheck: true})
	defer clus.Terminate(t)

	capi := clus.Client(1)
	resp, err := capi.MemberList(t.Context())
	if err != nil {
		t.Fatalf("failed to list member %v", err)
	}

	rmvID := resp.Members[0].ID
	// indexes in capi member list don't necessarily match cluster member list;
	// find member that is not the client to remove
	for _, m := range resp.Members {
		mURLs, _ := types.NewURLs(m.PeerURLs)
		if !reflect.DeepEqual(mURLs, clus.Members[1].ServerConfig.PeerURLs) {
			rmvID = m.ID
			break
		}
	}

	_, err = capi.MemberRemove(t.Context(), rmvID)
	if err != nil {
		t.Fatalf("failed to remove member %v", err)
	}

	resp, err = capi.MemberList(t.Context())
	if err != nil {
		t.Fatalf("failed to list member %v", err)
	}

	if len(resp.Members) != 2 {
		t.Errorf("number of members = %d, want %d", len(resp.Members), 2)
	}
}

func TestMemberUpdate(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	capi := clus.RandClient()
	resp, err := capi.MemberList(t.Context())
	if err != nil {
		t.Fatalf("failed to list member %v", err)
	}

	urls := []string{"http://127.0.0.1:1234"}
	_, err = capi.MemberUpdate(t.Context(), resp.Members[0].ID, urls)
	if err != nil {
		t.Fatalf("failed to update member %v", err)
	}

	resp, err = capi.MemberList(t.Context())
	if err != nil {
		t.Fatalf("failed to list member %v", err)
	}

	if !reflect.DeepEqual(resp.Members[0].PeerURLs, urls) {
		t.Errorf("urls = %v, want %v", urls, resp.Members[0].PeerURLs)
	}
}

func TestMemberUpdateLearner(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3, DisableStrictReconfigCheck: true})
	defer clus.Terminate(t)

	capi := clus.RandClient()

	urls := []string{"http://127.0.0.1:1234"}
	addResp, err := capi.MemberAddAsLearner(t.Context(), urls)
	require.NoError(t, err)
	learnerID := addResp.Member.ID

	learner, err := getMemberByID(t.Context(), capi, learnerID)
	require.NoError(t, err)
	require.Truef(t, learner.IsLearner, "added a member as learner, IsLearner is %t", learner.IsLearner)

	updatedURLs := []string{"http://127.0.0.1:5678"}
	_, err = capi.MemberUpdate(t.Context(), learnerID, updatedURLs)
	require.NoError(t, err)

	learner, err = getMemberByID(t.Context(), capi, learnerID)
	require.NoError(t, err)
	require.Equal(t, learner.PeerURLs, updatedURLs)
	require.Truef(t, learner.IsLearner, "updated peer address of a learner member, IsLearner is %t", learner.IsLearner)
}

func TestMemberAddUpdateWrongURLs(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	capi := clus.RandClient()
	tt := [][]string{
		// missing protocol scheme
		{"://127.0.0.1:2379"},
		// unsupported scheme
		{"mailto://127.0.0.1:2379"},
		// not conform to host:port
		{"http://127.0.0.1"},
		// contain a path
		{"http://127.0.0.1:2379/path"},
		// first path segment in URL cannot contain colon
		{"127.0.0.1:1234"},
		// URL scheme must be http, https, unix, or unixs
		{"localhost:1234"},
	}
	for i := range tt {
		_, err := capi.MemberAdd(t.Context(), tt[i])
		if err == nil {
			t.Errorf("#%d: MemberAdd err = nil, but error", i)
		}
		_, err = capi.MemberUpdate(t.Context(), 0, tt[i])
		if err == nil {
			t.Errorf("#%d: MemberUpdate err = nil, but error", i)
		}
	}
}

func TestMemberAddForLearner(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3, DisableStrictReconfigCheck: true})
	defer clus.Terminate(t)

	capi := clus.RandClient()

	urls := []string{"http://127.0.0.1:1234"}
	resp, err := capi.MemberAddAsLearner(t.Context(), urls)
	if err != nil {
		t.Fatalf("failed to add member %v", err)
	}

	if !resp.Member.IsLearner {
		t.Errorf("Added a member as learner, got resp.Member.IsLearner = %v", resp.Member.IsLearner)
	}

	numberOfLearners := 0
	for _, m := range resp.Members {
		if m.IsLearner {
			numberOfLearners++
		}
	}
	if numberOfLearners != 1 {
		t.Errorf("Added 1 learner node to cluster, got %d", numberOfLearners)
	}
}

func TestMemberPromote(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3, DisableStrictReconfigCheck: true})
	defer clus.Terminate(t)

	// member promote request can be sent to any server in cluster,
	// the request will be auto-forwarded to leader on server-side.
	// This test explicitly includes the server-side forwarding by
	// sending the request to follower.
	leaderIdx := clus.WaitLeader(t)
	followerIdx := (leaderIdx + 1) % 3
	capi := clus.Client(followerIdx)

	learnerMember := clus.MustNewMember(t)
	urls := learnerMember.PeerURLs.StringSlice()
	memberAddResp, err := capi.MemberAddAsLearner(t.Context(), urls)
	if err != nil {
		t.Fatalf("failed to add member %v", err)
	}

	if !memberAddResp.Member.IsLearner {
		t.Fatalf("Added a member as learner, got resp.Member.IsLearner = %v", memberAddResp.Member.IsLearner)
	}
	learnerID := memberAddResp.Member.ID

	numberOfLearners := 0
	for _, m := range memberAddResp.Members {
		if m.IsLearner {
			numberOfLearners++
		}
	}
	if numberOfLearners != 1 {
		t.Fatalf("Added 1 learner node to cluster, got %d", numberOfLearners)
	}

	// learner is not started yet. Expect learner progress check to fail.
	// As the result, member promote request will fail.
	_, err = capi.MemberPromote(t.Context(), learnerID)
	expectedErrKeywords := "can only promote a learner member which is in sync with leader"
	if err == nil {
		t.Fatalf("expecting promote not ready learner to fail, got no error")
	}
	if !strings.Contains(err.Error(), expectedErrKeywords) {
		t.Fatalf("expecting error to contain %s, got %s", expectedErrKeywords, err.Error())
	}

	// Initialize and launch learner member based on the response of V3 Member Add API.
	// (the response has information on peer urls of the existing members in cluster)
	clus.InitializeMemberWithResponse(t, learnerMember, memberAddResp)

	require.NoError(t, learnerMember.Launch())

	// retry until promote succeed or timeout
	timeout := time.After(5 * time.Second)
	for {
		select {
		case <-time.After(500 * time.Millisecond):
		case <-timeout:
			t.Fatalf("failed all attempts to promote learner member, last error: %v", err)
		}

		_, err = capi.MemberPromote(t.Context(), learnerID)
		// successfully promoted learner
		if err == nil {
			break
		}
		// if member promote fails due to learner not ready, retry.
		// otherwise fails the test.
		if !strings.Contains(err.Error(), expectedErrKeywords) {
			t.Fatalf("unexpected error when promoting learner member: %v", err)
		}
	}
}

// TestAutoPromoteLearner verifies that, when the AutoPromoteLearners
// feature gate is enabled on every cluster member, a learner that has
// caught up with the leader is promoted to a voting member without an
// explicit MemberPromote call.
func TestAutoPromoteLearner(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{
		Size:                       3,
		DisableStrictReconfigCheck: true,
		EnableAutoPromoteLearners:  true,
	})
	defer clus.Terminate(t)

	leaderIdx := clus.WaitLeader(t)
	capi := clus.Client(leaderIdx)

	learnerMember := clus.MustNewMember(t)
	urls := learnerMember.PeerURLs.StringSlice()
	memberAddResp, err := capi.MemberAddAsLearner(t.Context(), urls)
	require.NoError(t, err)
	require.Truef(t, memberAddResp.Member.IsLearner, "expected new member to be a learner")
	learnerID := memberAddResp.Member.ID

	// Initialize and launch the learner so it can start catching up.
	clus.InitializeMemberWithResponse(t, learnerMember, memberAddResp)
	require.NoError(t, learnerMember.Launch())

	// Poll MemberList until the learner has been auto-promoted, or fail
	// after a generous deadline. The auto-promote loop ticks at
	// monitorVersionInterval (~4s), so allow a few iterations.
	require.Eventuallyf(t, func() bool {
		resp, lerr := capi.MemberList(t.Context())
		if lerr != nil {
			return false
		}
		for _, m := range resp.Members {
			if m.ID == learnerID {
				return !m.IsLearner
			}
		}
		return false
	}, 30*time.Second, 500*time.Millisecond, "learner %x was not auto-promoted within deadline", learnerID)
}

// TestMemberPromoteMemberNotLearner ensures that promoting a voting member fails.
func TestMemberPromoteMemberNotLearner(t *testing.T) {
	integration.BeforeTest(t, integration.WithFailpoint("raftBeforeAdvance", `sleep(100)`))

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	// member promote request can be sent to any server in cluster,
	// the request will be auto-forwarded to leader on server-side.
	// This test explicitly includes the server-side forwarding by
	// sending the request to follower.
	leaderIdx := clus.WaitLeader(t)
	followerIdx := (leaderIdx + 1) % 3
	cli := clus.Client(followerIdx)

	resp, err := cli.MemberList(t.Context())
	if err != nil {
		t.Fatalf("failed to list member %v", err)
	}
	if len(resp.Members) != 3 {
		t.Fatalf("number of members = %d, want %d", len(resp.Members), 3)
	}

	// promoting any of the voting members in cluster should fail
	expectedErrKeywords := "can only promote a learner member"
	for _, m := range resp.Members {
		_, err = cli.MemberPromote(t.Context(), m.ID)
		if err == nil {
			t.Fatalf("expect promoting voting member to fail, got no error")
		}
		if !strings.Contains(err.Error(), expectedErrKeywords) {
			t.Fatalf("expect error to contain %s, got %s", expectedErrKeywords, err.Error())
		}
	}
}

// TestMemberPromoteMemberNotExist ensures that promoting a member that does not exist in cluster fails.
func TestMemberPromoteMemberNotExist(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	// member promote request can be sent to any server in cluster,
	// the request will be auto-forwarded to leader on server-side.
	// This test explicitly includes the server-side forwarding by
	// sending the request to follower.
	leaderIdx := clus.WaitLeader(t)
	followerIdx := (leaderIdx + 1) % 3
	cli := clus.Client(followerIdx)

	resp, err := cli.MemberList(t.Context())
	if err != nil {
		t.Fatalf("failed to list member %v", err)
	}
	if len(resp.Members) != 3 {
		t.Fatalf("number of members = %d, want %d", len(resp.Members), 3)
	}

	// generate an random ID that does not exist in cluster
	var randID uint64
	for {
		randID = rand.Uint64()
		notExist := true
		for _, m := range resp.Members {
			if m.ID == randID {
				notExist = false
				break
			}
		}
		if notExist {
			break
		}
	}

	expectedErrKeywords := "member not found"
	_, err = cli.MemberPromote(t.Context(), randID)
	if err == nil {
		t.Fatalf("expect promoting voting member to fail, got no error")
	}
	if !strings.Contains(err.Error(), expectedErrKeywords) {
		t.Errorf("expect error to contain %s, got %s", expectedErrKeywords, err.Error())
	}
}

// TestMaxLearnerInCluster verifies that the maximum number of learners allowed in a cluster
func TestMaxLearnerInCluster(t *testing.T) {
	integration.BeforeTest(t, integration.WithFailpoint("raftBeforeAdvance", `sleep(100)`))

	// 1. start with a cluster with 3 voting member and max learner 2
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3, MaxLearners: 2, DisableStrictReconfigCheck: true})
	defer clus.Terminate(t)

	// 2. adding 2 learner members should succeed
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		_, err := clus.Client(0).MemberAddAsLearner(ctx, []string{fmt.Sprintf("http://127.0.0.1:123%d", i)})
		cancel()
		if err != nil {
			t.Fatalf("failed to add learner member %v", err)
		}
	}

	// ensure client endpoint is voting member
	leaderIdx := clus.WaitLeader(t)
	capi := clus.Client(leaderIdx)
	resp1, err := capi.MemberList(t.Context())
	if err != nil {
		t.Fatalf("failed to get member list")
	}
	numberOfLearners := 0
	for _, m := range resp1.Members {
		if m.IsLearner {
			numberOfLearners++
		}
	}
	if numberOfLearners != 2 {
		t.Fatalf("added 2 learner node to cluster, got %d", numberOfLearners)
	}

	// 3. cluster has 3 voting member and 2 learner, adding another learner should fail
	_, err = clus.Client(0).MemberAddAsLearner(t.Context(), []string{"http://127.0.0.1:2342"})
	if err == nil {
		t.Fatalf("expect member add to fail, got no error")
	}
	expectedErrKeywords := "too many learner members in cluster"
	if !strings.Contains(err.Error(), expectedErrKeywords) {
		t.Fatalf("expecting error to contain %s, got %s", expectedErrKeywords, err.Error())
	}

	// 4. cluster has 3 voting member and 1 learner, adding a voting member should succeed
	_, err = clus.Client(0).MemberAdd(t.Context(), []string{"http://127.0.0.1:3453"})
	if err != nil {
		t.Errorf("failed to add member %v", err)
	}
}

func getMemberByID(ctx context.Context, cli *clientv3.Client, id uint64) (member *etcdserverpb.Member, err error) {
	var resp *clientv3.MemberListResponse
	resp, err = cli.MemberList(ctx)
	if err != nil {
		return member, err
	}

	for _, m := range resp.Members {
		if m.ID == id {
			member = m
			break
		}
	}

	if member == nil {
		err = fmt.Errorf("failed to find member by id %d", id)
	}

	return member, err
}
