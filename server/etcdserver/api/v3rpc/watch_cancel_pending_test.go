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

package v3rpc

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
	"google.golang.org/grpc/metadata"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"
	"go.etcd.io/etcd/client/pkg/v3/types"
	"go.etcd.io/etcd/server/v3/storage/mvcc"
)

// stubWatchStream is an mvcc.WatchStream whose event channel is driven by the
// test, so the interleaving between control messages and events is chosen
// explicitly instead of being left to the select statement in sendLoop.
type stubWatchStream struct {
	ch chan mvcc.WatchResponse
}

func (s *stubWatchStream) Watch(context.Context, mvcc.WatchID, []byte, []byte, int64, ...mvcc.FilterFunc) (mvcc.WatchID, error) {
	return 0, nil
}
func (s *stubWatchStream) Chan() <-chan mvcc.WatchResponse { return s.ch }
func (s *stubWatchStream) RequestProgress(mvcc.WatchID)    {}
func (s *stubWatchStream) RequestProgressAll() bool        { return false }
func (s *stubWatchStream) Cancel(mvcc.WatchID) error       { return nil }
func (s *stubWatchStream) Close()                          {}
func (s *stubWatchStream) Rev() int64                      { return 1 }

// recordingWatchServer records every WatchResponse that sendLoop hands to the
// gRPC layer, which is the only thing a client can actually observe.
type recordingWatchServer struct {
	ctx  context.Context
	sent chan *pb.WatchResponse
}

func (r *recordingWatchServer) Send(wr *pb.WatchResponse) error {
	r.sent <- wr
	return nil
}
func (r *recordingWatchServer) Recv() (*pb.WatchRequest, error) { return nil, io.EOF }
func (r *recordingWatchServer) SetHeader(metadata.MD) error     { return nil }
func (r *recordingWatchServer) SendHeader(metadata.MD) error    { return nil }
func (r *recordingWatchServer) SetTrailer(metadata.MD)          {}
func (r *recordingWatchServer) Context() context.Context        { return r.ctx }
func (r *recordingWatchServer) SendMsg(any) error               { return nil }
func (r *recordingWatchServer) RecvMsg(any) error               { return nil }

type stubRaftStatusGetter struct{}

func (stubRaftStatusGetter) MemberID() types.ID     { return 1 }
func (stubRaftStatusGetter) Leader() types.ID       { return 1 }
func (stubRaftStatusGetter) CommittedIndex() uint64 { return 1 }
func (stubRaftStatusGetter) AppliedIndex() uint64   { return 1 }
func (stubRaftStatusGetter) Term() uint64           { return 1 }

// sendLoopHarness runs the real serverWatchStream.sendLoop over stub channels.
type sendLoopHarness struct {
	t    *testing.T
	sws  *serverWatchStream
	ws   *stubWatchStream
	grpc *recordingWatchServer
}

func newSendLoopHarness(t *testing.T) *sendLoopHarness {
	t.Helper()

	ws := &stubWatchStream{ch: make(chan mvcc.WatchResponse, 8)}
	gs := &recordingWatchServer{ctx: t.Context(), sent: make(chan *pb.WatchResponse, 16)}

	sws := &serverWatchStream{
		lg:          zaptest.NewLogger(t),
		sg:          stubRaftStatusGetter{},
		gRPCStream:  gs,
		watchStream: ws,
		ctrlStream:  make(chan *pb.WatchResponse, ctrlStreamBufLen),
		creating:    make(map[mvcc.WatchID]struct{}),
		progress:    make(map[mvcc.WatchID]bool),
		prevKV:      make(map[mvcc.WatchID]bool),
		fragment:    make(map[mvcc.WatchID]bool),
		closec:      make(chan struct{}),
	}

	sws.wg.Add(1)
	go func() {
		defer sws.wg.Done()
		sws.sendLoop()
	}()

	h := &sendLoopHarness{t: t, sws: sws, ws: ws, grpc: gs}
	t.Cleanup(h.stop)
	return h
}

