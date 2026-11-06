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

package watch

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/testing/protocmp"

	mvccpb "go.etcd.io/etcd/api/v3/mvccpb"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	"go.etcd.io/etcd/api/v3/version"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/etcdserver/api/v3rpc"
	"go.etcd.io/etcd/tests/v3/framework/integration"
)

type watcherTest func(*testing.T, *watchctx)

type watchctx struct {
	clus          *integration.Cluster
	w             clientv3.Watcher
	kv            clientv3.KV
	wclientMember int
	kvMember      int
	ch            clientv3.WatchChan
}

func runWatchTest(t *testing.T, f watcherTest) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3, UseBridge: true})
	defer clus.Terminate(t)

	wclientMember := rand.Intn(3)
	w := clus.Client(wclientMember).Watcher
	// select a different client for KV operations so puts succeed if
	// a test knocks out the watcher client.
	kvMember := rand.Intn(3)
	for kvMember == wclientMember {
		kvMember = rand.Intn(3)
	}
	kv := clus.Client(kvMember).KV

	wctx := &watchctx{clus, w, kv, wclientMember, kvMember, nil}
	f(t, wctx)
}

// TestWatchMultiWatcher modifies multiple keys and observes the changes.
func TestWatchMultiWatcher(t *testing.T) {
	runWatchTest(t, testWatchMultiWatcher)
}

func testWatchMultiWatcher(t *testing.T, wctx *watchctx) {
	numKeyUpdates := 4
	keys := []string{"foo", "bar", "baz"}

	donec := make(chan struct{})
	// wait for watcher shutdown
	defer func() {
		for i := 0; i < len(keys)+1; i++ {
			<-donec
		}
	}()
	readyc := make(chan struct{})
	for _, k := range keys {
		// key watcher
		go func(key string) {
			ch := wctx.w.Watch(t.Context(), key)
			if ch == nil {
				t.Errorf("expected watcher channel, got nil")
			}
			readyc <- struct{}{}
			for i := 0; i < numKeyUpdates; i++ {
				resp, ok := <-ch
				if !ok {
					t.Errorf("watcher unexpectedly closed")
				}
				v := fmt.Sprintf("%s-%d", key, i)
				gotv := string(resp.Events[0].Kv.Value)
				if gotv != v {
					t.Errorf("#%d: got %s, wanted %s", i, gotv, v)
				}
			}
			donec <- struct{}{}
		}(k)
	}
	// prefix watcher on "b" (bar and baz)
	go func() {
		prefixc := wctx.w.Watch(t.Context(), "b", clientv3.WithPrefix())
		if prefixc == nil {
			t.Errorf("expected watcher channel, got nil")
		}
		readyc <- struct{}{}
		var evs []*clientv3.Event
		for i := 0; i < numKeyUpdates*2; i++ {
			resp, ok := <-prefixc
			if !ok {
				t.Errorf("watcher unexpectedly closed")
			}
			evs = append(evs, resp.Events...)
		}

		// check response
		var expected []string
		bkeys := []string{"bar", "baz"}
		for _, k := range bkeys {
			for i := 0; i < numKeyUpdates; i++ {
				expected = append(expected, fmt.Sprintf("%s-%d", k, i))
			}
		}
		var got []string
		for _, ev := range evs {
			got = append(got, string(ev.Kv.Value))
		}
		sort.Strings(got)
		if !reflect.DeepEqual(expected, got) {
			t.Errorf("got %v, expected %v", got, expected)
		}

		// ensure no extra data
		select {
		case resp, ok := <-prefixc:
			if !ok {
				t.Errorf("watcher unexpectedly closed")
			}
			t.Errorf("unexpected event %+v", resp)
		case <-time.After(time.Second):
		}
		donec <- struct{}{}
	}()

	// wait for watcher bring up
	for i := 0; i < len(keys)+1; i++ {
		<-readyc
	}
	// generate events
	ctx := t.Context()
	for i := 0; i < numKeyUpdates; i++ {
		for _, k := range keys {
			v := fmt.Sprintf("%s-%d", k, i)
			_, err := wctx.kv.Put(ctx, k, v)
			require.NoError(t, err)
		}
	}
}

// TestWatchRange tests watcher creates ranges
func TestWatchRange(t *testing.T) {
	runWatchTest(t, testWatchRange)
}

func testWatchRange(t *testing.T, wctx *watchctx) {
	wctx.ch = wctx.w.Watch(t.Context(), "a", clientv3.WithRange("c"))
	require.NotNilf(t, wctx.ch, "expected non-nil channel")
	putAndWatch(t, wctx, "a", "a")
	putAndWatch(t, wctx, "b", "b")
	putAndWatch(t, wctx, "bar", "bar")
}

// TestWatchReconnRequest tests the send failure path when requesting a watcher.
func TestWatchReconnRequest(t *testing.T) {
	runWatchTest(t, testWatchReconnRequest)
}

