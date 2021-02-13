// Copyright 2015 The etcd Authors
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

package raft

import (
	"bytes"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	pb "go.etcd.io/etcd/raft/v3/raftpb"
	"go.etcd.io/etcd/raft/v3/tracker"
)

// nextEnts returns the appliable entries and updates the applied index
func nextEnts(r *raft, s *MemoryStorage) (ents []pb.Entry) {
	// Transfer all unstable entries to "stable" storage.
	s.Append(r.raftLog.unstableEntries())
	r.raftLog.stableTo(r.raftLog.lastIndex(), r.raftLog.lastTerm())

	ents = r.raftLog.nextEnts()
	r.raftLog.appliedTo(r.raftLog.committed)
	return ents
}

func mustAppendEntry(r *raft, ents ...pb.Entry) {
	if !r.appendEntry(ents...) {
		panic("entry unexpectedly dropped")
	}
}

type stateMachine interface {
	Step(m pb.Message) error
	readMessages() []pb.Message
}

func (r *raft) readMessages() []pb.Message {
	msgs := r.msgs
	r.msgs = make([]pb.Message, 0)

	return msgs
}

func TestProgressLeader(t *testing.T) {
	r := newTestRaft(1, 5, 1, newTestMemoryStorage(withPeers(1, 2)))
	r.becomeCandidate()
	r.becomeLeader()
	r.prs.Progress[2].BecomeReplicate()

	// Send proposals to r1. The first 5 entries should be appended to the log.
	propMsg := pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{Data: []byte("foo")}}}
	for i := 0; i < 5; i++ {
		if pr := r.prs.Progress[r.id]; pr.State != tracker.StateReplicate || pr.Match != uint64(i+1) || pr.Next != pr.Match+1 {
			t.Errorf("unexpected progress %v", pr)
		}
		if err := r.Step(propMsg); err != nil {
			t.Fatalf("proposal resulted in error: %v", err)
		}
	}
}

// TestProgressResumeByHeartbeatResp ensures raft.heartbeat reset progress.paused by heartbeat response.
func TestProgressResumeByHeartbeatResp(t *testing.T) {
	r := newTestRaft(1, 5, 1, newTestMemoryStorage(withPeers(1, 2)))
	r.becomeCandidate()
	r.becomeLeader()

	r.prs.Progress[2].ProbeSent = true

	r.Step(pb.Message{From: 1, To: 1, Type: pb.MsgBeat})
	if !r.prs.Progress[2].ProbeSent {
		t.Errorf("paused = %v, want true", r.prs.Progress[2].ProbeSent)
	}

	r.prs.Progress[2].BecomeReplicate()
	r.Step(pb.Message{From: 2, To: 1, Type: pb.MsgHeartbeatResp})
	if r.prs.Progress[2].ProbeSent {
		t.Errorf("paused = %v, want false", r.prs.Progress[2].ProbeSent)
	}
}

func TestProgressPaused(t *testing.T) {
	r := newTestRaft(1, 5, 1, newTestMemoryStorage(withPeers(1, 2)))
	r.becomeCandidate()
	r.becomeLeader()
	r.Step(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{Data: []byte("somedata")}}})
	r.Step(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{Data: []byte("somedata")}}})
	r.Step(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{Data: []byte("somedata")}}})

	ms := r.readMessages()
	if len(ms) != 1 {
		t.Errorf("len(ms) = %d, want 1", len(ms))
	}
}

func TestProgressFlowControl(t *testing.T) {
	cfg := newTestConfig(1, 5, 1, newTestMemoryStorage(withPeers(1, 2)))
	cfg.MaxInflightMsgs = 3
	cfg.MaxSizePerMsg = 2048
	r := newRaft(cfg)
	r.becomeCandidate()
	r.becomeLeader()

	// Throw away all the messages relating to the initial election.
	r.readMessages()

	// While node 2 is in probe state, propose a bunch of entries.
	r.prs.Progress[2].BecomeProbe()
	blob := []byte(strings.Repeat("a", 1000))
	for i := 0; i < 10; i++ {
		r.Step(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{Data: blob}}})
	}

	ms := r.readMessages()
	// First append has two entries: the empty entry to confirm the
	// election, and the first proposal (only one proposal gets sent
	// because we're in probe state).
	if len(ms) != 1 || ms[0].Type != pb.MsgApp {
		t.Fatalf("expected 1 MsgApp, got %v", ms)
	}
	if len(ms[0].Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(ms[0].Entries))
	}
	if len(ms[0].Entries[0].Data) != 0 || len(ms[0].Entries[1].Data) != 1000 {
		t.Fatalf("unexpected entry sizes: %v", ms[0].Entries)
	}

	// When this append is acked, we change to replicate state and can
	// send multiple messages at once.
	r.Step(pb.Message{From: 2, To: 1, Type: pb.MsgAppResp, Index: ms[0].Entries[1].Index})
	ms = r.readMessages()
	if len(ms) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(ms))
	}
	for i, m := range ms {
		if m.Type != pb.MsgApp {
			t.Errorf("%d: expected MsgApp, got %s", i, m.Type)
		}
		if len(m.Entries) != 2 {
			t.Errorf("%d: expected 2 entries, got %d", i, len(m.Entries))
		}
	}

	// Ack all three of those messages together and get the last two
	// messages (containing three entries).
	r.Step(pb.Message{From: 2, To: 1, Type: pb.MsgAppResp, Index: ms[2].Entries[1].Index})
	ms = r.readMessages()
	if len(ms) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(ms))
	}
	for i, m := range ms {
		if m.Type != pb.MsgApp {
			t.Errorf("%d: expected MsgApp, got %s", i, m.Type)
		}
	}
	if len(ms[0].Entries) != 2 {
		t.Errorf("%d: expected 2 entries, got %d", 0, len(ms[0].Entries))
	}
	if len(ms[1].Entries) != 1 {
		t.Errorf("%d: expected 1 entry, got %d", 1, len(ms[1].Entries))
	}
}

func TestUncommittedEntryLimit(t *testing.T) {
	// Use a relatively large number of entries here to prevent regression of a
	// bug which computed the size before it was fixed. This test would fail
	// with the bug, either because we'd get dropped proposals earlier than we
	// expect them, or because the final tally ends up nonzero. (At the time of
	// writing, the former).
	const maxEntries = 1024
	testEntry := pb.Entry{Data: []byte("testdata")}
	maxEntrySize := maxEntries * PayloadSize(testEntry)

	if n := PayloadSize(pb.Entry{Data: nil}); n != 0 {
		t.Fatal("entry with no Data must have zero payload size")
	}

	cfg := newTestConfig(1, 5, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
	cfg.MaxUncommittedEntriesSize = uint64(maxEntrySize)
	cfg.MaxInflightMsgs = 2 * 1024 // avoid interference
	r := newRaft(cfg)
	r.becomeCandidate()
	r.becomeLeader()
	if n := r.uncommittedSize; n != 0 {
		t.Fatalf("expected zero uncommitted size, got %d bytes", n)
	}

	// Set the two followers to the replicate state. Commit to tail of log.
	const numFollowers = 2
	r.prs.Progress[2].BecomeReplicate()
	r.prs.Progress[3].BecomeReplicate()
	r.uncommittedSize = 0

	// Send proposals to r1. The first 5 entries should be appended to the log.
	propMsg := pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{testEntry}}
	propEnts := make([]pb.Entry, maxEntries)
	for i := 0; i < maxEntries; i++ {
		if err := r.Step(propMsg); err != nil {
			t.Fatalf("proposal resulted in error: %v", err)
		}
		propEnts[i] = testEntry
	}

	// Send one more proposal to r1. It should be rejected.
	if err := r.Step(propMsg); err != ErrProposalDropped {
		t.Fatalf("proposal not dropped: %v", err)
	}

	// Read messages and reduce the uncommitted size as if we had committed
	// these entries.
	ms := r.readMessages()
	if e := maxEntries * numFollowers; len(ms) != e {
		t.Fatalf("expected %d messages, got %d", e, len(ms))
	}
	r.reduceUncommittedSize(propEnts)
	if r.uncommittedSize != 0 {
		t.Fatalf("committed everything, but still tracking %d", r.uncommittedSize)
	}

	// Send a single large proposal to r1. Should be accepted even though it
	// pushes us above the limit because we were beneath it before the proposal.
	propEnts = make([]pb.Entry, 2*maxEntries)
	for i := range propEnts {
		propEnts[i] = testEntry
	}
	propMsgLarge := pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: propEnts}
	if err := r.Step(propMsgLarge); err != nil {
		t.Fatalf("proposal resulted in error: %v", err)
	}

	// Send one more proposal to r1. It should be rejected, again.
	if err := r.Step(propMsg); err != ErrProposalDropped {
		t.Fatalf("proposal not dropped: %v", err)
	}

	// But we can always append an entry with no Data. This is used both for the
	// leader's first empty entry and for auto-transitioning out of joint config
	// states.
	if err := r.Step(
		pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{}}},
	); err != nil {
		t.Fatal(err)
	}

	// Read messages and reduce the uncommitted size as if we had committed
	// these entries.
	ms = r.readMessages()
	if e := 2 * numFollowers; len(ms) != e {
		t.Fatalf("expected %d messages, got %d", e, len(ms))
	}
	r.reduceUncommittedSize(propEnts)
	if n := r.uncommittedSize; n != 0 {
		t.Fatalf("expected zero uncommitted size, got %d", n)
	}
}

func TestLeaderElection(t *testing.T) {
	testLeaderElection(t, false)
}

func TestLeaderElectionPreVote(t *testing.T) {
	testLeaderElection(t, true)
}

func testLeaderElection(t *testing.T, preVote bool) {
	var cfg func(*Config)
	candState := StateCandidate
	candTerm := uint64(1)
	if preVote {
		cfg = preVoteConfig
		// In pre-vote mode, an election that fails to complete
		// leaves the node in pre-candidate state without advancing
		// the term.
		candState = StatePreCandidate
		candTerm = 0
	}
	tests := []struct {
		*network
		state   StateType
		expTerm uint64
	}{
		{newNetworkWithConfig(cfg, nil, nil, nil), StateLeader, 1},
		{newNetworkWithConfig(cfg, nil, nil, nopStepper), StateLeader, 1},
		{newNetworkWithConfig(cfg, nil, nopStepper, nopStepper), candState, candTerm},
		{newNetworkWithConfig(cfg, nil, nopStepper, nopStepper, nil), candState, candTerm},
		{newNetworkWithConfig(cfg, nil, nopStepper, nopStepper, nil, nil), StateLeader, 1},

		// three logs further along than 0, but in the same term so rejections
		// are returned instead of the votes being ignored.
		{newNetworkWithConfig(cfg,
			nil, entsWithConfig(cfg, 1), entsWithConfig(cfg, 1), entsWithConfig(cfg, 1, 1), nil),
			StateFollower, 1},
	}

	for i, tt := range tests {
		tt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})
		sm := tt.network.peers[1].(*raft)
		if sm.state != tt.state {
			t.Errorf("#%d: state = %s, want %s", i, sm.state, tt.state)
		}
		if g := sm.Term; g != tt.expTerm {
			t.Errorf("#%d: term = %d, want %d", i, g, tt.expTerm)
		}
	}
}

// TestLearnerElectionTimeout verfies that the leader should not start election even
// when times out.
func TestLearnerElectionTimeout(t *testing.T) {
	n1 := newTestLearnerRaft(1, 10, 1, newTestMemoryStorage(withPeers(1), withLearners(2)))
	n2 := newTestLearnerRaft(2, 10, 1, newTestMemoryStorage(withPeers(1), withLearners(2)))

	n1.becomeFollower(1, None)
	n2.becomeFollower(1, None)

	// n2 is learner. Learner should not start election even when times out.
	setRandomizedElectionTimeout(n2, n2.electionTimeout)
	for i := 0; i < n2.electionTimeout; i++ {
		n2.tick()
	}

	if n2.state != StateFollower {
		t.Errorf("peer 2 state: %s, want %s", n2.state, StateFollower)
	}
}

// TestLearnerPromotion verifies that the learner should not election until
// it is promoted to a normal peer.
func TestLearnerPromotion(t *testing.T) {
	n1 := newTestLearnerRaft(1, 10, 1, newTestMemoryStorage(withPeers(1), withLearners(2)))
	n2 := newTestLearnerRaft(2, 10, 1, newTestMemoryStorage(withPeers(1), withLearners(2)))

	n1.becomeFollower(1, None)
	n2.becomeFollower(1, None)

	nt := newNetwork(n1, n2)

	if n1.state == StateLeader {
		t.Error("peer 1 state is leader, want not", n1.state)
	}

	// n1 should become leader
	setRandomizedElectionTimeout(n1, n1.electionTimeout)
	for i := 0; i < n1.electionTimeout; i++ {
		n1.tick()
	}

	if n1.state != StateLeader {
		t.Errorf("peer 1 state: %s, want %s", n1.state, StateLeader)
	}
	if n2.state != StateFollower {
		t.Errorf("peer 2 state: %s, want %s", n2.state, StateFollower)
	}

	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgBeat})

	n1.applyConfChange(pb.ConfChange{NodeID: 2, Type: pb.ConfChangeAddNode}.AsV2())
	n2.applyConfChange(pb.ConfChange{NodeID: 2, Type: pb.ConfChangeAddNode}.AsV2())
	if n2.isLearner {
		t.Error("peer 2 is learner, want not")
	}

	// n2 start election, should become leader
	setRandomizedElectionTimeout(n2, n2.electionTimeout)
	for i := 0; i < n2.electionTimeout; i++ {
		n2.tick()
	}

	nt.send(pb.Message{From: 2, To: 2, Type: pb.MsgBeat})

	if n1.state != StateFollower {
		t.Errorf("peer 1 state: %s, want %s", n1.state, StateFollower)
	}
	if n2.state != StateLeader {
		t.Errorf("peer 2 state: %s, want %s", n2.state, StateLeader)
	}
}

// TestLearnerCanVote checks that a learner can vote when it receives a valid Vote request.
// See (*raft).Step for why this is necessary and correct behavior.
func TestLearnerCanVote(t *testing.T) {
	n2 := newTestLearnerRaft(2, 10, 1, newTestMemoryStorage(withPeers(1), withLearners(2)))

	n2.becomeFollower(1, None)

	n2.Step(pb.Message{From: 1, To: 2, Term: 2, Type: pb.MsgVote, LogTerm: 11, Index: 11})

	if len(n2.msgs) != 1 {
		t.Fatalf("expected exactly one message, not %+v", n2.msgs)
	}
	msg := n2.msgs[0]
	if msg.Type != pb.MsgVoteResp && !msg.Reject {
		t.Fatal("expected learner to not reject vote")
	}
}

func TestLeaderCycle(t *testing.T) {
	testLeaderCycle(t, false)
}

func TestLeaderCyclePreVote(t *testing.T) {
	testLeaderCycle(t, true)
}

// testLeaderCycle verifies that each node in a cluster can campaign
// and be elected in turn. This ensures that elections (including
// pre-vote) work when not starting from a clean slate (as they do in
// TestLeaderElection)
func testLeaderCycle(t *testing.T, preVote bool) {
	var cfg func(*Config)
	if preVote {
		cfg = preVoteConfig
	}
	n := newNetworkWithConfig(cfg, nil, nil, nil)
	for campaignerID := uint64(1); campaignerID <= 3; campaignerID++ {
		n.send(pb.Message{From: campaignerID, To: campaignerID, Type: pb.MsgHup})

		for _, peer := range n.peers {
			sm := peer.(*raft)
			if sm.id == campaignerID && sm.state != StateLeader {
				t.Errorf("preVote=%v: campaigning node %d state = %v, want StateLeader",
					preVote, sm.id, sm.state)
			} else if sm.id != campaignerID && sm.state != StateFollower {
				t.Errorf("preVote=%v: after campaign of node %d, "+
					"node %d had state = %v, want StateFollower",
					preVote, campaignerID, sm.id, sm.state)
			}
		}
	}
}

// TestLeaderElectionOverwriteNewerLogs tests a scenario in which a
// newly-elected leader does *not* have the newest (i.e. highest term)
// log entries, and must overwrite higher-term log entries with
// lower-term ones.
func TestLeaderElectionOverwriteNewerLogs(t *testing.T) {
	testLeaderElectionOverwriteNewerLogs(t, false)
}

func TestLeaderElectionOverwriteNewerLogsPreVote(t *testing.T) {
	testLeaderElectionOverwriteNewerLogs(t, true)
}

func testLeaderElectionOverwriteNewerLogs(t *testing.T, preVote bool) {
	var cfg func(*Config)
	if preVote {
		cfg = preVoteConfig
	}
	// This network represents the results of the following sequence of
	// events:
	// - Node 1 won the election in term 1.
	// - Node 1 replicated a log entry to node 2 but died before sending
	//   it to other nodes.
	// - Node 3 won the second election in term 2.
	// - Node 3 wrote an entry to its logs but died without sending it
	//   to any other nodes.
	//
	// At this point, nodes 1, 2, and 3 all have uncommitted entries in
	// their logs and could win an election at term 3. The winner's log
	// entry overwrites the losers'. (TestLeaderSyncFollowerLog tests
	// the case where older log entries are overwritten, so this test
	// focuses on the case where the newer entries are lost).
	n := newNetworkWithConfig(cfg,
		entsWithConfig(cfg, 1),     // Node 1: Won first election
		entsWithConfig(cfg, 1),     // Node 2: Got logs from node 1
		entsWithConfig(cfg, 2),     // Node 3: Won second election
		votedWithConfig(cfg, 3, 2), // Node 4: Voted but didn't get logs
		votedWithConfig(cfg, 3, 2)) // Node 5: Voted but didn't get logs

	// Node 1 campaigns. The election fails because a quorum of nodes
	// know about the election that already happened at term 2. Node 1's
	// term is pushed ahead to 2.
	n.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})
	sm1 := n.peers[1].(*raft)
	if sm1.state != StateFollower {
		t.Errorf("state = %s, want StateFollower", sm1.state)
	}
	if sm1.Term != 2 {
		t.Errorf("term = %d, want 2", sm1.Term)
	}

	// Node 1 campaigns again with a higher term. This time it succeeds.
	n.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})
	if sm1.state != StateLeader {
		t.Errorf("state = %s, want StateLeader", sm1.state)
	}
	if sm1.Term != 3 {
		t.Errorf("term = %d, want 3", sm1.Term)
	}

	// Now all nodes agree on a log entry with term 1 at index 1 (and
	// term 3 at index 2).
	for i := range n.peers {
		sm := n.peers[i].(*raft)
		entries := sm.raftLog.allEntries()
		if len(entries) != 2 {
			t.Fatalf("node %d: len(entries) == %d, want 2", i, len(entries))
		}
		if entries[0].Term != 1 {
			t.Errorf("node %d: term at index 1 == %d, want 1", i, entries[0].Term)
		}
		if entries[1].Term != 3 {
			t.Errorf("node %d: term at index 2 == %d, want 3", i, entries[1].Term)
		}
	}
}

func TestVoteFromAnyState(t *testing.T) {
	testVoteFromAnyState(t, pb.MsgVote)
}

func TestPreVoteFromAnyState(t *testing.T) {
	testVoteFromAnyState(t, pb.MsgPreVote)
}

func testVoteFromAnyState(t *testing.T, vt pb.MessageType) {
	for st := StateType(0); st < numStates; st++ {
		r := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
		r.Term = 1

		switch st {
		case StateFollower:
			r.becomeFollower(r.Term, 3)
		case StatePreCandidate:
			r.becomePreCandidate()
		case StateCandidate:
			r.becomeCandidate()
		case StateLeader:
			r.becomeCandidate()
			r.becomeLeader()
		}

		// Note that setting our state above may have advanced r.Term
		// past its initial value.
		origTerm := r.Term
		newTerm := r.Term + 1

		msg := pb.Message{
			From:    2,
			To:      1,
			Type:    vt,
			Term:    newTerm,
			LogTerm: newTerm,
			Index:   42,
		}
		if err := r.Step(msg); err != nil {
			t.Errorf("%s,%s: Step failed: %s", vt, st, err)
		}
		if len(r.msgs) != 1 {
			t.Errorf("%s,%s: %d response messages, want 1: %+v", vt, st, len(r.msgs), r.msgs)
		} else {
			resp := r.msgs[0]
			if resp.Type != voteRespMsgType(vt) {
				t.Errorf("%s,%s: response message is %s, want %s",
					vt, st, resp.Type, voteRespMsgType(vt))
			}
			if resp.Reject {
				t.Errorf("%s,%s: unexpected rejection", vt, st)
			}
		}

		// If this was a real vote, we reset our state and term.
		if vt == pb.MsgVote {
			if r.state != StateFollower {
				t.Errorf("%s,%s: state %s, want %s", vt, st, r.state, StateFollower)
			}
			if r.Term != newTerm {
				t.Errorf("%s,%s: term %d, want %d", vt, st, r.Term, newTerm)
			}
			if r.Vote != 2 {
				t.Errorf("%s,%s: vote %d, want 2", vt, st, r.Vote)
			}
		} else {
			// In a prevote, nothing changes.
			if r.state != st {
				t.Errorf("%s,%s: state %s, want %s", vt, st, r.state, st)
			}
			if r.Term != origTerm {
				t.Errorf("%s,%s: term %d, want %d", vt, st, r.Term, origTerm)
			}
			// if st == StateFollower or StatePreCandidate, r hasn't voted yet.
			// In StateCandidate or StateLeader, it's voted for itself.
			if r.Vote != None && r.Vote != 1 {
				t.Errorf("%s,%s: vote %d, want %d or 1", vt, st, r.Vote, None)
			}
		}
	}
}

