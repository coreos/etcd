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
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/pkg/v3/testutil"
	"go.etcd.io/etcd/pkg/v3/types"
	"go.etcd.io/etcd/tests/v3/integration"
)

func TestMemberList(t *testing.T) {
	defer testutil.AfterTest(t)

	clus := integration.NewClusterV3(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	capi := clus.RandClient()

	resp, err := capi.MemberList(context.Background())
	if err != nil {
		t.Fatalf("failed to list member %v", err)
	}

	if len(resp.Members) != 3 {
		t.Errorf("number of members = %d, want %d", len(resp.Members), 3)
	}
}

func TestMemberAdd(t *testing.T) {
	defer testutil.AfterTest(t)

	clus := integration.NewClusterV3(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	capi := clus.RandClient()

	urls := []string{"http://127.0.0.1:1234"}
	resp, err := capi.MemberAdd(context.Background(), urls)
	if err != nil {
		t.Fatalf("failed to add member %v", err)
	}

	if !reflect.DeepEqual(resp.Member.PeerURLs, urls) {
		t.Errorf("urls = %v, want %v", urls, resp.Member.PeerURLs)
	}
}

func TestMemberAddWithExistingURLs(t *testing.T) {
	defer testutil.AfterTest(t)

	clus := integration.NewClusterV3(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	capi := clus.RandClient()

	resp, err := capi.MemberList(context.Background())
	if err != nil {
		t.Fatalf("failed to list member %v", err)
	}

	existingURL := resp.Members[0].PeerURLs[0]
	_, err = capi.MemberAdd(context.Background(), []string{existingURL})
	expectedErrKeywords := "Peer URLs already exists"
	if err == nil {
		t.Fatalf("expecting add member to fail, got no error")
	}
	if !strings.Contains(err.Error(), expectedErrKeywords) {
		t.Errorf("expecting error to contain %s, got %s", expectedErrKeywords, err.Error())
	}
}

func TestMemberRemove(t *testing.T) {
	defer testutil.AfterTest(t)

	clus := integration.NewClusterV3(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	capi := clus.Client(1)
	resp, err := capi.MemberList(context.Background())
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

	_, err = capi.MemberRemove(context.Background(), rmvID)
	if err != nil {
		t.Fatalf("failed to remove member %v", err)
	}

	resp, err = capi.MemberList(context.Background())
	if err != nil {
		t.Fatalf("failed to list member %v", err)
	}

	if len(resp.Members) != 2 {
		t.Errorf("number of members = %d, want %d", len(resp.Members), 2)
	}
}

func TestMemberUpdate(t *testing.T) {
	defer testutil.AfterTest(t)

	clus := integration.NewClusterV3(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	capi := clus.RandClient()
	resp, err := capi.MemberList(context.Background())
	if err != nil {
		t.Fatalf("failed to list member %v", err)
	}

	urls := []string{"http://127.0.0.1:1234"}
	_, err = capi.MemberUpdate(context.Background(), resp.Members[0].ID, urls)
	if err != nil {
		t.Fatalf("failed to update member %v", err)
	}

	resp, err = capi.MemberList(context.Background())
	if err != nil {
		t.Fatalf("failed to list member %v", err)
	}

	if !reflect.DeepEqual(resp.Members[0].PeerURLs, urls) {
		t.Errorf("urls = %v, want %v", urls, resp.Members[0].PeerURLs)
	}
}

func TestMemberAddUpdateWrongURLs(t *testing.T) {
	defer testutil.AfterTest(t)

	clus := integration.NewClusterV3(t, &integration.ClusterConfig{Size: 1})
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
		_, err := capi.MemberAdd(context.Background(), tt[i])
		if err == nil {
			t.Errorf("#%d: MemberAdd err = nil, but error", i)
		}
		_, err = capi.MemberUpdate(context.Background(), 0, tt[i])
		if err == nil {
			t.Errorf("#%d: MemberUpdate err = nil, but error", i)
		}
	}
}

func TestMemberAddForLearner(t *testing.T) {
	defer testutil.AfterTest(t)

	clus := integration.NewClusterV3(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	capi := clus.RandClient()

	urls := []string{"http://127.0.0.1:1234"}
	resp, err := capi.MemberAddAsLearner(context.Background(), urls)
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

func TestMemberAddForAutoPromotee(t *testing.T) {
	defer testutil.AfterTest(t)

	clus := integration.NewClusterV3(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	capi := clus.RandClient()

	urls := []string{"http://127.0.0.1:1234"}
	rules := []clientv3.MemberPromoteRule{
		{
			Auto: true,
			Monitors: []*pb.MemberMonitor{
				{
					Op:        pb.MemberMonitor_GREATER_EQUAL,
					Type:      pb.MemberMonitor_PROGRESS,
					Threshold: 50,
					Delay:     0,
				},
			},
		},
	}
	resp, err := capi.MemberAddAsLearnerWithPromoteRules(context.Background(), urls, rules)
	if err != nil {
		t.Fatalf("failed to add member %v", err)
	}

	if !resp.Member.IsLearner {
		t.Errorf("Added a member as auto-promoting node, got resp.Member.IsLearner = %v", resp.Member.IsLearner)
	}

	numberOfLearners := 0
	for _, m := range resp.Members {
		if m.IsLearner {
			numberOfLearners++
		}
	}
	if numberOfLearners != 1 {
		t.Errorf("Added 1 auto-promoting learner to cluster, got %d", numberOfLearners)
	}
}

func TestMemberPromote(t *testing.T) {
	defer testutil.AfterTest(t)

	clus := integration.NewClusterV3(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	// member promote request can be sent to any server in cluster,
	// the request will be auto-forwarded to leader on server-side.
	// This test explicitly includes the server-side forwarding by
	// sending the request to follower.
	leaderIdx := clus.WaitLeader(t)
	followerIdx := (leaderIdx + 1) % 3
	capi := clus.Client(followerIdx)

	urls := []string{"http://127.0.0.1:1234"}
	memberAddResp, err := capi.MemberAddAsLearner(context.Background(), urls)
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
	_, err = capi.MemberPromote(context.Background(), learnerID)
	expectedErrKeywords := "can only promote a learner member which is in sync with leader"
	if err == nil {
		t.Fatalf("expecting promote not ready learner to fail, got no error")
	}
	if !strings.Contains(err.Error(), expectedErrKeywords) {
		t.Fatalf("expecting error to contain %s, got %s", expectedErrKeywords, err.Error())
	}

	// create and launch learner member based on the response of V3 Member Add API.
	// (the response has information on peer urls of the existing members in cluster)
	learnerMember := clus.MustNewMember(t, memberAddResp)
	clus.Members = append(clus.Members, learnerMember)
	if err := learnerMember.Launch(); err != nil {
		t.Fatal(err)
	}

	// retry until promote succeed or timeout
	timeout := time.After(5 * time.Second)
	for {
		select {
		case <-time.After(500 * time.Millisecond):
		case <-timeout:
			t.Fatalf("failed all attempts to promote learner member, last error: %v", err)
		}

		_, err = capi.MemberPromote(context.Background(), learnerID)
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

// TestMemberPromoteMemberNotLearner ensures that promoting a voting member fails.
func TestMemberPromoteMemberNotLearner(t *testing.T) {
	defer testutil.AfterTest(t)

	clus := integration.NewClusterV3(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	// member promote request can be sent to any server in cluster,
	// the request will be auto-forwarded to leader on server-side.
	// This test explicitly includes the server-side forwarding by
	// sending the request to follower.
	leaderIdx := clus.WaitLeader(t)
	followerIdx := (leaderIdx + 1) % 3
	cli := clus.Client(followerIdx)

	resp, err := cli.MemberList(context.Background())
	if err != nil {
		t.Fatalf("failed to list member %v", err)
	}
	if len(resp.Members) != 3 {
		t.Fatalf("number of members = %d, want %d", len(resp.Members), 3)
	}

	// promoting any of the voting members in cluster should fail
	expectedErrKeywords := "can only promote a learner member"
	for _, m := range resp.Members {
		_, err = cli.MemberPromote(context.Background(), m.ID)
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
	defer testutil.AfterTest(t)

	clus := integration.NewClusterV3(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	// member promote request can be sent to any server in cluster,
	// the request will be auto-forwarded to leader on server-side.
	// This test explicitly includes the server-side forwarding by
	// sending the request to follower.
	leaderIdx := clus.WaitLeader(t)
	followerIdx := (leaderIdx + 1) % 3
	cli := clus.Client(followerIdx)

	resp, err := cli.MemberList(context.Background())
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
	_, err = cli.MemberPromote(context.Background(), randID)
	if err == nil {
		t.Fatalf("expect promoting voting member to fail, got no error")
	}
	if !strings.Contains(err.Error(), expectedErrKeywords) {
		t.Errorf("expect error to contain %s, got %s", expectedErrKeywords, err.Error())
	}
}

// Test that auto-promoting learners are automatically promoted to voters
// upon catching up with the leader.
func TestMemberAutoPromotion(t *testing.T) {
	defer testutil.AfterTest(t)

	clus := integration.NewClusterV3(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	// add auto-promoting member request can be sent to any server
	// in cluster, the request will be auto-forwarded to leader on
	// server-side. This test explicitly includes the server-side
	// forwarding by sending the request to follower.
	leaderIdx := clus.WaitLeader(t)
	followerIdx := (leaderIdx + 1) % 3
	capi := clus.Client(followerIdx)

	urls := []string{"http://127.0.0.1:1234"}
	rules := []clientv3.MemberPromoteRule{
		{
			Auto: true,
			Monitors: []*pb.MemberMonitor{
				{
					Op:        pb.MemberMonitor_GREATER_EQUAL,
					Type:      pb.MemberMonitor_PROGRESS,
					Threshold: 50,
					Delay:     0,
				},
			},
		},
	}
	memberAddResp, err := capi.MemberAddAsLearnerWithPromoteRules(context.Background(), urls, rules)
	if err != nil {
		t.Fatalf("failed to add auto-promoting member %v", err)
	}

	if !memberAddResp.Member.IsLearner {
		t.Fatalf("Added a member as auto-promoting learner, got resp.Member.IsLearner = %v", memberAddResp.Member.IsLearner)
	}
	learnerID := memberAddResp.Member.ID

	numberOfLearners := 0
	for _, m := range memberAddResp.Members {
		if m.IsLearner {
			numberOfLearners++
		}
	}
	if numberOfLearners != 1 {
		t.Fatalf("Added 1 (auto-promoting) learner node to cluster, got %d learners", numberOfLearners)
	}

	// Verify that cluster stored new member as learner
	foundMember := false
	if memberList, err := capi.MemberList(context.Background()); err != nil {
		t.Errorf("before launching member, failed to get member list: %v", err)
	} else {
		for _, member := range memberList.Members {
			if member.ID == learnerID {
				foundMember = true
				if member.IsLearner == false {
					t.Errorf("expected new member %v to be stored as learner, but is stored as voter", learnerID)
				}
				if len(member.PromoteRules) == 0 {
					t.Errorf("expected new member %v to have 1 promote rules, but had 0", learnerID)
				}
			}
		}
	}
	if foundMember == false {
		t.Errorf("expected to find new member %v in member list, but new member was not found", learnerID)
	}

	// create and launch auto-promoting learner member based on the response of V3 Member Add API.
	// (the response has information on peer urls of the existing members in cluster)
	learnerMember := clus.MustNewMember(t, memberAddResp)
	clus.Members = append(clus.Members, learnerMember)
	if err := learnerMember.Launch(); err != nil {
		t.Fatal(err)
	}

	// retry until auto-promote success or timeout
	timeout := time.After(5 * time.Second)
	for {
		select {
		case <-time.After(500 * time.Millisecond):
		case <-timeout:
			t.Errorf("auto-promoting learner member was not promoted, last error: %v", err)
			break
		}

		if memberList, err := capi.MemberList(context.Background()); err != nil {
			t.Errorf("while waiting for member auto-promote, failed to get member list: %v", err)
		} else {
			success := false
			for _, member := range memberList.Members {
				if member.ID == learnerID {
					if member.IsLearner == false {
						success = true
					}
				}
			}
			if success {
				break
			}
		}
	}
}

// TestMaxLearnerInCluster verifies that the maximum number of learners allowed in a cluster is 1
func TestMaxLearnerInCluster(t *testing.T) {
	defer testutil.AfterTest(t)

	// 1. start with a cluster with 3 voting member and 0 learner member
	clus := integration.NewClusterV3(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	// 2. adding a learner member should succeed
	resp1, err := clus.Client(0).MemberAddAsLearner(context.Background(), []string{"http://127.0.0.1:1234"})
	if err != nil {
		t.Fatalf("failed to add learner member %v", err)
	}
	numberOfLearners := 0
	for _, m := range resp1.Members {
		if m.IsLearner {
			numberOfLearners++
		}
	}
	if numberOfLearners != 1 {
		t.Fatalf("Added 1 learner node to cluster, got %d", numberOfLearners)
	}

	// 3. cluster has 3 voting member and 1 learner, adding another learner should fail
	_, err = clus.Client(0).MemberAddAsLearner(context.Background(), []string{"http://127.0.0.1:2345"})
	if err == nil {
		t.Fatalf("expect member add to fail, got no error")
	}
	expectedErrKeywords := "too many learner members in cluster"
	if !strings.Contains(err.Error(), expectedErrKeywords) {
		t.Fatalf("expecting error to contain %s, got %s", expectedErrKeywords, err.Error())
	}

	// 4. cluster has 3 voting member and 1 learner, adding a voting member should succeed
	_, err = clus.Client(0).MemberAdd(context.Background(), []string{"http://127.0.0.1:3456"})
	if err != nil {
		t.Errorf("failed to add member %v", err)
	}
}
