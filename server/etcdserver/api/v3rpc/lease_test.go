package v3rpc

import (
	"context"
	"io"
	"runtime"
	"testing"
	"time"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/server/v3/etcdserver"
	"go.etcd.io/etcd/server/v3/etcdserver/apply"
	"go.etcd.io/etcd/server/v3/lease"
	"go.uber.org/zap/zaptest"
	"google.golang.org/grpc/metadata"
)

type mockLessor struct {
	etcdserver.Lessor // embed to satisfy the rest of the interface if needed, but methods are nil and will panic if called unexpectedly
}

func (m *mockLessor) LeaseRenew(ctx context.Context, id lease.LeaseID) (int64, error) {
	return 10, nil // return 10 TTL
}

type mockLeaseStream struct {
	ctx    context.Context
	cancel context.CancelFunc
	reqCh  chan *pb.LeaseKeepAliveRequest
}

func (m *mockLeaseStream) Recv() (*pb.LeaseKeepAliveRequest, error) {
	select {
	case req, ok := <-m.reqCh:
		if !ok {
			return nil, io.EOF
		}
		return req, nil
	case <-m.ctx.Done():
		return nil, m.ctx.Err()
	}
}

func (m *mockLeaseStream) Send(resp *pb.LeaseKeepAliveResponse) error {
	select {
	case <-m.ctx.Done():
		return m.ctx.Err()
	default:
		return nil
	}
}

func (m *mockLeaseStream) Context() context.Context {
	return m.ctx
}

func (m *mockLeaseStream) RecvMsg(m_ interface{}) error { return nil }
func (m *mockLeaseStream) SendMsg(m_ interface{}) error { return nil }
func (m *mockLeaseStream) SendHeader(metadata.MD) error { return nil }
func (m *mockLeaseStream) SetHeader(metadata.MD) error  { return nil }
func (m *mockLeaseStream) SetTrailer(metadata.MD)       {}

type mockRaftStatusGetter struct{
	apply.RaftStatusGetter
}

func (m *mockRaftStatusGetter) Term() uint64 { return 1 }

func TestLeaseKeepAlive_GoroutineLeakOnDisconnect(t *testing.T) {
	// Let the runtime stabilize
	time.Sleep(100 * time.Millisecond)
	baseNum := runtime.NumGoroutine()

	ls := &LeaseServer{
		lg:  zaptest.NewLogger(t),
		le:  &mockLessor{},
	}
	ls.hdr = header{
		rev: func() int64 { return 1 },
		sg:  &mockRaftStatusGetter{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	reqCh := make(chan *pb.LeaseKeepAliveRequest, 10)

	stream := &mockLeaseStream{
		ctx:    ctx,
		cancel: cancel,
		reqCh:  reqCh,
	}

	// Launch LeaseKeepAlive which shouldn't leak goroutines
	done := make(chan struct{})
	go func() {
		_ = ls.LeaseKeepAlive(stream)
		close(done)
	}()

	// Send 5 valid keep-alive frames
	for i := 0; i < 5; i++ {
		reqCh <- &pb.LeaseKeepAliveRequest{ID: int64(i + 1)}
	}

	// Yield and wait briefly so the frames are processed
	time.Sleep(100 * time.Millisecond)

	// Deliberately abort the client connection abruptly by cancelling the stream context
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("LeaseKeepAlive did not return promptly on stream context cancellation")
	}

	// Wait 500ms for network state propagation
	time.Sleep(500 * time.Millisecond)

	finalNum := runtime.NumGoroutine()
	// Tolerate a small drift for system goroutines (like GC), but not an orphaned stream reader
	if finalNum > baseNum+2 {
		t.Fatalf("Goroutine leak detected: baseline %d, final %d", baseNum, finalNum)
	}
}
