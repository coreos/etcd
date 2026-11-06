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

package e2e

import (
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.etcd.io/etcd/api/v3/version"
	"go.etcd.io/etcd/tests/v3/framework/config"
	"go.etcd.io/etcd/tests/v3/framework/e2e"
)

// msgTypeAppEntries is the leading byte of an AppEntries frame on the
// msgappv2 stream (see server/etcdserver/api/rafthttp/msgappv2_codec.go).
const msgTypeAppEntries = 1

// TestRaftMsgAppV2OversizeFrame verifies that a peer feeding the msgappv2 stream
// a crafted frame cannot crash the receiving member. A learner is added whose
// peer URL points at a server that, when the member dials its msgapp stream,
// replies with an AppEntries frame claiming the maximum possible entry count.
// Before the entry count and length fields were bounded, that count narrowed to
// a negative slice length and panicked makeslice in the stream reader goroutine,
// taking the member process down. With the bound the member resets the stream
// and keeps serving, so the Put below succeeds.
func TestRaftMsgAppV2OversizeFrame(t *testing.T) {
	e2e.BeforeTest(t)

	frame := binary.BigEndian.AppendUint64([]byte{msgTypeAppEntries}, ^uint64(0))

	mux := http.NewServeMux()
	mux.HandleFunc("/raft/stream/msgapp/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Server-Version", version.Version)
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		w.Write(frame)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"etcdserver":"` + version.Version + `","etcdcluster":"` + version.MinClusterVersion + `"}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Server-Version", version.Version)
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	})
	peer := httptest.NewServer(mux)
	defer peer.Close()

	ctx := context.Background()
	clus, err := e2e.NewEtcdProcessCluster(ctx, t, e2e.WithClusterSize(1))
	require.NoError(t, err)
	defer clus.Close()

	ctl := clus.Etcdctl()
	_, err = ctl.MemberAddAsLearner(ctx, "malicious", []string{peer.URL})
	require.NoError(t, err)

	// Give the member time to dial the learner's msgapp stream and decode the
	// crafted frame. Before the fix this panicked in the reader goroutine and
	// crashed the member within a second or so of the add.
	time.Sleep(3 * time.Second)

	require.True(t, clus.Procs[0].IsRunning(), "member crashed after receiving oversized msgappv2 frame")
	_, err = ctl.Put(ctx, "k", "v", config.PutOptions{})
	require.NoError(t, err)
}
