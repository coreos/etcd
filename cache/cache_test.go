// Copyright 2025 The etcd Authors
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

package cache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/testing/protocmp"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	mvccpb "go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"
)

func TestCacheWatchAtomicOrderedDelivery(t *testing.T) {
	tests := []struct {
		name        string
		sentBatches [][]*clientv3.Event
		wantBatch   []*clientv3.Event
	}{
		{
			name: "single_event",
			sentBatches: [][]*clientv3.Event{
				{event(mvccpb.Event_PUT, "/a", 5)},
			},
			wantBatch: []*clientv3.Event{
				event(mvccpb.Event_PUT, "/a", 5),
			},
		},
		{
			name: "same_revision_batch",
			sentBatches: [][]*clientv3.Event{
				{
					event(mvccpb.Event_PUT, "/a", 10),
					event(mvccpb.Event_PUT, "/b", 10),
				},
			},
			wantBatch: []*clientv3.Event{
				event(mvccpb.Event_PUT, "/a", 10),
				event(mvccpb.Event_PUT, "/b", 10),
			},
		},
		{
			name: "mixed_revisions_in_single_response",
			sentBatches: [][]*clientv3.Event{
				{
					event(mvccpb.Event_PUT, "/a", 11),
					event(mvccpb.Event_PUT, "/b", 11),
					event(mvccpb.Event_PUT, "/c", 12),
				},
			},
			wantBatch: []*clientv3.Event{
				event(mvccpb.Event_PUT, "/a", 11),
				event(mvccpb.Event_PUT, "/b", 11),
				event(mvccpb.Event_PUT, "/c", 12),
			},
		},
		{
			name: "mixed_event_types_same_revision",
			sentBatches: [][]*clientv3.Event{
				{
					event(mvccpb.Event_PUT, "/x", 5),
					event(mvccpb.Event_PUT, "/y", 6),
					event(mvccpb.Event_DELETE, "/x", 6),
				},
			},
			wantBatch: []*clientv3.Event{
				event(mvccpb.Event_PUT, "/x", 5),
				event(mvccpb.Event_PUT, "/y", 6),
				event(mvccpb.Event_DELETE, "/x", 6),
			},
		},
		{
			name: "all_events_in_one_response",
			sentBatches: [][]*clientv3.Event{
				{
					event(mvccpb.Event_PUT, "/a", 2),
					event(mvccpb.Event_PUT, "/b", 2),
					event(mvccpb.Event_PUT, "/c", 3),
					event(mvccpb.Event_PUT, "/d", 4),
					event(mvccpb.Event_PUT, "/e", 4),
					event(mvccpb.Event_PUT, "/f", 5),
					event(mvccpb.Event_PUT, "/g", 6),
					event(mvccpb.Event_PUT, "/h", 6),
					event(mvccpb.Event_PUT, "/i", 7),
					event(mvccpb.Event_PUT, "/j", 7),
				},
			},
			wantBatch: []*clientv3.Event{
				event(mvccpb.Event_PUT, "/a", 2),
				event(mvccpb.Event_PUT, "/b", 2),
				event(mvccpb.Event_PUT, "/c", 3),
				event(mvccpb.Event_PUT, "/d", 4),
				event(mvccpb.Event_PUT, "/e", 4),
				event(mvccpb.Event_PUT, "/f", 5),
				event(mvccpb.Event_PUT, "/g", 6),
				event(mvccpb.Event_PUT, "/h", 6),
				event(mvccpb.Event_PUT, "/i", 7),
				event(mvccpb.Event_PUT, "/j", 7),
			},
		},
		{
			name: "one_revision_group_per_response",
			sentBatches: [][]*clientv3.Event{
				{event(mvccpb.Event_PUT, "/a", 2), event(mvccpb.Event_PUT, "/b", 2)},
				{event(mvccpb.Event_PUT, "/c", 3)},
				{event(mvccpb.Event_PUT, "/d", 4), event(mvccpb.Event_PUT, "/e", 4)},
				{event(mvccpb.Event_PUT, "/f", 5)},
				{event(mvccpb.Event_PUT, "/g", 6), event(mvccpb.Event_PUT, "/h", 6)},
				{event(mvccpb.Event_PUT, "/i", 7), event(mvccpb.Event_PUT, "/j", 7)},
			},
			wantBatch: []*clientv3.Event{
				event(mvccpb.Event_PUT, "/a", 2),
				event(mvccpb.Event_PUT, "/b", 2),
				event(mvccpb.Event_PUT, "/c", 3),
				event(mvccpb.Event_PUT, "/d", 4),
				event(mvccpb.Event_PUT, "/e", 4),
				event(mvccpb.Event_PUT, "/f", 5),
				event(mvccpb.Event_PUT, "/g", 6),
				event(mvccpb.Event_PUT, "/h", 6),
				event(mvccpb.Event_PUT, "/i", 7),
				event(mvccpb.Event_PUT, "/j", 7),
			},
		},
		{
			name: "two_revision_groups_per_response",
			sentBatches: [][]*clientv3.Event{
				{event(mvccpb.Event_PUT, "/a", 2), event(mvccpb.Event_PUT, "/b", 2), event(mvccpb.Event_PUT, "/c", 3)},
				{event(mvccpb.Event_PUT, "/d", 4), event(mvccpb.Event_PUT, "/e", 4), event(mvccpb.Event_PUT, "/f", 5)},
				{event(mvccpb.Event_PUT, "/g", 6), event(mvccpb.Event_PUT, "/h", 6)},
				{event(mvccpb.Event_PUT, "/i", 7), event(mvccpb.Event_PUT, "/j", 7)},
			},
			wantBatch: []*clientv3.Event{
				event(mvccpb.Event_PUT, "/a", 2),
				event(mvccpb.Event_PUT, "/b", 2),
				event(mvccpb.Event_PUT, "/c", 3),
				event(mvccpb.Event_PUT, "/d", 4),
				event(mvccpb.Event_PUT, "/e", 4),
				event(mvccpb.Event_PUT, "/f", 5),
				event(mvccpb.Event_PUT, "/g", 6),
				event(mvccpb.Event_PUT, "/h", 6),
				event(mvccpb.Event_PUT, "/i", 7),
				event(mvccpb.Event_PUT, "/j", 7),
			},
		},
		{
			name: "three_revision_groups_per_response",
			sentBatches: [][]*clientv3.Event{
				{
					event(mvccpb.Event_PUT, "/a", 2), event(mvccpb.Event_PUT, "/b", 2),
					event(mvccpb.Event_PUT, "/c", 3),
					event(mvccpb.Event_PUT, "/d", 4), event(mvccpb.Event_PUT, "/e", 4),
				},
				{
					event(mvccpb.Event_PUT, "/f", 5),
					event(mvccpb.Event_PUT, "/g", 6), event(mvccpb.Event_PUT, "/h", 6),
					event(mvccpb.Event_PUT, "/i", 7), event(mvccpb.Event_PUT, "/j", 7),
				},
			},
			wantBatch: []*clientv3.Event{
				event(mvccpb.Event_PUT, "/a", 2),
				event(mvccpb.Event_PUT, "/b", 2),
				event(mvccpb.Event_PUT, "/c", 3),
				event(mvccpb.Event_PUT, "/d", 4),
				event(mvccpb.Event_PUT, "/e", 4),
				event(mvccpb.Event_PUT, "/f", 5),
				event(mvccpb.Event_PUT, "/g", 6),
				event(mvccpb.Event_PUT, "/h", 6),
				event(mvccpb.Event_PUT, "/i", 7),
				event(mvccpb.Event_PUT, "/j", 7),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mw := newMockWatcher(16)
			fakeClient := &clientv3.Client{
				Watcher: mw,
				KV:      newKVStub(),
			}
			cache, err := New(fakeClient, "")
			if err != nil {
				t.Fatalf("New cache: %v", err)
			}
			if err != nil {
				t.Fatalf("New cache: %v", err)
			}
			defer cache.Close()

			mw.responses <- clientv3.WatchResponse{}
			<-mw.registered

			ctxWait, cancelWait := context.WithTimeout(t.Context(), time.Second)
			if err := cache.WaitReady(ctxWait); err != nil {
				t.Fatalf("cache did not become Ready(): %v", err)
			}
			cancelWait()

			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			watchCh := cache.Watch(ctx, "", clientv3.WithPrefix())

			for _, batch := range tt.sentBatches {
				mw.responses <- clientv3.WatchResponse{Events: batch}
			}
			close(mw.responses)

			got := collectAndAssertAtomicEvents(ctx, t, watchCh, len(tt.wantBatch))

			if diff := cmp.Diff(tt.wantBatch, got, protocmp.Transform()); diff != "" {
				t.Fatalf("event mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestValidateWatchRange(t *testing.T) {
	type tc struct {
		name        string
		watchKey    string
		opts        []clientv3.OpOption
		cachePrefix string
		wantErr     bool
	}

	tests := []tc{
		{
			name:        "single key",
			watchKey:    "/a",
			cachePrefix: "",
			wantErr:     false,
		},
		{
			name:        "prefix single key",
			watchKey:    "/foo/a",
			cachePrefix: "/foo",
			wantErr:     false,
		},
		{
			name:        "single key outside prefix returns error",
			watchKey:    "/z",
			cachePrefix: "/foo",
			wantErr:     true,
		},
		{
			name:        "explicit range",
			watchKey:    "/a",
			opts:        []clientv3.OpOption{clientv3.WithRange("/b")},
			cachePrefix: "",
			wantErr:     false,
		},
		{
			name:        "exact prefix range",
			watchKey:    "/a",
			opts:        []clientv3.OpOption{clientv3.WithRange("/b")},
			cachePrefix: "/a",
			wantErr:     false,
		},
		{
			name:        "prefix subrange",
			watchKey:    "/foo",
			opts:        []clientv3.OpOption{clientv3.WithRange("/foo/a")},
			cachePrefix: "/foo",
			wantErr:     false,
		},
		{
			name:        "reverse range returns error",
			watchKey:    "/b",
			opts:        []clientv3.OpOption{clientv3.WithRange("/a")},
			cachePrefix: "",
			wantErr:     true,
		},
		{
			name:        "empty range returns error",
			watchKey:    "/foo",
			opts:        []clientv3.OpOption{clientv3.WithRange("/foo")},
			cachePrefix: "",
			wantErr:     true,
		},
		{
			name:        "range starting below cache prefix returns error",
			watchKey:    "/a",
			opts:        []clientv3.OpOption{clientv3.WithRange("/foo")},
			cachePrefix: "/foo",
			wantErr:     true,
		},
		{
			name:        "range encompassing cache prefix returns error",
			watchKey:    "/a",
			opts:        []clientv3.OpOption{clientv3.WithRange("/z")},
			cachePrefix: "/foo",
			wantErr:     true,
		},
		{
			name:        "range crossing prefixEnd returns error",
			watchKey:    "/foo",
			opts:        []clientv3.OpOption{clientv3.WithRange("/z")},
			cachePrefix: "/foo",
			wantErr:     true,
		},
		{
			name:        "empty prefix",
			watchKey:    "",
			opts:        []clientv3.OpOption{clientv3.WithPrefix()},
			cachePrefix: "",
			wantErr:     false,
		},
		{
			name:        "empty prefix with cachePrefix returns error",
			watchKey:    "",
			opts:        []clientv3.OpOption{clientv3.WithPrefix()},
			cachePrefix: "/foo",
			wantErr:     true,
		},
		{
			name:        "prefix watch matches cachePrefix exactly",
			watchKey:    "/foo",
			opts:        []clientv3.OpOption{clientv3.WithPrefix()},
			cachePrefix: "/foo",
			wantErr:     false,
		},
		{
			name:        "prefix watch inside cachePrefix",
			watchKey:    "/foo/bar",
			opts:        []clientv3.OpOption{clientv3.WithPrefix()},
			cachePrefix: "/foo",
			wantErr:     false,
		},
		{
			name:        "prefix starting below cachePrefix returns error",
			watchKey:    "/a",
			opts:        []clientv3.OpOption{clientv3.WithPrefix()},
			cachePrefix: "/foo",
			wantErr:     true,
		},
		{
			name:        "prefix starting above shard prefixEnd returns error",
			watchKey:    "/fop",
			opts:        []clientv3.OpOption{clientv3.WithPrefix()},
			cachePrefix: "/foo",
			wantErr:     true,
		},
		{
			name:        "fromKey open‑ended",
			watchKey:    "/a",
			opts:        []clientv3.OpOption{clientv3.WithFromKey()},
			cachePrefix: "",
			wantErr:     false,
		},
		{
			name:        "fromKey starting at prefix start",
			watchKey:    "/foo",
			opts:        []clientv3.OpOption{clientv3.WithFromKey()},
			cachePrefix: "/foo",
			wantErr:     true,
		},
		{
			name:        "fromKey starting below prefixEnd",
			watchKey:    "/a",
			opts:        []clientv3.OpOption{clientv3.WithFromKey()},
			cachePrefix: "/foo",
			wantErr:     true,
		},
		{
			name:        "fromKey starting above prefixEnd returns error",
			watchKey:    "/fop",
			opts:        []clientv3.OpOption{clientv3.WithFromKey()},
			cachePrefix: "/foo",
			wantErr:     true,
		},
	}

	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			dummyCache := &Cache{prefix: c.cachePrefix}
			op := clientv3.OpGet(c.watchKey, c.opts...)
			err := dummyCache.validateRange([]byte(c.watchKey), op.RangeBytes())
			if gotErr := err != nil; gotErr != c.wantErr {
				t.Fatalf("validateWatchRange(%q, %q, %v) err=%v, wantErr=%v",
					c.cachePrefix, c.watchKey, c.opts, err, c.wantErr)
			}
		})
	}
}

func TestCacheInvalidWatchResponseHasHeader(t *testing.T) {
	ready := newReady()
	ready.Set()
	c := &Cache{
		prefix: "/foo",
		ready:  ready,
	}

	ch := c.Watch(t.Context(), "/bar")

	resp := <-ch
	if !resp.Canceled {
		t.Fatalf("expected canceled response, got %+v", resp)
	}
	if resp.Header == nil {
		t.Fatal("expected non-nil response header")
	}
}

func TestCacheCompactionResync(t *testing.T) {
	firstSnapshot := &clientv3.GetResponse{
		Header: &pb.ResponseHeader{Revision: 5},
		Kvs: []*mvccpb.KeyValue{
			{Key: []byte("foo"), Value: []byte("old_value"), ModRevision: 5, CreateRevision: 5, Version: 1},
			{Key: []byte("bar"), Value: []byte("old_bar"), ModRevision: 3, CreateRevision: 3, Version: 1},
		},
	}
	secondSnapshot := &clientv3.GetResponse{
		Header: &pb.ResponseHeader{Revision: 20},
		Kvs: []*mvccpb.KeyValue{
			{Key: []byte("foo"), Value: []byte("new_value"), ModRevision: 20, CreateRevision: 5, Version: 2},
			{Key: []byte("baz"), Value: []byte("new_baz"), ModRevision: 18, CreateRevision: 18, Version: 1},
		},
	}
	fakeClient := &clientv3.Client{
		Watcher: newMockWatcher(16),
		KV:      newKVStub(firstSnapshot, secondSnapshot),
	}
	cache, err := New(fakeClient, "")
	if err != nil {
		t.Fatalf("New cache: %v", err)
	}
	defer cache.Close()
	mw := fakeClient.Watcher.(*mockWatcher)

	t.Log("Phase 1: initial getWatch bootstrap")
	mw.triggerCreatedNotify()
	<-mw.registered
	if err = cache.WaitReady(t.Context()); err != nil {
		t.Fatalf("initial WaitReady: %v", err)
	}
	verifySnapshot(t, cache, []*mvccpb.KeyValue{
		{Key: []byte("bar"), Value: []byte("old_bar"), ModRevision: 3, CreateRevision: 3, Version: 1},
		{Key: []byte("foo"), Value: []byte("old_value"), ModRevision: 5, CreateRevision: 5, Version: 1},
	})

	t.Log("Phase 2: simulate compaction")
	mw.errorCompacted(10)

	waitUntil(t, time.Second, 10*time.Millisecond, func() bool { return !cache.Ready() })

	ctxGet, cancelGet := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancelGet()
	snapshot, err := cache.Get(ctxGet, "foo", clientv3.WithSerializable())
	if err != nil {
		t.Fatalf("expected Get() to serve from cached snapshot after compaction, got %v", err)
	}
	if got := snapshot.Header.Revision; got != firstSnapshot.Header.Revision {
		t.Fatalf("expected cached revision %d after compaction, got %d", firstSnapshot.Header.Revision, got)
	}
	if string(snapshot.Kvs[0].Value) != "old_value" {
		t.Fatalf("expected cached value 'old_value' during compaction, got %q", string(snapshot.Kvs[0].Value))
	}

	t.Log("Phase 3: resync after compaction")
	mw.resetRegistered()
	mw.triggerCreatedNotify()
	<-mw.registered
	expectSnapshotRev := int64(20)
	ctxResync, cancelResync := context.WithTimeout(t.Context(), time.Second)
	defer cancelResync()
	if err = cache.WaitForRevision(ctxResync, expectSnapshotRev); err != nil {
		t.Fatalf("cache failed to resync to rev=%d within 1s: %v", expectSnapshotRev, err)
	}

	expectedWatchStart := secondSnapshot.Header.Revision + 1
	if gotWatchStart := mw.getLastStartRev(); gotWatchStart != expectedWatchStart {
		t.Errorf("Watch started at rev=%d; want %d", gotWatchStart, expectedWatchStart)
	}

	gotSnapshot, err := cache.Get(t.Context(), "foo", clientv3.WithSerializable())
	if err != nil {
		t.Fatalf("Get after resync: %v", err)
	}
	if gotSnapshot.Header.Revision != expectSnapshotRev {
		t.Errorf("unexpected Snapshot revision: got=%d, want=%d", gotSnapshot.Header.Revision, expectSnapshotRev)
	}
	verifySnapshot(t, cache, []*mvccpb.KeyValue{
		{Key: []byte("baz"), Value: []byte("new_baz"), ModRevision: 18, CreateRevision: 18, Version: 1},
		{Key: []byte("foo"), Value: []byte("new_value"), ModRevision: 20, CreateRevision: 5, Version: 2},
	})
}

func waitUntil(t *testing.T, timeout, poll time.Duration, cond func() bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(poll)
	}
	t.Fatalf("condition not satisfied within %s", timeout)
}

type mockWatcher struct {
	responses    chan clientv3.WatchResponse
	registered   chan struct{}
	closeOnce    sync.Once
	wg           sync.WaitGroup
	mu           sync.Mutex
	lastStartRev int64
	progressErr  error
}

func newMockWatcher(buf int) *mockWatcher {
	return &mockWatcher{
		responses:  make(chan clientv3.WatchResponse, buf),
		registered: make(chan struct{}),
	}
}

func (m *mockWatcher) Watch(ctx context.Context, _ string, opts ...clientv3.OpOption) clientv3.WatchChan {
	rev := m.extractRev(opts)
	m.recordStartRev(rev)

	m.signalRegistration()

	out := make(chan clientv3.WatchResponse)
	m.wg.Add(1)
	go m.streamResponses(ctx, out)
	return out
}

func (m *mockWatcher) RequestProgress(_ context.Context) error { return m.progressErr }

func (m *mockWatcher) Close() error {
	m.closeOnce.Do(func() { close(m.responses) })
	m.wg.Wait()
	return nil
}

func (m *mockWatcher) triggerCreatedNotify() { m.responses <- clientv3.WatchResponse{} }

func (m *mockWatcher) errorCompacted(compRev int64) {
	m.responses <- clientv3.WatchResponse{
		Canceled:        true,
		CompactRevision: compRev,
	}
}

func (m *mockWatcher) extractRev(opts []clientv3.OpOption) int64 {
	var op clientv3.Op
	for _, o := range opts {
		o(&op)
	}
	return op.Rev()
}

func (m *mockWatcher) recordStartRev(rev int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastStartRev = rev
}

func (m *mockWatcher) getLastStartRev() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastStartRev
}

