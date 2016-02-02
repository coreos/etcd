// Copyright 2016 CoreOS, Inc.
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
	"time"

	"github.com/coreos/etcd/Godeps/_workspace/src/golang.org/x/net/context"
	"github.com/coreos/etcd/Godeps/_workspace/src/google.golang.org/grpc"
	pb "github.com/coreos/etcd/etcdserver/etcdserverpb"
	"github.com/coreos/etcd/lease"
)

type (
	LeaseCreateResponse    pb.LeaseCreateResponse
	LeaseRevokeResponse    pb.LeaseRevokeResponse
	LeaseKeepAliveResponse pb.LeaseKeepAliveResponse
)

const (
	// a small buffer to store unsent lease responses.
	leaseResponseChSize = 16
)

type Lease interface {
	// Create creates a new lease.
	Create(ctx context.Context, ttl int64) (*LeaseCreateResponse, error)

	// Revoke revokes the given lease.
	Revoke(ctx context.Context, id lease.LeaseID) (*LeaseRevokeResponse, error)

	// KeepAlive keeps the given lease alive forever.
	KeepAlive(ctx context.Context, id lease.LeaseID) (<-chan *LeaseKeepAliveResponse, error)

	// KeepAliveOnce renews the lease once. In most of the cases, Keepalive
	// should be used instead of KeepAliveOnce.
	KeepAliveOnce(ctx context.Context, id lease.LeaseID) (*LeaseKeepAliveResponse, error)

	// Lease keeps internal routines and connections for efficient communication with etcd server.
	// After using Lease, call Close() to release all related resources.
	Close() error
}

type lessor struct {
	c *Client

	mu      sync.Mutex       // guards all fields
	conn    *grpc.ClientConn // conn in-use
	initedc chan bool

	remote pb.LeaseClient

	stream       pb.Lease_LeaseKeepAliveClient
	streamCancel context.CancelFunc

	stopCtx    context.Context
	stopCancel context.CancelFunc

	keepAlives map[lease.LeaseID]chan *LeaseKeepAliveResponse
	deadlines  map[lease.LeaseID]time.Time
}

func NewLease(c *Client) Lease {
	l := &lessor{
		c:    c,
		conn: c.ActiveConnection(),

		initedc: make(chan bool, 1),

		keepAlives: make(map[lease.LeaseID]chan *LeaseKeepAliveResponse),
		deadlines:  make(map[lease.LeaseID]time.Time),
	}

	l.remote = pb.NewLeaseClient(l.conn)
	l.stopCtx, l.stopCancel = context.WithCancel(context.Background())

	l.initedc <- false

	go l.recvKeepAliveLoop()
	go l.sendKeepAliveLoop()

	return l
}

func (l *lessor) Create(ctx context.Context, ttl int64) (*LeaseCreateResponse, error) {
	cctx, cancel := context.WithCancel(ctx)
	done := cancelWhenStop(cancel, l.stopCtx.Done())
	defer close(done)

	for {
		r := &pb.LeaseCreateRequest{TTL: ttl}
		resp, err := l.getRemote().LeaseCreate(cctx, r)
		if err == nil {
			return (*LeaseCreateResponse)(resp), nil
		}

		if isRPCError(err) {
			return nil, err
		}
		if nerr := l.switchRemoteAndStream(err); nerr != nil {
			return nil, nerr
		}
	}
}

func (l *lessor) Revoke(ctx context.Context, id lease.LeaseID) (*LeaseRevokeResponse, error) {
	cctx, cancel := context.WithCancel(ctx)
	done := cancelWhenStop(cancel, l.stopCtx.Done())
	defer close(done)

	for {
		r := &pb.LeaseRevokeRequest{ID: int64(id)}
		resp, err := l.getRemote().LeaseRevoke(cctx, r)

		if err == nil {
			return (*LeaseRevokeResponse)(resp), nil
		}

		if isRPCError(err) {
			return nil, err
		}

		if nerr := l.switchRemoteAndStream(err); nerr != nil {
			return nil, nerr
		}
	}
}

func (l *lessor) KeepAlive(ctx context.Context, id lease.LeaseID) (<-chan *LeaseKeepAliveResponse, error) {
	lc := make(chan *LeaseKeepAliveResponse, leaseResponseChSize)

	// todo: add concellation based on the passed in ctx

	l.mu.Lock()
	_, ok := l.keepAlives[id]
	if !ok {
		l.keepAlives[id] = lc
		l.deadlines[id] = time.Now()
		l.mu.Unlock()
		return lc, nil
	}
	l.mu.Unlock()

	resp, err := l.KeepAliveOnce(ctx, id)
	if err != nil {
		return nil, err
	}
	lc <- resp
	return lc, nil
}

func (l *lessor) KeepAliveOnce(ctx context.Context, id lease.LeaseID) (*LeaseKeepAliveResponse, error) {
	cctx, cancel := context.WithCancel(ctx)
	done := cancelWhenStop(cancel, l.stopCtx.Done())
	defer close(done)

	for {
		resp, err := l.keepAliveOnce(cctx, id)
		if err == nil {
			return resp, err
		}

		nerr := l.switchRemoteAndStream(err)
		if nerr != nil {
			return nil, nerr
		}
	}
}