func TestLogReplication(t *testing.T) {
	tests := []struct {
		*network
		msgs       []pb.Message
		wcommitted uint64
	}{
		{
			newNetwork(nil, nil, nil),
			[]pb.Message{
				{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{Data: []byte("somedata")}}},
			},
			2,
		},
		{
			newNetwork(nil, nil, nil),
			[]pb.Message{
				{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{Data: []byte("somedata")}}},
				{From: 1, To: 2, Type: pb.MsgHup},
				{From: 1, To: 2, Type: pb.MsgProp, Entries: []pb.Entry{{Data: []byte("somedata")}}},
			},
			4,
		},
	}

	for i, tt := range tests {
		tt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

		for _, m := range tt.msgs {
			tt.send(m)
		}

		for j, x := range tt.network.peers {
			sm := x.(*raft)

			if sm.raftLog.committed != tt.wcommitted {
				t.Errorf("#%d.%d: committed = %d, want %d", i, j, sm.raftLog.committed, tt.wcommitted)
			}

			ents := []pb.Entry{}
			for _, e := range nextEnts(sm, tt.network.storage[j]) {
				if e.Data != nil {
					ents = append(ents, e)
				}
			}
			props := []pb.Message{}
			for _, m := range tt.msgs {
				if m.Type == pb.MsgProp {
					props = append(props, m)
				}
			}
			for k, m := range props {
				if !bytes.Equal(ents[k].Data, m.Entries[0].Data) {
					t.Errorf("#%d.%d: data = %d, want %d", i, j, ents[k].Data, m.Entries[0].Data)
				}
			}
		}
	}
}

// TestLearnerLogReplication tests that a learner can receive entries from the leader.
func TestLearnerLogReplication(t *testing.T) {
	n1 := newTestLearnerRaft(1, 10, 1, newTestMemoryStorage(withPeers(1), withLearners(2)))
	n2 := newTestLearnerRaft(2, 10, 1, newTestMemoryStorage(withPeers(1), withLearners(2)))

	nt := newNetwork(n1, n2)

	n1.becomeFollower(1, None)
	n2.becomeFollower(1, None)

	setRandomizedElectionTimeout(n1, n1.electionTimeout)
	for i := 0; i < n1.electionTimeout; i++ {
		n1.tick()
	}

	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgBeat})

	// n1 is leader and n2 is learner
	if n1.state != StateLeader {
		t.Errorf("peer 1 state: %s, want %s", n1.state, StateLeader)
	}
	if !n2.isLearner {
		t.Error("peer 2 state: not learner, want yes")
	}

	nextCommitted := n1.raftLog.committed + 1
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{Data: []byte("somedata")}}})
	if n1.raftLog.committed != nextCommitted {
		t.Errorf("peer 1 wants committed to %d, but still %d", nextCommitted, n1.raftLog.committed)
	}

	if n1.raftLog.committed != n2.raftLog.committed {
		t.Errorf("peer 2 wants committed to %d, but still %d", n1.raftLog.committed, n2.raftLog.committed)
	}

	match := n1.prs.Progress[2].Match
	if match != n2.raftLog.committed {
		t.Errorf("progress 2 of leader 1 wants match %d, but got %d", n2.raftLog.committed, match)
	}
}

func TestSingleNodeCommit(t *testing.T) {
	tt := newNetwork(nil)
	tt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})
	tt.send(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{Data: []byte("some data")}}})
	tt.send(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{Data: []byte("some data")}}})

	sm := tt.peers[1].(*raft)
	if sm.raftLog.committed != 3 {
		t.Errorf("committed = %d, want %d", sm.raftLog.committed, 3)
	}
}

// TestCannotCommitWithoutNewTermEntry tests the entries cannot be committed
// when leader changes, no new proposal comes in and ChangeTerm proposal is
// filtered.
func TestCannotCommitWithoutNewTermEntry(t *testing.T) {
	tt := newNetwork(nil, nil, nil, nil, nil)
	tt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	// 0 cannot reach 2,3,4
	tt.cut(1, 3)
	tt.cut(1, 4)
	tt.cut(1, 5)

	tt.send(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{Data: []byte("some data")}}})
	tt.send(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{Data: []byte("some data")}}})

	sm := tt.peers[1].(*raft)
	if sm.raftLog.committed != 1 {
		t.Errorf("committed = %d, want %d", sm.raftLog.committed, 1)
	}

	// network recovery
	tt.recover()
	// avoid committing ChangeTerm proposal
	tt.ignore(pb.MsgApp)

	// elect 2 as the new leader with term 2
	tt.send(pb.Message{From: 2, To: 2, Type: pb.MsgHup})

	// no log entries from previous term should be committed
	sm = tt.peers[2].(*raft)
	if sm.raftLog.committed != 1 {
		t.Errorf("committed = %d, want %d", sm.raftLog.committed, 1)
	}

	tt.recover()
	// send heartbeat; reset wait
	tt.send(pb.Message{From: 2, To: 2, Type: pb.MsgBeat})
	// append an entry at current term
	tt.send(pb.Message{From: 2, To: 2, Type: pb.MsgProp, Entries: []pb.Entry{{Data: []byte("some data")}}})
	// expect the committed to be advanced
	if sm.raftLog.committed != 5 {
		t.Errorf("committed = %d, want %d", sm.raftLog.committed, 5)
	}
}

// TestCommitWithoutNewTermEntry tests the entries could be committed
// when leader changes, no new proposal comes in.
func TestCommitWithoutNewTermEntry(t *testing.T) {
	tt := newNetwork(nil, nil, nil, nil, nil)
	tt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	// 0 cannot reach 2,3,4
	tt.cut(1, 3)
	tt.cut(1, 4)
	tt.cut(1, 5)

	tt.send(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{Data: []byte("some data")}}})
	tt.send(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{Data: []byte("some data")}}})

	sm := tt.peers[1].(*raft)
	if sm.raftLog.committed != 1 {
		t.Errorf("committed = %d, want %d", sm.raftLog.committed, 1)
	}

	// network recovery
	tt.recover()

	// elect 2 as the new leader with term 2
	// after append a ChangeTerm entry from the current term, all entries
	// should be committed
	tt.send(pb.Message{From: 2, To: 2, Type: pb.MsgHup})

	if sm.raftLog.committed != 4 {
		t.Errorf("committed = %d, want %d", sm.raftLog.committed, 4)
	}
}

func TestDuelingCandidates(t *testing.T) {
	a := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
	b := newTestRaft(2, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
	c := newTestRaft(3, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))

	nt := newNetwork(a, b, c)
	nt.cut(1, 3)

	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})
	nt.send(pb.Message{From: 3, To: 3, Type: pb.MsgHup})

	// 1 becomes leader since it receives votes from 1 and 2
	sm := nt.peers[1].(*raft)
	if sm.state != StateLeader {
		t.Errorf("state = %s, want %s", sm.state, StateLeader)
	}

	// 3 stays as candidate since it receives a vote from 3 and a rejection from 2
	sm = nt.peers[3].(*raft)
	if sm.state != StateCandidate {
		t.Errorf("state = %s, want %s", sm.state, StateCandidate)
	}

	nt.recover()

	// candidate 3 now increases its term and tries to vote again
	// we expect it to disrupt the leader 1 since it has a higher term
	// 3 will be follower again since both 1 and 2 rejects its vote request since 3 does not have a long enough log
	nt.send(pb.Message{From: 3, To: 3, Type: pb.MsgHup})

	wlog := &raftLog{
		storage:   &MemoryStorage{ents: []pb.Entry{{}, {Data: nil, Term: 1, Index: 1}}},
		committed: 1,
		unstable:  unstable{offset: 2},
	}
	tests := []struct {
		sm      *raft
		state   StateType
		term    uint64
		raftLog *raftLog
	}{
		{a, StateFollower, 2, wlog},
		{b, StateFollower, 2, wlog},
		{c, StateFollower, 2, newLog(NewMemoryStorage(), raftLogger)},
	}

	for i, tt := range tests {
		if g := tt.sm.state; g != tt.state {
			t.Errorf("#%d: state = %s, want %s", i, g, tt.state)
		}
		if g := tt.sm.Term; g != tt.term {
			t.Errorf("#%d: term = %d, want %d", i, g, tt.term)
		}
		base := ltoa(tt.raftLog)
		if sm, ok := nt.peers[1+uint64(i)].(*raft); ok {
			l := ltoa(sm.raftLog)
			if g := diffu(base, l); g != "" {
				t.Errorf("#%d: diff:\n%s", i, g)
			}
		} else {
			t.Logf("#%d: empty log", i)
		}
	}
}

func TestDuelingPreCandidates(t *testing.T) {
	cfgA := newTestConfig(1, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
	cfgB := newTestConfig(2, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
	cfgC := newTestConfig(3, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
	cfgA.PreVote = true
	cfgB.PreVote = true
	cfgC.PreVote = true
	a := newRaft(cfgA)
	b := newRaft(cfgB)
	c := newRaft(cfgC)

	nt := newNetwork(a, b, c)
	nt.cut(1, 3)

	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})
	nt.send(pb.Message{From: 3, To: 3, Type: pb.MsgHup})

	// 1 becomes leader since it receives votes from 1 and 2
	sm := nt.peers[1].(*raft)
	if sm.state != StateLeader {
		t.Errorf("state = %s, want %s", sm.state, StateLeader)
	}

	// 3 campaigns then reverts to follower when its PreVote is rejected
	sm = nt.peers[3].(*raft)
	if sm.state != StateFollower {
		t.Errorf("state = %s, want %s", sm.state, StateFollower)
	}

	nt.recover()

	// Candidate 3 now increases its term and tries to vote again.
	// With PreVote, it does not disrupt the leader.
	nt.send(pb.Message{From: 3, To: 3, Type: pb.MsgHup})

	wlog := &raftLog{
		storage:   &MemoryStorage{ents: []pb.Entry{{}, {Data: nil, Term: 1, Index: 1}}},
		committed: 1,
		unstable:  unstable{offset: 2},
	}
	tests := []struct {
		sm      *raft
		state   StateType
		term    uint64
		raftLog *raftLog
	}{
		{a, StateLeader, 1, wlog},
		{b, StateFollower, 1, wlog},
		{c, StateFollower, 1, newLog(NewMemoryStorage(), raftLogger)},
	}

	for i, tt := range tests {
		if g := tt.sm.state; g != tt.state {
			t.Errorf("#%d: state = %s, want %s", i, g, tt.state)
		}
		if g := tt.sm.Term; g != tt.term {
			t.Errorf("#%d: term = %d, want %d", i, g, tt.term)
		}
		base := ltoa(tt.raftLog)
		if sm, ok := nt.peers[1+uint64(i)].(*raft); ok {
			l := ltoa(sm.raftLog)
			if g := diffu(base, l); g != "" {
				t.Errorf("#%d: diff:\n%s", i, g)
			}
		} else {
			t.Logf("#%d: empty log", i)
		}
	}
}

func TestCandidateConcede(t *testing.T) {
	tt := newNetwork(nil, nil, nil)
	tt.isolate(1)

	tt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})
	tt.send(pb.Message{From: 3, To: 3, Type: pb.MsgHup})

	// heal the partition
	tt.recover()
	// send heartbeat; reset wait
	tt.send(pb.Message{From: 3, To: 3, Type: pb.MsgBeat})

	data := []byte("force follower")
	// send a proposal to 3 to flush out a MsgApp to 1
	tt.send(pb.Message{From: 3, To: 3, Type: pb.MsgProp, Entries: []pb.Entry{{Data: data}}})
	// send heartbeat; flush out commit
	tt.send(pb.Message{From: 3, To: 3, Type: pb.MsgBeat})

	a := tt.peers[1].(*raft)
	if g := a.state; g != StateFollower {
		t.Errorf("state = %s, want %s", g, StateFollower)
	}
	if g := a.Term; g != 1 {
		t.Errorf("term = %d, want %d", g, 1)
	}
	wantLog := ltoa(&raftLog{
		storage: &MemoryStorage{
			ents: []pb.Entry{{}, {Data: nil, Term: 1, Index: 1}, {Term: 1, Index: 2, Data: data}},
		},
		unstable:  unstable{offset: 3},
		committed: 2,
	})
	for i, p := range tt.peers {
		if sm, ok := p.(*raft); ok {
			l := ltoa(sm.raftLog)
			if g := diffu(wantLog, l); g != "" {
				t.Errorf("#%d: diff:\n%s", i, g)
			}
		} else {
			t.Logf("#%d: empty log", i)
		}
	}
}

func TestSingleNodeCandidate(t *testing.T) {
	tt := newNetwork(nil)
	tt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	sm := tt.peers[1].(*raft)
	if sm.state != StateLeader {
		t.Errorf("state = %d, want %d", sm.state, StateLeader)
	}
}

func TestSingleNodePreCandidate(t *testing.T) {
	tt := newNetworkWithConfig(preVoteConfig, nil)
	tt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	sm := tt.peers[1].(*raft)
	if sm.state != StateLeader {
		t.Errorf("state = %d, want %d", sm.state, StateLeader)
	}
}

func TestOldMessages(t *testing.T) {
	tt := newNetwork(nil, nil, nil)
	// make 0 leader @ term 3
	tt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})
	tt.send(pb.Message{From: 2, To: 2, Type: pb.MsgHup})
	tt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})
	// pretend we're an old leader trying to make progress; this entry is expected to be ignored.
	tt.send(pb.Message{From: 2, To: 1, Type: pb.MsgApp, Term: 2, Entries: []pb.Entry{{Index: 3, Term: 2}}})
	// commit a new entry
	tt.send(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{Data: []byte("somedata")}}})

	ilog := &raftLog{
		storage: &MemoryStorage{
			ents: []pb.Entry{
				{}, {Data: nil, Term: 1, Index: 1},
				{Data: nil, Term: 2, Index: 2}, {Data: nil, Term: 3, Index: 3},
				{Data: []byte("somedata"), Term: 3, Index: 4},
			},
		},
		unstable:  unstable{offset: 5},
		committed: 4,
	}
	base := ltoa(ilog)
	for i, p := range tt.peers {
		if sm, ok := p.(*raft); ok {
			l := ltoa(sm.raftLog)
			if g := diffu(base, l); g != "" {
				t.Errorf("#%d: diff:\n%s", i, g)
			}
		} else {
			t.Logf("#%d: empty log", i)
		}
	}
}

// TestOldMessagesReply - optimization - reply with new term.

func TestProposal(t *testing.T) {
	tests := []struct {
		*network
		success bool
	}{
		{newNetwork(nil, nil, nil), true},
		{newNetwork(nil, nil, nopStepper), true},
		{newNetwork(nil, nopStepper, nopStepper), false},
		{newNetwork(nil, nopStepper, nopStepper, nil), false},
		{newNetwork(nil, nopStepper, nopStepper, nil, nil), true},
	}

	for j, tt := range tests {
		send := func(m pb.Message) {
			defer func() {
				// only recover if we expect it to panic (success==false)
				if !tt.success {
					e := recover()
					if e != nil {
						t.Logf("#%d: err: %s", j, e)
					}
				}
			}()
			tt.send(m)
		}

		data := []byte("somedata")

		// promote 1 to become leader
		send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})
		send(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{Data: data}}})

		wantLog := newLog(NewMemoryStorage(), raftLogger)
		if tt.success {
			wantLog = &raftLog{
				storage: &MemoryStorage{
					ents: []pb.Entry{{}, {Data: nil, Term: 1, Index: 1}, {Term: 1, Index: 2, Data: data}},
				},
				unstable:  unstable{offset: 3},
				committed: 2}
		}
		base := ltoa(wantLog)
		for i, p := range tt.peers {
			if sm, ok := p.(*raft); ok {
				l := ltoa(sm.raftLog)
				if g := diffu(base, l); g != "" {
					t.Errorf("#%d: peer %d diff:\n%s", j, i, g)
				}
			} else {
				t.Logf("#%d: peer %d empty log", j, i)
			}
		}
		sm := tt.network.peers[1].(*raft)
		if g := sm.Term; g != 1 {
			t.Errorf("#%d: term = %d, want %d", j, g, 1)
		}
	}
}

func TestProposalByProxy(t *testing.T) {
	data := []byte("somedata")
	tests := []*network{
		newNetwork(nil, nil, nil),
		newNetwork(nil, nil, nopStepper),
	}

	for j, tt := range tests {
		// promote 0 the leader
		tt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

		// propose via follower
		tt.send(pb.Message{From: 2, To: 2, Type: pb.MsgProp, Entries: []pb.Entry{{Data: []byte("somedata")}}})

		wantLog := &raftLog{
			storage: &MemoryStorage{
				ents: []pb.Entry{{}, {Data: nil, Term: 1, Index: 1}, {Term: 1, Data: data, Index: 2}},
			},
			unstable:  unstable{offset: 3},
			committed: 2}
		base := ltoa(wantLog)
		for i, p := range tt.peers {
			if sm, ok := p.(*raft); ok {
				l := ltoa(sm.raftLog)
				if g := diffu(base, l); g != "" {
					t.Errorf("#%d: peer %d diff:\n%s", j, i, g)
				}
			} else {
				t.Logf("#%d: peer %d empty log", j, i)
			}
		}
		sm := tt.peers[1].(*raft)
		if g := sm.Term; g != 1 {
			t.Errorf("#%d: term = %d, want %d", j, g, 1)
		}
	}
}

func TestCommit(t *testing.T) {
	tests := []struct {
		matches []uint64
		logs    []pb.Entry
		smTerm  uint64
		w       uint64
	}{
		// single
		{[]uint64{1}, []pb.Entry{{Index: 1, Term: 1}}, 1, 1},
		{[]uint64{1}, []pb.Entry{{Index: 1, Term: 1}}, 2, 0},
		{[]uint64{2}, []pb.Entry{{Index: 1, Term: 1}, {Index: 2, Term: 2}}, 2, 2},
		{[]uint64{1}, []pb.Entry{{Index: 1, Term: 2}}, 2, 1},

		// odd
		{[]uint64{2, 1, 1}, []pb.Entry{{Index: 1, Term: 1}, {Index: 2, Term: 2}}, 1, 1},
		{[]uint64{2, 1, 1}, []pb.Entry{{Index: 1, Term: 1}, {Index: 2, Term: 1}}, 2, 0},
		{[]uint64{2, 1, 2}, []pb.Entry{{Index: 1, Term: 1}, {Index: 2, Term: 2}}, 2, 2},
		{[]uint64{2, 1, 2}, []pb.Entry{{Index: 1, Term: 1}, {Index: 2, Term: 1}}, 2, 0},

		// even
		{[]uint64{2, 1, 1, 1}, []pb.Entry{{Index: 1, Term: 1}, {Index: 2, Term: 2}}, 1, 1},
		{[]uint64{2, 1, 1, 1}, []pb.Entry{{Index: 1, Term: 1}, {Index: 2, Term: 1}}, 2, 0},
		{[]uint64{2, 1, 1, 2}, []pb.Entry{{Index: 1, Term: 1}, {Index: 2, Term: 2}}, 1, 1},
		{[]uint64{2, 1, 1, 2}, []pb.Entry{{Index: 1, Term: 1}, {Index: 2, Term: 1}}, 2, 0},
		{[]uint64{2, 1, 2, 2}, []pb.Entry{{Index: 1, Term: 1}, {Index: 2, Term: 2}}, 2, 2},
		{[]uint64{2, 1, 2, 2}, []pb.Entry{{Index: 1, Term: 1}, {Index: 2, Term: 1}}, 2, 0},
	}

	for i, tt := range tests {
		storage := newTestMemoryStorage(withPeers(1))
		storage.Append(tt.logs)
		storage.hardState = pb.HardState{Term: tt.smTerm}

		sm := newTestRaft(1, 10, 2, storage)
		for j := 0; j < len(tt.matches); j++ {
			id := uint64(j) + 1
			if id > 1 {
				sm.applyConfChange(pb.ConfChange{Type: pb.ConfChangeAddNode, NodeID: id}.AsV2())
			}
			pr := sm.prs.Progress[id]
			pr.Match, pr.Next = tt.matches[j], tt.matches[j]+1
		}
		sm.maybeCommit()
		if g := sm.raftLog.committed; g != tt.w {
			t.Errorf("#%d: committed = %d, want %d", i, g, tt.w)
		}
	}
}

