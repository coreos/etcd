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

package mvcc

import (
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"go.etcd.io/etcd/server/v3/lease"
	betesting "go.etcd.io/etcd/server/v3/storage/backend/testing"
)

// newSyncWatchersStore builds a watchableStore with empty watcher groups, so a test controls
// unsynced membership and drives syncWatchers by hand rather than racing the background loop.
func newSyncWatchersStore(t *testing.T) *watchableStore {
	b, _ := betesting.NewDefaultTmpBackend(t)
	s := &watchableStore{
		store:    NewStore(zaptest.NewLogger(t), b, &lease.FakeLessor{}, StoreConfig{}),
		unsynced: newWatcherGroup(),
		synced:   newWatcherGroup(),
		// Close() closes stopc and waits on wg; no loops are started, so wg is already done.
		stopc: make(chan struct{}),
	}
	t.Cleanup(func() { cleanup(s, b) })
	return s
}

// putRevisions writes n revisions over keyCount keys, so the revision window grows while the key
// count stays constant.
func putRevisions(s *watchableStore, keyCount, n int) {
	for i := 0; i < n; i++ {
		s.Put([]byte(fmt.Sprintf("k%d", i%keyCount)), []byte("v"), lease.NoLease)
	}
}

func drainModRevs(ws *watchStream) []int64 {
	var out []int64
	for {
		select {
		case wr := <-ws.ch:
			for _, e := range wr.Events {
				out = append(out, e.Kv.ModRevision)
			}
		default:
			return out
		}
	}
}

// A watcher behind by more than watchBatchMaxRevs is caught up over several passes, and must
// receive every revision exactly once with no gaps across the window boundaries.
func TestSyncWatchersBoundedScanDeliversEveryRevisionOnce(t *testing.T) {
	s := newSyncWatchersStore(t)

	const backlog = 5000
	putRevisions(s, 10, backlog)

	ws := s.NewWatchStream().(*watchStream)
	defer ws.Close()
	id, err := ws.Watch(t.Context(), 0, []byte("k0"), []byte("l"), 1)
	require.NoError(t, err)
	w := ws.watchers[id]

	var got []int64
	for pass := 0; s.unsynced.size() > 0; pass++ {
		require.Lessf(t, pass, 100, "did not converge; minRev=%d curRev=%d", w.minRev, s.Rev())
		s.syncWatchers()
		got = append(got, drainModRevs(ws)...)
	}

	require.NotEmpty(t, got)
	seen := make(map[int64]bool, len(got))
	prev := int64(0)
	for _, r := range got {
		require.Greaterf(t, r, prev, "revisions not strictly ascending")
		require.Falsef(t, seen[r], "revision %d delivered twice", r)
		seen[r] = true
		prev = r
	}
	for r := got[0]; r <= got[len(got)-1]; r++ {
		require.Truef(t, seen[r], "gap: revision %d missing from [%d,%d]", r, got[0], got[len(got)-1])
	}
}

// A bounded window is anchored at the most-behind watcher, so watchers that are nearly caught up
// fall outside it. They must still be served, rather than waiting for the deep watcher to catch up.
func TestSyncWatchersShallowWatcherNotBlockedByDeepWatcher(t *testing.T) {
	s := newSyncWatchersStore(t)

	putRevisions(s, 10, 20000)

	ws := s.NewWatchStream().(*watchStream)
	defer ws.Close()

	deepID, err := ws.Watch(t.Context(), 0, []byte("k0"), []byte("l"), 1)
	require.NoError(t, err)
	shallowID, err := ws.Watch(t.Context(), 1, []byte("k0"), []byte("l"), s.Rev()-10)
	require.NoError(t, err)
	deep, shallow := ws.watchers[deepID], ws.watchers[shallowID]

	s.syncWatchers()

	require.NotContainsf(t, s.unsynced.watchers, shallow,
		"nearly caught-up watcher still unsynced after one pass (minRev=%d curRev=%d) while the "+
			"deep watcher is at minRev=%d", shallow.minRev, s.Rev(), deep.minRev)
	require.Containsf(t, s.unsynced.watchers, deep, "deep watcher should still be catching up")
	require.Greaterf(t, deep.minRev, int64(1), "deep watcher should have made progress")
}

// Restore moves every synced watcher into the unsynced group, including watchers whose minRev is
// in the future -- grpcproxy watches at math.MaxInt64-2. Deriving a scan ceiling from such a minRev
// overflows, which would leave the watcher stranded in the unsynced group.
func TestSyncWatchersFutureRevisionWatcherNotStranded(t *testing.T) {
	s := newSyncWatchersStore(t)

	putRevisions(s, 10, 50)

	ws := s.NewWatchStream().(*watchStream)
	defer ws.Close()
	id, err := ws.Watch(t.Context(), 0, []byte("k0"), []byte("l"), int64(math.MaxInt64-2))
	require.NoError(t, err)
	w := ws.watchers[id]
	require.Containsf(t, s.synced.watchers, w, "future-revision watch should start synced")

	require.NoError(t, s.Restore(s.store.b))
	require.Containsf(t, s.unsynced.watchers, w, "Restore should move the watcher to unsynced")

	s.syncWatchers()

	require.NotContainsf(t, s.unsynced.watchers, w,
		"watcher with minRev=%d left unsynced (curRev=%d); it has nothing to receive and should "+
			"be treated as caught up", w.minRev, s.Rev())
}