func testWatchReconnRequest(t *testing.T, wctx *watchctx) {
	donec, stopc := make(chan struct{}), make(chan struct{}, 1)
	go func() {
		timer := time.After(2 * time.Second)
		defer close(donec)
		// take down watcher connection
		for {
			wctx.clus.Members[wctx.wclientMember].Bridge().DropConnections()
			select {
			case <-timer:
				// spinning on close may live lock reconnection
				return
			case <-stopc:
				return
			default:
			}
		}
	}()
	// should reconnect when requesting watch
	wctx.ch = wctx.w.Watch(t.Context(), "a")
	require.NotNilf(t, wctx.ch, "expected non-nil channel")

	// wait for disconnections to stop
	stopc <- struct{}{}
	<-donec

	// spinning on dropping connections may trigger a leader election
	// due to resource starvation; l-read to ensure the cluster is stable
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	_, err := wctx.kv.Get(ctx, "_")
	require.NoError(t, err)
	cancel()

	// ensure watcher works
	putAndWatch(t, wctx, "a", "a")
}

// TestWatchReconnInit tests watcher resumes correctly if connection lost
// before any data was sent.
func TestWatchReconnInit(t *testing.T) {
	runWatchTest(t, testWatchReconnInit)
}

func testWatchReconnInit(t *testing.T, wctx *watchctx) {
	wctx.ch = wctx.w.Watch(t.Context(), "a")
	require.NotNilf(t, wctx.ch, "expected non-nil channel")
	wctx.clus.Members[wctx.wclientMember].Bridge().DropConnections()
	// watcher should recover
	putAndWatch(t, wctx, "a", "a")
}

// TestWatchReconnRunning tests watcher resumes correctly if connection lost
// after data was sent.
func TestWatchReconnRunning(t *testing.T) {
	runWatchTest(t, testWatchReconnRunning)
}

func testWatchReconnRunning(t *testing.T, wctx *watchctx) {
	wctx.ch = wctx.w.Watch(t.Context(), "a")
	require.NotNilf(t, wctx.ch, "expected non-nil channel")
	putAndWatch(t, wctx, "a", "a")
	// take down watcher connection
	wctx.clus.Members[wctx.wclientMember].Bridge().DropConnections()
	// watcher should recover
	putAndWatch(t, wctx, "a", "b")
}

// TestWatchCancelImmediate ensures a closed channel is returned
// if the context is cancelled.
func TestWatchCancelImmediate(t *testing.T) {
	runWatchTest(t, testWatchCancelImmediate)
}

func testWatchCancelImmediate(t *testing.T, wctx *watchctx) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	wch := wctx.w.Watch(ctx, "a")
	select {
	case wresp, ok := <-wch:
		require.Falsef(t, ok, "read wch got %v; expected closed channel", wresp)
	default:
		t.Fatalf("closed watcher channel should not block")
	}
}

// TestWatchCancelInit tests watcher closes correctly after no events.
func TestWatchCancelInit(t *testing.T) {
	runWatchTest(t, testWatchCancelInit)
}

func testWatchCancelInit(t *testing.T, wctx *watchctx) {
	ctx, cancel := context.WithCancel(t.Context())
	wctx.ch = wctx.w.Watch(ctx, "a")
	require.NotNilf(t, wctx.ch, "expected non-nil watcher channel")
	cancel()
	select {
	case <-time.After(time.Second):
		t.Fatalf("took too long to cancel")
	case _, ok := <-wctx.ch:
		require.Falsef(t, ok, "expected watcher channel to close")
	}
}

// TestWatchCancelRunning tests watcher closes correctly after events.
func TestWatchCancelRunning(t *testing.T) {
	runWatchTest(t, testWatchCancelRunning)
}

func testWatchCancelRunning(t *testing.T, wctx *watchctx) {
	ctx, cancel := context.WithCancel(t.Context())
	wctx.ch = wctx.w.Watch(ctx, "a")
	require.NotNilf(t, wctx.ch, "expected non-nil watcher channel")
	_, err := wctx.kv.Put(ctx, "a", "a")
	require.NoError(t, err)
	cancel()
	select {
	case <-time.After(time.Second):
		t.Fatalf("took too long to cancel")
	case _, ok := <-wctx.ch:
		if !ok {
			// closed before getting put; OK
			break
		}
		// got the PUT; should close next
		select {
		case <-time.After(time.Second):
			t.Fatalf("took too long to close")
		case v, ok2 := <-wctx.ch:
			require.Falsef(t, ok2, "expected watcher channel to close, got %v", v)
		}
	}
}

func putAndWatch(t *testing.T, wctx *watchctx, key, val string) {
	_, err := wctx.kv.Put(t.Context(), key, val)
	require.NoError(t, err)
	select {
	case <-time.After(5 * time.Second):
		t.Fatalf("watch timed out")
	case v, ok := <-wctx.ch:
		require.Truef(t, ok, "unexpected watch close")
		err := v.Err()
		require.NoErrorf(t, err, "unexpected watch response error")
		require.Equalf(t, string(v.Events[0].Kv.Value), val, "bad value got %v, wanted %v", v.Events[0].Kv.Value, val)
	}
}