func TestPastElectionTimeout(t *testing.T) {
	tests := []struct {
		elapse       int
		wprobability float64
		round        bool
	}{
		{5, 0, false},
		{10, 0.1, true},
		{13, 0.4, true},
		{15, 0.6, true},
		{18, 0.9, true},
		{20, 1, false},
	}

	for i, tt := range tests {
		sm := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(1)))
		sm.electionElapsed = tt.elapse
		c := 0
		for j := 0; j < 10000; j++ {
			sm.resetRandomizedElectionTimeout()
			if sm.pastElectionTimeout() {
				c++
			}
		}
		got := float64(c) / 10000.0
		if tt.round {
			got = math.Floor(got*10+0.5) / 10.0
		}
		if got != tt.wprobability {
			t.Errorf("#%d: probability = %v, want %v", i, got, tt.wprobability)
		}
	}
}

// ensure that the Step function ignores the message from old term and does not pass it to the
// actual stepX function.
func TestStepIgnoreOldTermMsg(t *testing.T) {
	called := false
	fakeStep := func(r *raft, m pb.Message) error {
		called = true
		return nil
	}
	sm := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(1)))
	sm.step = fakeStep
	sm.Term = 2
	sm.Step(pb.Message{Type: pb.MsgApp, Term: sm.Term - 1})
	if called {
		t.Errorf("stepFunc called = %v , want %v", called, false)
	}
}

// TestHandleMsgApp ensures:
// 1. Reply false if log doesn’t contain an entry at prevLogIndex whose term matches prevLogTerm.
// 2. If an existing entry conflicts with a new one (same index but different terms),
//    delete the existing entry and all that follow it; append any new entries not already in the log.
// 3. If leaderCommit > commitIndex, set commitIndex = min(leaderCommit, index of last new entry).
func TestHandleMsgApp(t *testing.T) {
	tests := []struct {
		m       pb.Message
		wIndex  uint64
		wCommit uint64
		wReject bool
	}{
		// Ensure 1
		{pb.Message{Type: pb.MsgApp, Term: 2, LogTerm: 3, Index: 2, Commit: 3}, 2, 0, true}, // previous log mismatch
		{pb.Message{Type: pb.MsgApp, Term: 2, LogTerm: 3, Index: 3, Commit: 3}, 2, 0, true}, // previous log non-exist

		// Ensure 2
		{pb.Message{Type: pb.MsgApp, Term: 2, LogTerm: 1, Index: 1, Commit: 1}, 2, 1, false},
		{pb.Message{Type: pb.MsgApp, Term: 2, LogTerm: 0, Index: 0, Commit: 1, Entries: []pb.Entry{{Index: 1, Term: 2}}}, 1, 1, false},
		{pb.Message{Type: pb.MsgApp, Term: 2, LogTerm: 2, Index: 2, Commit: 3, Entries: []pb.Entry{{Index: 3, Term: 2}, {Index: 4, Term: 2}}}, 4, 3, false},
		{pb.Message{Type: pb.MsgApp, Term: 2, LogTerm: 2, Index: 2, Commit: 4, Entries: []pb.Entry{{Index: 3, Term: 2}}}, 3, 3, false},
		{pb.Message{Type: pb.MsgApp, Term: 2, LogTerm: 1, Index: 1, Commit: 4, Entries: []pb.Entry{{Index: 2, Term: 2}}}, 2, 2, false},

		// Ensure 3
		{pb.Message{Type: pb.MsgApp, Term: 1, LogTerm: 1, Index: 1, Commit: 3}, 2, 1, false},                                           // match entry 1, commit up to last new entry 1
		{pb.Message{Type: pb.MsgApp, Term: 1, LogTerm: 1, Index: 1, Commit: 3, Entries: []pb.Entry{{Index: 2, Term: 2}}}, 2, 2, false}, // match entry 1, commit up to last new entry 2
		{pb.Message{Type: pb.MsgApp, Term: 2, LogTerm: 2, Index: 2, Commit: 3}, 2, 2, false},                                           // match entry 2, commit up to last new entry 2
		{pb.Message{Type: pb.MsgApp, Term: 2, LogTerm: 2, Index: 2, Commit: 4}, 2, 2, false},                                           // commit up to log.last()
	}

	for i, tt := range tests {
		storage := newTestMemoryStorage(withPeers(1))
		storage.Append([]pb.Entry{{Index: 1, Term: 1}, {Index: 2, Term: 2}})
		sm := newTestRaft(1, 10, 1, storage)
		sm.becomeFollower(2, None)

		sm.handleAppendEntries(tt.m)
		if sm.raftLog.lastIndex() != tt.wIndex {
			t.Errorf("#%d: lastIndex = %d, want %d", i, sm.raftLog.lastIndex(), tt.wIndex)
		}
		if sm.raftLog.committed != tt.wCommit {
			t.Errorf("#%d: committed = %d, want %d", i, sm.raftLog.committed, tt.wCommit)
		}
		m := sm.readMessages()
		if len(m) != 1 {
			t.Fatalf("#%d: msg = nil, want 1", i)
		}
		if m[0].Reject != tt.wReject {
			t.Errorf("#%d: reject = %v, want %v", i, m[0].Reject, tt.wReject)
		}
	}
}

// TestHandleHeartbeat ensures that the follower commits to the commit in the message.
func TestHandleHeartbeat(t *testing.T) {
	commit := uint64(2)
	tests := []struct {
		m       pb.Message
		wCommit uint64
	}{
		{pb.Message{From: 2, To: 1, Type: pb.MsgHeartbeat, Term: 2, Commit: commit + 1}, commit + 1},
		{pb.Message{From: 2, To: 1, Type: pb.MsgHeartbeat, Term: 2, Commit: commit - 1}, commit}, // do not decrease commit
	}

	for i, tt := range tests {
		storage := newTestMemoryStorage(withPeers(1, 2))
		storage.Append([]pb.Entry{{Index: 1, Term: 1}, {Index: 2, Term: 2}, {Index: 3, Term: 3}})
		sm := newTestRaft(1, 5, 1, storage)
		sm.becomeFollower(2, 2)
		sm.raftLog.commitTo(commit)
		sm.handleHeartbeat(tt.m)
		if sm.raftLog.committed != tt.wCommit {
			t.Errorf("#%d: committed = %d, want %d", i, sm.raftLog.committed, tt.wCommit)
		}
		m := sm.readMessages()
		if len(m) != 1 {
			t.Fatalf("#%d: msg = nil, want 1", i)
		}
		if m[0].Type != pb.MsgHeartbeatResp {
			t.Errorf("#%d: type = %v, want MsgHeartbeatResp", i, m[0].Type)
		}
	}
}

// TestHandleHeartbeatResp ensures that we re-send log entries when we get a heartbeat response.
func TestHandleHeartbeatResp(t *testing.T) {
	storage := newTestMemoryStorage(withPeers(1, 2))
	storage.Append([]pb.Entry{{Index: 1, Term: 1}, {Index: 2, Term: 2}, {Index: 3, Term: 3}})
	sm := newTestRaft(1, 5, 1, storage)
	sm.becomeCandidate()
	sm.becomeLeader()
	sm.raftLog.commitTo(sm.raftLog.lastIndex())

	// A heartbeat response from a node that is behind; re-send MsgApp
	sm.Step(pb.Message{From: 2, Type: pb.MsgHeartbeatResp})
	msgs := sm.readMessages()
	if len(msgs) != 1 {
		t.Fatalf("len(msgs) = %d, want 1", len(msgs))
	}
	if msgs[0].Type != pb.MsgApp {
		t.Errorf("type = %v, want MsgApp", msgs[0].Type)
	}

	// A second heartbeat response generates another MsgApp re-send
	sm.Step(pb.Message{From: 2, Type: pb.MsgHeartbeatResp})
	msgs = sm.readMessages()
	if len(msgs) != 1 {
		t.Fatalf("len(msgs) = %d, want 1", len(msgs))
	}
	if msgs[0].Type != pb.MsgApp {
		t.Errorf("type = %v, want MsgApp", msgs[0].Type)
	}

	// Once we have an MsgAppResp, heartbeats no longer send MsgApp.
	sm.Step(pb.Message{
		From:  2,
		Type:  pb.MsgAppResp,
		Index: msgs[0].Index + uint64(len(msgs[0].Entries)),
	})
	// Consume the message sent in response to MsgAppResp
	sm.readMessages()

	sm.Step(pb.Message{From: 2, Type: pb.MsgHeartbeatResp})
	msgs = sm.readMessages()
	if len(msgs) != 0 {
		t.Fatalf("len(msgs) = %d, want 0: %+v", len(msgs), msgs)
	}
}

// TestRaftFreesReadOnlyMem ensures raft will free read request from
// readOnly readIndexQueue and pendingReadIndex map.
// related issue: https://github.com/etcd-io/etcd/issues/7571
func TestRaftFreesReadOnlyMem(t *testing.T) {
	sm := newTestRaft(1, 5, 1, newTestMemoryStorage(withPeers(1, 2)))
	sm.becomeCandidate()
	sm.becomeLeader()
	sm.raftLog.commitTo(sm.raftLog.lastIndex())

	ctx := []byte("ctx")

	// leader starts linearizable read request.
	// more info: raft dissertation 6.4, step 2.
	sm.Step(pb.Message{From: 2, Type: pb.MsgReadIndex, Entries: []pb.Entry{{Data: ctx}}})
	msgs := sm.readMessages()
	if len(msgs) != 1 {
		t.Fatalf("len(msgs) = %d, want 1", len(msgs))
	}
	if msgs[0].Type != pb.MsgHeartbeat {
		t.Fatalf("type = %v, want MsgHeartbeat", msgs[0].Type)
	}
	if !bytes.Equal(msgs[0].Context, ctx) {
		t.Fatalf("Context = %v, want %v", msgs[0].Context, ctx)
	}
	if len(sm.readOnly.readIndexQueue) != 1 {
		t.Fatalf("len(readIndexQueue) = %v, want 1", len(sm.readOnly.readIndexQueue))
	}
	if len(sm.readOnly.pendingReadIndex) != 1 {
		t.Fatalf("len(pendingReadIndex) = %v, want 1", len(sm.readOnly.pendingReadIndex))
	}
	if _, ok := sm.readOnly.pendingReadIndex[string(ctx)]; !ok {
		t.Fatalf("can't find context %v in pendingReadIndex ", ctx)
	}

	// heartbeat responses from majority of followers (1 in this case)
	// acknowledge the authority of the leader.
	// more info: raft dissertation 6.4, step 3.
	sm.Step(pb.Message{From: 2, Type: pb.MsgHeartbeatResp, Context: ctx})
	if len(sm.readOnly.readIndexQueue) != 0 {
		t.Fatalf("len(readIndexQueue) = %v, want 0", len(sm.readOnly.readIndexQueue))
	}
	if len(sm.readOnly.pendingReadIndex) != 0 {
		t.Fatalf("len(pendingReadIndex) = %v, want 0", len(sm.readOnly.pendingReadIndex))
	}
	if _, ok := sm.readOnly.pendingReadIndex[string(ctx)]; ok {
		t.Fatalf("found context %v in pendingReadIndex, want none", ctx)
	}
}

// TestMsgAppRespWaitReset verifies the resume behavior of a leader
// MsgAppResp.
func TestMsgAppRespWaitReset(t *testing.T) {
	sm := newTestRaft(1, 5, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
	sm.becomeCandidate()
	sm.becomeLeader()

	// The new leader has just emitted a new Term 4 entry; consume those messages
	// from the outgoing queue.
	sm.bcastAppend()
	sm.readMessages()

	// Node 2 acks the first entry, making it committed.
	sm.Step(pb.Message{
		From:  2,
		Type:  pb.MsgAppResp,
		Index: 1,
	})
	if sm.raftLog.committed != 1 {
		t.Fatalf("expected committed to be 1, got %d", sm.raftLog.committed)
	}
	// Also consume the MsgApp messages that update Commit on the followers.
	sm.readMessages()

	// A new command is now proposed on node 1.
	sm.Step(pb.Message{
		From:    1,
		Type:    pb.MsgProp,
		Entries: []pb.Entry{{}},
	})

	// The command is broadcast to all nodes not in the wait state.
	// Node 2 left the wait state due to its MsgAppResp, but node 3 is still waiting.
	msgs := sm.readMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Type != pb.MsgApp || msgs[0].To != 2 {
		t.Errorf("expected MsgApp to node 2, got %v to %d", msgs[0].Type, msgs[0].To)
	}
	if len(msgs[0].Entries) != 1 || msgs[0].Entries[0].Index != 2 {
		t.Errorf("expected to send entry 2, but got %v", msgs[0].Entries)
	}

	// Now Node 3 acks the first entry. This releases the wait and entry 2 is sent.
	sm.Step(pb.Message{
		From:  3,
		Type:  pb.MsgAppResp,
		Index: 1,
	})
	msgs = sm.readMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Type != pb.MsgApp || msgs[0].To != 3 {
		t.Errorf("expected MsgApp to node 3, got %v to %d", msgs[0].Type, msgs[0].To)
	}
	if len(msgs[0].Entries) != 1 || msgs[0].Entries[0].Index != 2 {
		t.Errorf("expected to send entry 2, but got %v", msgs[0].Entries)
	}
}

func TestRecvMsgVote(t *testing.T) {
	testRecvMsgVote(t, pb.MsgVote)
}

func TestRecvMsgPreVote(t *testing.T) {
	testRecvMsgVote(t, pb.MsgPreVote)
}

func testRecvMsgVote(t *testing.T, msgType pb.MessageType) {
	tests := []struct {
		state          StateType
		index, logTerm uint64
		voteFor        uint64
		wreject        bool
	}{
		{StateFollower, 0, 0, None, true},
		{StateFollower, 0, 1, None, true},
		{StateFollower, 0, 2, None, true},
		{StateFollower, 0, 3, None, false},

		{StateFollower, 1, 0, None, true},
		{StateFollower, 1, 1, None, true},
		{StateFollower, 1, 2, None, true},
		{StateFollower, 1, 3, None, false},

		{StateFollower, 2, 0, None, true},
		{StateFollower, 2, 1, None, true},
		{StateFollower, 2, 2, None, false},
		{StateFollower, 2, 3, None, false},

		{StateFollower, 3, 0, None, true},
		{StateFollower, 3, 1, None, true},
		{StateFollower, 3, 2, None, false},
		{StateFollower, 3, 3, None, false},

		{StateFollower, 3, 2, 2, false},
		{StateFollower, 3, 2, 1, true},

		{StateLeader, 3, 3, 1, true},
		{StatePreCandidate, 3, 3, 1, true},
		{StateCandidate, 3, 3, 1, true},
	}

	max := func(a, b uint64) uint64 {
		if a > b {
			return a
		}
		return b
	}

	for i, tt := range tests {
		sm := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(1)))
		sm.state = tt.state
		switch tt.state {
		case StateFollower:
			sm.step = stepFollower
		case StateCandidate, StatePreCandidate:
			sm.step = stepCandidate
		case StateLeader:
			sm.step = stepLeader
		}
		sm.Vote = tt.voteFor
		sm.raftLog = &raftLog{
			storage:  &MemoryStorage{ents: []pb.Entry{{}, {Index: 1, Term: 2}, {Index: 2, Term: 2}}},
			unstable: unstable{offset: 3},
		}

		// raft.Term is greater than or equal to raft.raftLog.lastTerm. In this
		// test we're only testing MsgVote responses when the campaigning node
		// has a different raft log compared to the recipient node.
		// Additionally we're verifying behaviour when the recipient node has
		// already given out its vote for its current term. We're not testing
		// what the recipient node does when receiving a message with a
		// different term number, so we simply initialize both term numbers to
		// be the same.
		term := max(sm.raftLog.lastTerm(), tt.logTerm)
		sm.Term = term
		sm.Step(pb.Message{Type: msgType, Term: term, From: 2, Index: tt.index, LogTerm: tt.logTerm})

		msgs := sm.readMessages()
		if g := len(msgs); g != 1 {
			t.Fatalf("#%d: len(msgs) = %d, want 1", i, g)
			continue
		}
		if g := msgs[0].Type; g != voteRespMsgType(msgType) {
			t.Errorf("#%d, m.Type = %v, want %v", i, g, voteRespMsgType(msgType))
		}
		if g := msgs[0].Reject; g != tt.wreject {
			t.Errorf("#%d, m.Reject = %v, want %v", i, g, tt.wreject)
		}
	}
}

func TestStateTransition(t *testing.T) {
	tests := []struct {
		from   StateType
		to     StateType
		wallow bool
		wterm  uint64
		wlead  uint64
	}{
		{StateFollower, StateFollower, true, 1, None},
		{StateFollower, StatePreCandidate, true, 0, None},
		{StateFollower, StateCandidate, true, 1, None},
		{StateFollower, StateLeader, false, 0, None},

		{StatePreCandidate, StateFollower, true, 0, None},
		{StatePreCandidate, StatePreCandidate, true, 0, None},
		{StatePreCandidate, StateCandidate, true, 1, None},
		{StatePreCandidate, StateLeader, true, 0, 1},

		{StateCandidate, StateFollower, true, 0, None},
		{StateCandidate, StatePreCandidate, true, 0, None},
		{StateCandidate, StateCandidate, true, 1, None},
		{StateCandidate, StateLeader, true, 0, 1},

		{StateLeader, StateFollower, true, 1, None},
		{StateLeader, StatePreCandidate, false, 0, None},
		{StateLeader, StateCandidate, false, 1, None},
		{StateLeader, StateLeader, true, 0, 1},
	}

	for i, tt := range tests {
		func() {
			defer func() {
				if r := recover(); r != nil {
					if tt.wallow {
						t.Errorf("%d: allow = %v, want %v", i, false, true)
					}
				}
			}()

			sm := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(1)))
			sm.state = tt.from

			switch tt.to {
			case StateFollower:
				sm.becomeFollower(tt.wterm, tt.wlead)
			case StatePreCandidate:
				sm.becomePreCandidate()
			case StateCandidate:
				sm.becomeCandidate()
			case StateLeader:
				sm.becomeLeader()
			}

			if sm.Term != tt.wterm {
				t.Errorf("%d: term = %d, want %d", i, sm.Term, tt.wterm)
			}
			if sm.lead != tt.wlead {
				t.Errorf("%d: lead = %d, want %d", i, sm.lead, tt.wlead)
			}
		}()
	}
}

func TestAllServerStepdown(t *testing.T) {
	tests := []struct {
		state StateType

		wstate StateType
		wterm  uint64
		windex uint64
	}{
		{StateFollower, StateFollower, 3, 0},
		{StatePreCandidate, StateFollower, 3, 0},
		{StateCandidate, StateFollower, 3, 0},
		{StateLeader, StateFollower, 3, 1},
	}

	tmsgTypes := [...]pb.MessageType{pb.MsgVote, pb.MsgApp}
	tterm := uint64(3)

	for i, tt := range tests {
		sm := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
		switch tt.state {
		case StateFollower:
			sm.becomeFollower(1, None)
		case StatePreCandidate:
			sm.becomePreCandidate()
		case StateCandidate:
			sm.becomeCandidate()
		case StateLeader:
			sm.becomeCandidate()
			sm.becomeLeader()
		}

		for j, msgType := range tmsgTypes {
			sm.Step(pb.Message{From: 2, Type: msgType, Term: tterm, LogTerm: tterm})

			if sm.state != tt.wstate {
				t.Errorf("#%d.%d state = %v , want %v", i, j, sm.state, tt.wstate)
			}
			if sm.Term != tt.wterm {
				t.Errorf("#%d.%d term = %v , want %v", i, j, sm.Term, tt.wterm)
			}
			if sm.raftLog.lastIndex() != tt.windex {
				t.Errorf("#%d.%d index = %v , want %v", i, j, sm.raftLog.lastIndex(), tt.windex)
			}
			if uint64(len(sm.raftLog.allEntries())) != tt.windex {
				t.Errorf("#%d.%d len(ents) = %v , want %v", i, j, len(sm.raftLog.allEntries()), tt.windex)
			}
			wlead := uint64(2)
			if msgType == pb.MsgVote {
				wlead = None
			}
			if sm.lead != wlead {
				t.Errorf("#%d, sm.lead = %d, want %d", i, sm.lead, None)
			}
		}
	}
}

func TestCandidateResetTermMsgHeartbeat(t *testing.T) {
	testCandidateResetTerm(t, pb.MsgHeartbeat)
}

func TestCandidateResetTermMsgApp(t *testing.T) {
	testCandidateResetTerm(t, pb.MsgApp)
}