func (m *mockWatcher) signalRegistration() {
	m.mu.Lock()
	defer m.mu.Unlock()
	select {
	case <-m.registered:
	default:
		close(m.registered)
	}
}

func (m *mockWatcher) resetRegistered() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.registered = make(chan struct{})
}

func (m *mockWatcher) streamResponses(ctx context.Context, out chan<- clientv3.WatchResponse) {
	defer func() {
		close(out)
		m.wg.Done()
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case resp, ok := <-m.responses:
			if !ok {
				return
			}
			out <- resp
			if resp.Canceled {
				return
			}
		}
	}
}

type kvStub struct {
	queued      []*clientv3.GetResponse
	defaultResp *clientv3.GetResponse
	defaultErr  error
}

func newKVStub(resps ...*clientv3.GetResponse) *kvStub {
	queue := append([]*clientv3.GetResponse(nil), resps...)
	return &kvStub{
		queued:      queue,
		defaultResp: &clientv3.GetResponse{Header: &pb.ResponseHeader{Revision: 0}},
	}
}

func (s *kvStub) Get(_ context.Context, key string, opts ...clientv3.OpOption) (*clientv3.GetResponse, error) {
	if s.defaultErr != nil {
		return nil, s.defaultErr
	}

	if len(s.queued) > 0 {
		next := s.queued[0]
		s.queued = s.queued[1:]
		return next, nil
	}
	return s.defaultResp, nil
}

