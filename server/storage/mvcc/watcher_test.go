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

package mvcc

import (
	"bytes"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"go.etcd.io/etcd/api/v3/mvccpb"
	"go.etcd.io/etcd/server/v3/lease"
	betesting "go.etcd.io/etcd/server/v3/storage/backend/testing"
	"go.uber.org/zap"
)

// TestWatcherWatchID tests that each watcher provides unique watchID,
// and the watched event attaches the correct watchID.
func TestWatcherWatchID(t *testing.T) {
	b, tmpPath := betesting.NewDefaultTmpBackend(t)
	s := WatchableKV(newWatchableStore(zap.NewExample(), b, &lease.FakeLessor{}, StoreConfig{}))
	defer cleanup(s, b, tmpPath)

	w := s.NewWatchStream()
	defer w.Close()

	idm := make(map[WatchID]struct{})

	for i := 0; i < 10; i++ {
		id, _ := w.Watch(0, []byte("foo"), nil, 0)
		if _, ok := idm[id]; ok {
			t.Errorf("#%d: id %d exists", i, id)
		}
		idm[id] = struct{}{}

		s.Put([]byte("foo"), []byte("bar"), lease.NoLease)

		resp := <-w.Chan()
		if resp.WatchID != id {
			t.Errorf("#%d: watch id in event = %d, want %d", i, resp.WatchID, id)
		}

		if err := w.Cancel(id); err != nil {
			t.Error(err)
		}
	}

	s.Put([]byte("foo2"), []byte("bar"), lease.NoLease)

	// unsynced watchers
	for i := 10; i < 20; i++ {
		id, _ := w.Watch(0, []byte("foo2"), nil, 1)
		if _, ok := idm[id]; ok {
			t.Errorf("#%d: id %d exists", i, id)
		}
		idm[id] = struct{}{}

		resp := <-w.Chan()
		if resp.WatchID != id {
			t.Errorf("#%d: watch id in event = %d, want %d", i, resp.WatchID, id)
		}

		if err := w.Cancel(id); err != nil {
			t.Error(err)
		}
	}
}

func TestWatcherRequestsCustomID(t *testing.T) {
	b, tmpPath := betesting.NewDefaultTmpBackend(t)
	s := WatchableKV(newWatchableStore(zap.NewExample(), b, &lease.FakeLessor{}, StoreConfig{}))
	defer cleanup(s, b, tmpPath)

	w := s.NewWatchStream()
	defer w.Close()

	// - Request specifically ID #1
	// - Try to duplicate it, get an error
	// - Make sure the auto-assignment skips over things we manually assigned

	tt := []struct {
		givenID     WatchID
		expectedID  WatchID
		expectedErr error
	}{
		{1, 1, nil},
		{1, 0, ErrWatcherDuplicateID},
		{0, 0, nil},
		{0, 2, nil},
	}

	for i, tcase := range tt {
		id, err := w.Watch(tcase.givenID, []byte("foo"), nil, 0)
		if tcase.expectedErr != nil || err != nil {
			if err != tcase.expectedErr {
				t.Errorf("expected get error %q in test case %q, got %q", tcase.expectedErr, i, err)
			}
		} else if tcase.expectedID != id {
			t.Errorf("expected to create ID %d, got %d in test case %d", tcase.expectedID, id, i)
		}
	}
}