// testCandidateResetTerm tests when a candidate receives a
// MsgHeartbeat or MsgApp from leader, "Step" resets the term
// with leader's and reverts back to follower.
func testCandidateResetTerm(t *testing.T, mt pb.MessageType) {
	a := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
	b := newTestRaft(2, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
	c := newTestRaft(3, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))

	nt := newNetwork(a, b, c)

	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})
	if a.state != StateLeader {
		t.Errorf("state = %s, want %s", a.state, StateLeader)
	}
	if b.state != StateFollower {
		t.Errorf("state = %s, want %s", b.state, StateFollower)
	}
	if c.state != StateFollower {
		t.Errorf("state = %s, want %s", c.state, StateFollower)
	}

	// isolate 3 and increase term in rest
	nt.isolate(3)

	nt.send(pb.Message{From: 2, To: 2, Type: pb.MsgHup})
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	if a.state != StateLeader {
		t.Errorf("state = %s, want %s", a.state, StateLeader)
	}
	if b.state != StateFollower {
		t.Errorf("state = %s, want %s", b.state, StateFollower)
	}

	// trigger campaign in isolated c
	c.resetRandomizedElectionTimeout()
	for i := 0; i < c.randomizedElectionTimeout; i++ {
		c.tick()
	}

	if c.state != StateCandidate {
		t.Errorf("state = %s, want %s", c.state, StateCandidate)
	}

	nt.recover()

	// leader sends to isolated candidate
	// and expects candidate to revert to follower
	nt.send(pb.Message{From: 1, To: 3, Term: a.Term, Type: mt})

	if c.state != StateFollower {
		t.Errorf("state = %s, want %s", c.state, StateFollower)
	}

	// follower c term is reset with leader's
	if a.Term != c.Term {
		t.Errorf("follower term expected same term as leader's %d, got %d", a.Term, c.Term)
	}
}

func TestLeaderStepdownWhenQuorumActive(t *testing.T) {
	sm := newTestRaft(1, 5, 1, newTestMemoryStorage(withPeers(1, 2, 3)))

	sm.checkQuorum = true

	sm.becomeCandidate()
	sm.becomeLeader()

	for i := 0; i < sm.electionTimeout+1; i++ {
		sm.Step(pb.Message{From: 2, Type: pb.MsgHeartbeatResp, Term: sm.Term})
		sm.tick()
	}

	if sm.state != StateLeader {
		t.Errorf("state = %v, want %v", sm.state, StateLeader)
	}
}

func TestLeaderStepdownWhenQuorumLost(t *testing.T) {
	sm := newTestRaft(1, 5, 1, newTestMemoryStorage(withPeers(1, 2, 3)))

	sm.checkQuorum = true

	sm.becomeCandidate()
	sm.becomeLeader()

	for i := 0; i < sm.electionTimeout+1; i++ {
		sm.tick()
	}

	if sm.state != StateFollower {
		t.Errorf("state = %v, want %v", sm.state, StateFollower)
	}
}

func TestLeaderSupersedingWithCheckQuorum(t *testing.T) {
	a := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
	b := newTestRaft(2, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
	c := newTestRaft(3, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))

	a.checkQuorum = true
	b.checkQuorum = true
	c.checkQuorum = true

	nt := newNetwork(a, b, c)
	setRandomizedElectionTimeout(b, b.electionTimeout+1)

	for i := 0; i < b.electionTimeout; i++ {
		b.tick()
	}
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	if a.state != StateLeader {
		t.Errorf("state = %s, want %s", a.state, StateLeader)
	}

	if c.state != StateFollower {
		t.Errorf("state = %s, want %s", c.state, StateFollower)
	}

	nt.send(pb.Message{From: 3, To: 3, Type: pb.MsgHup})

	// Peer b rejected c's vote since its electionElapsed had not reached to electionTimeout
	if c.state != StateCandidate {
		t.Errorf("state = %s, want %s", c.state, StateCandidate)
	}

	// Letting b's electionElapsed reach to electionTimeout
	for i := 0; i < b.electionTimeout; i++ {
		b.tick()
	}
	nt.send(pb.Message{From: 3, To: 3, Type: pb.MsgHup})

	if c.state != StateLeader {
		t.Errorf("state = %s, want %s", c.state, StateLeader)
	}
}

func TestLeaderElectionWithCheckQuorum(t *testing.T) {
	a := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
	b := newTestRaft(2, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
	c := newTestRaft(3, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))

	a.checkQuorum = true
	b.checkQuorum = true
	c.checkQuorum = true

	nt := newNetwork(a, b, c)
	setRandomizedElectionTimeout(a, a.electionTimeout+1)
	setRandomizedElectionTimeout(b, b.electionTimeout+2)

	// Immediately after creation, votes are cast regardless of the
	// election timeout.
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	if a.state != StateLeader {
		t.Errorf("state = %s, want %s", a.state, StateLeader)
	}

	if c.state != StateFollower {
		t.Errorf("state = %s, want %s", c.state, StateFollower)
	}

	// need to reset randomizedElectionTimeout larger than electionTimeout again,
	// because the value might be reset to electionTimeout since the last state changes
	setRandomizedElectionTimeout(a, a.electionTimeout+1)
	setRandomizedElectionTimeout(b, b.electionTimeout+2)
	for i := 0; i < a.electionTimeout; i++ {
		a.tick()
	}
	for i := 0; i < b.electionTimeout; i++ {
		b.tick()
	}
	nt.send(pb.Message{From: 3, To: 3, Type: pb.MsgHup})

	if a.state != StateFollower {
		t.Errorf("state = %s, want %s", a.state, StateFollower)
	}

	if c.state != StateLeader {
		t.Errorf("state = %s, want %s", c.state, StateLeader)
	}
}

// TestFreeStuckCandidateWithCheckQuorum ensures that a candidate with a higher term
// can disrupt the leader even if the leader still "officially" holds the lease, The
// leader is expected to step down and adopt the candidate's term
func TestFreeStuckCandidateWithCheckQuorum(t *testing.T) {
	a := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
	b := newTestRaft(2, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
	c := newTestRaft(3, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))

	a.checkQuorum = true
	b.checkQuorum = true
	c.checkQuorum = true

	nt := newNetwork(a, b, c)
	setRandomizedElectionTimeout(b, b.electionTimeout+1)

	for i := 0; i < b.electionTimeout; i++ {
		b.tick()
	}
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	nt.isolate(1)
	nt.send(pb.Message{From: 3, To: 3, Type: pb.MsgHup})

	if b.state != StateFollower {
		t.Errorf("state = %s, want %s", b.state, StateFollower)
	}

	if c.state != StateCandidate {
		t.Errorf("state = %s, want %s", c.state, StateCandidate)
	}

	if c.Term != b.Term+1 {
		t.Errorf("term = %d, want %d", c.Term, b.Term+1)
	}

	// Vote again for safety
	nt.send(pb.Message{From: 3, To: 3, Type: pb.MsgHup})

	if b.state != StateFollower {
		t.Errorf("state = %s, want %s", b.state, StateFollower)
	}

	if c.state != StateCandidate {
		t.Errorf("state = %s, want %s", c.state, StateCandidate)
	}

	if c.Term != b.Term+2 {
		t.Errorf("term = %d, want %d", c.Term, b.Term+2)
	}

	nt.recover()
	nt.send(pb.Message{From: 1, To: 3, Type: pb.MsgHeartbeat, Term: a.Term})

	// Disrupt the leader so that the stuck peer is freed
	if a.state != StateFollower {
		t.Errorf("state = %s, want %s", a.state, StateFollower)
	}

	if c.Term != a.Term {
		t.Errorf("term = %d, want %d", c.Term, a.Term)
	}

	// Vote again, should become leader this time
	nt.send(pb.Message{From: 3, To: 3, Type: pb.MsgHup})

	if c.state != StateLeader {
		t.Errorf("peer 3 state: %s, want %s", c.state, StateLeader)
	}
}

func TestNonPromotableVoterWithCheckQuorum(t *testing.T) {
	a := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(1, 2)))
	b := newTestRaft(2, 10, 1, newTestMemoryStorage(withPeers(1)))

	a.checkQuorum = true
	b.checkQuorum = true

	nt := newNetwork(a, b)
	setRandomizedElectionTimeout(b, b.electionTimeout+1)
	// Need to remove 2 again to make it a non-promotable node since newNetwork overwritten some internal states
	b.applyConfChange(pb.ConfChange{Type: pb.ConfChangeRemoveNode, NodeID: 2}.AsV2())

	if b.promotable() {
		t.Fatalf("promotable = %v, want false", b.promotable())
	}

	for i := 0; i < b.electionTimeout; i++ {
		b.tick()
	}
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	if a.state != StateLeader {
		t.Errorf("state = %s, want %s", a.state, StateLeader)
	}

	if b.state != StateFollower {
		t.Errorf("state = %s, want %s", b.state, StateFollower)
	}

	if b.lead != 1 {
		t.Errorf("lead = %d, want 1", b.lead)
	}
}

// TestDisruptiveFollower tests isolated follower,
// with slow network incoming from leader, election times out
// to become a candidate with an increased term. Then, the
// candiate's response to late leader heartbeat forces the leader
// to step down.
func TestDisruptiveFollower(t *testing.T) {
	n1 := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
	n2 := newTestRaft(2, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
	n3 := newTestRaft(3, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))

	n1.checkQuorum = true
	n2.checkQuorum = true
	n3.checkQuorum = true

	n1.becomeFollower(1, None)
	n2.becomeFollower(1, None)
	n3.becomeFollower(1, None)

	nt := newNetwork(n1, n2, n3)

	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	// check state
	// n1.state == StateLeader
	// n2.state == StateFollower
	// n3.state == StateFollower
	if n1.state != StateLeader {
		t.Fatalf("node 1 state: %s, want %s", n1.state, StateLeader)
	}
	if n2.state != StateFollower {
		t.Fatalf("node 2 state: %s, want %s", n2.state, StateFollower)
	}
	if n3.state != StateFollower {
		t.Fatalf("node 3 state: %s, want %s", n3.state, StateFollower)
	}

	// etcd server "advanceTicksForElection" on restart;
	// this is to expedite campaign trigger when given larger
	// election timeouts (e.g. multi-datacenter deploy)
	// Or leader messages are being delayed while ticks elapse
	setRandomizedElectionTimeout(n3, n3.electionTimeout+2)
	for i := 0; i < n3.randomizedElectionTimeout-1; i++ {
		n3.tick()
	}

	// ideally, before last election tick elapses,
	// the follower n3 receives "pb.MsgApp" or "pb.MsgHeartbeat"
	// from leader n1, and then resets its "electionElapsed"
	// however, last tick may elapse before receiving any
	// messages from leader, thus triggering campaign
	n3.tick()

	// n1 is still leader yet
	// while its heartbeat to candidate n3 is being delayed

	// check state
	// n1.state == StateLeader
	// n2.state == StateFollower
	// n3.state == StateCandidate
	if n1.state != StateLeader {
		t.Fatalf("node 1 state: %s, want %s", n1.state, StateLeader)
	}
	if n2.state != StateFollower {
		t.Fatalf("node 2 state: %s, want %s", n2.state, StateFollower)
	}
	if n3.state != StateCandidate {
		t.Fatalf("node 3 state: %s, want %s", n3.state, StateCandidate)
	}
	// check term
	// n1.Term == 2
	// n2.Term == 2
	// n3.Term == 3
	if n1.Term != 2 {
		t.Fatalf("node 1 term: %d, want %d", n1.Term, 2)
	}
	if n2.Term != 2 {
		t.Fatalf("node 2 term: %d, want %d", n2.Term, 2)
	}
	if n3.Term != 3 {
		t.Fatalf("node 3 term: %d, want %d", n3.Term, 3)
	}

	// while outgoing vote requests are still queued in n3,
	// leader heartbeat finally arrives at candidate n3
	// however, due to delayed network from leader, leader
	// heartbeat was sent with lower term than candidate's
	nt.send(pb.Message{From: 1, To: 3, Term: n1.Term, Type: pb.MsgHeartbeat})

	// then candidate n3 responds with "pb.MsgAppResp" of higher term
	// and leader steps down from a message with higher term
	// this is to disrupt the current leader, so that candidate
	// with higher term can be freed with following election

	// check state
	// n1.state == StateFollower
	// n2.state == StateFollower
	// n3.state == StateCandidate
	if n1.state != StateFollower {
		t.Fatalf("node 1 state: %s, want %s", n1.state, StateFollower)
	}
	if n2.state != StateFollower {
		t.Fatalf("node 2 state: %s, want %s", n2.state, StateFollower)
	}
	if n3.state != StateCandidate {
		t.Fatalf("node 3 state: %s, want %s", n3.state, StateCandidate)
	}
	// check term
	// n1.Term == 3
	// n2.Term == 2
	// n3.Term == 3
	if n1.Term != 3 {
		t.Fatalf("node 1 term: %d, want %d", n1.Term, 3)
	}
	if n2.Term != 2 {
		t.Fatalf("node 2 term: %d, want %d", n2.Term, 2)
	}
	if n3.Term != 3 {
		t.Fatalf("node 3 term: %d, want %d", n3.Term, 3)
	}
}

// TestDisruptiveFollowerPreVote tests isolated follower,
// with slow network incoming from leader, election times out
// to become a pre-candidate with less log than current leader.
// Then pre-vote phase prevents this isolated node from forcing
// current leader to step down, thus less disruptions.
func TestDisruptiveFollowerPreVote(t *testing.T) {
	n1 := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
	n2 := newTestRaft(2, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
	n3 := newTestRaft(3, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))

	n1.checkQuorum = true
	n2.checkQuorum = true
	n3.checkQuorum = true

	n1.becomeFollower(1, None)
	n2.becomeFollower(1, None)
	n3.becomeFollower(1, None)

	nt := newNetwork(n1, n2, n3)

	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	// check state
	// n1.state == StateLeader
	// n2.state == StateFollower
	// n3.state == StateFollower
	if n1.state != StateLeader {
		t.Fatalf("node 1 state: %s, want %s", n1.state, StateLeader)
	}
	if n2.state != StateFollower {
		t.Fatalf("node 2 state: %s, want %s", n2.state, StateFollower)
	}
	if n3.state != StateFollower {
		t.Fatalf("node 3 state: %s, want %s", n3.state, StateFollower)
	}

	nt.isolate(3)
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{Data: []byte("somedata")}}})
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{Data: []byte("somedata")}}})
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{Data: []byte("somedata")}}})
	n1.preVote = true
	n2.preVote = true
	n3.preVote = true
	nt.recover()
	nt.send(pb.Message{From: 3, To: 3, Type: pb.MsgHup})

	// check state
	// n1.state == StateLeader
	// n2.state == StateFollower
	// n3.state == StatePreCandidate
	if n1.state != StateLeader {
		t.Fatalf("node 1 state: %s, want %s", n1.state, StateLeader)
	}
	if n2.state != StateFollower {
		t.Fatalf("node 2 state: %s, want %s", n2.state, StateFollower)
	}
	if n3.state != StatePreCandidate {
		t.Fatalf("node 3 state: %s, want %s", n3.state, StatePreCandidate)
	}
	// check term
	// n1.Term == 2
	// n2.Term == 2
	// n3.Term == 2
	if n1.Term != 2 {
		t.Fatalf("node 1 term: %d, want %d", n1.Term, 2)
	}
	if n2.Term != 2 {
		t.Fatalf("node 2 term: %d, want %d", n2.Term, 2)
	}
	if n3.Term != 2 {
		t.Fatalf("node 2 term: %d, want %d", n3.Term, 2)
	}

	// delayed leader heartbeat does not force current leader to step down
	nt.send(pb.Message{From: 1, To: 3, Term: n1.Term, Type: pb.MsgHeartbeat})
	if n1.state != StateLeader {
		t.Fatalf("node 1 state: %s, want %s", n1.state, StateLeader)
	}
}

func TestReadOnlyOptionSafe(t *testing.T) {
	a := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
	b := newTestRaft(2, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
	c := newTestRaft(3, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))

	nt := newNetwork(a, b, c)
	setRandomizedElectionTimeout(b, b.electionTimeout+1)

	for i := 0; i < b.electionTimeout; i++ {
		b.tick()
	}
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	if a.state != StateLeader {
		t.Fatalf("state = %s, want %s", a.state, StateLeader)
	}

	tests := []struct {
		sm        *raft
		proposals int
		wri       uint64
		wctx      []byte
	}{
		{a, 10, 11, []byte("ctx1")},
		{b, 10, 21, []byte("ctx2")},
		{c, 10, 31, []byte("ctx3")},
		{a, 10, 41, []byte("ctx4")},
		{b, 10, 51, []byte("ctx5")},
		{c, 10, 61, []byte("ctx6")},
	}

	for i, tt := range tests {
		for j := 0; j < tt.proposals; j++ {
			nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{}}})
		}

		nt.send(pb.Message{From: tt.sm.id, To: tt.sm.id, Type: pb.MsgReadIndex, Entries: []pb.Entry{{Data: tt.wctx}}})

		r := tt.sm
		if len(r.readStates) == 0 {
			t.Errorf("#%d: len(readStates) = 0, want non-zero", i)
		}
		rs := r.readStates[0]
		if rs.Index != tt.wri {
			t.Errorf("#%d: readIndex = %d, want %d", i, rs.Index, tt.wri)
		}

		if !bytes.Equal(rs.RequestCtx, tt.wctx) {
			t.Errorf("#%d: requestCtx = %v, want %v", i, rs.RequestCtx, tt.wctx)
		}
		r.readStates = nil
	}
}

func TestReadOnlyWithLearner(t *testing.T) {
	a := newTestLearnerRaft(1, 10, 1, newTestMemoryStorage(withPeers(1), withLearners(2)))
	b := newTestLearnerRaft(2, 10, 1, newTestMemoryStorage(withPeers(1), withLearners(2)))

	nt := newNetwork(a, b)
	setRandomizedElectionTimeout(b, b.electionTimeout+1)

	for i := 0; i < b.electionTimeout; i++ {
		b.tick()
	}
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	if a.state != StateLeader {
		t.Fatalf("state = %s, want %s", a.state, StateLeader)
	}

	tests := []struct {
		sm        *raft
		proposals int
		wri       uint64
		wctx      []byte
	}{
		{a, 10, 11, []byte("ctx1")},
		{b, 10, 21, []byte("ctx2")},
		{a, 10, 31, []byte("ctx3")},
		{b, 10, 41, []byte("ctx4")},
	}

	for i, tt := range tests {
		for j := 0; j < tt.proposals; j++ {
			nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{}}})
		}

		nt.send(pb.Message{From: tt.sm.id, To: tt.sm.id, Type: pb.MsgReadIndex, Entries: []pb.Entry{{Data: tt.wctx}}})

		r := tt.sm
		if len(r.readStates) == 0 {
			t.Fatalf("#%d: len(readStates) = 0, want non-zero", i)
		}
		rs := r.readStates[0]
		if rs.Index != tt.wri {
			t.Errorf("#%d: readIndex = %d, want %d", i, rs.Index, tt.wri)
		}

		if !bytes.Equal(rs.RequestCtx, tt.wctx) {
			t.Errorf("#%d: requestCtx = %v, want %v", i, rs.RequestCtx, tt.wctx)
		}
		r.readStates = nil
	}
}

func TestReadOnlyOptionLease(t *testing.T) {
	a := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
	b := newTestRaft(2, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
	c := newTestRaft(3, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
	a.readOnly.option = ReadOnlyLeaseBased
	b.readOnly.option = ReadOnlyLeaseBased
	c.readOnly.option = ReadOnlyLeaseBased
	a.checkQuorum = true
	b.checkQuorum = true
	c.checkQuorum = true

	nt := newNetwork(a, b, c)
	setRandomizedElectionTimeout(b, b.electionTimeout+1)

	for i := 0; i < b.electionTimeout; i++ {
		b.tick()
	}
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	if a.state != StateLeader {
		t.Fatalf("state = %s, want %s", a.state, StateLeader)
	}

	tests := []struct {
		sm        *raft
		proposals int
		wri       uint64
		wctx      []byte
	}{
		{a, 10, 11, []byte("ctx1")},
		{b, 10, 21, []byte("ctx2")},
		{c, 10, 31, []byte("ctx3")},
		{a, 10, 41, []byte("ctx4")},
		{b, 10, 51, []byte("ctx5")},
		{c, 10, 61, []byte("ctx6")},
	}

	for i, tt := range tests {
		for j := 0; j < tt.proposals; j++ {
			nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{}}})
		}

		nt.send(pb.Message{From: tt.sm.id, To: tt.sm.id, Type: pb.MsgReadIndex, Entries: []pb.Entry{{Data: tt.wctx}}})

		r := tt.sm
		rs := r.readStates[0]
		if rs.Index != tt.wri {
			t.Errorf("#%d: readIndex = %d, want %d", i, rs.Index, tt.wri)
		}

		if !bytes.Equal(rs.RequestCtx, tt.wctx) {
			t.Errorf("#%d: requestCtx = %v, want %v", i, rs.RequestCtx, tt.wctx)
		}
		r.readStates = nil
	}
}