// stop shuts sendLoop down the way serverWatchStream.close() does. The loop
// returns on closec and its deferred cleanup ranges over the event channel, so
// that channel has to be closed for Wait to return.
func (h *sendLoopHarness) stop() {
	close(h.sws.closec)
	close(h.ws.ch)
	h.sws.wg.Wait()
}

// ctrl queues a control response the way recvLoop does.
func (h *sendLoopHarness) ctrl(wr *pb.WatchResponse) { h.sws.ctrlStream <- wr }

// register mirrors what recvLoop does when it creates a watcher: the watch ID
// is recorded as awaiting its created response while mu is held across
// watchStream.Watch(), so that sendLoop can tell an event that arrives before
// the created response from an event of an already canceled watch.
func (h *sendLoopHarness) register(wid mvcc.WatchID) {
	h.sws.mu.Lock()
	h.sws.creating[wid] = struct{}{}
	h.sws.mu.Unlock()
}

// event queues a response on the mvcc event channel that every watcher of a
// stream shares.
func (h *sendLoopHarness) event(wr mvcc.WatchResponse) { h.ws.ch <- wr }

// nextSent waits for the next response sendLoop writes to the gRPC stream.
func (h *sendLoopHarness) nextSent(what string) *pb.WatchResponse {
	h.t.Helper()
	select {
	case wr := <-h.grpc.sent:
		return wr
	case <-time.After(10 * time.Second):
		h.t.Fatalf("timed out waiting for %s", what)
		return nil
	}
}

// expectNothingSent asserts that sendLoop forwards nothing for a grace period.
func (h *sendLoopHarness) expectNothingSent(grace time.Duration, what string) {
	h.t.Helper()
	select {
	case wr := <-h.grpc.sent:
		h.t.Fatalf("%s: unexpected response sent to the client: %+v", what, wr)
	case <-time.After(grace):
	}
}

// barrier pushes an event for a still-active watch and waits for it to be
// forwarded. Every watcher of a stream shares one FIFO event channel, so
// observing this response proves sendLoop has already classified everything
// queued ahead of it. It doubles as the assertion that nothing else was
// forwarded first.
func (h *sendLoopHarness) barrier(active mvcc.WatchID, rev int64) {
	h.t.Helper()
	h.event(putEvent(active, "barrier", rev))
	got := h.nextSent("barrier event")
	require.Equalf(h.t, int64(active), got.WatchId,
		"expected the barrier event of watch %d, got a response for watch %d: %+v",
		active, got.WatchId, got)
}

func putEvent(wid mvcc.WatchID, key string, rev int64) mvcc.WatchResponse {
	return mvcc.WatchResponse{
		WatchID:  wid,
		Revision: rev,
		Events: []*mvccpb.Event{{
			Type: mvccpb.Event_PUT,
			Kv: &mvccpb.KeyValue{
				Key:            []byte(key),
				CreateRevision: rev,
				ModRevision:    rev,
			},
		}},
	}
}

// pendingEventsGaugeValue reads etcd_debugging_mvcc_pending_events_total, which
// the mvcc package registers in the default prometheus registry.
func pendingEventsGaugeValue(t *testing.T) float64 {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() != "etcd_debugging_mvcc_pending_events_total" {
			continue
		}
		require.Len(t, mf.GetMetric(), 1)
		return mf.GetMetric()[0].GetGauge().GetValue()
	}
	t.Fatal("etcd_debugging_mvcc_pending_events_total is not registered")
	return 0
}