// TestWatchResumeAfterDisconnect tests watch resume after member disconnects then connects.
// It ensures that correct events are returned corresponding to the start revision.
func TestWatchResumeAfterDisconnect(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1, UseBridge: true})
	defer clus.Terminate(t)

	cli := clus.Client(0)
	_, err := cli.Put(t.Context(), "b", "2")
	require.NoError(t, err)
	_, err = cli.Put(t.Context(), "a", "3")
	require.NoError(t, err)
	// if resume is broken, it'll pick up this key first instead of a=3
	_, err = cli.Put(t.Context(), "a", "4")
	require.NoError(t, err)

	// watch from revision 1
	wch := clus.Client(0).Watch(t.Context(), "a", clientv3.WithRev(1), clientv3.WithCreatedNotify())
	// response for the create watch request, no events are in this response
	// the current revision of etcd should be 4
	if resp, ok := <-wch; !ok || resp.Header.Revision != 4 {
		t.Fatalf("got (%v, %v), expected create notification rev=4", resp, ok)
	}
	// pause wch
	clus.Members[0].Bridge().DropConnections()
	clus.Members[0].Bridge().PauseConnections()

	select {
	case resp, ok := <-wch:
		t.Skipf("wch should block, got (%+v, %v); drop not fast enough", resp, ok)
	case <-time.After(100 * time.Millisecond):
	}

	// resume wch
	clus.Members[0].Bridge().UnpauseConnections()

	select {
	case resp, ok := <-wch:
		if !ok {
			t.Fatal("unexpected watch close")
		}
		// Events should be put(a, 3) and put(a, 4)
		if len(resp.Events) != 2 {
			t.Fatal("expected two events on watch")
		}
		require.Equalf(t, "3", string(resp.Events[0].Kv.Value), "expected value=3, got event %+v", resp.Events[0])
		require.Equalf(t, "4", string(resp.Events[1].Kv.Value), "expected value=4, got event %+v", resp.Events[1])
	case <-time.After(5 * time.Second):
		t.Fatal("watch timed out")
	}
}

// TestWatchResumeCompacted checks that the watcher gracefully closes in case
// that it tries to resume to a revision that's been compacted out of the store.
// Since the watcher's server restarts with stale data, the watcher will receive
// either a compaction error or all keys by staying in sync before the compaction
// is finally applied.
func TestWatchResumeCompacted(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3, UseBridge: true})
	defer clus.Terminate(t)

	// create a waiting watcher at rev 1
	w := clus.Client(0)
	wch := w.Watch(t.Context(), "foo", clientv3.WithRev(1))
	select {
	case w := <-wch:
		t.Errorf("unexpected message from wch %v", w)
	default:
	}
	clus.Members[0].Stop(t)

	clus.WaitLeader(t)

	// put some data and compact away
	numPuts := 5
	kv := clus.Client(1)
	for i := 0; i < numPuts; i++ {
		_, err := kv.Put(t.Context(), "foo", "bar")
		require.NoError(t, err)
	}
	_, err := kv.Compact(t.Context(), 3)
	require.NoError(t, err)

	clus.Members[0].Restart(t)

	// since watch's server isn't guaranteed to be synced with the cluster when
	// the watch resumes, there is a window where the watch can stay synced and
	// read off all events; if the watcher misses the window, it will go out of
	// sync and get a compaction error.
	wRev := int64(2)
	for int(wRev) <= numPuts+1 {
		var wresp clientv3.WatchResponse
		var ok bool
		select {
		case wresp, ok = <-wch:
			require.Truef(t, ok, "expected wresp, but got closed channel")
		case <-time.After(5 * time.Second):
			t.Fatalf("compacted watch timed out")
		}
		for _, ev := range wresp.Events {
			require.Equalf(t, ev.Kv.ModRevision, wRev, "expected modRev %v, got %+v", wRev, ev)
			wRev++
		}
		if wresp.Err() == nil {
			continue
		}
		if !errors.Is(wresp.Err(), rpctypes.ErrCompacted) {
			t.Fatalf("wresp.Err() expected %v, got %+v", rpctypes.ErrCompacted, wresp.Err())
		}
		break
	}
	if int(wRev) > numPuts+1 {
		// got data faster than the compaction
		return
	}
	// received compaction error; ensure the channel closes
	select {
	case wresp, ok := <-wch:
		if ok {
			t.Fatalf("expected closed channel, but got %v", wresp)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for channel close")
	}
}

// TestWatchCompactRevision ensures the CompactRevision error is given on a
// compaction event ahead of a watcher.
func TestWatchCompactRevision(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	// set some keys
	kv := clus.RandClient()
	for i := 0; i < 5; i++ {
		_, err := kv.Put(t.Context(), "foo", "bar")
		require.NoError(t, err)
	}

	w := clus.RandClient()

	_, err := kv.Compact(t.Context(), 4)
	require.NoError(t, err)
	wch := w.Watch(t.Context(), "foo", clientv3.WithRev(2))

	// get compacted error message
	wresp, ok := <-wch
	if !ok {
		t.Fatalf("expected wresp, but got closed channel")
	}
	if !errors.Is(wresp.Err(), rpctypes.ErrCompacted) {
		t.Fatalf("wresp.Err() expected %v, but got %v", rpctypes.ErrCompacted, wresp.Err())
	}
	if !wresp.Canceled {
		t.Fatalf("wresp.Canceled expected true, got %+v", wresp)
	}

	// ensure the channel is closed
	if wresp, ok = <-wch; ok {
		t.Fatalf("expected closed channel, but got %v", wresp)
	}
}