// TestWatcherWatchPrefix tests if Watch operation correctly watches
// and returns events with matching prefixes.
func TestWatcherWatchPrefix(t *testing.T) {
	b, tmpPath := betesting.NewDefaultTmpBackend(t)
	s := WatchableKV(newWatchableStore(zap.NewExample(), b, &lease.FakeLessor{}, StoreConfig{}))
	defer cleanup(s, b, tmpPath)

	w := s.NewWatchStream()
	defer w.Close()

	idm := make(map[WatchID]struct{})

	val := []byte("bar")
	keyWatch, keyEnd, keyPut := []byte("foo"), []byte("fop"), []byte("foobar")

	for i := 0; i < 10; i++ {
		id, _ := w.Watch(0, keyWatch, keyEnd, 0)
		if _, ok := idm[id]; ok {
			t.Errorf("#%d: unexpected duplicated id %x", i, id)
		}
		idm[id] = struct{}{}

		s.Put(keyPut, val, lease.NoLease)

		resp := <-w.Chan()
		if resp.WatchID != id {
			t.Errorf("#%d: watch id in event = %d, want %d", i, resp.WatchID, id)
		}

		if err := w.Cancel(id); err != nil {
			t.Errorf("#%d: unexpected cancel error %v", i, err)
		}

		if len(resp.Events) != 1 {
			t.Errorf("#%d: len(resp.Events) got = %d, want = 1", i, len(resp.Events))
		}
		if len(resp.Events) == 1 {
			if !bytes.Equal(resp.Events[0].Kv.Key, keyPut) {
				t.Errorf("#%d: resp.Events got = %s, want = %s", i, resp.Events[0].Kv.Key, keyPut)
			}
		}
	}

	keyWatch1, keyEnd1, keyPut1 := []byte("foo1"), []byte("foo2"), []byte("foo1bar")
	s.Put(keyPut1, val, lease.NoLease)

	// unsynced watchers
	for i := 10; i < 15; i++ {
		id, _ := w.Watch(0, keyWatch1, keyEnd1, 1)
		if _, ok := idm[id]; ok {
			t.Errorf("#%d: id %d exists", i, id)
		}
		idm[id] = struct{}{}

		resp := <-w.Chan()
		if resp.WatchID != id {
			t.Errorf("#%d: watch id in event = %d, want %d", i, resp.WatchID, id)
		}

		if err := w.Cancel(id); err != nil {
			t.Error(err)
		}

		if len(resp.Events) != 1 {
			t.Errorf("#%d: len(resp.Events) got = %d, want = 1", i, len(resp.Events))
		}
		if len(resp.Events) == 1 {
			if !bytes.Equal(resp.Events[0].Kv.Key, keyPut1) {
				t.Errorf("#%d: resp.Events got = %s, want = %s", i, resp.Events[0].Kv.Key, keyPut1)
			}
		}
	}
}

// TestWatcherWatchWrongRange ensures that watcher with wrong 'end' range
// does not create watcher, which panics when canceling in range tree.
func TestWatcherWatchWrongRange(t *testing.T) {
	b, tmpPath := betesting.NewDefaultTmpBackend(t)
	s := WatchableKV(newWatchableStore(zap.NewExample(), b, &lease.FakeLessor{}, StoreConfig{}))
	defer cleanup(s, b, tmpPath)

	w := s.NewWatchStream()
	defer w.Close()

	if _, err := w.Watch(0, []byte("foa"), []byte("foa"), 1); err != ErrEmptyWatcherRange {
		t.Fatalf("key == end range given; expected ErrEmptyWatcherRange, got %+v", err)
	}
	if _, err := w.Watch(0, []byte("fob"), []byte("foa"), 1); err != ErrEmptyWatcherRange {
		t.Fatalf("key > end range given; expected ErrEmptyWatcherRange, got %+v", err)
	}
	// watch request with 'WithFromKey' has empty-byte range end
	if id, _ := w.Watch(0, []byte("foo"), []byte{}, 1); id != 0 {
		t.Fatalf("\x00 is range given; id expected 0, got %d", id)
	}
}