func (s *kvStub) Put(ctx context.Context, key, val string, _ ...clientv3.OpOption) (*clientv3.PutResponse, error) {
	return nil, nil
}

func (s *kvStub) Delete(ctx context.Context, key string, _ ...clientv3.OpOption) (*clientv3.DeleteResponse, error) {
	return nil, nil
}

func (s *kvStub) Compact(ctx context.Context, rev int64, _ ...clientv3.CompactOption) (*clientv3.CompactResponse, error) {
	return nil, nil
}

func (s *kvStub) GetStream(ctx context.Context, key string, opts ...clientv3.OpOption) (clientv3.GetStreamChan, error) {
	return nil, errors.New("GetStream not implemented")
}

func (s *kvStub) Do(ctx context.Context, op clientv3.Op) (clientv3.OpResponse, error) {
	return clientv3.OpResponse{}, nil
}

func (s *kvStub) Txn(ctx context.Context) clientv3.Txn {
	return nil
}

func event(eventType mvccpb.Event_EventType, key string, rev int64) *clientv3.Event {
	return &clientv3.Event{
		Type: eventType,
		Kv: &mvccpb.KeyValue{
			Key:            []byte(key),
			ModRevision:    rev,
			CreateRevision: rev,
			Version:        1,
		},
	}
}