// TestReadOnlyForNewLeader ensures that a leader only accepts MsgReadIndex message
// when it commits at least one log entry at it term.
func TestReadOnlyForNewLeader(t *testing.T) {
	nodeConfigs := []struct {
		id           uint64
		committed    uint64
		applied      uint64
		compactIndex uint64
	}{
		{1, 1, 1, 0},
		{2, 2, 2, 2},
		{3, 2, 2, 2},
	}
	peers := make([]stateMachine, 0)
	for _, c := range nodeConfigs {
		storage := newTestMemoryStorage(withPeers(1, 2, 3))
		storage.Append([]pb.Entry{{Index: 1, Term: 1}, {Index: 2, Term: 1}})
		storage.SetHardState(pb.HardState{Term: 1, Commit: c.committed})
		if c.compactIndex != 0 {
			storage.Compact(c.compactIndex)
		}
		cfg := newTestConfig(c.id, 10, 1, storage)
		cfg.Applied = c.applied
		raft := newRaft(cfg)
		peers = append(peers, raft)
	}
	nt := newNetwork(peers...)

	// Drop MsgApp to forbid peer a to commit any log entry at its term after it becomes leader.
	nt.ignore(pb.MsgApp)
	// Force peer a to become leader.
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	sm := nt.peers[1].(*raft)
	if sm.state != StateLeader {
		t.Fatalf("state = %s, want %s", sm.state, StateLeader)
	}

	// Ensure peer a drops read only request.
	var windex uint64 = 4
	wctx := []byte("ctx")
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgReadIndex, Entries: []pb.Entry{{Data: wctx}}})
	if len(sm.readStates) != 0 {
		t.Fatalf("len(readStates) = %d, want zero", len(sm.readStates))
	}

	nt.recover()

	// Force peer a to commit a log entry at its term
	for i := 0; i < sm.heartbeatTimeout; i++ {
		sm.tick()
	}
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{}}})
	if sm.raftLog.committed != 4 {
		t.Fatalf("committed = %d, want 4", sm.raftLog.committed)
	}
	lastLogTerm := sm.raftLog.zeroTermOnErrCompacted(sm.raftLog.term(sm.raftLog.committed))
	if lastLogTerm != sm.Term {
		t.Fatalf("last log term = %d, want %d", lastLogTerm, sm.Term)
	}

	// Ensure peer a accepts read only request after it commits a entry at its term.
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgReadIndex, Entries: []pb.Entry{{Data: wctx}}})
	if len(sm.readStates) != 1 {
		t.Fatalf("len(readStates) = %d, want 1", len(sm.readStates))
	}
	rs := sm.readStates[0]
	if rs.Index != windex {
		t.Fatalf("readIndex = %d, want %d", rs.Index, windex)
	}
	if !bytes.Equal(rs.RequestCtx, wctx) {
		t.Fatalf("requestCtx = %v, want %v", rs.RequestCtx, wctx)
	}
}

func TestLeaderAppResp(t *testing.T) {
	// initial progress: match = 0; next = 3
	tests := []struct {
		index  uint64
		reject bool
		// progress
		wmatch uint64
		wnext  uint64
		// message
		wmsgNum    int
		windex     uint64
		wcommitted uint64
	}{
		{3, true, 0, 3, 0, 0, 0},  // stale resp; no replies
		{2, true, 0, 2, 1, 1, 0},  // denied resp; leader does not commit; decrease next and send probing msg
		{2, false, 2, 4, 2, 2, 2}, // accept resp; leader commits; broadcast with commit index
		{0, false, 0, 3, 0, 0, 0}, // ignore heartbeat replies
	}

	for i, tt := range tests {
		// sm term is 1 after it becomes the leader.
		// thus the last log term must be 1 to be committed.
		sm := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
		sm.raftLog = &raftLog{
			storage:  &MemoryStorage{ents: []pb.Entry{{}, {Index: 1, Term: 0}, {Index: 2, Term: 1}}},
			unstable: unstable{offset: 3},
		}
		sm.becomeCandidate()
		sm.becomeLeader()
		sm.readMessages()
		sm.Step(pb.Message{From: 2, Type: pb.MsgAppResp, Index: tt.index, Term: sm.Term, Reject: tt.reject, RejectHint: tt.index})

		p := sm.prs.Progress[2]
		if p.Match != tt.wmatch {
			t.Errorf("#%d match = %d, want %d", i, p.Match, tt.wmatch)
		}
		if p.Next != tt.wnext {
			t.Errorf("#%d next = %d, want %d", i, p.Next, tt.wnext)
		}

		msgs := sm.readMessages()

		if len(msgs) != tt.wmsgNum {
			t.Errorf("#%d msgNum = %d, want %d", i, len(msgs), tt.wmsgNum)
		}
		for j, msg := range msgs {
			if msg.Index != tt.windex {
				t.Errorf("#%d.%d index = %d, want %d", i, j, msg.Index, tt.windex)
			}
			if msg.Commit != tt.wcommitted {
				t.Errorf("#%d.%d commit = %d, want %d", i, j, msg.Commit, tt.wcommitted)
			}
		}
	}
}

// When the leader receives a heartbeat tick, it should
// send a MsgHeartbeat with m.Index = 0, m.LogTerm=0 and empty entries.
func TestBcastBeat(t *testing.T) {
	offset := uint64(1000)
	// make a state machine with log.offset = 1000
	s := pb.Snapshot{
		Metadata: pb.SnapshotMetadata{
			Index:     offset,
			Term:      1,
			ConfState: pb.ConfState{Voters: []uint64{1, 2, 3}},
		},
	}
	storage := NewMemoryStorage()
	storage.ApplySnapshot(s)
	sm := newTestRaft(1, 10, 1, storage)
	sm.Term = 1

	sm.becomeCandidate()
	sm.becomeLeader()
	for i := 0; i < 10; i++ {
		mustAppendEntry(sm, pb.Entry{Index: uint64(i) + 1})
	}
	// slow follower
	sm.prs.Progress[2].Match, sm.prs.Progress[2].Next = 5, 6
	// normal follower
	sm.prs.Progress[3].Match, sm.prs.Progress[3].Next = sm.raftLog.lastIndex(), sm.raftLog.lastIndex()+1

	sm.Step(pb.Message{Type: pb.MsgBeat})
	msgs := sm.readMessages()
	if len(msgs) != 2 {
		t.Fatalf("len(msgs) = %v, want 2", len(msgs))
	}
	wantCommitMap := map[uint64]uint64{
		2: min(sm.raftLog.committed, sm.prs.Progress[2].Match),
		3: min(sm.raftLog.committed, sm.prs.Progress[3].Match),
	}
	for i, m := range msgs {
		if m.Type != pb.MsgHeartbeat {
			t.Fatalf("#%d: type = %v, want = %v", i, m.Type, pb.MsgHeartbeat)
		}
		if m.Index != 0 {
			t.Fatalf("#%d: prevIndex = %d, want %d", i, m.Index, 0)
		}
		if m.LogTerm != 0 {
			t.Fatalf("#%d: prevTerm = %d, want %d", i, m.LogTerm, 0)
		}
		if wantCommitMap[m.To] == 0 {
			t.Fatalf("#%d: unexpected to %d", i, m.To)
		} else {
			if m.Commit != wantCommitMap[m.To] {
				t.Fatalf("#%d: commit = %d, want %d", i, m.Commit, wantCommitMap[m.To])
			}
			delete(wantCommitMap, m.To)
		}
		if len(m.Entries) != 0 {
			t.Fatalf("#%d: len(entries) = %d, want 0", i, len(m.Entries))
		}
	}
}

// tests the output of the state machine when receiving MsgBeat
func TestRecvMsgBeat(t *testing.T) {
	tests := []struct {
		state StateType
		wMsg  int
	}{
		{StateLeader, 2},
		// candidate and follower should ignore MsgBeat
		{StateCandidate, 0},
		{StateFollower, 0},
	}

	for i, tt := range tests {
		sm := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
		sm.raftLog = &raftLog{storage: &MemoryStorage{ents: []pb.Entry{{}, {Index: 1, Term: 0}, {Index: 2, Term: 1}}}}
		sm.Term = 1
		sm.state = tt.state
		switch tt.state {
		case StateFollower:
			sm.step = stepFollower
		case StateCandidate:
			sm.step = stepCandidate
		case StateLeader:
			sm.step = stepLeader
		}
		sm.Step(pb.Message{From: 1, To: 1, Type: pb.MsgBeat})

		msgs := sm.readMessages()
		if len(msgs) != tt.wMsg {
			t.Errorf("%d: len(msgs) = %d, want %d", i, len(msgs), tt.wMsg)
		}
		for _, m := range msgs {
			if m.Type != pb.MsgHeartbeat {
				t.Errorf("%d: msg.type = %v, want %v", i, m.Type, pb.MsgHeartbeat)
			}
		}
	}
}

func TestLeaderIncreaseNext(t *testing.T) {
	previousEnts := []pb.Entry{{Term: 1, Index: 1}, {Term: 1, Index: 2}, {Term: 1, Index: 3}}
	tests := []struct {
		// progress
		state tracker.StateType
		next  uint64

		wnext uint64
	}{
		// state replicate, optimistically increase next
		// previous entries + noop entry + propose + 1
		{tracker.StateReplicate, 2, uint64(len(previousEnts) + 1 + 1 + 1)},
		// state probe, not optimistically increase next
		{tracker.StateProbe, 2, 2},
	}

	for i, tt := range tests {
		sm := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(1, 2)))
		sm.raftLog.append(previousEnts...)
		sm.becomeCandidate()
		sm.becomeLeader()
		sm.prs.Progress[2].State = tt.state
		sm.prs.Progress[2].Next = tt.next
		sm.Step(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{Data: []byte("somedata")}}})

		p := sm.prs.Progress[2]
		if p.Next != tt.wnext {
			t.Errorf("#%d next = %d, want %d", i, p.Next, tt.wnext)
		}
	}
}

func TestSendAppendForProgressProbe(t *testing.T) {
	r := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(1, 2)))
	r.becomeCandidate()
	r.becomeLeader()
	r.readMessages()
	r.prs.Progress[2].BecomeProbe()

	// each round is a heartbeat
	for i := 0; i < 3; i++ {
		if i == 0 {
			// we expect that raft will only send out one msgAPP on the first
			// loop. After that, the follower is paused until a heartbeat response is
			// received.
			mustAppendEntry(r, pb.Entry{Data: []byte("somedata")})
			r.sendAppend(2)
			msg := r.readMessages()
			if len(msg) != 1 {
				t.Errorf("len(msg) = %d, want %d", len(msg), 1)
			}
			if msg[0].Index != 0 {
				t.Errorf("index = %d, want %d", msg[0].Index, 0)
			}
		}

		if !r.prs.Progress[2].ProbeSent {
			t.Errorf("paused = %v, want true", r.prs.Progress[2].ProbeSent)
		}
		for j := 0; j < 10; j++ {
			mustAppendEntry(r, pb.Entry{Data: []byte("somedata")})
			r.sendAppend(2)
			if l := len(r.readMessages()); l != 0 {
				t.Errorf("len(msg) = %d, want %d", l, 0)
			}
		}

		// do a heartbeat
		for j := 0; j < r.heartbeatTimeout; j++ {
			r.Step(pb.Message{From: 1, To: 1, Type: pb.MsgBeat})
		}
		if !r.prs.Progress[2].ProbeSent {
			t.Errorf("paused = %v, want true", r.prs.Progress[2].ProbeSent)
		}

		// consume the heartbeat
		msg := r.readMessages()
		if len(msg) != 1 {
			t.Errorf("len(msg) = %d, want %d", len(msg), 1)
		}
		if msg[0].Type != pb.MsgHeartbeat {
			t.Errorf("type = %v, want %v", msg[0].Type, pb.MsgHeartbeat)
		}
	}

	// a heartbeat response will allow another message to be sent
	r.Step(pb.Message{From: 2, To: 1, Type: pb.MsgHeartbeatResp})
	msg := r.readMessages()
	if len(msg) != 1 {
		t.Errorf("len(msg) = %d, want %d", len(msg), 1)
	}
	if msg[0].Index != 0 {
		t.Errorf("index = %d, want %d", msg[0].Index, 0)
	}
	if !r.prs.Progress[2].ProbeSent {
		t.Errorf("paused = %v, want true", r.prs.Progress[2].ProbeSent)
	}
}

func TestSendAppendForProgressReplicate(t *testing.T) {
	r := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(1, 2)))
	r.becomeCandidate()
	r.becomeLeader()
	r.readMessages()
	r.prs.Progress[2].BecomeReplicate()

	for i := 0; i < 10; i++ {
		mustAppendEntry(r, pb.Entry{Data: []byte("somedata")})
		r.sendAppend(2)
		msgs := r.readMessages()
		if len(msgs) != 1 {
			t.Errorf("len(msg) = %d, want %d", len(msgs), 1)
		}
	}
}

func TestSendAppendForProgressSnapshot(t *testing.T) {
	r := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(1, 2)))
	r.becomeCandidate()
	r.becomeLeader()
	r.readMessages()
	r.prs.Progress[2].BecomeSnapshot(10)

	for i := 0; i < 10; i++ {
		mustAppendEntry(r, pb.Entry{Data: []byte("somedata")})
		r.sendAppend(2)
		msgs := r.readMessages()
		if len(msgs) != 0 {
			t.Errorf("len(msg) = %d, want %d", len(msgs), 0)
		}
	}
}

func TestRecvMsgUnreachable(t *testing.T) {
	previousEnts := []pb.Entry{{Term: 1, Index: 1}, {Term: 1, Index: 2}, {Term: 1, Index: 3}}
	s := newTestMemoryStorage(withPeers(1, 2))
	s.Append(previousEnts)
	r := newTestRaft(1, 10, 1, s)
	r.becomeCandidate()
	r.becomeLeader()
	r.readMessages()
	// set node 2 to state replicate
	r.prs.Progress[2].Match = 3
	r.prs.Progress[2].BecomeReplicate()
	r.prs.Progress[2].OptimisticUpdate(5)

	r.Step(pb.Message{From: 2, To: 1, Type: pb.MsgUnreachable})

	if r.prs.Progress[2].State != tracker.StateProbe {
		t.Errorf("state = %s, want %s", r.prs.Progress[2].State, tracker.StateProbe)
	}
	if wnext := r.prs.Progress[2].Match + 1; r.prs.Progress[2].Next != wnext {
		t.Errorf("next = %d, want %d", r.prs.Progress[2].Next, wnext)
	}
}

func TestRestore(t *testing.T) {
	s := pb.Snapshot{
		Metadata: pb.SnapshotMetadata{
			Index:     11, // magic number
			Term:      11, // magic number
			ConfState: pb.ConfState{Voters: []uint64{1, 2, 3}},
		},
	}

	storage := newTestMemoryStorage(withPeers(1, 2))
	sm := newTestRaft(1, 10, 1, storage)
	if ok := sm.restore(s); !ok {
		t.Fatal("restore fail, want succeed")
	}

	if sm.raftLog.lastIndex() != s.Metadata.Index {
		t.Errorf("log.lastIndex = %d, want %d", sm.raftLog.lastIndex(), s.Metadata.Index)
	}
	if mustTerm(sm.raftLog.term(s.Metadata.Index)) != s.Metadata.Term {
		t.Errorf("log.lastTerm = %d, want %d", mustTerm(sm.raftLog.term(s.Metadata.Index)), s.Metadata.Term)
	}
	sg := sm.prs.VoterNodes()
	if !reflect.DeepEqual(sg, s.Metadata.ConfState.Voters) {
		t.Errorf("sm.Voters = %+v, want %+v", sg, s.Metadata.ConfState.Voters)
	}

	if ok := sm.restore(s); ok {
		t.Fatal("restore succeed, want fail")
	}
	// It should not campaign before actually applying data.
	for i := 0; i < sm.randomizedElectionTimeout; i++ {
		sm.tick()
	}
	if sm.state != StateFollower {
		t.Errorf("state = %d, want %d", sm.state, StateFollower)
	}
}

// TestRestoreWithLearner restores a snapshot which contains learners.
func TestRestoreWithLearner(t *testing.T) {
	s := pb.Snapshot{
		Metadata: pb.SnapshotMetadata{
			Index:     11, // magic number
			Term:      11, // magic number
			ConfState: pb.ConfState{Voters: []uint64{1, 2}, Learners: []uint64{3}},
		},
	}

	storage := newTestMemoryStorage(withPeers(1, 2), withLearners(3))
	sm := newTestLearnerRaft(3, 8, 2, storage)
	if ok := sm.restore(s); !ok {
		t.Error("restore fail, want succeed")
	}

	if sm.raftLog.lastIndex() != s.Metadata.Index {
		t.Errorf("log.lastIndex = %d, want %d", sm.raftLog.lastIndex(), s.Metadata.Index)
	}
	if mustTerm(sm.raftLog.term(s.Metadata.Index)) != s.Metadata.Term {
		t.Errorf("log.lastTerm = %d, want %d", mustTerm(sm.raftLog.term(s.Metadata.Index)), s.Metadata.Term)
	}
	sg := sm.prs.VoterNodes()
	if len(sg) != len(s.Metadata.ConfState.Voters) {
		t.Errorf("sm.Voters = %+v, length not equal with %+v", sg, s.Metadata.ConfState.Voters)
	}
	lns := sm.prs.LearnerNodes()
	if len(lns) != len(s.Metadata.ConfState.Learners) {
		t.Errorf("sm.LearnerNodes = %+v, length not equal with %+v", sg, s.Metadata.ConfState.Learners)
	}
	for _, n := range s.Metadata.ConfState.Voters {
		if sm.prs.Progress[n].IsLearner {
			t.Errorf("sm.Node %x isLearner = %s, want %t", n, sm.prs.Progress[n], false)
		}
	}
	for _, n := range s.Metadata.ConfState.Learners {
		if !sm.prs.Progress[n].IsLearner {
			t.Errorf("sm.Node %x isLearner = %s, want %t", n, sm.prs.Progress[n], true)
		}
	}

	if ok := sm.restore(s); ok {
		t.Error("restore succeed, want fail")
	}
}

// TestRestoreVoterToLearner verifies that a normal peer can be downgraded to a
// learner through a snapshot. At the time of writing, we don't allow
// configuration changes to do this directly, but note that the snapshot may
// compress multiple changes to the configuration into one: the voter could have
// been removed, then readded as a learner and the snapshot reflects both
// changes. In that case, a voter receives a snapshot telling it that it is now
// a learner. In fact, the node has to accept that snapshot, or it is
// permanently cut off from the Raft log.
func TestRestoreVoterToLearner(t *testing.T) {
	s := pb.Snapshot{
		Metadata: pb.SnapshotMetadata{
			Index:     11, // magic number
			Term:      11, // magic number
			ConfState: pb.ConfState{Voters: []uint64{1, 2}, Learners: []uint64{3}},
		},
	}

	storage := newTestMemoryStorage(withPeers(1, 2, 3))
	sm := newTestRaft(3, 10, 1, storage)

	if sm.isLearner {
		t.Errorf("%x is learner, want not", sm.id)
	}
	if ok := sm.restore(s); !ok {
		t.Error("restore failed unexpectedly")
	}
}

// TestRestoreLearnerPromotion checks that a learner can become to a follower after
// restoring snapshot.
func TestRestoreLearnerPromotion(t *testing.T) {
	s := pb.Snapshot{
		Metadata: pb.SnapshotMetadata{
			Index:     11, // magic number
			Term:      11, // magic number
			ConfState: pb.ConfState{Voters: []uint64{1, 2, 3}},
		},
	}

	storage := newTestMemoryStorage(withPeers(1, 2), withLearners(3))
	sm := newTestLearnerRaft(3, 10, 1, storage)

	if !sm.isLearner {
		t.Errorf("%x is not learner, want yes", sm.id)
	}

	if ok := sm.restore(s); !ok {
		t.Error("restore fail, want succeed")
	}

	if sm.isLearner {
		t.Errorf("%x is learner, want not", sm.id)
	}
}

// TestLearnerReceiveSnapshot tests that a learner can receive a snpahost from leader
func TestLearnerReceiveSnapshot(t *testing.T) {
	// restore the state machine from a snapshot so it has a compacted log and a snapshot
	s := pb.Snapshot{
		Metadata: pb.SnapshotMetadata{
			Index:     11, // magic number
			Term:      11, // magic number
			ConfState: pb.ConfState{Voters: []uint64{1}, Learners: []uint64{2}},
		},
	}

	store := newTestMemoryStorage(withPeers(1), withLearners(2))
	n1 := newTestLearnerRaft(1, 10, 1, store)
	n2 := newTestLearnerRaft(2, 10, 1, newTestMemoryStorage(withPeers(1), withLearners(2)))

	n1.restore(s)
	ready := newReady(n1, &SoftState{}, pb.HardState{})
	store.ApplySnapshot(ready.Snapshot)
	n1.advance(ready)

	// Force set n1 appplied index.
	n1.raftLog.appliedTo(n1.raftLog.committed)

	nt := newNetwork(n1, n2)

	setRandomizedElectionTimeout(n1, n1.electionTimeout)
	for i := 0; i < n1.electionTimeout; i++ {
		n1.tick()
	}

	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgBeat})

	if n2.raftLog.committed != n1.raftLog.committed {
		t.Errorf("peer 2 must commit to %d, but %d", n1.raftLog.committed, n2.raftLog.committed)
	}
}

