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

package grpcproxy

import (
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
	"google.golang.org/grpc"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/proxy/grpcproxy"
	"go.etcd.io/etcd/tests/v3/framework/integration"
)

// TestWatchProxyCatchUpNotCoalescedPastGap checks that a watcher started far behind the
// current revision still receives every event when another watcher is already current.
func TestWatchProxyCatchUpNotCoalescedPastGap(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	client, err := integration.NewClient(t, clientv3.Config{Endpoints: []string{clus.Members[0].GRPCURL}, DialTimeout: 5 * time.Second})
	require.NoError(t, err)
	defer client.Close()

	wp, _ := grpcproxy.NewWatchProxy(t.Context(), zaptest.NewLogger(t), client)
	server := grpc.NewServer()
	pb.RegisterWatchServer(server, wp)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go server.Serve(l)
	defer server.Stop()

	pc, err := integration.NewClient(t, clientv3.Config{Endpoints: []string{l.Addr().String()}, DialTimeout: 5 * time.Second})
	require.NoError(t, err)
	defer pc.Close()

	// More than the server's unsynced batch size (1000), so catch-up takes several responses.
	const total = 2500
	for i := 0; i < total; i++ {
		_, err = client.Put(t.Context(), "k", strconv.Itoa(i))
		require.NoError(t, err)
	}

	current := pc.Watch(t.Context(), "k")
	time.Sleep(time.Second) // let the proxy start the current watcher's broadcast
	behind := pc.Watch(t.Context(), "k", clientv3.WithRev(1))

	got := 0
	timeout := time.After(10 * time.Second)
	for got < total {
		select {
		case wr := <-behind:
			for _, ev := range wr.Events {
				got++
				require.Equal(t, int64(got+1), ev.Kv.ModRevision) // the first put is revision 2
			}
		case <-timeout:
			t.Fatalf("got %d events, want %d", got, total)
		}
	}
	_ = current
}