func (l *lessor) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.stopCancel()
	l.stream = nil

	return nil
}

func (l *lessor) keepAliveOnce(ctx context.Context, id lease.LeaseID) (*LeaseKeepAliveResponse, error) {
	stream, err := l.getRemote().LeaseKeepAlive(ctx)
	if err != nil {
		return nil, err
	}

	err = stream.Send(&pb.LeaseKeepAliveRequest{ID: int64(id)})
	if err != nil {
		return nil, err
	}

	resp, rerr := stream.Recv()
	if rerr != nil {
		return nil, rerr
	}
	return (*LeaseKeepAliveResponse)(resp), nil
}

func (l *lessor) recvKeepAliveLoop() {
	if !l.initStream() {
		l.Close()
		return
	}

	defer func() {
		l.mu.Lock()
		defer l.mu.Unlock()

		for _, ch := range l.keepAlives {
			close(ch)
		}
	}()

	for {
		stream := l.getKeepAliveStream()

		resp, err := stream.Recv()
		if err != nil {
			if isRPCError(err) {
				l.Close()
				return
			}

			err = l.switchRemoteAndStream(err)
			if err != nil {
				l.Close()
				return
			}
			continue
		}

		l.mu.Lock()
		lch, ok := l.keepAlives[lease.LeaseID(resp.ID)]
		if !ok {
			l.mu.Unlock()
			continue
		}

		if resp.TTL <= 0 {
			close(lch)
			delete(l.deadlines, lease.LeaseID(resp.ID))
			delete(l.keepAlives, lease.LeaseID(resp.ID))
		} else {
			select {
			case lch <- (*LeaseKeepAliveResponse)(resp):
				l.deadlines[lease.LeaseID(resp.ID)] =
					time.Now().Add(1 + time.Duration(resp.TTL/3)*time.Second)
			default:
			}
		}
		l.mu.Unlock()
	}
}

func (l *lessor) sendKeepAliveLoop() {
	if !l.initStream() {
		l.Close()
		return
	}

	for {
		select {
		case <-time.After(500 * time.Millisecond):
		case <-l.stopCtx.Done():
			return
		}

		tosend := make([]lease.LeaseID, 0)

		now := time.Now()
		l.mu.Lock()
		for id, d := range l.deadlines {
			if d.Before(now) {
				tosend = append(tosend, id)
			}
		}
		l.mu.Unlock()

		stream := l.getKeepAliveStream()

		var err error
		for _, id := range tosend {
			r := &pb.LeaseKeepAliveRequest{ID: int64(id)}
			err = stream.Send(r)
			if err != nil {
				if isRPCError(err) {
					l.Close()
					return
				}
				break
			}
		}

		if err != nil {
			err = l.switchRemoteAndStream(err)
			if err != nil {
				l.Close()
				return
			}
		}
	}
}

func (l *lessor) getRemote() pb.LeaseClient {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.remote
}

func (l *lessor) getKeepAliveStream() pb.Lease_LeaseKeepAliveClient {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.stream
}

func (l *lessor) switchRemoteAndStream(prevErr error) error {
	l.mu.Lock()
	conn := l.conn
	l.mu.Unlock()

	var (
		err     error
		newConn *grpc.ClientConn
	)

	if prevErr != nil {
		conn.Close()
		newConn, err = l.c.retryConnection(conn, prevErr)
		if err != nil {
			return err
		}
	}

	l.mu.Lock()
	if newConn != nil {
		l.conn = newConn
	}

	l.remote = pb.NewLeaseClient(l.conn)
	l.mu.Unlock()

	serr := l.newStream()
	if serr != nil {
		return serr
	}
	return nil
}

func (l *lessor) newStream() error {
	sctx, cancel := context.WithCancel(l.stopCtx)
	stream, err := l.getRemote().LeaseKeepAlive(sctx)
	if err != nil {
		cancel()
		return err
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stream != nil && l.streamCancel != nil {
		l.stream.CloseSend()
		l.streamCancel()
	}

	l.streamCancel = cancel
	l.stream = stream
	return nil
}

func (l *lessor) initStream() bool {
	ok := <-l.initedc
	if ok {
		return true
	}

	err := l.switchRemoteAndStream(nil)
	if err == nil {
		l.initedc <- true
		return true
	}
	l.initedc <- false
	return false
}

// cancelWhenStop calls cancel when the given stopc fires. It returns a done chan. done
// should be closed when the work is finished. When done fires, cancelWhenStop will release
// its internal resource.
func cancelWhenStop(cancel context.CancelFunc, stopc <-chan struct{}) chan<- struct{} {
	done := make(chan struct{}, 1)

	go func() {
		select {
		case <-stopc:
		case <-done:
		}
		cancel()
	}()

	return done
}