func TestRestoreIgnoreSnapshot(t *testing.T) {
	previousEnts := []pb.Entry{{Term: 1, Index: 1}, {Term: 1, Index: 2}, {Term: 1, Index: 3}}
	commit := uint64(1)
	storage := newTestMemoryStorage(withPeers(1, 2))
	sm := newTestRaft(1, 10, 1, storage)
	sm.raftLog.append(previousEnts...)
	sm.raftLog.commitTo(commit)

	s := pb.Snapshot{
		Metadata: pb.SnapshotMetadata{
			Index:     commit,
			Term:      1,
			ConfState: pb.ConfState{Voters: []uint64{1, 2}},
		},
	}

	// ignore snapshot
	if ok := sm.restore(s); ok {
		t.Errorf("restore = %t, want %t", ok, false)
	}
	if sm.raftLog.committed != commit {
		t.Errorf("commit = %d, want %d", sm.raftLog.committed, commit)
	}

	// ignore snapshot and fast forward commit
	s.Metadata.Index = commit + 1
	if ok := sm.restore(s); ok {
		t.Errorf("restore = %t, want %t", ok, false)
	}
	if sm.raftLog.committed != commit+1 {
		t.Errorf("commit = %d, want %d", sm.raftLog.committed, commit+1)
	}
}

func TestProvideSnap(t *testing.T) {
	// restore the state machine from a snapshot so it has a compacted log and a snapshot
	s := pb.Snapshot{
		Metadata: pb.SnapshotMetadata{
			Index:     11, // magic number
			Term:      11, // magic number
			ConfState: pb.ConfState{Voters: []uint64{1, 2}},
		},
	}
	storage := newTestMemoryStorage(withPeers(1))
	sm := newTestRaft(1, 10, 1, storage)
	sm.restore(s)

	sm.becomeCandidate()
	sm.becomeLeader()

	// force set the next of node 2, so that node 2 needs a snapshot
	sm.prs.Progress[2].Next = sm.raftLog.firstIndex()
	sm.Step(pb.Message{From: 2, To: 1, Type: pb.MsgAppResp, Index: sm.prs.Progress[2].Next - 1, Reject: true})

	msgs := sm.readMessages()
	if len(msgs) != 1 {
		t.Fatalf("len(msgs) = %d, want 1", len(msgs))
	}
	m := msgs[0]
	if m.Type != pb.MsgSnap {
		t.Errorf("m.Type = %v, want %v", m.Type, pb.MsgSnap)
	}
}

func TestIgnoreProvidingSnap(t *testing.T) {
	// restore the state machine from a snapshot so it has a compacted log and a snapshot
	s := pb.Snapshot{
		Metadata: pb.SnapshotMetadata{
			Index:     11, // magic number
			Term:      11, // magic number
			ConfState: pb.ConfState{Voters: []uint64{1, 2}},
		},
	}
	storage := newTestMemoryStorage(withPeers(1))
	sm := newTestRaft(1, 10, 1, storage)
	sm.restore(s)

	sm.becomeCandidate()
	sm.becomeLeader()

	// force set the next of node 2, so that node 2 needs a snapshot
	// change node 2 to be inactive, expect node 1 ignore sending snapshot to 2
	sm.prs.Progress[2].Next = sm.raftLog.firstIndex() - 1
	sm.prs.Progress[2].RecentActive = false

	sm.Step(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{Data: []byte("somedata")}}})

	msgs := sm.readMessages()
	if len(msgs) != 0 {
		t.Errorf("len(msgs) = %d, want 0", len(msgs))
	}
}

func TestRestoreFromSnapMsg(t *testing.T) {
	s := pb.Snapshot{
		Metadata: pb.SnapshotMetadata{
			Index:     11, // magic number
			Term:      11, // magic number
			ConfState: pb.ConfState{Voters: []uint64{1, 2}},
		},
	}
	m := pb.Message{Type: pb.MsgSnap, From: 1, Term: 2, Snapshot: s}

	sm := newTestRaft(2, 10, 1, newTestMemoryStorage(withPeers(1, 2)))
	sm.Step(m)

	if sm.lead != uint64(1) {
		t.Errorf("sm.lead = %d, want 1", sm.lead)
	}

	// TODO(bdarnell): what should this test?
}

func TestSlowNodeRestore(t *testing.T) {
	nt := newNetwork(nil, nil, nil)
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	nt.isolate(3)
	for j := 0; j <= 100; j++ {
		nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{}}})
	}
	lead := nt.peers[1].(*raft)
	nextEnts(lead, nt.storage[1])
	nt.storage[1].CreateSnapshot(lead.raftLog.applied, &pb.ConfState{Voters: lead.prs.VoterNodes()}, nil)
	nt.storage[1].Compact(lead.raftLog.applied)

	nt.recover()
	// send heartbeats so that the leader can learn everyone is active.
	// node 3 will only be considered as active when node 1 receives a reply from it.
	for {
		nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgBeat})
		if lead.prs.Progress[3].RecentActive {
			break
		}
	}

	// trigger a snapshot
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{}}})

	follower := nt.peers[3].(*raft)

	// trigger a commit
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{}}})
	if follower.raftLog.committed != lead.raftLog.committed {
		t.Errorf("follower.committed = %d, want %d", follower.raftLog.committed, lead.raftLog.committed)
	}
}

// TestStepConfig tests that when raft step msgProp in EntryConfChange type,
// it appends the entry to log and sets pendingConf to be true.
func TestStepConfig(t *testing.T) {
	// a raft that cannot make progress
	r := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(1, 2)))
	r.becomeCandidate()
	r.becomeLeader()
	index := r.raftLog.lastIndex()
	r.Step(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{Type: pb.EntryConfChange}}})
	if g := r.raftLog.lastIndex(); g != index+1 {
		t.Errorf("index = %d, want %d", g, index+1)
	}
	if r.pendingConfIndex != index+1 {
		t.Errorf("pendingConfIndex = %d, want %d", r.pendingConfIndex, index+1)
	}
}

// TestStepIgnoreConfig tests that if raft step the second msgProp in
// EntryConfChange type when the first one is uncommitted, the node will set
// the proposal to noop and keep its original state.
func TestStepIgnoreConfig(t *testing.T) {
	// a raft that cannot make progress
	r := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(1, 2)))
	r.becomeCandidate()
	r.becomeLeader()
	r.Step(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{Type: pb.EntryConfChange}}})
	index := r.raftLog.lastIndex()
	pendingConfIndex := r.pendingConfIndex
	r.Step(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{Type: pb.EntryConfChange}}})
	wents := []pb.Entry{{Type: pb.EntryNormal, Term: 1, Index: 3, Data: nil}}
	ents, err := r.raftLog.entries(index+1, noLimit)
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	if !reflect.DeepEqual(ents, wents) {
		t.Errorf("ents = %+v, want %+v", ents, wents)
	}
	if r.pendingConfIndex != pendingConfIndex {
		t.Errorf("pendingConfIndex = %d, want %d", r.pendingConfIndex, pendingConfIndex)
	}
}

// TestNewLeaderPendingConfig tests that new leader sets its pendingConfigIndex
// based on uncommitted entries.
func TestNewLeaderPendingConfig(t *testing.T) {
	tests := []struct {
		addEntry      bool
		wpendingIndex uint64
	}{
		{false, 0},
		{true, 1},
	}
	for i, tt := range tests {
		r := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(1, 2)))
		if tt.addEntry {
			mustAppendEntry(r, pb.Entry{Type: pb.EntryNormal})
		}
		r.becomeCandidate()
		r.becomeLeader()
		if r.pendingConfIndex != tt.wpendingIndex {
			t.Errorf("#%d: pendingConfIndex = %d, want %d",
				i, r.pendingConfIndex, tt.wpendingIndex)
		}
	}
}

// TestAddNode tests that addNode could update nodes correctly.
func TestAddNode(t *testing.T) {
	r := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(1)))
	r.applyConfChange(pb.ConfChange{NodeID: 2, Type: pb.ConfChangeAddNode}.AsV2())
	nodes := r.prs.VoterNodes()
	wnodes := []uint64{1, 2}
	if !reflect.DeepEqual(nodes, wnodes) {
		t.Errorf("nodes = %v, want %v", nodes, wnodes)
	}
}

// TestAddLearner tests that addLearner could update nodes correctly.
func TestAddLearner(t *testing.T) {
	r := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(1)))
	// Add new learner peer.
	r.applyConfChange(pb.ConfChange{NodeID: 2, Type: pb.ConfChangeAddLearnerNode}.AsV2())
	if r.isLearner {
		t.Fatal("expected 1 to be voter")
	}
	nodes := r.prs.LearnerNodes()
	wnodes := []uint64{2}
	if !reflect.DeepEqual(nodes, wnodes) {
		t.Errorf("nodes = %v, want %v", nodes, wnodes)
	}
	if !r.prs.Progress[2].IsLearner {
		t.Fatal("expected 2 to be learner")
	}

	// Promote peer to voter.
	r.applyConfChange(pb.ConfChange{NodeID: 2, Type: pb.ConfChangeAddNode}.AsV2())
	if r.prs.Progress[2].IsLearner {
		t.Fatal("expected 2 to be voter")
	}

	// Demote r.
	r.applyConfChange(pb.ConfChange{NodeID: 1, Type: pb.ConfChangeAddLearnerNode}.AsV2())
	if !r.prs.Progress[1].IsLearner {
		t.Fatal("expected 1 to be learner")
	}
	if !r.isLearner {
		t.Fatal("expected 1 to be learner")
	}

	// Promote r again.
	r.applyConfChange(pb.ConfChange{NodeID: 1, Type: pb.ConfChangeAddNode}.AsV2())
	if r.prs.Progress[1].IsLearner {
		t.Fatal("expected 1 to be voter")
	}
	if r.isLearner {
		t.Fatal("expected 1 to be voter")
	}
}

// TestAddNodeCheckQuorum tests that addNode does not trigger a leader election
// immediately when checkQuorum is set.
func TestAddNodeCheckQuorum(t *testing.T) {
	r := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(1)))
	r.checkQuorum = true

	r.becomeCandidate()
	r.becomeLeader()

	for i := 0; i < r.electionTimeout-1; i++ {
		r.tick()
	}

	r.applyConfChange(pb.ConfChange{NodeID: 2, Type: pb.ConfChangeAddNode}.AsV2())

	// This tick will reach electionTimeout, which triggers a quorum check.
	r.tick()

	// Node 1 should still be the leader after a single tick.
	if r.state != StateLeader {
		t.Errorf("state = %v, want %v", r.state, StateLeader)
	}

	// After another electionTimeout ticks without hearing from node 2,
	// node 1 should step down.
	for i := 0; i < r.electionTimeout; i++ {
		r.tick()
	}

	if r.state != StateFollower {
		t.Errorf("state = %v, want %v", r.state, StateFollower)
	}
}

// TestRemoveNode tests that removeNode could update nodes and
// and removed list correctly.
func TestRemoveNode(t *testing.T) {
	r := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(1, 2)))
	r.applyConfChange(pb.ConfChange{NodeID: 2, Type: pb.ConfChangeRemoveNode}.AsV2())
	w := []uint64{1}
	if g := r.prs.VoterNodes(); !reflect.DeepEqual(g, w) {
		t.Errorf("nodes = %v, want %v", g, w)
	}

	// Removing the remaining voter will panic.
	defer func() {
		if r := recover(); r == nil {
			t.Error("did not panic")
		}
	}()
	r.applyConfChange(pb.ConfChange{NodeID: 1, Type: pb.ConfChangeRemoveNode}.AsV2())
}

// TestRemoveLearner tests that removeNode could update nodes and
// and removed list correctly.
func TestRemoveLearner(t *testing.T) {
	r := newTestLearnerRaft(1, 10, 1, newTestMemoryStorage(withPeers(1), withLearners(2)))
	r.applyConfChange(pb.ConfChange{NodeID: 2, Type: pb.ConfChangeRemoveNode}.AsV2())
	w := []uint64{1}
	if g := r.prs.VoterNodes(); !reflect.DeepEqual(g, w) {
		t.Errorf("nodes = %v, want %v", g, w)
	}

	w = nil
	if g := r.prs.LearnerNodes(); !reflect.DeepEqual(g, w) {
		t.Errorf("nodes = %v, want %v", g, w)
	}

	// Removing the remaining voter will panic.
	defer func() {
		if r := recover(); r == nil {
			t.Error("did not panic")
		}
	}()
	r.applyConfChange(pb.ConfChange{NodeID: 1, Type: pb.ConfChangeRemoveNode}.AsV2())
}

func TestPromotable(t *testing.T) {
	id := uint64(1)
	tests := []struct {
		peers []uint64
		wp    bool
	}{
		{[]uint64{1}, true},
		{[]uint64{1, 2, 3}, true},
		{[]uint64{}, false},
		{[]uint64{2, 3}, false},
	}
	for i, tt := range tests {
		r := newTestRaft(id, 5, 1, newTestMemoryStorage(withPeers(tt.peers...)))
		if g := r.promotable(); g != tt.wp {
			t.Errorf("#%d: promotable = %v, want %v", i, g, tt.wp)
		}
	}
}

func TestRaftNodes(t *testing.T) {
	tests := []struct {
		ids  []uint64
		wids []uint64
	}{
		{
			[]uint64{1, 2, 3},
			[]uint64{1, 2, 3},
		},
		{
			[]uint64{3, 2, 1},
			[]uint64{1, 2, 3},
		},
	}
	for i, tt := range tests {
		r := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(tt.ids...)))
		if !reflect.DeepEqual(r.prs.VoterNodes(), tt.wids) {
			t.Errorf("#%d: nodes = %+v, want %+v", i, r.prs.VoterNodes(), tt.wids)
		}
	}
}

func TestCampaignWhileLeader(t *testing.T) {
	testCampaignWhileLeader(t, false)
}

func TestPreCampaignWhileLeader(t *testing.T) {
	testCampaignWhileLeader(t, true)
}

func testCampaignWhileLeader(t *testing.T, preVote bool) {
	cfg := newTestConfig(1, 5, 1, newTestMemoryStorage(withPeers(1)))
	cfg.PreVote = preVote
	r := newRaft(cfg)
	if r.state != StateFollower {
		t.Errorf("expected new node to be follower but got %s", r.state)
	}
	// We don't call campaign() directly because it comes after the check
	// for our current state.
	r.Step(pb.Message{From: 1, To: 1, Type: pb.MsgHup})
	if r.state != StateLeader {
		t.Errorf("expected single-node election to become leader but got %s", r.state)
	}
	term := r.Term
	r.Step(pb.Message{From: 1, To: 1, Type: pb.MsgHup})
	if r.state != StateLeader {
		t.Errorf("expected to remain leader but got %s", r.state)
	}
	if r.Term != term {
		t.Errorf("expected to remain in term %v but got %v", term, r.Term)
	}
}

// TestCommitAfterRemoveNode verifies that pending commands can become
// committed when a config change reduces the quorum requirements.
func TestCommitAfterRemoveNode(t *testing.T) {
	// Create a cluster with two nodes.
	s := newTestMemoryStorage(withPeers(1, 2))
	r := newTestRaft(1, 5, 1, s)
	r.becomeCandidate()
	r.becomeLeader()

	// Begin to remove the second node.
	cc := pb.ConfChange{
		Type:   pb.ConfChangeRemoveNode,
		NodeID: 2,
	}
	ccData, err := cc.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	r.Step(pb.Message{
		Type: pb.MsgProp,
		Entries: []pb.Entry{
			{Type: pb.EntryConfChange, Data: ccData},
		},
	})
	// Stabilize the log and make sure nothing is committed yet.
	if ents := nextEnts(r, s); len(ents) > 0 {
		t.Fatalf("unexpected committed entries: %v", ents)
	}
	ccIndex := r.raftLog.lastIndex()

	// While the config change is pending, make another proposal.
	r.Step(pb.Message{
		Type: pb.MsgProp,
		Entries: []pb.Entry{
			{Type: pb.EntryNormal, Data: []byte("hello")},
		},
	})

	// Node 2 acknowledges the config change, committing it.
	r.Step(pb.Message{
		Type:  pb.MsgAppResp,
		From:  2,
		Index: ccIndex,
	})
	ents := nextEnts(r, s)
	if len(ents) != 2 {
		t.Fatalf("expected two committed entries, got %v", ents)
	}
	if ents[0].Type != pb.EntryNormal || ents[0].Data != nil {
		t.Fatalf("expected ents[0] to be empty, but got %v", ents[0])
	}
	if ents[1].Type != pb.EntryConfChange {
		t.Fatalf("expected ents[1] to be EntryConfChange, got %v", ents[1])
	}

	// Apply the config change. This reduces quorum requirements so the
	// pending command can now commit.
	r.applyConfChange(cc.AsV2())
	ents = nextEnts(r, s)
	if len(ents) != 1 || ents[0].Type != pb.EntryNormal ||
		string(ents[0].Data) != "hello" {
		t.Fatalf("expected one committed EntryNormal, got %v", ents)
	}
}

// TestLeaderTransferToUpToDateNode verifies transferring should succeed
// if the transferee has the most up-to-date log entries when transfer starts.
func TestLeaderTransferToUpToDateNode(t *testing.T) {
	nt := newNetwork(nil, nil, nil)
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	lead := nt.peers[1].(*raft)

	if lead.lead != 1 {
		t.Fatalf("after election leader is %x, want 1", lead.lead)
	}

	// Transfer leadership to 2.
	nt.send(pb.Message{From: 2, To: 1, Type: pb.MsgTransferLeader})

	checkLeaderTransferState(t, lead, StateFollower, 2)

	// After some log replication, transfer leadership back to 1.
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{}}})

	nt.send(pb.Message{From: 1, To: 2, Type: pb.MsgTransferLeader})

	checkLeaderTransferState(t, lead, StateLeader, 1)
}

// TestLeaderTransferToUpToDateNodeFromFollower verifies transferring should succeed
// if the transferee has the most up-to-date log entries when transfer starts.
// Not like TestLeaderTransferToUpToDateNode, where the leader transfer message
// is sent to the leader, in this test case every leader transfer message is sent
// to the follower.
func TestLeaderTransferToUpToDateNodeFromFollower(t *testing.T) {
	nt := newNetwork(nil, nil, nil)
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	lead := nt.peers[1].(*raft)

	if lead.lead != 1 {
		t.Fatalf("after election leader is %x, want 1", lead.lead)
	}

	// Transfer leadership to 2.
	nt.send(pb.Message{From: 2, To: 2, Type: pb.MsgTransferLeader})

	checkLeaderTransferState(t, lead, StateFollower, 2)

	// After some log replication, transfer leadership back to 1.
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{}}})

	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgTransferLeader})

	checkLeaderTransferState(t, lead, StateLeader, 1)
}

// TestLeaderTransferWithCheckQuorum ensures transferring leader still works
// even the current leader is still under its leader lease
func TestLeaderTransferWithCheckQuorum(t *testing.T) {
	nt := newNetwork(nil, nil, nil)
	for i := 1; i < 4; i++ {
		r := nt.peers[uint64(i)].(*raft)
		r.checkQuorum = true
		setRandomizedElectionTimeout(r, r.electionTimeout+i)
	}

	// Letting peer 2 electionElapsed reach to timeout so that it can vote for peer 1
	f := nt.peers[2].(*raft)
	for i := 0; i < f.electionTimeout; i++ {
		f.tick()
	}

	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	lead := nt.peers[1].(*raft)

	if lead.lead != 1 {
		t.Fatalf("after election leader is %x, want 1", lead.lead)
	}

	// Transfer leadership to 2.
	nt.send(pb.Message{From: 2, To: 1, Type: pb.MsgTransferLeader})

	checkLeaderTransferState(t, lead, StateFollower, 2)

	// After some log replication, transfer leadership back to 1.
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{}}})

	nt.send(pb.Message{From: 1, To: 2, Type: pb.MsgTransferLeader})

	checkLeaderTransferState(t, lead, StateLeader, 1)
}

func TestLeaderTransferToSlowFollower(t *testing.T) {
	defaultLogger.EnableDebug()
	nt := newNetwork(nil, nil, nil)
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	nt.isolate(3)
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{}}})

	nt.recover()
	lead := nt.peers[1].(*raft)
	if lead.prs.Progress[3].Match != 1 {
		t.Fatalf("node 1 has match %x for node 3, want %x", lead.prs.Progress[3].Match, 1)
	}

	// Transfer leadership to 3 when node 3 is lack of log.
	nt.send(pb.Message{From: 3, To: 1, Type: pb.MsgTransferLeader})

	checkLeaderTransferState(t, lead, StateFollower, 3)
}