func TestWatchWithProgressNotify2(t *testing.T)       { testWatchWithProgressNotify(t, true) }
func TestWatchWithProgressNotifyNoEvent(t *testing.T) { testWatchWithProgressNotify(t, false) }

func testWatchWithProgressNotify(t *testing.T, watchOnPut bool) {
	integration.BeforeTest(t)

	// accelerate report interval so test terminates quickly
	oldpi := v3rpc.GetProgressReportInterval()
	// using atomics to avoid race warnings
	v3rpc.SetProgressReportInterval(3 * time.Second)
	pi := 3 * time.Second
	defer func() { v3rpc.SetProgressReportInterval(oldpi) }()

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	wc := clus.RandClient()

	opts := []clientv3.OpOption{clientv3.WithProgressNotify()}
	if watchOnPut {
		opts = append(opts, clientv3.WithPrefix())
	}
	rch := wc.Watch(t.Context(), "foo", opts...)

	select {
	case resp := <-rch: // wait for notification
		if len(resp.Events) != 0 {
			t.Fatalf("resp.Events expected none, got %+v", resp.Events)
		}
	case <-time.After(2 * pi):
		t.Fatalf("watch response expected in %v, but timed out", pi)
	}

	kvc := clus.RandClient()
	_, err := kvc.Put(t.Context(), "foox", "bar")
	require.NoError(t, err)

	select {
	case resp := <-rch:
		if resp.Header.Revision != 2 {
			t.Fatalf("resp.Header.Revision expected 2, got %d", resp.Header.Revision)
		}
		if watchOnPut { // wait for put if watch on the put key
			ev := []*clientv3.Event{{
				Type: clientv3.EventTypePut,
				Kv:   &mvccpb.KeyValue{Key: []byte("foox"), Value: []byte("bar"), CreateRevision: 2, ModRevision: 2, Version: 1},
			}}
			if !reflect.DeepEqual(ev, resp.Events) {
				t.Fatalf("expected %+v, got %+v", ev, resp.Events)
			}
		} else if len(resp.Events) != 0 { // wait for notification otherwise
			t.Fatalf("expected no events, but got %+v", resp.Events)
		}
	case <-time.After(time.Duration(1.5 * float64(pi))):
		t.Fatalf("watch response expected in %v, but timed out", pi)
	}
}

func TestConfigurableWatchProgressNotifyInterval(t *testing.T) {
	integration.BeforeTest(t)

	progressInterval := 200 * time.Millisecond
	clus := integration.NewCluster(t,
		&integration.ClusterConfig{
			Size:                        3,
			WatchProgressNotifyInterval: progressInterval,
		})
	defer clus.Terminate(t)

	opts := []clientv3.OpOption{clientv3.WithProgressNotify()}
	rch := clus.RandClient().Watch(t.Context(), "foo", opts...)

	timeout := 1 * time.Second // we expect to receive watch progress notify in 2 * progressInterval,
	// but for CPU-starved situation it may take longer. So we use 1 second here for timeout.
	select {
	case resp := <-rch: // waiting for a watch progress notify response
		if !resp.IsProgressNotify() {
			t.Fatalf("expected resp.IsProgressNotify() == true")
		}
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for watch progress notify response in %v", timeout)
	}
}

func TestWatchRequestProgress(t *testing.T) {
	if integration.ThroughProxy {
		t.Skipf("grpc-proxy does not support WatchProgress yet")
	}
	testCases := []struct {
		name     string
		watchers []string
	}{
		{"0-watcher", []string{}},
		{"1-watcher", []string{"/"}},
		{"2-watcher", []string{"/", "/"}},
	}

	for _, c := range testCases {
		t.Run(c.name, func(t *testing.T) {
			integration.BeforeTest(t)

			watchTimeout := 3 * time.Second

			clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3})
			defer clus.Terminate(t)

			wc := clus.RandClient()

			var watchChans []clientv3.WatchChan

			for _, prefix := range c.watchers {
				watchChans = append(watchChans, wc.Watch(t.Context(), prefix, clientv3.WithPrefix()))
			}

			_, err := wc.Put(t.Context(), "/a", "1")
			require.NoError(t, err)

			for _, rch := range watchChans {
				select {
				case resp := <-rch: // wait for notification
					require.Lenf(t, resp.Events, 1, "resp.Events expected 1, got %d", len(resp.Events))
				case <-time.After(watchTimeout):
					t.Fatalf("watch response expected in %v, but timed out", watchTimeout)
				}
			}

			// put a value not being watched to increment revision
			_, err = wc.Put(t.Context(), "x", "1")
			require.NoError(t, err)

			require.NoError(t, wc.RequestProgress(t.Context()))

			// verify all watch channels receive a progress notify
			for _, rch := range watchChans {
				select {
				case resp := <-rch:
					require.Truef(t, resp.IsProgressNotify(), "expected resp.IsProgressNotify() == true")
					require.Equalf(t, int64(3), resp.Header.Revision, "resp.Header.Revision expected 3, got %d", resp.Header.Revision)
				case <-time.After(watchTimeout):
					t.Fatalf("progress response expected in %v, but timed out", watchTimeout)
				}
			}
		})
	}
}

