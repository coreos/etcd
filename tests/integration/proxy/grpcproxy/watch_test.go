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
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/proxy/grpcproxy"
	"go.etcd.io/etcd/server/v3/storage/mvcc"
	"go.etcd.io/etcd/tests/v3/framework/integration"
)

// TestWatchProxyClientWatchID verifies that the watch proxy honors a
// client-supplied watch ID instead of always auto-assigning one, and that it
// rejects a duplicate watch ID, mirroring the behavior of the etcd server.
// See https://github.com/etcd-io/etcd/issues/12819.
func TestWatchProxyClientWatchID(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	wpts := newWatchProxyServer([]string{clus.Members[0].GRPCURL}, t)
	defer wpts.close()

	conn, err := grpc.NewClient(wpts.l.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	ws, err := pb.NewWatchClient(conn).Watch(ctx)
	require.NoError(t, err)

	const explicitID int64 = 7

	// An explicit watch ID must be preserved in the created response.
	err = ws.Send(&pb.WatchRequest{RequestUnion: &pb.WatchRequest_CreateRequest{
		CreateRequest: &pb.WatchCreateRequest{Key: []byte("foo"), WatchId: explicitID},
	}})
	require.NoError(t, err)

	resp, err := ws.Recv()
	require.NoError(t, err)
	require.Truef(t, resp.Created, "expected created response, got %+v", resp)
	require.False(t, resp.Canceled)
	require.Equalf(t, explicitID, resp.WatchId, "watch proxy did not honor client-supplied watch id")

	// Reusing the same explicit watch ID must be rejected as a duplicate.
	err = ws.Send(&pb.WatchRequest{RequestUnion: &pb.WatchRequest_CreateRequest{
		CreateRequest: &pb.WatchCreateRequest{Key: []byte("bar"), WatchId: explicitID},
	}})
	require.NoError(t, err)

	resp, err = ws.Recv()
	require.NoError(t, err)
	require.Truef(t, resp.Created, "expected created response, got %+v", resp)
	require.Truef(t, resp.Canceled, "duplicate watch id should be canceled, got %+v", resp)
	require.Equal(t, explicitID, resp.WatchId)
	require.Equal(t, mvcc.ErrWatcherDuplicateID.Error(), resp.CancelReason)

	// AutoWatchID still auto-assigns an id distinct from the explicit one.
	err = ws.Send(&pb.WatchRequest{RequestUnion: &pb.WatchRequest_CreateRequest{
		CreateRequest: &pb.WatchCreateRequest{Key: []byte("baz"), WatchId: clientv3.AutoWatchID},
	}})
	require.NoError(t, err)

	resp, err = ws.Recv()
	require.NoError(t, err)
	require.Truef(t, resp.Created, "expected created response, got %+v", resp)
	require.False(t, resp.Canceled)
	require.NotEqual(t, explicitID, resp.WatchId)
}

type watchproxyTestServer struct {
	wp     pb.WatchServer
	c      *clientv3.Client
	server *grpc.Server
	l      net.Listener
}

func (wts *watchproxyTestServer) close() {
	wts.server.Stop()
	wts.l.Close()
	wts.c.Close()
}

func newWatchProxyServer(endpoints []string, t *testing.T) *watchproxyTestServer {
	cfg := clientv3.Config{
		Endpoints:   endpoints,
		DialTimeout: 5 * time.Second,
	}
	client, err := integration.NewClient(t, cfg)
	require.NoError(t, err)

	wp, _ := grpcproxy.NewWatchProxy(t.Context(), zaptest.NewLogger(t), client)

	wts := &watchproxyTestServer{
		wp: wp,
		c:  client,
	}

	wts.server = grpc.NewServer()
	pb.RegisterWatchServer(wts.server, wts.wp)

	wts.l, err = net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	go wts.server.Serve(wts.l)

	return wts
}