func collectAndAssertAtomicEvents(ctx context.Context, t *testing.T, watchCh clientv3.WatchChan, wantCount int) []*clientv3.Event {
	t.Helper()
	var events []*clientv3.Event
	var lastRevision int64
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for events (%d/%d received)",
				len(events), wantCount)

		case resp, ok := <-watchCh:
			if !ok {
				return events
			}
			if len(resp.Events) != 0 && resp.Events[0].Kv.ModRevision == lastRevision {
				t.Fatalf("same revision found as in previous response: %d", lastRevision)
			}
			for _, ev := range resp.Events {
				if ev.Kv.ModRevision < lastRevision {
					t.Fatalf("revision went backwards: last %d, now %d", lastRevision, ev.Kv.ModRevision)
				}
				events = append(events, ev)
				lastRevision = ev.Kv.ModRevision
			}
			if wantCount != 0 && len(events) >= wantCount {
				return events
			}
		}
	}
}

func verifySnapshot(t *testing.T, cache *Cache, want []*mvccpb.KeyValue) {
	resp, err := cache.Get(t.Context(), "", clientv3.WithPrefix(), clientv3.WithSerializable())
	if err != nil {
		t.Fatalf("Get all keys: %v", err)
	}

	if diff := cmp.Diff(want, resp.Kvs, protocmp.Transform()); diff != "" {
		t.Fatalf("cache snapshot mismatch (-want +got):\n%s", diff)
	}
}