func TestWatchEventType(t *testing.T) {
	integration.BeforeTest(t)

	cluster := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer cluster.Terminate(t)

	client := cluster.RandClient()
	ctx := t.Context()
	watchChan := client.Watch(ctx, "/", clientv3.WithPrefix())

	_, err := client.Put(ctx, "/toDelete", "foo")
	require.NoErrorf(t, err, "Put failed: %v", err)
	_, err = client.Put(ctx, "/toDelete", "bar")
	require.NoErrorf(t, err, "Put failed: %v", err)
	_, err = client.Delete(ctx, "/toDelete")
	require.NoErrorf(t, err, "Delete failed: %v", err)
	lcr, err := client.Lease.Grant(ctx, 1)
	require.NoErrorf(t, err, "lease create failed: %v", err)
	_, err = client.Put(ctx, "/toExpire", "foo", clientv3.WithLease(lcr.ID))
	require.NoErrorf(t, err, "Put failed: %v", err)

	tests := []struct {
		et       mvccpb.Event_EventType
		isCreate bool
		isModify bool
	}{{
		et:       clientv3.EventTypePut,
		isCreate: true,
	}, {
		et:       clientv3.EventTypePut,
		isModify: true,
	}, {
		et: clientv3.EventTypeDelete,
	}, {
		et:       clientv3.EventTypePut,
		isCreate: true,
	}, {
		et: clientv3.EventTypeDelete,
	}}

	var res []*clientv3.Event

	for {
		select {
		case wres := <-watchChan:
			res = append(res, wres.Events...)
		case <-time.After(10 * time.Second):
			t.Fatalf("Should receive %d events and then break out loop", len(tests))
		}
		if len(res) == len(tests) {
			break
		}
	}

	for i, tt := range tests {
		ev := res[i]
		if tt.et != ev.Type {
			t.Errorf("#%d: event type want=%s, get=%s", i, tt.et, ev.Type)
		}
		if tt.isCreate && !ev.IsCreate() {
			t.Errorf("#%d: event should be CreateEvent", i)
		}
		if tt.isModify && !ev.IsModify() {
			t.Errorf("#%d: event should be ModifyEvent", i)
		}
	}
}

func TestWatchErrConnClosed(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	cli := clus.Client(0)

	donec := make(chan struct{})
	go func() {
		defer close(donec)
		ch := cli.Watch(t.Context(), "foo")

		if wr := <-ch; !IsCanceled(wr.Err()) {
			t.Errorf("expected context canceled, got %v", wr.Err())
		}
	}()

	require.NoError(t, cli.ActiveConnection().Close())
	clus.TakeClient(0)

	select {
	case <-time.After(integration.RequestWaitTimeout):
		t.Fatal("wc.Watch took too long")
	case <-donec:
	}
}

func IsCanceled(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	ev, ok := status.FromError(err)
	if !ok {
		return false
	}
	code := ev.Code()
	return code == codes.Canceled
}

func TestWatchAfterClose(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	cli := clus.Client(0)
	clus.TakeClient(0)
	require.NoError(t, cli.Close())

	donec := make(chan struct{})
	go func() {
		cli.Watch(t.Context(), "foo")
		if err := cli.Close(); err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("expected %v, got %v", context.Canceled, err)
		}
		close(donec)
	}()
	select {
	case <-time.After(integration.RequestWaitTimeout):
		t.Fatal("wc.Watch took too long")
	case <-donec:
	}
}

// TestWatchWithRequireLeader checks the watch channel closes when no leader.
func TestWatchWithRequireLeader(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	// Put a key for the non-require leader watch to read as an event.
	// The watchers will be on member[0]; put key through member[0] to
	// ensure that it receives the update so watching after killing quorum
	// is guaranteed to have the key.
	liveClient := clus.Client(0)
	_, err := liveClient.Put(t.Context(), "foo", "bar")
	require.NoError(t, err)

	clus.Members[1].Stop(t)
	clus.Members[2].Stop(t)
	clus.Client(1).Close()
	clus.Client(2).Close()
	clus.TakeClient(1)
	clus.TakeClient(2)

	// wait for election timeout, then member[0] will not have a leader.
	tickDuration := 10 * time.Millisecond
	// existing streams need three elections before they're torn down; wait until 5 elections cycle
	// so proxy tests receive a leader loss event on its existing watch before creating a new watch.
	time.Sleep(time.Duration(5*clus.Members[0].ElectionTicks) * tickDuration)

	chLeader := liveClient.Watch(clientv3.WithRequireLeader(t.Context()), "foo", clientv3.WithRev(1))
	chNoLeader := liveClient.Watch(t.Context(), "foo", clientv3.WithRev(1))

	select {
	case resp, ok := <-chLeader:
		require.Truef(t, ok, "expected %v watch channel, got closed channel", rpctypes.ErrNoLeader)
		require.ErrorIsf(t, resp.Err(), rpctypes.ErrNoLeader, "expected %v watch response error, got %+v", rpctypes.ErrNoLeader, resp)
	case <-time.After(integration.RequestWaitTimeout):
		t.Fatal("watch without leader took too long to close")
	}

	select {
	case resp, ok := <-chLeader:
		require.Falsef(t, ok, "expected closed channel, got response %v", resp)
	case <-time.After(integration.RequestWaitTimeout):
		t.Fatal("waited too long for channel to close")
	}

	_, ok := <-chNoLeader
	require.Truef(t, ok, "expected response, got closed channel")

	cnt, err := clus.Members[0].Metric(
		"etcd_server_client_requests_total",
		`type="stream"`,
		fmt.Sprintf(`client_api_version="%v"`, version.APIVersion),
	)
	require.NoError(t, err)
	cv, err := strconv.ParseInt(cnt, 10, 32)
	require.NoError(t, err)
	require.GreaterOrEqualf(t, cv, int64(2), "expected at least 2, got %q", cnt)
}