func TestLeaderTransferAfterSnapshot(t *testing.T) {
	nt := newNetwork(nil, nil, nil)
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	nt.isolate(3)

	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{}}})
	lead := nt.peers[1].(*raft)
	nextEnts(lead, nt.storage[1])
	nt.storage[1].CreateSnapshot(lead.raftLog.applied, &pb.ConfState{Voters: lead.prs.VoterNodes()}, nil)
	nt.storage[1].Compact(lead.raftLog.applied)

	nt.recover()
	if lead.prs.Progress[3].Match != 1 {
		t.Fatalf("node 1 has match %x for node 3, want %x", lead.prs.Progress[3].Match, 1)
	}

	filtered := pb.Message{}
	// Snapshot needs to be applied before sending MsgAppResp
	nt.msgHook = func(m pb.Message) bool {
		if m.Type != pb.MsgAppResp || m.From != 3 || m.Reject {
			return true
		}
		filtered = m
		return false
	}
	// Transfer leadership to 3 when node 3 is lack of snapshot.
	nt.send(pb.Message{From: 3, To: 1, Type: pb.MsgTransferLeader})
	if lead.state != StateLeader {
		t.Fatalf("node 1 should still be leader as snapshot is not applied, got %x", lead.state)
	}
	if reflect.DeepEqual(filtered, pb.Message{}) {
		t.Fatalf("Follower should report snapshot progress automatically.")
	}

	// Apply snapshot and resume progress
	follower := nt.peers[3].(*raft)
	ready := newReady(follower, &SoftState{}, pb.HardState{})
	nt.storage[3].ApplySnapshot(ready.Snapshot)
	follower.advance(ready)
	nt.msgHook = nil
	nt.send(filtered)

	checkLeaderTransferState(t, lead, StateFollower, 3)
}

func TestLeaderTransferToSelf(t *testing.T) {
	nt := newNetwork(nil, nil, nil)
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	lead := nt.peers[1].(*raft)

	// Transfer leadership to self, there will be noop.
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgTransferLeader})
	checkLeaderTransferState(t, lead, StateLeader, 1)
}

func TestLeaderTransferToNonExistingNode(t *testing.T) {
	nt := newNetwork(nil, nil, nil)
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	lead := nt.peers[1].(*raft)
	// Transfer leadership to non-existing node, there will be noop.
	nt.send(pb.Message{From: 4, To: 1, Type: pb.MsgTransferLeader})
	checkLeaderTransferState(t, lead, StateLeader, 1)
}

func TestLeaderTransferTimeout(t *testing.T) {
	nt := newNetwork(nil, nil, nil)
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	nt.isolate(3)

	lead := nt.peers[1].(*raft)

	// Transfer leadership to isolated node, wait for timeout.
	nt.send(pb.Message{From: 3, To: 1, Type: pb.MsgTransferLeader})
	if lead.leadTransferee != 3 {
		t.Fatalf("wait transferring, leadTransferee = %v, want %v", lead.leadTransferee, 3)
	}
	for i := 0; i < lead.heartbeatTimeout; i++ {
		lead.tick()
	}
	if lead.leadTransferee != 3 {
		t.Fatalf("wait transferring, leadTransferee = %v, want %v", lead.leadTransferee, 3)
	}

	for i := 0; i < lead.electionTimeout-lead.heartbeatTimeout; i++ {
		lead.tick()
	}

	checkLeaderTransferState(t, lead, StateLeader, 1)
}

func TestLeaderTransferIgnoreProposal(t *testing.T) {
	nt := newNetwork(nil, nil, nil)
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	nt.isolate(3)

	lead := nt.peers[1].(*raft)

	// Transfer leadership to isolated node to let transfer pending, then send proposal.
	nt.send(pb.Message{From: 3, To: 1, Type: pb.MsgTransferLeader})
	if lead.leadTransferee != 3 {
		t.Fatalf("wait transferring, leadTransferee = %v, want %v", lead.leadTransferee, 3)
	}

	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{}}})
	err := lead.Step(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{}}})
	if err != ErrProposalDropped {
		t.Fatalf("should return drop proposal error while transferring")
	}

	if lead.prs.Progress[1].Match != 1 {
		t.Fatalf("node 1 has match %x, want %x", lead.prs.Progress[1].Match, 1)
	}
}

func TestLeaderTransferReceiveHigherTermVote(t *testing.T) {
	nt := newNetwork(nil, nil, nil)
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	nt.isolate(3)

	lead := nt.peers[1].(*raft)

	// Transfer leadership to isolated node to let transfer pending.
	nt.send(pb.Message{From: 3, To: 1, Type: pb.MsgTransferLeader})
	if lead.leadTransferee != 3 {
		t.Fatalf("wait transferring, leadTransferee = %v, want %v", lead.leadTransferee, 3)
	}

	nt.send(pb.Message{From: 2, To: 2, Type: pb.MsgHup, Index: 1, Term: 2})

	checkLeaderTransferState(t, lead, StateFollower, 2)
}

func TestLeaderTransferRemoveNode(t *testing.T) {
	nt := newNetwork(nil, nil, nil)
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	nt.ignore(pb.MsgTimeoutNow)

	lead := nt.peers[1].(*raft)

	// The leadTransferee is removed when leadship transferring.
	nt.send(pb.Message{From: 3, To: 1, Type: pb.MsgTransferLeader})
	if lead.leadTransferee != 3 {
		t.Fatalf("wait transferring, leadTransferee = %v, want %v", lead.leadTransferee, 3)
	}

	lead.applyConfChange(pb.ConfChange{NodeID: 3, Type: pb.ConfChangeRemoveNode}.AsV2())

	checkLeaderTransferState(t, lead, StateLeader, 1)
}

func TestLeaderTransferDemoteNode(t *testing.T) {
	nt := newNetwork(nil, nil, nil)
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	nt.ignore(pb.MsgTimeoutNow)

	lead := nt.peers[1].(*raft)

	// The leadTransferee is demoted when leadship transferring.
	nt.send(pb.Message{From: 3, To: 1, Type: pb.MsgTransferLeader})
	if lead.leadTransferee != 3 {
		t.Fatalf("wait transferring, leadTransferee = %v, want %v", lead.leadTransferee, 3)
	}

	lead.applyConfChange(pb.ConfChangeV2{
		Changes: []pb.ConfChangeSingle{
			{
				Type:   pb.ConfChangeRemoveNode,
				NodeID: 3,
			},
			{
				Type:   pb.ConfChangeAddLearnerNode,
				NodeID: 3,
			},
		},
	})

	// Make the Raft group commit the LeaveJoint entry.
	lead.applyConfChange(pb.ConfChangeV2{})
	checkLeaderTransferState(t, lead, StateLeader, 1)
}

// TestLeaderTransferBack verifies leadership can transfer back to self when last transfer is pending.
func TestLeaderTransferBack(t *testing.T) {
	nt := newNetwork(nil, nil, nil)
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	nt.isolate(3)

	lead := nt.peers[1].(*raft)

	nt.send(pb.Message{From: 3, To: 1, Type: pb.MsgTransferLeader})
	if lead.leadTransferee != 3 {
		t.Fatalf("wait transferring, leadTransferee = %v, want %v", lead.leadTransferee, 3)
	}

	// Transfer leadership back to self.
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgTransferLeader})

	checkLeaderTransferState(t, lead, StateLeader, 1)
}

// TestLeaderTransferSecondTransferToAnotherNode verifies leader can transfer to another node
// when last transfer is pending.
func TestLeaderTransferSecondTransferToAnotherNode(t *testing.T) {
	nt := newNetwork(nil, nil, nil)
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	nt.isolate(3)

	lead := nt.peers[1].(*raft)

	nt.send(pb.Message{From: 3, To: 1, Type: pb.MsgTransferLeader})
	if lead.leadTransferee != 3 {
		t.Fatalf("wait transferring, leadTransferee = %v, want %v", lead.leadTransferee, 3)
	}

	// Transfer leadership to another node.
	nt.send(pb.Message{From: 2, To: 1, Type: pb.MsgTransferLeader})

	checkLeaderTransferState(t, lead, StateFollower, 2)
}

// TestLeaderTransferSecondTransferToSameNode verifies second transfer leader request
// to the same node should not extend the timeout while the first one is pending.
func TestLeaderTransferSecondTransferToSameNode(t *testing.T) {
	nt := newNetwork(nil, nil, nil)
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	nt.isolate(3)

	lead := nt.peers[1].(*raft)

	nt.send(pb.Message{From: 3, To: 1, Type: pb.MsgTransferLeader})
	if lead.leadTransferee != 3 {
		t.Fatalf("wait transferring, leadTransferee = %v, want %v", lead.leadTransferee, 3)
	}

	for i := 0; i < lead.heartbeatTimeout; i++ {
		lead.tick()
	}
	// Second transfer leadership request to the same node.
	nt.send(pb.Message{From: 3, To: 1, Type: pb.MsgTransferLeader})

	for i := 0; i < lead.electionTimeout-lead.heartbeatTimeout; i++ {
		lead.tick()
	}

	checkLeaderTransferState(t, lead, StateLeader, 1)
}

func checkLeaderTransferState(t *testing.T, r *raft, state StateType, lead uint64) {
	if r.state != state || r.lead != lead {
		t.Fatalf("after transferring, node has state %v lead %v, want state %v lead %v", r.state, r.lead, state, lead)
	}
	if r.leadTransferee != None {
		t.Fatalf("after transferring, node has leadTransferee %v, want leadTransferee %v", r.leadTransferee, None)
	}
}

// TestTransferNonMember verifies that when a MsgTimeoutNow arrives at
// a node that has been removed from the group, nothing happens.
// (previously, if the node also got votes, it would panic as it
// transitioned to StateLeader)
func TestTransferNonMember(t *testing.T) {
	r := newTestRaft(1, 5, 1, newTestMemoryStorage(withPeers(2, 3, 4)))
	r.Step(pb.Message{From: 2, To: 1, Type: pb.MsgTimeoutNow})

	r.Step(pb.Message{From: 2, To: 1, Type: pb.MsgVoteResp})
	r.Step(pb.Message{From: 3, To: 1, Type: pb.MsgVoteResp})
	if r.state != StateFollower {
		t.Fatalf("state is %s, want StateFollower", r.state)
	}
}

// TestNodeWithSmallerTermCanCompleteElection tests the scenario where a node
// that has been partitioned away (and fallen behind) rejoins the cluster at
// about the same time the leader node gets partitioned away.
// Previously the cluster would come to a standstill when run with PreVote
// enabled.
func TestNodeWithSmallerTermCanCompleteElection(t *testing.T) {
	n1 := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
	n2 := newTestRaft(2, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
	n3 := newTestRaft(3, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))

	n1.becomeFollower(1, None)
	n2.becomeFollower(1, None)
	n3.becomeFollower(1, None)

	n1.preVote = true
	n2.preVote = true
	n3.preVote = true

	// cause a network partition to isolate node 3
	nt := newNetwork(n1, n2, n3)
	nt.cut(1, 3)
	nt.cut(2, 3)

	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	sm := nt.peers[1].(*raft)
	if sm.state != StateLeader {
		t.Errorf("peer 1 state: %s, want %s", sm.state, StateLeader)
	}

	sm = nt.peers[2].(*raft)
	if sm.state != StateFollower {
		t.Errorf("peer 2 state: %s, want %s", sm.state, StateFollower)
	}

	nt.send(pb.Message{From: 3, To: 3, Type: pb.MsgHup})
	sm = nt.peers[3].(*raft)
	if sm.state != StatePreCandidate {
		t.Errorf("peer 3 state: %s, want %s", sm.state, StatePreCandidate)
	}

	nt.send(pb.Message{From: 2, To: 2, Type: pb.MsgHup})

	// check whether the term values are expected
	// a.Term == 3
	// b.Term == 3
	// c.Term == 1
	sm = nt.peers[1].(*raft)
	if sm.Term != 3 {
		t.Errorf("peer 1 term: %d, want %d", sm.Term, 3)
	}

	sm = nt.peers[2].(*raft)
	if sm.Term != 3 {
		t.Errorf("peer 2 term: %d, want %d", sm.Term, 3)
	}

	sm = nt.peers[3].(*raft)
	if sm.Term != 1 {
		t.Errorf("peer 3 term: %d, want %d", sm.Term, 1)
	}

	// check state
	// a == follower
	// b == leader
	// c == pre-candidate
	sm = nt.peers[1].(*raft)
	if sm.state != StateFollower {
		t.Errorf("peer 1 state: %s, want %s", sm.state, StateFollower)
	}
	sm = nt.peers[2].(*raft)
	if sm.state != StateLeader {
		t.Errorf("peer 2 state: %s, want %s", sm.state, StateLeader)
	}
	sm = nt.peers[3].(*raft)
	if sm.state != StatePreCandidate {
		t.Errorf("peer 3 state: %s, want %s", sm.state, StatePreCandidate)
	}

	sm.logger.Infof("going to bring back peer 3 and kill peer 2")
	// recover the network then immediately isolate b which is currently
	// the leader, this is to emulate the crash of b.
	nt.recover()
	nt.cut(2, 1)
	nt.cut(2, 3)

	// call for election
	nt.send(pb.Message{From: 3, To: 3, Type: pb.MsgHup})
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	// do we have a leader?
	sma := nt.peers[1].(*raft)
	smb := nt.peers[3].(*raft)
	if sma.state != StateLeader && smb.state != StateLeader {
		t.Errorf("no leader")
	}
}

// TestPreVoteWithSplitVote verifies that after split vote, cluster can complete
// election in next round.
func TestPreVoteWithSplitVote(t *testing.T) {
	n1 := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
	n2 := newTestRaft(2, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
	n3 := newTestRaft(3, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))

	n1.becomeFollower(1, None)
	n2.becomeFollower(1, None)
	n3.becomeFollower(1, None)

	n1.preVote = true
	n2.preVote = true
	n3.preVote = true

	nt := newNetwork(n1, n2, n3)
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	// simulate leader down. followers start split vote.
	nt.isolate(1)
	nt.send([]pb.Message{
		{From: 2, To: 2, Type: pb.MsgHup},
		{From: 3, To: 3, Type: pb.MsgHup},
	}...)

	// check whether the term values are expected
	// n2.Term == 3
	// n3.Term == 3
	sm := nt.peers[2].(*raft)
	if sm.Term != 3 {
		t.Errorf("peer 2 term: %d, want %d", sm.Term, 3)
	}
	sm = nt.peers[3].(*raft)
	if sm.Term != 3 {
		t.Errorf("peer 3 term: %d, want %d", sm.Term, 3)
	}

	// check state
	// n2 == candidate
	// n3 == candidate
	sm = nt.peers[2].(*raft)
	if sm.state != StateCandidate {
		t.Errorf("peer 2 state: %s, want %s", sm.state, StateCandidate)
	}
	sm = nt.peers[3].(*raft)
	if sm.state != StateCandidate {
		t.Errorf("peer 3 state: %s, want %s", sm.state, StateCandidate)
	}

	// node 2 election timeout first
	nt.send(pb.Message{From: 2, To: 2, Type: pb.MsgHup})

	// check whether the term values are expected
	// n2.Term == 4
	// n3.Term == 4
	sm = nt.peers[2].(*raft)
	if sm.Term != 4 {
		t.Errorf("peer 2 term: %d, want %d", sm.Term, 4)
	}
	sm = nt.peers[3].(*raft)
	if sm.Term != 4 {
		t.Errorf("peer 3 term: %d, want %d", sm.Term, 4)
	}

	// check state
	// n2 == leader
	// n3 == follower
	sm = nt.peers[2].(*raft)
	if sm.state != StateLeader {
		t.Errorf("peer 2 state: %s, want %s", sm.state, StateLeader)
	}
	sm = nt.peers[3].(*raft)
	if sm.state != StateFollower {
		t.Errorf("peer 3 state: %s, want %s", sm.state, StateFollower)
	}
}

// TestPreVoteWithCheckQuorum ensures that after a node become pre-candidate,
// it will checkQuorum correctly.
func TestPreVoteWithCheckQuorum(t *testing.T) {
	n1 := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
	n2 := newTestRaft(2, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
	n3 := newTestRaft(3, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))

	n1.becomeFollower(1, None)
	n2.becomeFollower(1, None)
	n3.becomeFollower(1, None)

	n1.preVote = true
	n2.preVote = true
	n3.preVote = true

	n1.checkQuorum = true
	n2.checkQuorum = true
	n3.checkQuorum = true

	nt := newNetwork(n1, n2, n3)
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	// isolate node 1. node 2 and node 3 have leader info
	nt.isolate(1)

	// check state
	sm := nt.peers[1].(*raft)
	if sm.state != StateLeader {
		t.Fatalf("peer 1 state: %s, want %s", sm.state, StateLeader)
	}
	sm = nt.peers[2].(*raft)
	if sm.state != StateFollower {
		t.Fatalf("peer 2 state: %s, want %s", sm.state, StateFollower)
	}
	sm = nt.peers[3].(*raft)
	if sm.state != StateFollower {
		t.Fatalf("peer 3 state: %s, want %s", sm.state, StateFollower)
	}

	// node 2 will ignore node 3's PreVote
	nt.send(pb.Message{From: 3, To: 3, Type: pb.MsgHup})
	nt.send(pb.Message{From: 2, To: 2, Type: pb.MsgHup})

	// Do we have a leader?
	if n2.state != StateLeader && n3.state != StateFollower {
		t.Errorf("no leader")
	}
}

// TestLearnerCampaign verifies that a learner won't campaign even if it receives
// a MsgHup or MsgTimeoutNow.
func TestLearnerCampaign(t *testing.T) {
	n1 := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(1)))
	n1.applyConfChange(pb.ConfChange{NodeID: 2, Type: pb.ConfChangeAddLearnerNode}.AsV2())
	n2 := newTestRaft(2, 10, 1, newTestMemoryStorage(withPeers(1)))
	n2.applyConfChange(pb.ConfChange{NodeID: 2, Type: pb.ConfChangeAddLearnerNode}.AsV2())
	nt := newNetwork(n1, n2)
	nt.send(pb.Message{From: 2, To: 2, Type: pb.MsgHup})

	if !n2.isLearner {
		t.Fatalf("failed to make n2 a learner")
	}

	if n2.state != StateFollower {
		t.Fatalf("n2 campaigned despite being learner")
	}

	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})
	if n1.state != StateLeader || n1.lead != 1 {
		t.Fatalf("n1 did not become leader")
	}

	// NB: TransferLeader already checks that the recipient is not a learner, but
	// the check could have happened by the time the recipient becomes a learner,
	// in which case it will receive MsgTimeoutNow as in this test case and we
	// verify that it's ignored.
	nt.send(pb.Message{From: 1, To: 2, Type: pb.MsgTimeoutNow})

	if n2.state != StateFollower {
		t.Fatalf("n2 accepted leadership transfer despite being learner")
	}
}

// simulate rolling update a cluster for Pre-Vote. cluster has 3 nodes [n1, n2, n3].
// n1 is leader with term 2
// n2 is follower with term 2
// n3 is partitioned, with term 4 and less log, state is candidate
func newPreVoteMigrationCluster(t *testing.T) *network {
	n1 := newTestRaft(1, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
	n2 := newTestRaft(2, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))
	n3 := newTestRaft(3, 10, 1, newTestMemoryStorage(withPeers(1, 2, 3)))

	n1.becomeFollower(1, None)
	n2.becomeFollower(1, None)
	n3.becomeFollower(1, None)

	n1.preVote = true
	n2.preVote = true
	// We intentionally do not enable PreVote for n3, this is done so in order
	// to simulate a rolling restart process where it's possible to have a mixed
	// version cluster with replicas with PreVote enabled, and replicas without.

	nt := newNetwork(n1, n2, n3)
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

	// Cause a network partition to isolate n3.
	nt.isolate(3)
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{Data: []byte("some data")}}})
	nt.send(pb.Message{From: 3, To: 3, Type: pb.MsgHup})
	nt.send(pb.Message{From: 3, To: 3, Type: pb.MsgHup})

	// check state
	// n1.state == StateLeader
	// n2.state == StateFollower
	// n3.state == StateCandidate
	if n1.state != StateLeader {
		t.Fatalf("node 1 state: %s, want %s", n1.state, StateLeader)
	}
	if n2.state != StateFollower {
		t.Fatalf("node 2 state: %s, want %s", n2.state, StateFollower)
	}
	if n3.state != StateCandidate {
		t.Fatalf("node 3 state: %s, want %s", n3.state, StateCandidate)
	}

	// check term
	// n1.Term == 2
	// n2.Term == 2
	// n3.Term == 4
	if n1.Term != 2 {
		t.Fatalf("node 1 term: %d, want %d", n1.Term, 2)
	}
	if n2.Term != 2 {
		t.Fatalf("node 2 term: %d, want %d", n2.Term, 2)
	}
	if n3.Term != 4 {
		t.Fatalf("node 3 term: %d, want %d", n3.Term, 4)
	}

	// Enable prevote on n3, then recover the network
	n3.preVote = true
	nt.recover()

	return nt
}

