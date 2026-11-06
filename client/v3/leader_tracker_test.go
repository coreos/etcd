// Copyright 2026 The etcd Authors
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

package clientv3

import (
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"go.etcd.io/etcd/client/v3/internal/resolver"
)

func TestSelectLeader(t *testing.T) {
	member := func(endpoint string, memberID, leaderID uint64, isLearner bool) leaderStatus {
		return leaderStatus{endpoint: endpoint, memberID: memberID, leaderID: leaderID, isLearner: isLearner}
	}
	tests := []struct {
		name     string
		statuses []leaderStatus
		want     string
	}{
		{
			name: "all voters agree and the leader confirms itself",
			statuses: []leaderStatus{
				member("a", 1, 1, false),
				member("b", 2, 1, false),
				member("c", 3, 1, false),
			},
			want: "a",
		},
		{
			name: "a learner with a stale view is ignored",
			statuses: []leaderStatus{
				member("a", 1, 1, false),
				member("b", 2, 1, false),
				member("d", 4, 2, true),
			},
			want: "a",
		},
		{
			name: "disagreeing voters fall back to round_robin",
			statuses: []leaderStatus{
				member("a", 1, 1, false),
				member("b", 2, 2, false),
			},
			want: "",
		},
		{
			name: "a voter without a leader view blocks the hint",
			statuses: []leaderStatus{
				member("a", 1, 1, false),
				member("b", 2, 0, false),
			},
			want: "",
		},
		{
			name: "peer reports alone are insufficient without self-confirmation",
			statuses: []leaderStatus{
				member("b", 2, 1, false),
				member("c", 3, 1, false),
			},
			want: "",
		},
		{
			// Both endpoints reach the leader member, so either is a valid
			// route; the later report wins the map slot.
			name: "two endpoints reaching the same member deduplicate",
			statuses: []leaderStatus{
				member("a", 1, 1, false),
				member("a-alt", 1, 1, false),
				member("b", 2, 1, false),
			},
			want: "a-alt",
		},
		{
			name: "two endpoints reaching the same member must agree",
			statuses: []leaderStatus{
				member("a", 1, 1, false),
				member("a-alt", 1, 2, false),
				member("b", 2, 1, false),
			},
			want: "",
		},
		{
			name:     "no responses",
			statuses: nil,
			want:     "",
		},
		{
			name:     "only learners responded",
			statuses: []leaderStatus{member("d", 4, 1, true)},
			want:     "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := selectLeader(tt.statuses); got != tt.want {
				t.Errorf("selectLeader() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewLeaderTrackerDefaults(t *testing.T) {
	tracker := newLeaderTracker(&Client{})
	if tracker.refreshInterval != 30*time.Second {
		t.Errorf("refreshInterval = %s, want 30s", tracker.refreshInterval)
	}
	if tracker.rediscoveryDelay != 3*time.Second {
		t.Errorf("rediscoveryDelay = %s, want 3s", tracker.rediscoveryDelay)
	}
	if tracker.statusTimeout != 5*time.Second {
		t.Errorf("statusTimeout = %s, want 5s", tracker.statusTimeout)
	}

	cfg := Config{}.
		WithLeaderAwareRefreshInterval(15 * time.Second).
		WithLeaderAwareRediscoveryDelay(time.Second).
		WithLeaderAwareStatusTimeout(2 * time.Second)
	custom := newLeaderTracker(&Client{cfg: cfg})
	if custom.refreshInterval != 15*time.Second ||
		custom.rediscoveryDelay != time.Second ||
		custom.statusTimeout != 2*time.Second {
		t.Errorf("custom settings not applied: %+v", custom)
	}

	// Non-positive values restore the defaults.
	nonPositive := newLeaderTracker(&Client{cfg: Config{}.WithLeaderAwareRefreshInterval(-time.Second)})
	if nonPositive.refreshInterval != 30*time.Second {
		t.Errorf("negative refreshInterval = %s, want 30s default", nonPositive.refreshInterval)
	}
}

func newTestLeaderTracker() *leaderTracker {
	client := &Client{
		epMu:               &sync.RWMutex{},
		endpoints:          []string{"http://127.0.0.1:2379"},
		endpointGeneration: 1,
		resolver:           resolver.New("http://127.0.0.1:2379"),
	}
	client.lg.Store(zap.NewNop())
	client.resolver.SetEndpoints([]string{"http://127.0.0.1:2379"}, 1)
	return &leaderTracker{
		client:      client,
		invalidatec: make(chan struct{}, 1),
		donec:       make(chan struct{}),
	}
}

func (tracker *leaderTracker) pending() bool {
	return tracker.pendingInvalidation.Load()
}

func TestInvalidateHint(t *testing.T) {
	t.Run("zero hint ID is a no-op", func(t *testing.T) {
		tracker := newTestLeaderTracker()
		tracker.invalidateHint(0, "127.0.0.1:2379")
		if tracker.pending() {
			t.Fatal("zero hint ID scheduled an invalidation")
		}
	})

	t.Run("no current hint is a no-op", func(t *testing.T) {
		tracker := newTestLeaderTracker()
		tracker.invalidateHint(1, "127.0.0.1:2379")
		if tracker.pending() {
			t.Fatal("failure without a current hint scheduled an invalidation")
		}
	})

	t.Run("only the current hint clears itself", func(t *testing.T) {
		tracker := newTestLeaderTracker()
		tracker.current.Store(&hintIdentity{id: 1, address: "127.0.0.1:2379"})
		tracker.invalidateHint(2, "127.0.0.2:2379")
		if tracker.current.Load() == nil {
			t.Fatal("a stale failure cleared the current hint")
		}
		if tracker.pending() {
			t.Fatal("a stale failure at a different address scheduled an invalidation")
		}
	})

	t.Run("a stale failure at the repeated address forces rediscovery", func(t *testing.T) {
		tracker := newTestLeaderTracker()
		current := &hintIdentity{id: 2, address: "127.0.0.1:2379"}
		tracker.current.Store(current)
		// Hint 1 failed after hint 2 republished the same address: the newer
		// probes predate this failure, so rediscover instead of trusting it.
		// The hint stays published until the tracker goroutine clears it.
		tracker.invalidateHint(1, "127.0.0.1:2379")
		if tracker.current.Load() != current {
			t.Fatal("a stale failure replaced the current hint")
		}
		if !tracker.pending() {
			t.Fatal("a stale failure at the current address did not schedule rediscovery")
		}
	})

	t.Run("the current hint clears itself once", func(t *testing.T) {
		tracker := newTestLeaderTracker()
		tracker.current.Store(&hintIdentity{id: 1, address: "127.0.0.1:2379"})
		tracker.invalidateHint(1, "127.0.0.1:2379")
		if tracker.current.Load() != nil {
			t.Fatal("the current hint did not clear itself")
		}
		if !tracker.pending() {
			t.Fatal("clearing the hint did not schedule an invalidation")
		}
	})
}

func TestPublishFencing(t *testing.T) {
	const leader = "http://127.0.0.1:2379"

	t.Run("publishes and deduplicates", func(t *testing.T) {
		tracker := newTestLeaderTracker()
		if !tracker.publish(0, 1, leader, 1) {
			t.Fatal("first publish was not a change")
		}
		if tracker.hintID != 1 || tracker.hintAddress != leader {
			t.Fatalf("hint = %d at %q, want 1 at %q", tracker.hintID, tracker.hintAddress, leader)
		}
		if hint := tracker.current.Load(); hint == nil || hint.memberID != 1 {
			t.Fatal("publish did not store the leader member ID")
		}
		if tracker.publish(0, 1, leader, 1) {
			t.Fatal("republishing the same leader reported a change")
		}
	})

	t.Run("rejects a stale endpoint generation and rolls back", func(t *testing.T) {
		tracker := newTestLeaderTracker()
		if tracker.publish(0, 2, leader, 1) {
			t.Fatal("publish accepted a generation the resolver does not have")
		}
		if tracker.current.Load() != nil {
			t.Fatal("rejected publish left a hint identity stored")
		}
		if tracker.hintAddress != "" || tracker.hintID != 0 {
			t.Fatalf("rejected publish set hint %d at %q", tracker.hintID, tracker.hintAddress)
		}
	})

	t.Run("refuses to publish over a pending invalidation", func(t *testing.T) {
		tracker := newTestLeaderTracker()
		tracker.signalInvalidation()
		if tracker.publish(tracker.epoch.Load(), 1, leader, 1) {
			t.Fatal("publish succeeded with a pending invalidation")
		}
		if !tracker.consumeInvalidation() {
			t.Fatal("consumeInvalidation found nothing pending")
		}
		if tracker.hintAddress != "" {
			t.Fatalf("consumeInvalidation left hint %q", tracker.hintAddress)
		}
		if !tracker.publish(tracker.epoch.Load(), 1, leader, 1) {
			t.Fatal("publish after consuming the invalidation was rejected")
		}
	})

	t.Run("clear after invalidation removes the published hint", func(t *testing.T) {
		tracker := newTestLeaderTracker()
		if !tracker.publish(0, 1, leader, 1) {
			t.Fatal("first publish was not a change")
		}
		tracker.invalidate()
		if tracker.current.Load() != nil {
			t.Fatal("invalidate did not clear the hint identity")
		}
		if !tracker.consumeInvalidation() {
			t.Fatal("consumeInvalidation found nothing pending")
		}
		if tracker.hintAddress != "" || tracker.hintID != 0 {
			t.Fatalf("clear left hint %d at %q", tracker.hintID, tracker.hintAddress)
		}
		// The resolver hint was cleared for the current generation, so a
		// late publish from the pre-invalidation epoch stays rejected.
		if tracker.publish(0, 1, leader, 1) {
			t.Fatal("publish from the pre-invalidation epoch succeeded")
		}
	})
}