func TestWatchDeleteRange(t *testing.T) {
	b, tmpPath := betesting.NewDefaultTmpBackend(t)
	s := newWatchableStore(zap.NewExample(), b, &lease.FakeLessor{}, StoreConfig{})

	defer func() {
		s.store.Close()
		os.Remove(tmpPath)
	}()

	testKeyPrefix := []byte("foo")

	for i := 0; i < 3; i++ {
		s.Put([]byte(fmt.Sprintf("%s_%d", testKeyPrefix, i)), []byte("bar"), lease.NoLease)
	}

	w := s.NewWatchStream()
	from, to := testKeyPrefix, []byte(fmt.Sprintf("%s_%d", testKeyPrefix, 99))
	w.Watch(0, from, to, 0)

	s.DeleteRange(from, to)

	we := []mvccpb.Event{
		{Type: mvccpb.DELETE, Kv: &mvccpb.KeyValue{Key: []byte("foo_0"), ModRevision: 5}},
		{Type: mvccpb.DELETE, Kv: &mvccpb.KeyValue{Key: []byte("foo_1"), ModRevision: 5}},
		{Type: mvccpb.DELETE, Kv: &mvccpb.KeyValue{Key: []byte("foo_2"), ModRevision: 5}},
	}

	select {
	case r := <-w.Chan():
		if !reflect.DeepEqual(r.Events, we) {
			t.Errorf("event = %v, want %v", r.Events, we)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("failed to receive event after 10 seconds!")
	}
}

// TestWatchStreamCancelWatcherByID ensures cancel calls the cancel func of the watcher
// with given id inside watchStream.
func TestWatchStreamCancelWatcherByID(t *testing.T) {
	b, tmpPath := betesting.NewDefaultTmpBackend(t)
	s := WatchableKV(newWatchableStore(zap.NewExample(), b, &lease.FakeLessor{}, StoreConfig{}))
	defer cleanup(s, b, tmpPath)

	w := s.NewWatchStream()
	defer w.Close()

	id, _ := w.Watch(0, []byte("foo"), nil, 0)

	tests := []struct {
		cancelID WatchID
		werr     error
	}{
		// no error should be returned when cancel the created watcher.
		{id, nil},
		// not exist error should be returned when cancel again.
		{id, ErrWatcherNotExist},
		// not exist error should be returned when cancel a bad id.
		{id + 1, ErrWatcherNotExist},
	}

	for i, tt := range tests {
		gerr := w.Cancel(tt.cancelID)

		if gerr != tt.werr {
			t.Errorf("#%d: err = %v, want %v", i, gerr, tt.werr)
		}
	}

	if l := len(w.(*watchStream).cancels); l != 0 {
		t.Errorf("cancels = %d, want 0", l)
	}
}

// TestWatcherRequestProgress ensures synced watcher can correctly
// report its correct progress.
func TestWatcherRequestProgress(t *testing.T) {
	b, tmpPath := betesting.NewDefaultTmpBackend(t)

	// manually create watchableStore instead of newWatchableStore
	// because newWatchableStore automatically calls syncWatchers
	// method to sync watchers in unsynced map. We want to keep watchers
	// in unsynced to test if syncWatchers works as expected.
	s := &watchableStore{
		store:    NewStore(zap.NewExample(), b, &lease.FakeLessor{}, StoreConfig{}),
		unsynced: newWatcherGroup(),
		synced:   newWatcherGroup(),
	}

	defer func() {
		s.store.Close()
		os.Remove(tmpPath)
	}()

	testKey := []byte("foo")
	notTestKey := []byte("bad")
	testValue := []byte("bar")
	s.Put(testKey, testValue, lease.NoLease)

	w := s.NewWatchStream()

	badID := WatchID(1000)
	w.RequestProgress(badID)
	select {
	case resp := <-w.Chan():
		t.Fatalf("unexpected %+v", resp)
	default:
	}

	id, _ := w.Watch(0, notTestKey, nil, 1)
	w.RequestProgress(id)
	select {
	case resp := <-w.Chan():
		t.Fatalf("unexpected %+v", resp)
	default:
	}

	s.syncWatchers()

	w.RequestProgress(id)
	wrs := WatchResponse{WatchID: id, Revision: 2}
	select {
	case resp := <-w.Chan():
		if !reflect.DeepEqual(resp, wrs) {
			t.Fatalf("got %+v, expect %+v", resp, wrs)
		}
	case <-time.After(time.Second):
		t.Fatal("failed to receive progress")
	}
}

func TestWatcherWatchWithFilter(t *testing.T) {
	b, tmpPath := betesting.NewDefaultTmpBackend(t)
	s := WatchableKV(newWatchableStore(zap.NewExample(), b, &lease.FakeLessor{}, StoreConfig{}))
	defer cleanup(s, b, tmpPath)

	w := s.NewWatchStream()
	defer w.Close()

	filterPut := func(e mvccpb.Event) bool {
		return e.Type == mvccpb.PUT
	}

	w.Watch(0, []byte("foo"), nil, 0, filterPut)
	done := make(chan struct{}, 1)

	go func() {
		<-w.Chan()
		done <- struct{}{}
	}()

	s.Put([]byte("foo"), []byte("bar"), 0)

	select {
	case <-done:
		t.Fatal("failed to filter put request")
	case <-time.After(100 * time.Millisecond):
	}

	s.DeleteRange([]byte("foo"), nil)

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("failed to receive delete request")
	}
}