type noopProgressNotifier struct{}

func (n *noopProgressNotifier) RequestProgress(_ context.Context) error {
	return nil
}

func newTestProgressRequestor() *conditionalProgressRequestor {
	return newConditionalProgressRequestor(&noopProgressNotifier{}, realClock{}, 100*time.Millisecond)
}

func newCacheForWaitTest(serverRev int64, localRev int64, pr progressRequestor) (*Cache, *store) {
	cfg := defaultConfig()
	st := newStore(cfg.BTreeDegree, cfg.HistoryWindowSize)
	if localRev > 0 {
		st.Restore(nil, localRev)
	}
	kv := &kvStub{
		defaultResp: &clientv3.GetResponse{Header: &pb.ResponseHeader{Revision: serverRev}},
	}
	return &Cache{
		kv:                kv,
		store:             st,
		prefix:            "/",
		progressRequestor: pr,
		cfg:               cfg,
	}, st
}

func setupWatcherWithFakeClock(t *testing.T, opts ...clientv3.OpOption) (clientv3.WatchChan, *mockWatcher, *fakeClock) {
	t.Helper()
	fc := newFakeClock()
	mw := newMockWatcher(8)
	fakeClient := &clientv3.Client{
		Watcher: mw,
		KV:      newKVStub(),
	}
	cfg := defaultConfig()
	cfg.ProgressNotifyInterval = 100 * time.Millisecond
	c, err := newCache(fakeClient, "", cfg, fc)
	if err != nil {
		t.Fatalf("newCache: %v", err)
	}
	t.Cleanup(c.Close)

	mw.responses <- clientv3.WatchResponse{}
	<-mw.registered

	ctxWait, cancelWait := context.WithTimeout(t.Context(), time.Second)
	defer cancelWait()
	if err := c.WaitReady(ctxWait); err != nil {
		t.Fatalf("cache did not become Ready: %v", err)
	}

	watchOpts := append([]clientv3.OpOption{clientv3.WithPrefix()}, opts...)
	watchCh := c.Watch(t.Context(), "", watchOpts...)

	// Seed an event so the cache has a known revision (rev 5),
	// then drain it from the watch channel.
	mw.responses <- clientv3.WatchResponse{
		Events: []*clientv3.Event{event(mvccpb.Event_PUT, "/a", 5)},
	}
	readResponse(t, watchCh)

	return watchCh, mw, fc
}