// TestWatchWithFilter checks that watch filtering works.
func TestWatchWithFilter(t *testing.T) {
	integration.BeforeTest(t)

	cluster := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer cluster.Terminate(t)

	client := cluster.RandClient()
	ctx := t.Context()

	wcNoPut := client.Watch(ctx, "a", clientv3.WithFilterPut())
	wcNoDel := client.Watch(ctx, "a", clientv3.WithFilterDelete())

	_, err := client.Put(ctx, "a", "abc")
	require.NoError(t, err)
	_, err = client.Delete(ctx, "a")
	require.NoError(t, err)

	npResp := <-wcNoPut
	if len(npResp.Events) != 1 || npResp.Events[0].Type != clientv3.EventTypeDelete {
		t.Fatalf("expected delete event, got %+v", npResp.Events)
	}
	ndResp := <-wcNoDel
	if len(ndResp.Events) != 1 || ndResp.Events[0].Type != clientv3.EventTypePut {
		t.Fatalf("expected put event, got %+v", ndResp.Events)
	}

	select {
	case resp := <-wcNoPut:
		t.Fatalf("unexpected event on filtered put (%+v)", resp)
	case resp := <-wcNoDel:
		t.Fatalf("unexpected event on filtered delete (%+v)", resp)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestWatchWithCreatedNotificationDropConn ensures that
// a watcher with created notify does not post duplicate
// created events from disconnect.
func TestWatchWithCreatedNotificationDropConn(t *testing.T) {
	integration.BeforeTest(t)

	cluster := integration.NewCluster(t, &integration.ClusterConfig{Size: 1, UseBridge: true})
	defer cluster.Terminate(t)

	client := cluster.RandClient()

	wch := client.Watch(t.Context(), "a", clientv3.WithCreatedNotify())

	resp := <-wch

	require.Truef(t, resp.Created, "expected created event, got %v", resp)

	cluster.Members[0].Bridge().DropConnections()

	// check watch channel doesn't post another watch response.
	select {
	case wresp := <-wch:
		t.Fatalf("got unexpected watch response: %+v\n", wresp)
	case <-time.After(time.Second):
		// watcher may not reconnect by the time it hits the select,
		// so it wouldn't have a chance to filter out the second create event
	}
}

func TestWatchMixedPrevKVOnSameKeySeparateStreams(t *testing.T) {
	integration.BeforeTest(t)

	cluster := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer cluster.Terminate(t)

	client := cluster.RandClient()
	key := "prevkv-race-key"
	const (
		totalWatchers    = 10
		watchersWithPrev = 5
		updates          = 10
	)

	_, err := client.Put(t.Context(), key, "v0")
	require.NoError(t, err)

	type watchCase struct {
		withPrev bool
		cancel   context.CancelFunc
		ch       clientv3.WatchChan
	}
	watches := make([]watchCase, totalWatchers)
	for i := 0; i < totalWatchers; i++ {
		// Different metadata forces the client to open independent watch streams.
		md := metadata.Pairs("watch-stream-id", fmt.Sprintf("%d", i))
		mctx := metadata.NewOutgoingContext(t.Context(), md)
		wctx, cancel := context.WithCancel(mctx)

		opts := []clientv3.OpOption{clientv3.WithCreatedNotify()}
		withPrev := i < watchersWithPrev
		if withPrev {
			opts = append(opts, clientv3.WithPrevKV())
		}

		watches[i] = watchCase{
			withPrev: withPrev,
			cancel:   cancel,
			ch:       client.Watch(wctx, key, opts...),
		}
	}
	defer func() {
		for _, watch := range watches {
			watch.cancel()
		}
	}()

	for i, watch := range watches {
		resp := <-watch.ch
		require.Truef(t, resp.Created, "watcher %d expected created response, got %+v", i, resp)
		require.NoErrorf(t, resp.Err(), "watcher %d created response should not have error", i)
	}

	prevValue := "v0"
	for rev := 1; rev <= updates; rev++ {
		nextValue := fmt.Sprintf("v%d", rev)
		_, err = client.Put(t.Context(), key, nextValue)
		require.NoError(t, err)

		for i, watch := range watches {
			var resp clientv3.WatchResponse
			select {
			case resp = <-watch.ch:
			case <-time.After(10 * time.Second):
				t.Fatalf("watcher %d timed out waiting for watch response at revision %d", i, rev)
			}

			require.NoErrorf(t, resp.Err(), "watcher %d got unexpected watch error", i)
			require.Lenf(t, resp.Events, 1, "watcher %d expected one event at revision %d", i, rev)

			event := resp.Events[0]
			require.Equalf(t, key, string(event.Kv.Key), "watcher %d received event for wrong key", i)
			require.Equalf(t, nextValue, string(event.Kv.Value), "watcher %d got wrong value", i)

			if watch.withPrev {
				require.NotNilf(t, event.PrevKv, "watcher %d expected PrevKv", i)
				require.Equalf(t, prevValue, string(event.PrevKv.Value), "watcher %d got wrong PrevKv value", i)
			} else {
				require.Nilf(t, event.PrevKv, "watcher %d should not receive PrevKv", i)
			}
		}

		prevValue = nextValue
	}
}

// TestWatchCancelOnServer ensures client watcher cancels propagate back to the server.
func TestWatchCancelOnServer(t *testing.T) {
	integration.BeforeTest(t)

	cluster := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer cluster.Terminate(t)

	client := cluster.RandClient()
	numWatches := 10

	// The grpc proxy starts watches to detect leadership after the proxy server
	// returns as started; to avoid racing on the proxy's internal watches, wait
	// until require leader watches get create responses to ensure the leadership
	// watches have started.
	for {
		ctx, cancel := context.WithCancel(clientv3.WithRequireLeader(t.Context()))
		ww := client.Watch(ctx, "a", clientv3.WithCreatedNotify())
		wresp := <-ww
		cancel()
		if wresp.Err() == nil {
			break
		}
	}

	cancels := make([]context.CancelFunc, numWatches)
	for i := 0; i < numWatches; i++ {
		// force separate streams in client
		md := metadata.Pairs("some-key", fmt.Sprintf("%d", i))
		mctx := metadata.NewOutgoingContext(t.Context(), md)
		ctx, cancel := context.WithCancel(mctx)
		cancels[i] = cancel
		w := client.Watch(ctx, fmt.Sprintf("%d", i), clientv3.WithCreatedNotify())
		<-w
	}

	// get max watches; proxy tests have leadership watches, so total may be >numWatches
	maxWatches, _ := cluster.Members[0].Metric("etcd_debugging_mvcc_watcher_total")

	// cancel all and wait for cancels to propagate to etcd server
	for i := 0; i < numWatches; i++ {
		cancels[i]()
	}
	time.Sleep(time.Second)

	minWatches, err := cluster.Members[0].Metric("etcd_debugging_mvcc_watcher_total")
	require.NoError(t, err)

	maxWatchV, minWatchV := 0, 0
	n, serr := fmt.Sscanf(maxWatches+" "+minWatches, "%d %d", &maxWatchV, &minWatchV)
	if n != 2 || serr != nil {
		t.Fatalf("expected n=2 and err=nil, got n=%d and err=%v", n, serr)
	}

	require.GreaterOrEqualf(t, maxWatchV-minWatchV, numWatches, "expected %d canceled watchers, got %d", numWatches, maxWatchV-minWatchV)
}

// TestWatchOverlapContextCancel stresses the watcher stream teardown path by
// creating/canceling watchers to ensure that new watchers are not taken down
// by a torn down watch stream. The sort of race that's being detected:
//  1. create w1 using a cancelable ctx with %v as "ctx"
//  2. cancel ctx
//  3. watcher client begins tearing down watcher grpc stream since no more watchers
//  3. start creating watcher w2 using a new "ctx" (not canceled), attaches to old grpc stream
//  4. watcher client finishes tearing down stream on "ctx"
//  5. w2 comes back canceled
func TestWatchOverlapContextCancel(t *testing.T) {
	f := func(clus *integration.Cluster) {}
	testWatchOverlapContextCancel(t, f)
}

func TestWatchOverlapDropConnContextCancel(t *testing.T) {
	f := func(clus *integration.Cluster) {
		clus.Members[0].Bridge().DropConnections()
	}
	testWatchOverlapContextCancel(t, f)
}

func testWatchOverlapContextCancel(t *testing.T, f func(*integration.Cluster)) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1, UseBridge: true})
	defer clus.Terminate(t)

	n := 100
	ctxs, ctxc := make([]context.Context, 5), make([]chan struct{}, 5)
	for i := range ctxs {
		// make unique stream
		md := metadata.Pairs("some-key", fmt.Sprintf("%d", i))
		ctxs[i] = metadata.NewOutgoingContext(t.Context(), md)
		// limits the maximum number of outstanding watchers per stream
		ctxc[i] = make(chan struct{}, 2)
	}

	// issue concurrent watches on "abc" with cancel
	cli := clus.RandClient()
	_, err := cli.Put(t.Context(), "abc", "def")
	require.NoError(t, err)
	ch := make(chan struct{}, n)
	tCtx, cancelFunc := context.WithCancel(t.Context())
	defer cancelFunc()
	for i := 0; i < n; i++ {
		go func() {
			defer func() { ch <- struct{}{} }()
			idx := rand.Intn(len(ctxs))
			ctx, cancel := context.WithCancel(ctxs[idx])
			ctxc[idx] <- struct{}{}
			wch := cli.Watch(ctx, "abc", clientv3.WithRev(1))
			select {
			case <-tCtx.Done():
				cancel()
				return
			default:
			}
			f(clus)
			select {
			case _, ok := <-wch:
				if !ok {
					t.Errorf("unexpected closed channel %p", wch)
				}
			// may take a second or two to reestablish a watcher because of
			// grpc back off policies for disconnects
			case <-time.After(5 * time.Second):
				t.Errorf("timed out waiting for watch on %p", wch)
			}
			// randomize how cancel overlaps with watch creation
			if rand.Intn(2) == 0 {
				<-ctxc[idx]
				cancel()
			} else {
				cancel()
				<-ctxc[idx]
			}
		}()
	}
	// join on watches
	for i := 0; i < n; i++ {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for completed watch")
		}
	}
}