func TestPreVoteMigrationCanCompleteElection(t *testing.T) {
	nt := newPreVoteMigrationCluster(t)

	// n1 is leader with term 2
	// n2 is follower with term 2
	// n3 is pre-candidate with term 4, and less log
	n2 := nt.peers[2].(*raft)
	n3 := nt.peers[3].(*raft)

	// simulate leader down
	nt.isolate(1)

	// Call for elections from both n2 and n3.
	nt.send(pb.Message{From: 3, To: 3, Type: pb.MsgHup})
	nt.send(pb.Message{From: 2, To: 2, Type: pb.MsgHup})

	// check state
	// n2.state == Follower
	// n3.state == PreCandidate
	if n2.state != StateFollower {
		t.Errorf("node 2 state: %s, want %s", n2.state, StateFollower)
	}
	if n3.state != StatePreCandidate {
		t.Errorf("node 3 state: %s, want %s", n3.state, StatePreCandidate)
	}

	nt.send(pb.Message{From: 3, To: 3, Type: pb.MsgHup})
	nt.send(pb.Message{From: 2, To: 2, Type: pb.MsgHup})

	// Do we have a leader?
	if n2.state != StateLeader && n3.state != StateFollower {
		t.Errorf("no leader")
	}
}

func TestPreVoteMigrationWithFreeStuckPreCandidate(t *testing.T) {
	nt := newPreVoteMigrationCluster(t)

	// n1 is leader with term 2
	// n2 is follower with term 2
	// n3 is pre-candidate with term 4, and less log
	n1 := nt.peers[1].(*raft)
	n2 := nt.peers[2].(*raft)
	n3 := nt.peers[3].(*raft)

	nt.send(pb.Message{From: 3, To: 3, Type: pb.MsgHup})

	if n1.state != StateLeader {
		t.Errorf("node 1 state: %s, want %s", n1.state, StateLeader)
	}
	if n2.state != StateFollower {
		t.Errorf("node 2 state: %s, want %s", n2.state, StateFollower)
	}
	if n3.state != StatePreCandidate {
		t.Errorf("node 3 state: %s, want %s", n3.state, StatePreCandidate)
	}

	// Pre-Vote again for safety
	nt.send(pb.Message{From: 3, To: 3, Type: pb.MsgHup})

	if n1.state != StateLeader {
		t.Errorf("node 1 state: %s, want %s", n1.state, StateLeader)
	}
	if n2.state != StateFollower {
		t.Errorf("node 2 state: %s, want %s", n2.state, StateFollower)
	}
	if n3.state != StatePreCandidate {
		t.Errorf("node 3 state: %s, want %s", n3.state, StatePreCandidate)
	}

	nt.send(pb.Message{From: 1, To: 3, Type: pb.MsgHeartbeat, Term: n1.Term})

	// Disrupt the leader so that the stuck peer is freed
	if n1.state != StateFollower {
		t.Errorf("state = %s, want %s", n1.state, StateFollower)
	}
	if n3.Term != n1.Term {
		t.Errorf("term = %d, want %d", n3.Term, n1.Term)
	}
}

func testConfChangeCheckBeforeCampaign(t *testing.T, v2 bool) {
	nt := newNetwork(nil, nil, nil)
	n1 := nt.peers[1].(*raft)
	n2 := nt.peers[2].(*raft)
	nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})
	if n1.state != StateLeader {
		t.Errorf("node 1 state: %s, want %s", n1.state, StateLeader)
	}

	// Begin to remove the third node.
	cc := pb.ConfChange{
		Type:   pb.ConfChangeRemoveNode,
		NodeID: 2,
	}
	var ccData []byte
	var err error
	var ty pb.EntryType
	if v2 {
		ccv2 := cc.AsV2()
		ccData, err = ccv2.Marshal()
		ty = pb.EntryConfChangeV2
	} else {
		ccData, err = cc.Marshal()
		ty = pb.EntryConfChange
	}
	if err != nil {
		t.Fatal(err)
	}
	nt.send(pb.Message{
		From: 1,
		To:   1,
		Type: pb.MsgProp,
		Entries: []pb.Entry{
			{Type: ty, Data: ccData},
		},
	})

	// Trigger campaign in node 2
	for i := 0; i < n2.randomizedElectionTimeout; i++ {
		n2.tick()
	}
	// It's still follower because committed conf change is not applied.
	if n2.state != StateFollower {
		t.Errorf("node 2 state: %s, want %s", n2.state, StateFollower)
	}

	// Transfer leadership to peer 2.
	nt.send(pb.Message{From: 2, To: 1, Type: pb.MsgTransferLeader})
	if n1.state != StateLeader {
		t.Errorf("node 1 state: %s, want %s", n1.state, StateLeader)
	}
	// It's still follower because committed conf change is not applied.
	if n2.state != StateFollower {
		t.Errorf("node 2 state: %s, want %s", n2.state, StateFollower)
	}
	// Abort transfer leader
	for i := 0; i < n1.electionTimeout; i++ {
		n1.tick()
	}

	// Advance apply
	nextEnts(n2, nt.storage[2])

	// Transfer leadership to peer 2 again.
	nt.send(pb.Message{From: 2, To: 1, Type: pb.MsgTransferLeader})
	if n1.state != StateFollower {
		t.Errorf("node 1 state: %s, want %s", n1.state, StateFollower)
	}
	if n2.state != StateLeader {
		t.Errorf("node 2 state: %s, want %s", n2.state, StateLeader)
	}

	nextEnts(n1, nt.storage[1])
	// Trigger campaign in node 2
	for i := 0; i < n1.randomizedElectionTimeout; i++ {
		n1.tick()
	}
	if n1.state != StateCandidate {
		t.Errorf("node 1 state: %s, want %s", n1.state, StateCandidate)
	}
}

// Tests if unapplied ConfChange is checked before campaign.
func TestConfChangeCheckBeforeCampaign(t *testing.T) {
	testConfChangeCheckBeforeCampaign(t, false)
}

// Tests if unapplied ConfChangeV2 is checked before campaign.
func TestConfChangeV2CheckBeforeCampaign(t *testing.T) {
	testConfChangeCheckBeforeCampaign(t, true)
}

// Tests the commit index can be forwarded by vote request
func TestForwardCommitIndexByVoteRequest(t *testing.T) {
	tests := []pb.ConfChangeI{
		// Simple confchange
		pb.ConfChange{Type: pb.ConfChangeAddNode, NodeID: 4},
		// Enter joint confchange
		pb.ConfChangeV2{
			Changes: []pb.ConfChangeSingle{
				{
					Type:   pb.ConfChangeAddLearnerNode,
					NodeID: 3,
				},
				{
					Type:   pb.ConfChangeAddNode,
					NodeID: 4,
				},
			},
		},
	}
	for _, cc := range tests {
		n1 := newTestLearnerRaft(1, []uint64{1, 2, 3}, []uint64{4}, 10, 1, NewMemoryStorage())
		n2 := newTestLearnerRaft(2, []uint64{1, 2, 3}, []uint64{4}, 10, 1, NewMemoryStorage())
		n3 := newTestLearnerRaft(3, []uint64{1, 2, 3}, []uint64{4}, 10, 1, NewMemoryStorage())
		n4 := newTestLearnerRaft(4, []uint64{1, 2, 3}, []uint64{4}, 10, 1, NewMemoryStorage())

		nt := newNetwork(n1, n2, n3, n4)
		nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

		typ, ccData, err := pb.MarshalConfChange(cc)
		if err != nil {
			t.Fatal(err)
		}

		// propose a confchange entry but don't let it commit
		nt.ignore(pb.MsgAppResp)
		nt.send(pb.Message{
			From:    1,
			To:      1,
			Type:    pb.MsgProp,
			Entries: []pb.Entry{{Type: typ, Data: ccData}},
		})
		confChangeIndex := n1.raftLog.lastIndex()

		// let node 4 have more up to data log than other voter
		nt.recover()
		nt.cut(1, 2)
		nt.cut(1, 3)
		nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{Data: []byte("somedata")}}})

		// let the confchange entry commit but don't let node 4 know
		nt.recover()
		nt.cut(1, 4)
		nt.ignore(pb.MsgApp)
		nt.send(pb.Message{From: 2, To: 1, Type: pb.MsgAppResp, Index: n2.raftLog.lastIndex()})
		nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgBeat})

		// simulate the leader down
		nt.recover()
		nt.isolate(1)

		if n4.raftLog.committed >= confChangeIndex {
			t.Fatalf("expected node 4 commit index less than %d, got %d", confChangeIndex, n4.raftLog.committed)
		}
		// node 4 can't start new election because it think itself is a learner
		nt.send(pb.Message{From: 4, To: 4, Type: pb.MsgHup})
		if n4.state != StateFollower {
			t.Fatalf("node 4 state: %s, want %s", n4.state, StateFollower)
		}

		if n2.raftLog.committed < confChangeIndex {
			t.Fatalf("expected node 2 commit index not less than %d, got %d", confChangeIndex, n2.raftLog.committed)
		} else {
			n2.applyConfChange(cc.AsV2())
			n2.raftLog.appliedTo(confChangeIndex)
		}
		// node 2 need votes from both node 3 and node 4, but node 4 will reject it
		nt.send(pb.Message{From: 2, To: 2, Type: pb.MsgHup})
		if n2.state == StateLeader {
			t.Fatalf("node 2 state: %s, want %s", n2.state, StateCandidate)
		}

		// node 4's commit index should be forwarded by node 2's vote request
		if n4.raftLog.committed < confChangeIndex {
			t.Fatalf("expected node 4 commit index not less than %d, got %d", confChangeIndex, n4.raftLog.committed)
		} else {
			n4.applyConfChange(cc.AsV2())
			n4.raftLog.appliedTo(confChangeIndex)
		}

		// now node 4 can start new election and become leader
		nt.send(pb.Message{From: 4, To: 4, Type: pb.MsgHup})
		if n4.state != StateLeader {
			t.Fatalf("node 4 state: %s, want %s", n4.state, StateLeader)
		}
	}
}

// Tests the commit index can be forwarded by vote response
func TestForwardCommitIndexByVoteResponse(t *testing.T) {
	tests := []pb.ConfChangeI{
		// Simple confchange
		pb.ConfChange{Type: pb.ConfChangeRemoveNode, NodeID: 4},
		// Leave joint confchange
		pb.ConfChangeV2{},
	}
	// Enter joint confchange
	enterJoint := pb.ConfChangeV2{
		Changes: []pb.ConfChangeSingle{
			{
				Type:   pb.ConfChangeAddNode,
				NodeID: 3,
			},
			{
				Type:   pb.ConfChangeAddLearnerNode,
				NodeID: 4,
			},
		},
	}
	for _, cc := range tests {
		nt := newNetwork(nil, nil, nil, nil)
		n1 := nt.peers[1].(*raft)
		n2 := nt.peers[2].(*raft)
		n3 := nt.peers[3].(*raft)
		n4 := nt.peers[4].(*raft)

		// Joint confchange, let's enter joint first
		if _, isSimple := cc.AsV1(); !isSimple {
			n1.applyConfChange(enterJoint)
			n2.applyConfChange(enterJoint)
			n3.applyConfChange(enterJoint)
			n4.applyConfChange(enterJoint)
		}

		nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgHup})

		typ, ccData, err := pb.MarshalConfChange(cc)
		if err != nil {
			t.Fatal(err)
		}

		// propose a confchange entry but don't let it commit
		nt.ignore(pb.MsgAppResp)
		nt.send(pb.Message{
			From:    1,
			To:      1,
			Type:    pb.MsgProp,
			Entries: []pb.Entry{{Type: typ, Data: ccData}},
		})
		confChangeIndex := n1.raftLog.lastIndex()

		// let node 4 have more up to data log than other voter
		nt.recover()
		nt.cut(1, 2)
		nt.cut(1, 3)
		nt.send(pb.Message{From: 1, To: 1, Type: pb.MsgProp, Entries: []pb.Entry{{Data: []byte("somedata")}}})

		// A delayed MsgAppResp message make the confchange entry become committed
		nt.send(pb.Message{From: 2, To: 1, Type: pb.MsgAppResp, Index: n2.raftLog.lastIndex()})

		// simulate the leader down
		nt.recover()
		nt.isolate(1)

		if n4.raftLog.committed < confChangeIndex {
			t.Fatalf("expected node 4 commit index not less than %d, got %d", confChangeIndex, n4.raftLog.committed)
		} else {
			n4.applyConfChange(cc.AsV2())
			n4.raftLog.appliedTo(confChangeIndex)
		}
		// node 4 can't start new election because it think itself is a learner
		nt.send(pb.Message{From: 4, To: 4, Type: pb.MsgHup})
		if n4.state != StateFollower {
			t.Fatalf("node 4 state: %s, want %s", n4.state, StateFollower)
		}

		if n2.raftLog.committed >= confChangeIndex {
			t.Fatalf("expected node 2 commit index less than %d, got %d", confChangeIndex, n2.raftLog.committed)
		}
		// node 2 can't become leader, because it need votes from both node 3 and node 4, but node 4 will reject it
		nt.send(pb.Message{From: 2, To: 2, Type: pb.MsgHup})
		if n2.state == StateLeader {
			t.Fatalf("unexpected node 2 state: %s, want %s", n2.state, StateCandidate)
		}

		// node 2's commit index should be forwarded by vote response
		if n2.raftLog.committed < confChangeIndex {
			t.Fatalf("expected node 2 commit index not less than %d, got %d", confChangeIndex, n2.raftLog.committed)
		} else {
			n2.applyConfChange(cc.AsV2())
			n2.raftLog.appliedTo(confChangeIndex)
		}

		// now node 2 only need vote from node 3
		nt.send(pb.Message{From: 2, To: 2, Type: pb.MsgHup})
		if n2.state != StateLeader {
			t.Fatalf("node 2 state: %s, want %s", n2.state, StateLeader)
		}
	}
}

func entsWithConfig(configFunc func(*Config), terms ...uint64) *raft {
	storage := NewMemoryStorage()
	for i, term := range terms {
		storage.Append([]pb.Entry{{Index: uint64(i + 1), Term: term}})
	}
	cfg := newTestConfig(1, 5, 1, storage)
	if configFunc != nil {
		configFunc(cfg)
	}
	sm := newRaft(cfg)
	sm.reset(terms[len(terms)-1])
	return sm
}

// votedWithConfig creates a raft state machine with Vote and Term set
// to the given value but no log entries (indicating that it voted in
// the given term but has not received any logs).
func votedWithConfig(configFunc func(*Config), vote, term uint64) *raft {
	storage := NewMemoryStorage()
	storage.SetHardState(pb.HardState{Vote: vote, Term: term})
	cfg := newTestConfig(1, 5, 1, storage)
	if configFunc != nil {
		configFunc(cfg)
	}
	sm := newRaft(cfg)
	sm.reset(term)
	return sm
}

type network struct {
	peers   map[uint64]stateMachine
	storage map[uint64]*MemoryStorage
	dropm   map[connem]float64
	ignorem map[pb.MessageType]bool

	// msgHook is called for each message sent. It may inspect the
	// message and return true to send it or false to drop it.
	msgHook func(pb.Message) bool
}

// newNetwork initializes a network from peers.
// A nil node will be replaced with a new *stateMachine.
// A *stateMachine will get its k, id.
// When using stateMachine, the address list is always [1, n].
func newNetwork(peers ...stateMachine) *network {
	return newNetworkWithConfig(nil, peers...)
}

// newNetworkWithConfig is like newNetwork but calls the given func to
// modify the configuration of any state machines it creates.
func newNetworkWithConfig(configFunc func(*Config), peers ...stateMachine) *network {
	size := len(peers)
	peerAddrs := idsBySize(size)

	npeers := make(map[uint64]stateMachine, size)
	nstorage := make(map[uint64]*MemoryStorage, size)

	for j, p := range peers {
		id := peerAddrs[j]
		switch v := p.(type) {
		case nil:
			nstorage[id] = newTestMemoryStorage(withPeers(peerAddrs...))
			cfg := newTestConfig(id, 10, 1, nstorage[id])
			if configFunc != nil {
				configFunc(cfg)
			}
			sm := newRaft(cfg)
			npeers[id] = sm
		case *raft:
			// TODO(tbg): this is all pretty confused. Clean this up.
			learners := make(map[uint64]bool, len(v.prs.Learners))
			for i := range v.prs.Learners {
				learners[i] = true
			}
			v.id = id
			v.prs = tracker.MakeProgressTracker(v.prs.MaxInflight)
			if len(learners) > 0 {
				v.prs.Learners = map[uint64]struct{}{}
			}
			for i := 0; i < size; i++ {
				pr := &tracker.Progress{}
				if _, ok := learners[peerAddrs[i]]; ok {
					pr.IsLearner = true
					v.prs.Learners[peerAddrs[i]] = struct{}{}
				} else {
					v.prs.Voters[0][peerAddrs[i]] = struct{}{}
				}
				v.prs.Progress[peerAddrs[i]] = pr
			}
			v.reset(v.Term)
			npeers[id] = v
		case *blackHole:
			npeers[id] = v
		default:
			panic(fmt.Sprintf("unexpected state machine type: %T", p))
		}
	}
	return &network{
		peers:   npeers,
		storage: nstorage,
		dropm:   make(map[connem]float64),
		ignorem: make(map[pb.MessageType]bool),
	}
}

func preVoteConfig(c *Config) {
	c.PreVote = true
}

func (nw *network) send(msgs ...pb.Message) {
	for len(msgs) > 0 {
		m := msgs[0]
		p := nw.peers[m.To]
		p.Step(m)
		msgs = append(msgs[1:], nw.filter(p.readMessages())...)
	}
}

func (nw *network) drop(from, to uint64, perc float64) {
	nw.dropm[connem{from, to}] = perc
}

func (nw *network) cut(one, other uint64) {
	nw.drop(one, other, 2.0) // always drop
	nw.drop(other, one, 2.0) // always drop
}

func (nw *network) isolate(id uint64) {
	for i := 0; i < len(nw.peers); i++ {
		nid := uint64(i) + 1
		if nid != id {
			nw.drop(id, nid, 1.0) // always drop
			nw.drop(nid, id, 1.0) // always drop
		}
	}
}

func (nw *network) ignore(t pb.MessageType) {
	nw.ignorem[t] = true
}

func (nw *network) recover() {
	nw.dropm = make(map[connem]float64)
	nw.ignorem = make(map[pb.MessageType]bool)
}

func (nw *network) filter(msgs []pb.Message) []pb.Message {
	mm := []pb.Message{}
	for _, m := range msgs {
		if nw.ignorem[m.Type] {
			continue
		}
		switch m.Type {
		case pb.MsgHup:
			// hups never go over the network, so don't drop them but panic
			panic("unexpected msgHup")
		default:
			perc := nw.dropm[connem{m.From, m.To}]
			if n := rand.Float64(); n < perc {
				continue
			}
		}
		if nw.msgHook != nil {
			if !nw.msgHook(m) {
				continue
			}
		}
		mm = append(mm, m)
	}
	return mm
}

type connem struct {
	from, to uint64
}

type blackHole struct{}

func (blackHole) Step(pb.Message) error      { return nil }
func (blackHole) readMessages() []pb.Message { return nil }

var nopStepper = &blackHole{}

func idsBySize(size int) []uint64 {
	ids := make([]uint64, size)
	for i := 0; i < size; i++ {
		ids[i] = 1 + uint64(i)
	}
	return ids
}

// setRandomizedElectionTimeout set up the value by caller instead of choosing
// by system, in some test scenario we need to fill in some expected value to
// ensure the certainty
func setRandomizedElectionTimeout(r *raft, v int) {
	r.randomizedElectionTimeout = v
}

func newTestConfig(id uint64, election, heartbeat int, storage Storage) *Config {
	return &Config{
		ID:              id,
		ElectionTick:    election,
		HeartbeatTick:   heartbeat,
		Storage:         storage,
		MaxSizePerMsg:   noLimit,
		MaxInflightMsgs: 256,
	}
}

type testMemoryStorageOptions func(*MemoryStorage)

func withPeers(peers ...uint64) testMemoryStorageOptions {
	return func(ms *MemoryStorage) {
		ms.snapshot.Metadata.ConfState.Voters = peers
	}
}

func withLearners(learners ...uint64) testMemoryStorageOptions {
	return func(ms *MemoryStorage) {
		ms.snapshot.Metadata.ConfState.Learners = learners
	}
}

func newTestMemoryStorage(opts ...testMemoryStorageOptions) *MemoryStorage {
	ms := NewMemoryStorage()
	for _, o := range opts {
		o(ms)
	}
	return ms
}

func newTestRaft(id uint64, election, heartbeat int, storage Storage) *raft {
	return newRaft(newTestConfig(id, election, heartbeat, storage))
}

func newTestLearnerRaft(id uint64, election, heartbeat int, storage Storage) *raft {
	cfg := newTestConfig(id, election, heartbeat, storage)
	return newRaft(cfg)
}

// newTestRawNode sets up a RawNode with the given peers. The configuration will
// not be reflected in the Storage.
func newTestRawNode(id uint64, election, heartbeat int, storage Storage) *RawNode {
	cfg := newTestConfig(id, election, heartbeat, storage)
	rn, err := NewRawNode(cfg)
	if err != nil {
		panic(err)
	}
	return rn
}
