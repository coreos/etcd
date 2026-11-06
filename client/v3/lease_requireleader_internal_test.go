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
	"context"
	"testing"
	"time"
)

// TestLessorCloseRequireLeaderRemovesOrphanedKeepAlive verifies that
// closeRequireLeader removes the keepAlive entry from l.keepAlives once every
// one of its subscriptions required a leader (see etcd-io/etcd#22094). Without
// the deletion the entry stays in the map and its keepalive goroutine keeps
// sending keepalive requests forever after leader loss.
func TestLessorCloseRequireLeaderRemovesOrphanedKeepAlive(t *testing.T) {
	l := &lessor{
		donec:                 make(chan struct{}),
		keepAlives:            make(map[LeaseID]*keepAlive),
		firstKeepAliveTimeout: 5 * time.Second,
	}

	ch := make(chan *LeaseKeepAliveResponse)
	reqLeaderCtx := WithRequireLeader(context.Background())
	l.keepAlives[1] = &keepAlive{
		chs:           []chan<- *LeaseKeepAliveResponse{ch},
		ctxs:          []context.Context{reqLeaderCtx},
		deadline:      time.Now().Add(time.Second),
		nextKeepAlive: time.Now(),
		donec:         make(chan struct{}),
	}

	l.closeRequireLeader()

	if len(l.keepAlives) != 0 {
		t.Fatalf("expected orphaned keepAlive entry to be removed from l.keepAlives, got %d entr(y/ies): %v",
			len(l.keepAlives), l.keepAlives)
	}

	select {
	case <-ch:
		// expected: channel was closed by closeRequireLeader
	default:
		t.Fatal("expected keepAlive channel to be closed by closeRequireLeader")
	}
}