func TestCacheWatchProgressNotify(t *testing.T) {
	t.Run("watcher requesting notification, receives them periodically", func(t *testing.T) {
		progressCh, _, fc := setupWatcherWithFakeClock(t, clientv3.WithProgressNotify())

		t.Log("First interval — progress notification arrives")
		fc.Advance(100 * time.Millisecond)
		resp := readResponse(t, progressCh)
		if !resp.IsProgressNotify() {
			t.Fatalf("expected progress notify, got events: %v", resp.Events)
		}

		t.Log("Second interval — another progress notification arrives")
		fc.Advance(100 * time.Millisecond)
		resp = readResponse(t, progressCh)
		if !resp.IsProgressNotify() {
			t.Fatalf("expected progress notify, got events: %v", resp.Events)
		}
	})

	t.Run("watcher that didn't request progress doesn't receive any", func(t *testing.T) {
		plainCh, _, fc := setupWatcherWithFakeClock(t)

		t.Log("Advance past the interval — plain watcher should not receive anything")
		fc.Advance(150 * time.Millisecond)

		select {
		case got, ok := <-plainCh:
			if ok {
				t.Fatalf("expected no response on plain watcher, got: IsProgressNotify=%v events=%v", got.IsProgressNotify(), got.Events)
			}
		default:
		}
	})

	t.Run("event resets timer and delays sending progress", func(t *testing.T) {
		progressCh, mw, fc := setupWatcherWithFakeClock(t, clientv3.WithProgressNotify())

		t.Log("Advance partway into the interval, then deliver an event to reset the timer")
		fc.Advance(50 * time.Millisecond)
		mw.responses <- clientv3.WatchResponse{
			Events: []*clientv3.Event{event(mvccpb.Event_PUT, "/b", 6)},
		}
		readResponse(t, progressCh)

		t.Log("100 ms since original start but only 50 ms since event reset — no progress notify")
		fc.Advance(50 * time.Millisecond)
		select {
		case got, ok := <-progressCh:
			if ok {
				t.Fatalf("expected no progress notify within interval after event, got: IsProgressNotify=%v events=%v", got.IsProgressNotify(), got.Events)
			}
		default:
		}

		t.Log("Full interval after the event — progress notify arrives")
		fc.Advance(50 * time.Millisecond)
		resp := readResponse(t, progressCh)
		if !resp.IsProgressNotify() {
			t.Fatalf("expected progress notify, got events: %v", resp.Events)
		}
		if resp.Header.Revision != 6 {
			t.Fatalf("expected progress revision 6, got %d", resp.Header.Revision)
		}
	})
}

