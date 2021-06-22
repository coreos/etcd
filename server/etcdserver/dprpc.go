// Copyright 2021 The etcd Authors
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

package etcdserver

import (
	"context"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"sync"
	"time"
)

// wrappedSteam is used to intercepts grpc watch streams
type wrappedStream struct {
	grpc.ServerStream
	id int64
	ch chan *KVRequestDumpEntry
	chMu sync.RWMutex
}

// RecvMsg captures messages going from client to server
// and send it to dump channel for further processing
func (w *wrappedStream) RecvMsg(m interface{}) error {
	w.chMu.RLock()
	if w.ch != nil {
		entry := KVRequestDumpEntry{
			Time:  time.Now().UnixNano(),
			Type:  kvDumpDataTypeStream,
			Unary: UnaryEntry{},
			Stream: StreamEntry{
				SteamID: w.id,
				Recv:    m,
				Send:    nil,
			},
		}
		w.ch <-&entry
	}
	w.chMu.RUnlock()
	return w.ServerStream.RecvMsg(m)
}

// SendMsg captures messages going from server to client
// and send it to dump channel for further processing
func (w *wrappedStream) SendMsg(m interface{}) error {
	w.chMu.RLock()
	if w.ch != nil {
		entry := KVRequestDumpEntry{
			Time:  time.Now().UnixNano(),
			Type:  kvDumpDataTypeStream,
			Unary: UnaryEntry{},
			Stream: StreamEntry{
				SteamID: w.id,
				Recv:    nil,
				Send:    m,
			},
		}
		w.ch <-&entry
	}
	w.chMu.RUnlock()
	return w.ServerStream.SendMsg(m)
}

func (w *wrappedStream) Finish() {
	w.chMu.Lock()
	defer w.chMu.Unlock()
	w.ch = nil
}


// newWrappedStream intercepts incoming watch stream
func newWrappedStream(s grpc.ServerStream) grpc.ServerStream {
	dumpInfo.entriesChMu.RLock()
	defer dumpInfo.entriesChMu.RUnlock()
	dumpInfo.nextStreamId++
	ret := &wrappedStream{
		ServerStream: s,
		id: dumpInfo.nextStreamId,
		ch: dumpInfo.entriesCh,
	}
	dumpInfo.streams = append(dumpInfo.streams, ret)
	return ret
}

// StreamServerDumpInterceptor installs a new interceptor that
// dumps stream requests & responses
func StreamServerDumpInterceptor(_ *EtcdServer) grpc.StreamServerInterceptor {
	return func(
		srv interface{},
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		dumpInfo.mu.RLock()
		defer dumpInfo.mu.RUnlock()
		if dumpInfo.inProgress {
			// only track new streams established when a dump is in progress
			switch srv.(type) {
			case pb.WatchServer:
				err := handler(srv, newWrappedStream(ss))
				return err
			}
		}

		err := handler(srv, ss)
		return err
	}
}

// UnaryServerDumpInterceptor installs a new interceptor for
// unary grpc requests & responses
func UnaryServerDumpInterceptor(_ *EtcdServer) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler) (interface{}, error) {
		// it is okay to check without a lock here since it is just for running fast.
		ch := dumpInfo.entriesCh
		if ch != nil {
			entry := KVRequestDumpEntry{
				Time: time.Now().UnixNano(),
				Type: kvDumpDataTypeUnary,
			}
			entry.Unary = UnaryEntry{
				Method:  info.FullMethod,
				Request: req,
			}

			requestMetadata, _ := metadata.FromIncomingContext(ctx)
			entry.Unary.Metadata = requestMetadata

			resp, err := handler(ctx, req)

			entry.Unary.End = time.Now().UnixNano()
			entry.Unary.Response = resp

			dumpInfo.entriesChMu.RLock()
			// make sure entries channel is valid
			if dumpInfo.entriesCh != nil {
				dumpInfo.entriesCh <- &entry
			}
			dumpInfo.entriesChMu.RUnlock()
			return resp, err
		}
		return handler(ctx, req)
	}
}