// TestWatchCancelAndCloseClient ensures that canceling a watcher then immediately
// closing the client does not return a client closing error.
func TestWatchCancelAndCloseClient(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)
	cli := clus.Client(0)
	ctx, cancel := context.WithCancel(t.Context())
	wch := cli.Watch(ctx, "abc")
	donec := make(chan struct{})
	go func() {
		defer close(donec)
		select {
		case wr, ok := <-wch:
			if ok {
				t.Errorf("expected closed watch after cancel(), got resp=%+v err=%v", wr, wr.Err())
			}
		case <-time.After(5 * time.Second):
			t.Error("timed out waiting for closed channel")
		}
	}()
	cancel()
	require.NoError(t, cli.Close())
	<-donec
	clus.TakeClient(0)
}

// TestWatchStressResumeClose establishes a bunch of watchers, disconnects
// to put them in resuming mode, cancels them so some resumes by cancel fail,
// then closes the watcher interface to ensure correct clean up.
func TestWatchStressResumeClose(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1, UseBridge: true})
	defer clus.Terminate(t)
	cli := clus.Client(0)

	ctx, cancel := context.WithCancel(t.Context())
	// add more watches than can be resumed before the cancel
	wchs := make([]clientv3.WatchChan, 2000)
	for i := range wchs {
		wchs[i] = cli.Watch(ctx, "abc")
	}
	clus.Members[0].Bridge().DropConnections()
	cancel()
	require.NoError(t, cli.Close())
	clus.TakeClient(0)
}

