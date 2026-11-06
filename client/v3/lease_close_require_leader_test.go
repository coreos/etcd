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

	"github.com/stretchr/testify/require"
)

// subscriberKey tags a context with the name of the test subscriber that
// created it, so surviving pairings can be checked by identity rather than
// by channel/context equality.
type subscriberKey struct{}

func TestLessorCloseRequireLeaderKeepsSurvivorsPairedWithOwnContext(t *testing.T) {
	newSubscriber := func(name string, requireLeader bool) (chan *LeaseKeepAliveResponse, context.Context) {
		ch := make(chan *LeaseKeepAliveResponse, 1)
		ctx := context.WithValue(t.Context(), subscriberKey{}, name)
		if requireLeader {
			ctx = WithRequireLeader(ctx)
		}
		return ch, ctx
	}

	// Three subscribers keep-aliving the same lease: A requires a leader and
	// will be removed by closeRequireLeader, B and C are plain subscribers
	// that should survive with their own contexts intact.
	chA, ctxA := newSubscriber("A", true)
	chB, ctxB := newSubscriber("B", false)
	chC, ctxC := newSubscriber("C", false)

	ka := &keepAlive{
		chs:   []chan<- *LeaseKeepAliveResponse{chA, chB, chC},
		ctxs:  []context.Context{ctxA, ctxB, ctxC},
		donec: make(chan struct{}),
	}
	l := &lessor{keepAlives: map[LeaseID]*keepAlive{1: ka}}

	l.closeRequireLeader()

	survivors, ok := l.keepAlives[1]
	require.True(t, ok, "keepAlive entry should still exist for surviving subscribers")
	require.Len(t, survivors.chs, 2, "the requireLeader subscriber should have been removed")
	require.Len(t, survivors.ctxs, 2)

	for i, ch := range survivors.chs {
		var want string
		switch ch {
		case chB:
			want = "B"
		case chC:
			want = "C"
		default:
			t.Fatalf("survivor chs[%d] is not one of the surviving subscriber channels", i)
		}
		owner := survivors.ctxs[i].Value(subscriberKey{})
		require.Equalf(t, want, owner,
			"chs[%d] belongs to subscriber %s but got paired with a context owned by %v", i, want, owner)
	}

	_, stillOpen := <-chA
	require.False(t, stillOpen, "the requireLeader subscriber's channel should have been closed")
}
