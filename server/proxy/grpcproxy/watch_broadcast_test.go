// Copyright 2024 The etcd Authors
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

package grpcproxy

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// TestWatchBroadcastSuppressReestablishCreated verifies that a broadcast
// forwards only its first Created response to receivers. A second plain Created,
// which the underlying etcd watcher emits when it is re-established after a
// restart or transient disconnect, must be dropped: forwarding it would corrupt
// clientv3's positional Created<->substream matching and deliver this
// broadcast's events to a different watcher (cross-prefix delivery). Events that
// follow the dropped Created must still be delivered.
func TestWatchBroadcastSuppressReestablishCreated(t *testing.T) {
	lg := zaptest.NewLogger(t)
	wps := &watchProxyStream{
		watchCh: make(chan *pb.WatchResponse, 8),
		lg:      lg,
	}
	w := &watcher{id: 1, wps: wps}
	wb := &watchBroadcast{
		receivers: map[*watcher]struct{}{w: {}},
		lg:        lg,
	}

	// First response: the initial Created emitted when the broadcast is set up.
	wb.bcast(clientv3.WatchResponse{Header: &pb.ResponseHeader{Revision: 5}, Created: true})
	// A normal event flows through.
	wb.bcast(clientv3.WatchResponse{
		Header: &pb.ResponseHeader{Revision: 6},
		Events: []*clientv3.Event{{Kv: &mvccpb.KeyValue{Key: []byte("k"), ModRevision: 6}}},
	})
	// Second plain Created: the backend watcher was re-established. Must be dropped.
	wb.bcast(clientv3.WatchResponse{Header: &pb.ResponseHeader{Revision: 7}, Created: true})
	// A further event after re-establishment still flows through.
	wb.bcast(clientv3.WatchResponse{
		Header: &pb.ResponseHeader{Revision: 8},
		Events: []*clientv3.Event{{Kv: &mvccpb.KeyValue{Key: []byte("k"), ModRevision: 8}}},
	})

	var got []*pb.WatchResponse
	for {
		select {
		case resp := <-wps.watchCh:
			got = append(got, resp)
			continue
		default:
		}
		break
	}

	require.Len(t, got, 3, "expected one Created plus two events, re-establishment Created dropped")

	createdCount := 0
	eventCount := 0
	for _, resp := range got {
		if resp.Created {
			createdCount++
			require.Empty(t, resp.Events, "Created response should carry no events")
		} else {
			eventCount++
		}
	}
	require.Equal(t, 1, createdCount, "exactly one Created should reach the client")
	require.Equal(t, 2, eventCount, "both events should reach the client")
}