// TestWatchCancelDisconnected ensures canceling a watcher works when
// its grpc stream is disconnected / reconnecting.
func TestWatchCancelDisconnected(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)
	cli := clus.Client(0)
	ctx, cancel := context.WithCancel(t.Context())
	// add more watches than can be resumed before the cancel
	wch := cli.Watch(ctx, "abc")
	clus.Members[0].Stop(t)
	cancel()
	select {
	case <-wch:
	case <-time.After(time.Second):
		t.Fatal("took too long to cancel disconnected watcher")
	}
}

// TestWatchClose ensures that close does not return error
func TestWatchClose(t *testing.T) {
	runWatchTest(t, testWatchClose)
}

func testWatchClose(t *testing.T, wctx *watchctx) {
	ctx, cancel := context.WithCancel(t.Context())
	wch := wctx.w.Watch(ctx, "a")
	cancel()
	require.NotNilf(t, wch, "expected watcher channel, got nil")
	require.NoErrorf(t, wctx.w.Close(), "watch did not close successfully")
	wresp, ok := <-wch
	require.Falsef(t, ok, "read wch got %v; expected closed channel", wresp)
}

func TestWatch(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	t.Cleanup(func() { clus.Terminate(t) })
	client := clus.Client(0)

	tcs := []struct {
		name       string
		key        string
		opts       []clientv3.OpOption
		wantError  error
		wantEvents []*clientv3.Event
	}{
		{
			name:      "Watch with negative revision",
			key:       "/",
			opts:      []clientv3.OpOption{clientv3.WithRev(-1)},
			wantError: rpctypes.ErrCompacted,
		},
		{
			name: "Watch with zero revision",
			key:  "/",
			opts: []clientv3.OpOption{clientv3.WithRev(0)},
		},
		{
			name: "Watch with positive revision",
			key:  "/",
			opts: []clientv3.OpOption{clientv3.WithRev(1)},
		},
	}
	ctx := t.Context()

	t.Log("Open watches")
	watches := make([]clientv3.WatchChan, len(tcs))
	for i, tc := range tcs {
		watchCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		watches[i] = client.Watch(watchCtx, tc.key, tc.opts...)
	}

	t.Log("Validate")
	for i, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			events, err := collectEvents(ctx, watches[i])
			if tc.wantError == nil {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.wantError.Error())
			}
			if diff := cmp.Diff(tc.wantEvents, events, protocmp.Transform()); diff != "" {
				t.Errorf("unexpected events (-want +got):\n%s", diff)
			}
		})
	}
}

func collectEvents(ctx context.Context, watch clientv3.WatchChan) (events []*clientv3.Event, err error) {
	for {
		select {
		case resp, ok := <-watch:
			if !ok {
				return events, nil
			}
			err := resp.Err()
			if err != nil {
				return events, err
			}
			events = append(events, resp.Events...)
		case <-ctx.Done():
			return events, ctx.Err()
		// Watch resync interval * 1.5
		case <-time.After(150 * time.Millisecond):
			return events, nil
		}
	}
}
