package rafthttp

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/coreos/etcd/etcdserver/stats"
	"github.com/coreos/etcd/pkg/testutil"
	"github.com/coreos/etcd/pkg/types"
	"github.com/coreos/etcd/raft/raftpb"
)

// TestStreamWriterAttachOutgoingConn tests that outgoingConn can be attached
// to streamWriter. After that, streamWriter can use it to send messages
// continuously, and closes it when stopped.
func TestStreamWriterAttachOutgoingConn(t *testing.T) {
	sw := startStreamWriter(&stats.FollowerStats{})
	// it is not in working status now
	if g := sw.isWorking(); g != false {
		t.Errorf("working = %v, want false", g)
	}

	wfc := &nopWriteFlushCloser{}
	sw.attach(&outgoingConn{t: streamTypeMessage, Writer: wfc, Flusher: wfc, Closer: wfc})
	// starts to working
	if g := sw.isWorking(); g != true {
		t.Errorf("working = %v, want true", g)
	}

	sw.msgc <- raftpb.Message{}
	testutil.ForceGosched()
	// still working
	if g := sw.isWorking(); g != true {
		t.Errorf("working = %v, want true", g)
	}
	if wfc.written != true {
		t.Errorf("failed to write to the underlying connection")
	}

	sw.stop()
	// no longer in working status now
	if g := sw.isWorking(); g != false {
		t.Errorf("working = %v, want false", g)
	}
	if wfc.closed != true {
		t.Errorf("failed to close the underlying connection")
	}
}

// TestStreamWriterAttachBadOutgoingConn tests that streamWriter with bad
// outgoingConn will close the outgoingConn and fall back to non-working status.
func TestStreamWriterAttachBadOutgoingConn(t *testing.T) {
	sw := startStreamWriter(&stats.FollowerStats{})
	defer sw.stop()
	wfc := &errWriteFlushCloser{err: errors.New("blah")}
	sw.attach(&outgoingConn{t: streamTypeMessage, Writer: wfc, Flusher: wfc, Closer: wfc})

	sw.msgc <- raftpb.Message{}
	testutil.ForceGosched()
	// no longer working
	if g := sw.isWorking(); g != false {
		t.Errorf("working = %v, want false", g)
	}
	if wfc.closed != true {
		t.Errorf("failed to close the underlying connection")
	}
}

func TestStreamReaderRoundtrip(t *testing.T) {
	tr := &roundTripperRecorder{}
	sr := &streamReader{
		tr:   tr,
		u:    "http://localhost:7001",
		t:    streamTypeMessage,
		from: types.ID(1),
		to:   types.ID(2),
		cid:  types.ID(1),
	}
	sr.roundtrip()

	req := tr.Request()
	if w := "http://localhost:7001/raft/stream/message/1"; req.URL.String() != w {
		t.Errorf("url = %s, want %s", req.URL.String(), w)
	}
	if w := "GET"; req.Method != w {
		t.Errorf("method = %s, want %s", req.Method, w)
	}
	if g := req.Header.Get("X-Etcd-Cluster-ID"); g != "1" {
		t.Errorf("header X-Etcd-Cluster-ID = %s, want 1", g)
	}
	if g := req.Header.Get("X-Raft-To"); g != "2" {
		t.Errorf("header X-Raft-To = %s, want 2", g)
	}
}

// TestStream tests that streamReader and streamWriter can build stream to
// send messages between each other.
func TestStream(t *testing.T) {
	tests := []struct {
		t    streamType
		term uint64
		m    raftpb.Message
	}{
		{
			streamTypeMessage,
			0,
			raftpb.Message{Type: raftpb.MsgProp, To: 2},
		},
		{
			streamTypeMsgApp,
			1,
			raftpb.Message{
				Type:    raftpb.MsgApp,
				From:    2,
				To:      1,
				Term:    1,
				LogTerm: 1,
				Index:   3,
				Entries: []raftpb.Entry{{Term: 1, Index: 4}},
			},
		},
	}
	for i, tt := range tests {
		h := &fakeStreamHandler{t: tt.t}
		srv := httptest.NewServer(h)
		defer srv.Close()
		sw := startStreamWriter(&stats.FollowerStats{})
		defer sw.stop()
		h.sw = sw
		recvc := make(chan raftpb.Message)
		sr := startStreamReader(&http.Transport{}, srv.URL, tt.t, types.ID(1), types.ID(2), types.ID(1), recvc)
		defer sr.stop()
		if tt.t == streamTypeMsgApp {
			sr.updateMsgAppTerm(tt.term)
		}

		sw.msgc <- tt.m
		m := <-recvc
		if !reflect.DeepEqual(m, tt.m) {
			t.Errorf("#%d: message = %+v, want %+v", i, m, tt.m)
		}
	}
}

type nopWriteFlushCloser struct {
	written bool
	closed  bool
}

func (wfc *nopWriteFlushCloser) Write(p []byte) (n int, err error) {
	wfc.written = true
	return len(p), nil
}
func (wfc *nopWriteFlushCloser) Flush() {}
func (wfc *nopWriteFlushCloser) Close() error {
	wfc.closed = true
	return nil
}

type errWriteFlushCloser struct {
	err    error
	closed bool
}

func (wfc *errWriteFlushCloser) Write(p []byte) (n int, err error) { return 0, wfc.err }
func (wfc *errWriteFlushCloser) Flush()                            {}
func (wfc *errWriteFlushCloser) Close() error {
	wfc.closed = true
	return wfc.err
}

type fakeStreamHandler struct {
	t  streamType
	sw *streamWriter
}

func (h *fakeStreamHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.(http.Flusher).Flush()
	c := newCloseNotifier()
	h.sw.attach(&outgoingConn{
		t:       h.t,
		termStr: r.Header.Get("X-Raft-Term"),
		Writer:  w,
		Flusher: w.(http.Flusher),
		Closer:  c,
	})
	<-c.closeNotify()
}
