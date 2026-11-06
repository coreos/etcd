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

package watch

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/tests/v3/framework/integration"
	gofail "go.etcd.io/gofail/runtime"
)

func watchCreateReq(wid int64, key string) *pb.WatchRequest {
	return &pb.WatchRequest{RequestUnion: &pb.WatchRequest_CreateRequest{
		CreateRequest: &pb.WatchCreateRequest{
			WatchId: wid,
			Key:     []byte(key),
		},
	}}
}

func watchCancelReq(wid int64) *pb.WatchRequest {
	return &pb.WatchRequest{RequestUnion: &pb.WatchRequest_CancelRequest{
		CancelRequest: &pb.WatchCancelRequest{WatchId: wid},
	}}
}

func watchGauge(t *testing.T, m *integration.Member, name string) float64 {
	t.Helper()
	raw, err := m.Metric(name)
	require.NoErrorf(t, err, "scraping %s", name)
	v, err := strconv.ParseFloat(raw, 64)
	require.NoErrorf(t, err, "parsing %s=%q", name, raw)
	return v
}

const (
	pendingEventsMetric = "etcd_debugging_mvcc_pending_events_total"
	watcherTotalMetric  = "etcd_debugging_mvcc_watcher_total"
)

// TestV3WatchCancelRetainsPendingEvents reproduces over a real gRPC watch
// stream what TestSendLoopDiscardsEventsOfCanceledWatch shows at the unit
// level: sendLoop treats a watch ID that is missing from its ids map as "the
// created response has not been processed yet", which is also true of a watch
// that was just canceled. Events mvcc had already queued for the canceled
// watch are therefore buffered in pending[wid] instead of being dropped.
//
// They are released only when the whole gRPC stream goes away, and until then
// a watch that reuses the ID receives them. Reusing an explicit watch ID is
// allowed: rpc.proto only rejects an ID that is still in use on the stream.
func TestV3WatchCancelRetainsPendingEvents(t *testing.T) {
	integration.BeforeTest(t)
	integration.SkipIfNoGoFail(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	const (
		staleWID = int64(5)
		keepWID  = int64(9)
		writes   = 30
	)

	member := clus.Members[0]
	client := clus.RandClient()
	kvc := integration.ToGRPC(client).KV

	wStream, err := integration.ToGRPC(client).Watch.Watch(ctx)
	require.NoError(t, err)

	// A watch that stays alive for the whole test. Real clients multiplex many
	// watches onto one gRPC stream and clientv3 tears the stream down only
	// once the last one is gone, so sendLoop keeps running with its pending
	// map intact.
	require.NoError(t, wStream.Send(watchCreateReq(keepWID, "keep")))
	resp, err := wStream.Recv()
	require.NoError(t, err)
	require.True(t, resp.Created)
	require.Equal(t, keepWID, resp.WatchId)

	// The watch that gets canceled while its events are still queued.
	require.NoError(t, wStream.Send(watchCreateReq(staleWID, "foo")))
	resp, err = wStream.Recv()
	require.NoError(t, err)
	require.True(t, resp.Created)
	require.Equal(t, staleWID, resp.WatchId)

	basePending := watchGauge(t, member, pendingEventsMetric)
	baseWatchers := watchGauge(t, member, watcherTotalMetric)
	t.Logf("after creating both watches: pending_events=%.0f watcher_total=%.0f",
		basePending, baseWatchers)

	// Throttle sendLoop to one forwarded event per second, so the writes below
	// pile up in the 128 slot mvcc event channel every watcher of the stream
	// shares.
	require.NoError(t, gofail.Enable("beforeSendWatchResponse", `sleep("1s")`))
	failpointEnabled := true
	defer func() {
		if failpointEnabled {
			require.NoError(t, gofail.Disable("beforeSendWatchResponse"))
		}
	}()

	for range writes {
		_, perr := kvc.Put(ctx, &pb.PutRequest{Key: []byte("foo"), Value: []byte("bar")})
		require.NoError(t, perr)
	}

	queuedPending := watchGauge(t, member, pendingEventsMetric)
	t.Logf("after %d writes to foo: pending_events=%.0f", writes, queuedPending)

	// Cancel the watch. The cancel ack travels on ctrlStream, a different
	// channel from the queued events, so sendLoop's select can and does pick
	// it up before it has drained those events.
	require.NoError(t, wStream.Send(watchCancelReq(staleWID)))

	forwarded := 0
	for {
		resp, err = wStream.Recv()
		require.NoError(t, err)
		if resp.WatchId == staleWID && resp.Canceled {
			break
		}
		if resp.WatchId == staleWID {
			forwarded += len(resp.Events)
		}
	}
	t.Logf("%d of %d foo events were forwarded before the cancel was processed",
		forwarded, writes)
	require.Lessf(t, forwarded, writes,
		"the cancel did not overtake the queued events, nothing is left to retain")

	require.NoError(t, gofail.Disable("beforeSendWatchResponse"))
	failpointEnabled = false

	// Let sendLoop drain the rest of the event channel. Events of the canceled
	// watch take the pending branch, which continues before the send, so this
	// is fast and puts nothing on the wire.
	time.Sleep(2 * time.Second)

	heldPending := watchGauge(t, member, pendingEventsMetric)
	heldWatchers := watchGauge(t, member, watcherTotalMetric)
	t.Logf("after the cancel: pending_events=%.0f watcher_total=%.0f", heldPending, heldWatchers)

	// One of the two watchers is gone from the store, so the watcher gauge
	// drops. The pending-events gauge is the one that should have followed it.
	assert.InDeltaf(t, baseWatchers-1, heldWatchers, 0.5,
		"the canceled watcher should no longer be in the store")
	assert.InDeltaf(t, basePending, heldPending, 0.5,
		"%.0f events of the canceled watch are still counted as pending",
		heldPending-basePending)

	// Reuse the watch ID on a different key. mvcc only rejects an ID that is
	// still live, so this succeeds, and sendLoop's Created branch flushes
	// pending[staleWID] unconditionally.
	require.NoError(t, wStream.Send(watchCreateReq(staleWID, "bar")))
	resp, err = wStream.Recv()
	require.NoError(t, err)
	require.True(t, resp.Created)
	require.Equal(t, staleWID, resp.WatchId)

	recvc := make(chan *pb.WatchResponse, 256)
	go func() {
		defer close(recvc)
		for {
			r, rerr := wStream.Recv()
			if rerr != nil {
				return
			}
			recvc <- r
		}
	}()

	// The new watch asked for "bar". Anything carrying "foo" belongs to the
	// watch that was canceled before this one existed.
	inherited := 0
	collected := time.After(5 * time.Second)
	for done := false; !done; {
		select {
		case r, ok := <-recvc:
			if !ok {
				done = true
				continue
			}
			for _, ev := range r.Events {
				if string(ev.Kv.Key) == "foo" {
					inherited++
				}
			}
		case <-collected:
			done = true
		}
	}
	t.Logf("the watch that reused id %d received %d events for key foo", staleWID, inherited)
	assert.Zerof(t, inherited,
		"watch %d inherited %d events of the watch that was canceled before it existed",
		staleWID, inherited)

	// Closing the whole gRPC stream is what releases the retained responses
	// today: sendLoop returns and its deferred cleanup reconciles the count.
	cancel()
	afterClose := heldPending
	for range 50 {
		time.Sleep(200 * time.Millisecond)
		afterClose = watchGauge(t, member, pendingEventsMetric)
		if afterClose <= basePending {
			break
		}
	}
	t.Logf("after closing the gRPC stream: pending_events=%.0f", afterClose)
}