// TestSendLoopDiscardsEventsOfCanceledWatch exercises the sendLoop state
// confusion between "the created response has not been processed yet" and "the
// watch is already canceled". Both look identical to sendLoop, which decides
// whether to buffer a response by looking the watch ID up in ids while the
// cancel branch only removes the ID from ids. An mvcc event that was queued
// before the cancel therefore lands in pending[wid], waiting for a Created
// response that will never arrive for that watch.
//
// The interleaving is forced, not raced: the event channel is empty while the
// cancel is processed and sendLoop is a single goroutine, so the late event
// cannot be observed before the cancel branch has run to completion.
func TestSendLoopDiscardsEventsOfCanceledWatch(t *testing.T) {
	const (
		wid  = 5 // the watch that gets canceled
		keep = 9 // a watch that stays active for the whole test
	)

	h := newSendLoopHarness(t)

	// Both watches are created and acknowledged, so sendLoop records them.
	h.register(wid)
	h.ctrl(&pb.WatchResponse{WatchId: wid, Created: true})
	require.True(t, h.nextSent("created response").Created)
	h.register(keep)
	h.ctrl(&pb.WatchResponse{WatchId: keep, Created: true})
	require.True(t, h.nextSent("created response").Created)

	// The client cancels wid, and sendLoop drops it from ids.
	h.ctrl(&pb.WatchResponse{WatchId: wid, Canceled: true})
	require.True(t, h.nextSent("canceled response").Canceled)

	// An event mvcc had queued for the now dead watch arrives afterwards. Not
	// forwarding it is correct, the client already got the cancel ack.
	h.event(putEvent(wid, "stale-key", 42))
	h.barrier(keep, 43)

	// It must not be retained either. A client may reuse an explicit watch ID
	// as soon as the cancel is acknowledged, because mvcc only rejects IDs that
	// are still live, and the Created branch flushes pending[wid]
	// unconditionally. The new watch must not inherit the dead watch's event.
	h.register(wid)
	h.ctrl(&pb.WatchResponse{WatchId: wid, Created: true})
	require.True(t, h.nextSent("created response for the reused watch id").Created)
	h.barrier(keep, 44)
}

// TestSendLoopAccountsEventsOfCanceledWatch asserts that dropping the late
// event of a canceled watch also reconciles the pending-events gauge. Before
// the fix the late event took the pending branch of sendLoop, which continues
// before mvcc.ReportEventReceived is reached, so the gauge stayed elevated for
// as long as the gRPC watch stream lived.
func TestSendLoopAccountsEventsOfCanceledWatch(t *testing.T) {
	const (
		wid  = 5
		keep = 9
	)

	h := newSendLoopHarness(t)

	h.register(wid)
	h.ctrl(&pb.WatchResponse{WatchId: wid, Created: true})
	require.True(t, h.nextSent("created response").Created)
	h.register(keep)
	h.ctrl(&pb.WatchResponse{WatchId: keep, Created: true})
	require.True(t, h.nextSent("created response").Created)
	h.ctrl(&pb.WatchResponse{WatchId: wid, Canceled: true})
	require.True(t, h.nextSent("canceled response").Canceled)

	before := pendingEventsGaugeValue(t)

	h.event(putEvent(wid, "stale-key", 42))
	h.barrier(keep, 43)

	// sendLoop consumed two events, so both should have been accounted for.
	require.Equalf(t, before-2, pendingEventsGaugeValue(t),
		"the dropped event of canceled watch %d was never accounted for", wid)
}

// TestSendLoopFlushesEventsBufferedBeforeCreated guards the behavior pending
// actually exists for: an event that reaches sendLoop before the Created
// response must be buffered and delivered once the watch is announced. The fix
// for the canceled case must not break this.
func TestSendLoopFlushesEventsBufferedBeforeCreated(t *testing.T) {
	const (
		wid  = 5
		keep = 9
	)

	h := newSendLoopHarness(t)

	h.register(keep)
	h.ctrl(&pb.WatchResponse{WatchId: keep, Created: true})
	require.True(t, h.nextSent("created response").Created)

	// recvLoop has created the watcher, so the ID is registered as awaiting its
	// created response, but sendLoop has not processed that response yet.
	h.register(wid)
	h.event(putEvent(wid, "early-key", 42))
	h.barrier(keep, 43)

	h.ctrl(&pb.WatchResponse{WatchId: wid, Created: true})
	require.True(t, h.nextSent("created response").Created)

	flushed := h.nextSent("event buffered before the created response")
	require.Equal(t, int64(wid), flushed.WatchId)
	require.Len(t, flushed.Events, 1)
	require.Equal(t, []byte("early-key"), flushed.Events[0].Kv.Key)
}
