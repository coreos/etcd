// Copyright 2015 CoreOS, Inc.
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

package rafthttp

import (
	"log"
	"net/http"
	"time"

	"github.com/coreos/etcd/Godeps/_workspace/src/golang.org/x/net/context"
	"github.com/coreos/etcd/etcdserver/stats"
	"github.com/coreos/etcd/pkg/types"
	"github.com/coreos/etcd/raft/raftpb"
)

const (
	DialTimeout = time.Second
	// ConnRead/WriteTimeout is the i/o timeout set on each connection rafthttp pkg creates.
	// A 5 seconds timeout is good enough for recycling bad connections. Or we have to wait for
	// tcp keepalive failing to detect a bad connection, which is at minutes level.
	// For long term streaming connections, rafthttp pkg sends application level linkHeartbeat
	// to keep the connection alive.
	// For short term pipeline connections, the connection MUST be killed to avoid it being
	// put back to http pkg connection pool.
	ConnReadTimeout  = 5 * time.Second
	ConnWriteTimeout = 5 * time.Second

	recvBufSize = 4096

	streamApp   = "streamMsgApp"
	streamMsg   = "streamMsg"
	pipelineMsg = "pipeline"
)

var (
	bufSizeMap = map[string]int{
		streamApp:   streamBufSize,
		streamMsg:   streamBufSize,
		pipelineMsg: pipelineBufSize,
	}
)

type Peer interface {
	// Send sends the message to the remote peer. The function is non-blocking
	// and has no promise that the message will be received by the remote.
	// When it fails to send message out, it will report the status to underlying
	// raft.
	Send(m raftpb.Message)
	// Update updates the urls of remote peer.
	Update(u string)
	// attachOutgoingConn attachs the outgoing connection to the peer for
	// stream usage. After the call, the ownership of the outgoing
	// connection hands over to the peer. The peer will close the connection
	// when it is no longer used.
	attachOutgoingConn(conn *outgoingConn)
	// Stop performs any necessary finalization and terminates the peer
	// elegantly.
	Stop()
}

// peer is the representative of a remote raft node. Local raft node sends
// messages to the remote through peer.
// Each peer has two underlying mechanisms to send out a message: stream and
// pipeline.
// A stream is a receiver initialized long-polling connection, which
// is always open to transfer messages. Besides general stream, peer also has
// a optimized stream for sending msgApp since msgApp accounts for large part
// of all messages. Only raft leader uses the optimized stream to send msgApp
// to the remote follower node.
// A pipeline is a series of http clients that send http requests to the remote.
// It is only used when the stream has not been established.
type peer struct {
	// id of the remote raft peer node
	id types.ID

	msgAppWriter *streamWriter
	writer       *streamWriter
	pipeline     *pipeline

	sendc   chan raftpb.Message
	recvc   chan raftpb.Message
	newURLc chan string

	// for testing
	pausec  chan struct{}
	resumec chan struct{}

	stopc chan struct{}
	done  chan struct{}
}

func startPeer(tr http.RoundTripper, u string, local, remote, cid types.ID, r Raft, fs *stats.FollowerStats, errorc chan error) *peer {
	p := &peer{
		id:           remote,
		msgAppWriter: startStreamWriter(local, remote, streamTypeMsgApp, fs, r),
		writer:       startStreamWriter(local, remote, streamTypeMessage, fs, r),
		pipeline:     newPipeline(tr, u, local, remote, cid, fs, r, errorc),
		sendc:        make(chan raftpb.Message),
		recvc:        make(chan raftpb.Message, recvBufSize),
		newURLc:      make(chan string),
		pausec:       make(chan struct{}),
		resumec:      make(chan struct{}),
		stopc:        make(chan struct{}),
		done:         make(chan struct{}),
	}
	go func() {
		var paused bool
		msgAppReader := startStreamReader(tr, u, streamTypeMsgApp, local, remote, cid, p.recvc)
		reader := startStreamReader(tr, u, streamTypeMessage, local, remote, cid, p.recvc)
		for {
			select {
			case m := <-p.sendc:
				if paused {
					continue
				}
				writec, name := p.pick(m)
				select {
				case writec <- m:
				default:
					log.Printf("peer: dropping %s to %s since %s with %d-size buffer is blocked",
						m.Type, p.id, name, bufSizeMap[name])
				}
			case mm := <-p.recvc:
				if mm.Type == raftpb.MsgApp {
					msgAppReader.updateMsgAppTerm(mm.Term)
				}
				if err := r.Process(context.TODO(), mm); err != nil {
					log.Printf("peer: process raft message error: %v", err)
				}
			case u := <-p.newURLc:
				msgAppReader.update(u)
				reader.update(u)
				p.pipeline.update(u)
			case <-p.pausec:
				paused = true
			case <-p.resumec:
				paused = false
			case <-p.stopc:
				p.msgAppWriter.stop()
				p.writer.stop()
				p.pipeline.stop()
				msgAppReader.stop()
				reader.stop()
				close(p.done)
				return
			}
		}
	}()

	return p
}

func (p *peer) Send(m raftpb.Message) {
	select {
	case p.sendc <- m:
	case <-p.done:
		log.Panicf("peer: unexpected stopped")
	}
}

func (p *peer) Update(u string) {
	select {
	case p.newURLc <- u:
	case <-p.done:
		log.Panicf("peer: unexpected stopped")
	}
}

func (p *peer) attachOutgoingConn(conn *outgoingConn) {
	var ok bool
	switch conn.t {
	case streamTypeMsgApp:
		ok = p.msgAppWriter.attach(conn)
	case streamTypeMessage:
		ok = p.writer.attach(conn)
	default:
		log.Panicf("rafthttp: unhandled stream type %s", conn.t)
	}
	if !ok {
		conn.Close()
	}
}

// Pause pauses the peer. The peer will simply drops all incoming
// messages without retruning an error.
func (p *peer) Pause() {
	select {
	case p.pausec <- struct{}{}:
	case <-p.done:
	}
}

// Resume resumes a paused peer.
func (p *peer) Resume() {
	select {
	case p.resumec <- struct{}{}:
	case <-p.done:
	}
}

func (p *peer) Stop() {
	close(p.stopc)
	<-p.done
}

// pick picks a chan for sending the given message. The picked chan and the picked chan
// string name are returned.
func (p *peer) pick(m raftpb.Message) (writec chan raftpb.Message, picked string) {
	switch {
	// Considering MsgSnap may have a big size, e.g., 1G, and will block
	// stream for a long time, only use one of the N pipelines to send MsgSnap.
	case isMsgSnap(m):
		return p.pipeline.msgc, pipelineMsg
	case p.msgAppWriter.isWorking() && canUseMsgAppStream(m):
		return p.msgAppWriter.msgc, streamApp
	case p.writer.isWorking():
		return p.writer.msgc, streamMsg
	default:
		return p.pipeline.msgc, pipelineMsg
	}
	return
}

func isMsgSnap(m raftpb.Message) bool { return m.Type == raftpb.MsgSnap }