func TestWaitTillRevision(t *testing.T) {
	t.Run("cache_already_caught_up", func(t *testing.T) {
		c, _ := newCacheForWaitTest(10, 10, newTestProgressRequestor())

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := c.waitTillRevision(ctx, 10); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("local_rev_sufficient_skips_server_call", func(t *testing.T) {
		cfg := defaultConfig()
		st := newStore(cfg.BTreeDegree, cfg.HistoryWindowSize)
		st.Restore(nil, 10)
		c := &Cache{
			kv:                &kvStub{defaultErr: fmt.Errorf("should not be called")},
			store:             st,
			prefix:            "/",
			progressRequestor: newTestProgressRequestor(),
			cfg:               cfg,
		}

		if err := c.waitTillRevision(context.Background(), 5); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("cache_catches_up", func(t *testing.T) {
		c, st := newCacheForWaitTest(15, 5, newTestProgressRequestor())

		go func() {
			time.Sleep(200 * time.Millisecond)
			st.Restore(nil, 10)
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := c.waitTillRevision(ctx, 10); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("rev_zero_cache_caught_up", func(t *testing.T) {
		c, _ := newCacheForWaitTest(10, 10, newTestProgressRequestor())

		if err := c.waitTillRevision(context.Background(), 0); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("rev_zero_waits_for_server_rev", func(t *testing.T) {
		c, st := newCacheForWaitTest(10, 5, newTestProgressRequestor())

		go func() {
			time.Sleep(200 * time.Millisecond)
			st.Restore(nil, 10)
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := c.waitTillRevision(ctx, 0); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("context_cancelled", func(t *testing.T) {
		c, _ := newCacheForWaitTest(10, 5, newTestProgressRequestor())

		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		err := c.waitTillRevision(ctx, 10)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("got %v, want context.DeadlineExceeded", err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			c, _ := newCacheForWaitTest(10, 5, newTestProgressRequestor())

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := c.waitTillRevision(ctx, 10)
			if !errors.Is(err, ErrCacheTimeout) {
				t.Fatalf("got %v, want ErrCacheTimeout", err)
			}
		})
	})
}

func TestWaitTillRevisionTriggersProgressRequests(t *testing.T) {
	fc := newFakeClock()
	pr := newTestConditionalProgressRequestor(fc, 50*time.Millisecond)
	c, st := newCacheForWaitTest(15, 5, pr)

	// Start progress requestor
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pr.run(ctx)

	// Wait for goroutine to start
	time.Sleep(10 * time.Millisecond)

	// Initially, no progress requests should be sent (no waiters)
	fc.Advance(100 * time.Millisecond)
	if err := pollConditionNoChange(func() bool {
		return pr.progressRequestsSentCount.Load() == 0
	}); err != nil {
		t.Fatal("expected no progress requests without active waiters")
	}

	// Start waiting - this should trigger progress requests
	errCh := make(chan error, 1)
	go func() {
		errCh <- c.waitTillRevision(context.Background(), 10)
	}()

	// Advance time and wait for progress requests to start
	fc.Advance(50 * time.Millisecond)
	time.Sleep(10 * time.Millisecond)

	// Verify progress requests are being sent while waiting
	if pr.progressRequestsSentCount.Load() == 0 {
		t.Fatal("expected progress requests during wait")
	}

	// Complete the wait
	st.Restore(nil, 15)

	if err := <-errCh; err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// After completion, progress requests should stop
	finalCount := pr.progressRequestsSentCount.Load()
	fc.Advance(100 * time.Millisecond)
	if err := pollConditionNoChange(func() bool {
		return pr.progressRequestsSentCount.Load() == finalCount
	}); err != nil {
		t.Fatalf("expected no new progress requests after completion, got %d initially, then changed", finalCount)
	}
}

// failingKV records the virtual time of every bootstrap Get and always fails, so
// the cache never leaves its retry loop.
type failingKV struct {
	clientv3.KV
	mu    sync.Mutex
	times []time.Time
}

func (f *failingKV) Get(_ context.Context, _ string, _ ...clientv3.OpOption) (*clientv3.GetResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.times = append(f.times, time.Now())
	return nil, errors.New("upstream unavailable")
}

func (f *failingKV) gaps() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	gaps := make([]time.Duration, 0, len(f.times))
	for i := 1; i < len(f.times); i++ {
		gaps = append(gaps, f.times[i].Sub(f.times[i-1]))
	}
	return gaps
}

// blockingKV never answers, so the only thing that can end a bootstrap Get is the
// deadline the cache is supposed to put on it.
type blockingKV struct {
	clientv3.KV
	mu   sync.Mutex
	held []time.Duration
}

func (b *blockingKV) Get(ctx context.Context, _ string, _ ...clientv3.OpOption) (*clientv3.GetResponse, error) {
	start := time.Now()
	<-ctx.Done()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.held = append(b.held, time.Since(start))
	return nil, ctx.Err()
}

func (b *blockingKV) first() (time.Duration, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.held) == 0 {
		return 0, false
	}
	return b.held[0], true
}

// The retry loop takes its schedule from the cache's own Config: the first delay
// from InitialBackoff, doubling after each failed attempt, capped by MaxBackoff.
// Reading defaultConfig() instead made WithInitialBackoff a silent no-op, and a
// constant delay left WithMaxBackoff with nothing to cap.
func TestCacheRetriesUpstreamWatchWithConfiguredBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		kv := &failingKV{}
		cfg := defaultConfig()
		cfg.InitialBackoff = 100 * time.Millisecond
		cfg.MaxBackoff = 400 * time.Millisecond

		c, err := newCache(&clientv3.Client{KV: kv, Watcher: newMockWatcher(8)}, "", cfg, realClock{})
		if err != nil {
			t.Fatalf("newCache: %v", err)
		}

		// Attempts land at 0, 100, 300, 700 and 1100ms of virtual time.
		time.Sleep(1150 * time.Millisecond)
		c.Close()

		want := []time.Duration{
			100 * time.Millisecond,
			200 * time.Millisecond,
			400 * time.Millisecond,
			400 * time.Millisecond,
		}
		if diff := cmp.Diff(want, kv.gaps()); diff != "" {
			t.Errorf("retry schedule differs (-want +got):\n%s", diff)
		}
	})
}

// GetTimeout bounds the bootstrap Get. Without it an unreachable endpoint parks
// that Get on the cache's own lifetime context: the retry loop never gets another
// turn and the cache never becomes ready.
func TestCacheBoundsBootstrapGetByGetTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		kv := &blockingKV{}
		cfg := defaultConfig()
		cfg.GetTimeout = 250 * time.Millisecond

		c, err := newCache(&clientv3.Client{KV: kv, Watcher: newMockWatcher(8)}, "", cfg, realClock{})
		if err != nil {
			t.Fatalf("newCache: %v", err)
		}

		time.Sleep(time.Second)
		c.Close()

		held, ok := kv.first()
		if !ok {
			t.Fatal("bootstrap Get was never called")
		}
		if held != cfg.GetTimeout {
			t.Errorf("bootstrap Get was held for %v, want GetTimeout %v", held, cfg.GetTimeout)
		}
	})
}

func TestNewCacheRejectsUnusableRetrySettings(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Config)
	}{
		{"zero InitialBackoff", func(c *Config) { c.InitialBackoff = 0 }},
		{"MaxBackoff below InitialBackoff", func(c *Config) { c.MaxBackoff = c.InitialBackoff - time.Nanosecond }},
		{"zero GetTimeout", func(c *Config) { c.GetTimeout = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaultConfig()
			tc.mutate(&cfg)
			c, err := newCache(&clientv3.Client{KV: newKVStub(), Watcher: newMockWatcher(8)}, "", cfg, realClock{})
			if err == nil {
				c.Close()
				t.Fatal("expected an error, got none")
			}
		})
	}
}
