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

package clientv3

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc/metadata"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"
)

func TestEvent(t *testing.T) {
	tests := []struct {
		ev       *Event
		isCreate bool
		isModify bool
	}{{
		ev: &Event{
			Type: EventTypePut,
			Kv: &mvccpb.KeyValue{
				CreateRevision: 3,
				ModRevision:    3,
			},
		},
		isCreate: true,
	}, {
		ev: &Event{
			Type: EventTypePut,
			Kv: &mvccpb.KeyValue{
				CreateRevision: 3,
				ModRevision:    4,
			},
		},
		isModify: true,
	}}
	for i, tt := range tests {
		if tt.isCreate && !tt.ev.IsCreate() {
			t.Errorf("#%d: event should be Create event", i)
		}
		if tt.isModify && !tt.ev.IsModify() {
			t.Errorf("#%d: event should be Modify event", i)
		}
	}
}

// TestStreamKeyFromCtx tests the streamKeyFromCtx function to ensure it correctly
// formats metadata as a map[string][]string when extracting metadata from the context.
//
// The fmt package in Go guarantees that maps are printed in a consistent order,
// sorted by the keys. This test verifies that the streamKeyFromCtx function
// produces the expected formatted string representation of metadata maps when called with
// various context scenarios.
func TestStreamKeyFromCtx(t *testing.T) {
	tests := []struct {
		name     string
		ctx      context.Context
		expected string
	}{
		{
			name: "multiple keys",
			ctx: metadata.NewOutgoingContext(t.Context(), metadata.MD{
				"key1": []string{"value1"},
				"key2": []string{"value2a", "value2b"},
			}),
			expected: "map[key1:[value1] key2:[value2a value2b]]",
		},
		{
			name:     "no keys",
			ctx:      metadata.NewOutgoingContext(t.Context(), metadata.MD{}),
			expected: "map[]",
		},
		{
			name: "only one key",
			ctx: metadata.NewOutgoingContext(t.Context(), metadata.MD{
				"key1": []string{"value1", "value1a"},
			}),
			expected: "map[key1:[value1 value1a]]",
		},
		{
			name:     "no metadata",
			ctx:      t.Context(),
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := streamKeyFromCtx(tt.ctx)
			if actual != tt.expected {
				t.Errorf("streamKeyFromCtx() = %v, expected %v", actual, tt.expected)
			}
		})
	}
}

func TestServeSubstreamLogsSlowConsumer(t *testing.T) {
	const (
		interval  = 5 * time.Second
		threshold = 100 * time.Millisecond
	)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var nowMu sync.Mutex
	now := time.Unix(0, 0)
	startedWaiting := make(chan struct{})
	waitStarted := false

	var logMu sync.Mutex
	logCalls := 0
	logged := make(chan struct{}, 1)

	ws := &watcherStream{
		initReq: watchRequest{ctx: ctx},
		outc:    make(chan WatchResponse),
		recvc:   make(chan *WatchResponse, 1),
		donec:   make(chan struct{}),
	}
	ws.bufLogger = newBlockLogger(interval, threshold, func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		if waitStarted {
			close(startedWaiting)
			waitStarted = false
		}
		return now
	}, func(eventCount int, timeWaiting time.Duration, window time.Duration) {
		logMu.Lock()
		defer logMu.Unlock()
		logCalls++
		select {
		case logged <- struct{}{}:
		default:
		}
	})

	nowMu.Lock()
	waitStarted = true
	nowMu.Unlock()

	w := &watchGRPCStream{
		ctx:      t.Context(),
		closingc: make(chan *watcherStream, 1),
		lg:       zap.NewNop(),
	}
	w.wg.Add(1)
	go w.serveSubstream(ws, make(chan struct{}))

	ws.recvc <- &WatchResponse{Header: &pb.ResponseHeader{Revision: 1}}

	select {
	case <-startedWaiting:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for serveSubstream to start buffering")
	}

	nowMu.Lock()
	now = now.Add(threshold + interval)
	nowMu.Unlock()

	select {
	case <-ws.outc:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for buffered response delivery")
	}

	select {
	case <-logged:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for backlog warning callback")
	}

	logMu.Lock()
	if logCalls != 1 {
		logMu.Unlock()
		t.Fatalf("expected one backlog warning, got %d", logCalls)
	}
	logMu.Unlock()

	cancel()
	select {
	case <-ws.donec:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for serveSubstream shutdown")
	}
	w.wg.Wait()
}


// TestBroadcastResponseSkipsRemovedSubstream ensures a canceled substream that
// has been removed from the map is not targeted by progress notify broadcasts.
// Regression for #21969 (panic: send on closed channel).
func TestBroadcastResponseSkipsRemovedSubstream(t *testing.T) {
	activeRecvc := make(chan *WatchResponse, 1)
	w := &watchGRPCStream{
		substreams: map[int64]*watcherStream{
			1: {recvc: activeRecvc, donec: make(chan struct{})},
		},
	}
	wr := &WatchResponse{Header: &pb.ResponseHeader{Revision: 1}}
	if !w.broadcastResponse(wr) {
		t.Fatal("broadcastResponse returned false")
	}
	select {
	case got := <-activeRecvc:
		if got != wr {
			t.Fatalf("unexpected response pointer")
		}
	default:
		t.Fatal("active substream did not receive response")
	}
}
