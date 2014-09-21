package etcdhttp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	etcdErr "github.com/coreos/etcd/error"
	"github.com/coreos/etcd/etcdserver"
	"github.com/coreos/etcd/etcdserver/etcdserverpb"
	"github.com/coreos/etcd/raft/raftpb"
	"github.com/coreos/etcd/store"
	"github.com/coreos/etcd/third_party/code.google.com/p/go.net/context"
)

func boolp(b bool) *bool { return &b }

func mustNewURL(t *testing.T, s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("error creating URL from %q: %v", s, err)
	}
	return u
}

// mustNewRequest takes a path, appends it to the standard keysPrefix, and constructs
// a GET *http.Request referencing the resulting URL
func mustNewRequest(t *testing.T, p string) *http.Request {
	return &http.Request{
		Method: "GET",
		URL:    mustNewURL(t, path.Join(keysPrefix, p)),
	}
}

// mustNewForm takes a set of Values and constructs a PUT *http.Request,
// with a URL constructed from appending the given path to the standard keysPrefix
func mustNewForm(t *testing.T, p string, vals url.Values) *http.Request {
	u := mustNewURL(t, path.Join(keysPrefix, p))
	req, err := http.NewRequest("PUT", u.String(), strings.NewReader(vals.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err != nil {
		t.Fatalf("error creating new request: %v", err)
	}
	return req
}

func TestBadParseRequest(t *testing.T) {
	tests := []struct {
		in    *http.Request
		wcode int
	}{
		{
			// parseForm failure
			&http.Request{
				Body:   nil,
				Method: "PUT",
			},
			etcdErr.EcodeInvalidForm,
		},
		{
			// bad key prefix
			&http.Request{
				URL: mustNewURL(t, "/badprefix/"),
			},
			etcdErr.EcodeInvalidForm,
		},
		// bad values for prevIndex, waitIndex, ttl
		{
			mustNewForm(t, "foo", url.Values{"prevIndex": []string{"garbage"}}),
			etcdErr.EcodeIndexNaN,
		},
		{
			mustNewForm(t, "foo", url.Values{"prevIndex": []string{"1.5"}}),
			etcdErr.EcodeIndexNaN,
		},
		{
			mustNewForm(t, "foo", url.Values{"prevIndex": []string{"-1"}}),
			etcdErr.EcodeIndexNaN,
		},
		{
			mustNewForm(t, "foo", url.Values{"waitIndex": []string{"garbage"}}),
			etcdErr.EcodeIndexNaN,
		},
		{
			mustNewForm(t, "foo", url.Values{"waitIndex": []string{"??"}}),
			etcdErr.EcodeIndexNaN,
		},
		{
			mustNewForm(t, "foo", url.Values{"ttl": []string{"-1"}}),
			etcdErr.EcodeTTLNaN,
		},
		// bad values for recursive, sorted, wait, prevExist
		{
			mustNewForm(t, "foo", url.Values{"recursive": []string{"hahaha"}}),
			etcdErr.EcodeInvalidField,
		},
		{
			mustNewForm(t, "foo", url.Values{"recursive": []string{"1234"}}),
			etcdErr.EcodeInvalidField,
		},
		{
			mustNewForm(t, "foo", url.Values{"recursive": []string{"?"}}),
			etcdErr.EcodeInvalidField,
		},
		{
			mustNewForm(t, "foo", url.Values{"sorted": []string{"?"}}),
			etcdErr.EcodeInvalidField,
		},
		{
			mustNewForm(t, "foo", url.Values{"sorted": []string{"x"}}),
			etcdErr.EcodeInvalidField,
		},
		{
			mustNewForm(t, "foo", url.Values{"wait": []string{"?!"}}),
			etcdErr.EcodeInvalidField,
		},
		{
			mustNewForm(t, "foo", url.Values{"wait": []string{"yes"}}),
			etcdErr.EcodeInvalidField,
		},
		{
			mustNewForm(t, "foo", url.Values{"prevExist": []string{"yes"}}),
			etcdErr.EcodeInvalidField,
		},
		{
			mustNewForm(t, "foo", url.Values{"prevExist": []string{"#2"}}),
			etcdErr.EcodeInvalidField,
		},
		// query values are considered
		{
			mustNewRequest(t, "foo?prevExist=wrong"),
			etcdErr.EcodeInvalidField,
		},
		{
			mustNewRequest(t, "foo?ttl=wrong"),
			etcdErr.EcodeTTLNaN,
		},
		// but body takes precedence if both are specified
		{
			mustNewForm(
				t,
				"foo?ttl=12",
				url.Values{"ttl": []string{"garbage"}},
			),
			etcdErr.EcodeTTLNaN,
		},
		{
			mustNewForm(
				t,
				"foo?prevExist=false",
				url.Values{"prevExist": []string{"yes"}},
			),
			etcdErr.EcodeInvalidField,
		},
	}
	for i, tt := range tests {
		got, err := parseRequest(tt.in, 1234)
		if err == nil {
			t.Errorf("#%d: unexpected nil error!", i)
			continue
		}
		ee, ok := err.(*etcdErr.Error)
		if !ok {
			t.Errorf("#%d: err is not etcd.Error!", i)
			continue
		}
		if ee.ErrorCode != tt.wcode {
			t.Errorf("#%d: code=%d, want %v", i, ee.ErrorCode, tt.wcode)
			t.Logf("cause: %#v", ee.Cause)
		}
		if !reflect.DeepEqual(got, etcdserverpb.Request{}) {
			t.Errorf("#%d: unexpected non-empty Request: %#v", i, got)
		}
	}
}

func TestGoodParseRequest(t *testing.T) {
	tests := []struct {
		in *http.Request
		w  etcdserverpb.Request
	}{
		{
			// good prefix, all other values default
			mustNewRequest(t, "foo"),
			etcdserverpb.Request{
				Id:     1234,
				Method: "GET",
				Path:   "/foo",
			},
		},
		{
			// value specified
			mustNewForm(
				t,
				"foo",
				url.Values{"value": []string{"some_value"}},
			),
			etcdserverpb.Request{
				Id:     1234,
				Method: "PUT",
				Val:    "some_value",
				Path:   "/foo",
			},
		},
		{
			// prevIndex specified
			mustNewForm(
				t,
				"foo",
				url.Values{"prevIndex": []string{"98765"}},
			),
			etcdserverpb.Request{
				Id:        1234,
				Method:    "PUT",
				PrevIndex: 98765,
				Path:      "/foo",
			},
		},
		{
			// recursive specified
			mustNewForm(
				t,
				"foo",
				url.Values{"recursive": []string{"true"}},
			),
			etcdserverpb.Request{
				Id:        1234,
				Method:    "PUT",
				Recursive: true,
				Path:      "/foo",
			},
		},
		{
			// sorted specified
			mustNewForm(
				t,
				"foo",
				url.Values{"sorted": []string{"true"}},
			),
			etcdserverpb.Request{
				Id:     1234,
				Method: "PUT",
				Sorted: true,
				Path:   "/foo",
			},
		},
		{
			// wait specified
			mustNewForm(
				t,
				"foo",
				url.Values{"wait": []string{"true"}},
			),
			etcdserverpb.Request{
				Id:     1234,
				Method: "PUT",
				Wait:   true,
				Path:   "/foo",
			},
		},
		{
			// prevExist should be non-null if specified
			mustNewForm(
				t,
				"foo",
				url.Values{"prevExist": []string{"true"}},
			),
			etcdserverpb.Request{
				Id:        1234,
				Method:    "PUT",
				PrevExist: boolp(true),
				Path:      "/foo",
			},
		},
		{
			// prevExist should be non-null if specified
			mustNewForm(
				t,
				"foo",
				url.Values{"prevExist": []string{"false"}},
			),
			etcdserverpb.Request{
				Id:        1234,
				Method:    "PUT",
				PrevExist: boolp(false),
				Path:      "/foo",
			},
		},
		// mix various fields
		{
			mustNewForm(
				t,
				"foo",
				url.Values{
					"value":     []string{"some value"},
					"prevExist": []string{"true"},
					"prevValue": []string{"previous value"},
				},
			),
			etcdserverpb.Request{
				Id:        1234,
				Method:    "PUT",
				PrevExist: boolp(true),
				PrevValue: "previous value",
				Val:       "some value",
				Path:      "/foo",
			},
		},
		// query parameters should be used if given
		{
			mustNewForm(
				t,
				"foo?prevValue=woof",
				url.Values{},
			),
			etcdserverpb.Request{
				Id:        1234,
				Method:    "PUT",
				PrevValue: "woof",
				Path:      "/foo",
			},
		},
		// but form values should take precedence over query parameters
		{
			mustNewForm(
				t,
				"foo?prevValue=woof",
				url.Values{
					"prevValue": []string{"miaow"},
				},
			),
			etcdserverpb.Request{
				Id:        1234,
				Method:    "PUT",
				PrevValue: "miaow",
				Path:      "/foo",
			},
		},
	}

	for i, tt := range tests {
		got, err := parseRequest(tt.in, 1234)
		if err != nil {
			t.Errorf("#%d: err = %v, want %v", i, err, nil)
		}
		if !reflect.DeepEqual(got, tt.w) {
			t.Errorf("#%d: request=%#v, want %#v", i, got, tt.w)
		}
	}
}

// eventingWatcher immediately returns a simple event of the given action on its channel
type eventingWatcher struct {
	action string
}

func (w *eventingWatcher) EventChan() chan *store.Event {
	ch := make(chan *store.Event)
	go func() {
		ch <- &store.Event{
			Action: w.action,
			Node:   &store.NodeExtern{},
		}
	}()
	return ch
}

func (w *eventingWatcher) Remove() {}

func TestWriteError(t *testing.T) {
	// nil error should not panic
	rw := httptest.NewRecorder()
	writeError(rw, nil)
	h := rw.Header()
	if len(h) > 0 {
		t.Fatalf("unexpected non-empty headers: %#v", h)
	}
	b := rw.Body.String()
	if len(b) > 0 {
		t.Fatalf("unexpected non-empty body: %q", b)
	}

	tests := []struct {
		err   error
		wcode int
		wi    string
	}{
		{
			etcdErr.NewError(etcdErr.EcodeKeyNotFound, "/foo/bar", 123),
			http.StatusNotFound,
			"123",
		},
		{
			etcdErr.NewError(etcdErr.EcodeTestFailed, "/foo/bar", 456),
			http.StatusPreconditionFailed,
			"456",
		},
		{
			err:   errors.New("something went wrong"),
			wcode: http.StatusInternalServerError,
		},
	}

	for i, tt := range tests {
		rw := httptest.NewRecorder()
		writeError(rw, tt.err)
		if code := rw.Code; code != tt.wcode {
			t.Errorf("#%d: code=%d, want %d", i, code, tt.wcode)
		}
		if idx := rw.Header().Get("X-Etcd-Index"); idx != tt.wi {
			t.Errorf("#%d: X-Etcd-Index=%q, want %q", i, idx, tt.wi)
		}
	}
}

func TestWriteEvent(t *testing.T) {
	// nil event should not panic
	rw := httptest.NewRecorder()
	writeEvent(rw, nil)
	h := rw.Header()
	if len(h) > 0 {
		t.Fatalf("unexpected non-empty headers: %#v", h)
	}
	b := rw.Body.String()
	if len(b) > 0 {
		t.Fatalf("unexpected non-empty body: %q", b)
	}

	tests := []struct {
		ev  *store.Event
		idx string
		// TODO(jonboulle): check body as well as just status code
		code int
		err  error
	}{
		// standard case, standard 200 response
		{
			&store.Event{
				Action:   store.Get,
				Node:     &store.NodeExtern{},
				PrevNode: &store.NodeExtern{},
			},
			"0",
			http.StatusOK,
			nil,
		},
		// check new nodes return StatusCreated
		{
			&store.Event{
				Action:   store.Create,
				Node:     &store.NodeExtern{},
				PrevNode: &store.NodeExtern{},
			},
			"0",
			http.StatusCreated,
			nil,
		},
	}

	for i, tt := range tests {
		rw := httptest.NewRecorder()
		writeEvent(rw, tt.ev)
		if gct := rw.Header().Get("Content-Type"); gct != "application/json" {
			t.Errorf("case %d: bad Content-Type: got %q, want application/json", i, gct)
		}
		if gei := rw.Header().Get("X-Etcd-Index"); gei != tt.idx {
			t.Errorf("case %d: bad X-Etcd-Index header: got %s, want %s", i, gei, tt.idx)
		}
		if rw.Code != tt.code {
			t.Errorf("case %d: bad response code: got %d, want %v", i, rw.Code, tt.code)
		}

	}
}

type dummyWatcher struct {
	echan chan *store.Event
}

func (w *dummyWatcher) EventChan() chan *store.Event {
	return w.echan
}
func (w *dummyWatcher) Remove() {}

type dummyResponseWriter struct {
	cnchan chan bool
	http.ResponseWriter
}

func (rw *dummyResponseWriter) CloseNotify() <-chan bool {
	return rw.cnchan
}

func TestWaitForEventChan(t *testing.T) {
	ctx := context.Background()
	ec := make(chan *store.Event)
	dw := &dummyWatcher{
		echan: ec,
	}
	w := httptest.NewRecorder()
	var wg sync.WaitGroup
	var ev *store.Event
	var err error
	wg.Add(1)
	go func() {
		ev, err = waitForEvent(ctx, w, dw)
		wg.Done()
	}()
	ec <- &store.Event{
		Action: store.Get,
		Node: &store.NodeExtern{
			Key:           "/foo/bar",
			ModifiedIndex: 12345,
		},
	}
	wg.Wait()
	want := &store.Event{
		Action: store.Get,
		Node: &store.NodeExtern{
			Key:           "/foo/bar",
			ModifiedIndex: 12345,
		},
	}
	if !reflect.DeepEqual(ev, want) {
		t.Fatalf("bad event: got %#v, want %#v", ev, want)
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWaitForEventCloseNotify(t *testing.T) {
	ctx := context.Background()
	dw := &dummyWatcher{}
	cnchan := make(chan bool)
	w := &dummyResponseWriter{
		cnchan: cnchan,
	}
	var wg sync.WaitGroup
	var ev *store.Event
	var err error
	wg.Add(1)
	go func() {
		ev, err = waitForEvent(ctx, w, dw)
		wg.Done()
	}()
	close(cnchan)
	wg.Wait()
	if ev != nil {
		t.Fatalf("non-nil Event returned with CloseNotifier: %v", ev)
	}
	if err == nil {
		t.Fatalf("nil err returned with CloseNotifier!")
	}
}

func TestWaitForEventCancelledContext(t *testing.T) {
	cctx, cancel := context.WithCancel(context.Background())
	dw := &dummyWatcher{}
	w := httptest.NewRecorder()
	var wg sync.WaitGroup
	var ev *store.Event
	var err error
	wg.Add(1)
	go func() {
		ev, err = waitForEvent(cctx, w, dw)
		wg.Done()
	}()
	cancel()
	wg.Wait()
	if ev != nil {
		t.Fatalf("non-nil Event returned with cancelled context: %v", ev)
	}
	if err == nil {
		t.Fatalf("nil err returned with cancelled context!")
	}
}

func TestV2MachinesEndpoint(t *testing.T) {
	tests := []struct {
		method string
		wcode  int
	}{
		{"GET", http.StatusOK},
		{"HEAD", http.StatusOK},
		{"POST", http.StatusMethodNotAllowed},
	}

	m := NewClientHandler(nil, &fakePeerGetter{}, time.Hour)
	s := httptest.NewServer(m)
	defer s.Close()

	for _, tt := range tests {
		req, err := http.NewRequest(tt.method, s.URL+machinesPrefix, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		if resp.StatusCode != tt.wcode {
			t.Errorf("StatusCode = %d, expected %d", resp.StatusCode, tt.wcode)
		}
	}
}

func TestServeMachines(t *testing.T) {
	peerGetter := &fakePeerGetter{
		peers: []etcdserver.PeerInfo{
			{ID: 0xBEEF0, PeerURLs: []string{"http://localhost:8080"}},
			{ID: 0xBEEF1, PeerURLs: []string{"http://localhost:8081"}},
			{ID: 0xBEEF2, PeerURLs: []string{"http://localhost:8082"}},
		},
	}

	writer := httptest.NewRecorder()
	req, err := http.NewRequest("GET", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	h := &serverHandler{peerGetter: peerGetter}
	h.serveMachines(writer, req)
	w := "http://localhost:8080, http://localhost:8081, http://localhost:8082"
	if g := writer.Body.String(); g != w {
		t.Errorf("body = %s, want %s", g, w)
	}
	if writer.Code != http.StatusOK {
		t.Errorf("header = %d, want %d", writer.Code, http.StatusOK)
	}
}

func TestPeerGetterGetEndpoints(t *testing.T) {
	tests := []struct {
		peerGetter etcdserver.PeerGetter
		endpoints  []string
	}{
		// single peer with a single address
		{
			peerGetter: &fakePeerGetter{
				peers: []etcdserver.PeerInfo{
					{ID: 1, PeerURLs: []string{"http://192.0.2.1"}},
				},
			},
			endpoints: []string{"http://192.0.2.1"},
		},

		// single peer with a single address with a port
		{
			peerGetter: &fakePeerGetter{
				peers: []etcdserver.PeerInfo{
					{ID: 1, PeerURLs: []string{"http://192.0.2.1:8001"}},
				},
			},
			endpoints: []string{"http://192.0.2.1:8001"},
		},

		// several peers explicitly unsorted
		{
			peerGetter: &fakePeerGetter{
				peers: []etcdserver.PeerInfo{
					{ID: 2, PeerURLs: []string{"http://192.0.2.3", "http://192.0.2.4"}},
					{ID: 3, PeerURLs: []string{"http://192.0.2.5", "http://192.0.2.6"}},
					{ID: 1, PeerURLs: []string{"http://192.0.2.1", "http://192.0.2.2"}},
				},
			},
			endpoints: []string{"http://192.0.2.1", "http://192.0.2.2", "http://192.0.2.3", "http://192.0.2.4", "http://192.0.2.5", "http://192.0.2.6"},
		},

		// no peers
		{
			peerGetter: &fakePeerGetter{peers: []etcdserver.PeerInfo{}},
			endpoints:  []string{},
		},

		// peer with no endpoints
		{
			peerGetter: &fakePeerGetter{
				peers: []etcdserver.PeerInfo{
					{ID: 3, PeerURLs: []string{}},
				},
			},
			endpoints: []string{},
		},
	}

	for i, tt := range tests {
		endpoints := getEndpoints(tt.peerGetter)
		if !reflect.DeepEqual(tt.endpoints, endpoints) {
			t.Errorf("#%d: peers.Endpoints() incorrect: want=%#v got=%#v", i, tt.endpoints, endpoints)
		}
	}
}

func TestAllowMethod(t *testing.T) {
	tests := []struct {
		m  string
		ms []string
		w  bool
		wh string
	}{
		// Accepted methods
		{
			m:  "GET",
			ms: []string{"GET", "POST", "PUT"},
			w:  true,
		},
		{
			m:  "POST",
			ms: []string{"POST"},
			w:  true,
		},
		// Made-up methods no good
		{
			m:  "FAKE",
			ms: []string{"GET", "POST", "PUT"},
			w:  false,
			wh: "GET,POST,PUT",
		},
		// Empty methods no good
		{
			m:  "",
			ms: []string{"GET", "POST"},
			w:  false,
			wh: "GET,POST",
		},
		// Empty accepted methods no good
		{
			m:  "GET",
			ms: []string{""},
			w:  false,
			wh: "",
		},
		// No methods accepted
		{
			m:  "GET",
			ms: []string{},
			w:  false,
			wh: "",
		},
	}

	for i, tt := range tests {
		rw := httptest.NewRecorder()
		g := allowMethod(rw, tt.m, tt.ms...)
		if g != tt.w {
			t.Errorf("#%d: got allowMethod()=%t, want %t", i, g, tt.w)
		}
		if !tt.w {
			if rw.Code != http.StatusMethodNotAllowed {
				t.Errorf("#%d: code=%d, want %d", i, rw.Code, http.StatusMethodNotAllowed)
			}
			gh := rw.Header().Get("Allow")
			if gh != tt.wh {
				t.Errorf("#%d: Allow header=%q, want %q", i, gh, tt.wh)
			}
		}
	}
}

// errServer implements the etcd.Server interface for testing.
// It returns the given error from any Do/Process calls.
type errServer struct {
	err error
}

func (fs *errServer) Do(ctx context.Context, r etcdserverpb.Request) (etcdserver.Response, error) {
	return etcdserver.Response{}, fs.err
}
func (fs *errServer) Process(ctx context.Context, m raftpb.Message) error {
	return fs.err
}
func (fs *errServer) Start() {}
func (fs *errServer) Stop()  {}

// errReader implements io.Reader to facilitate a broken request.
type errReader struct{}

func (er *errReader) Read(_ []byte) (int, error) { return 0, errors.New("some error") }

func mustMarshalMsg(t *testing.T, m raftpb.Message) []byte {
	json, err := m.Marshal()
	if err != nil {
		t.Fatalf("error marshalling raft Message: %#v", err)
	}
	return json
}

func TestServeRaft(t *testing.T) {
	testCases := []struct {
		method    string
		body      io.Reader
		serverErr error

		wcode int
	}{
		{
			// bad method
			"GET",
			bytes.NewReader(
				mustMarshalMsg(
					t,
					raftpb.Message{},
				),
			),
			nil,
			http.StatusMethodNotAllowed,
		},
		{
			// bad method
			"PUT",
			bytes.NewReader(
				mustMarshalMsg(
					t,
					raftpb.Message{},
				),
			),
			nil,
			http.StatusMethodNotAllowed,
		},
		{
			// bad method
			"DELETE",
			bytes.NewReader(
				mustMarshalMsg(
					t,
					raftpb.Message{},
				),
			),
			nil,
			http.StatusMethodNotAllowed,
		},
		{
			// bad request body
			"POST",
			&errReader{},
			nil,
			http.StatusBadRequest,
		},
		{
			// bad request protobuf
			"POST",
			strings.NewReader("malformed garbage"),
			nil,
			http.StatusBadRequest,
		},
		{
			// good request, etcdserver.Server error
			"POST",
			bytes.NewReader(
				mustMarshalMsg(
					t,
					raftpb.Message{},
				),
			),
			errors.New("some error"),
			http.StatusInternalServerError,
		},
		{
			// good request
			"POST",
			bytes.NewReader(
				mustMarshalMsg(
					t,
					raftpb.Message{},
				),
			),
			nil,
			http.StatusNoContent,
		},
	}
	for i, tt := range testCases {
		req, err := http.NewRequest(tt.method, "foo", tt.body)
		if err != nil {
			t.Fatalf("#%d: could not create request: %#v", i, err)
		}
		h := &serverHandler{
			timeout:    time.Hour,
			server:     &errServer{tt.serverErr},
			peerGetter: nil,
		}
		rw := httptest.NewRecorder()
		h.serveRaft(rw, req)
		if rw.Code != tt.wcode {
			t.Errorf("#%d: got code=%d, want %d", i, rw.Code, tt.wcode)
		}
	}
}

// resServer implements the etcd.Server interface for testing.
// It returns the given responsefrom any Do calls, and nil error
type resServer struct {
	res etcdserver.Response
}

func (rs *resServer) Do(_ context.Context, _ etcdserverpb.Request) (etcdserver.Response, error) {
	return rs.res, nil
}
func (rs *resServer) Process(_ context.Context, _ raftpb.Message) error { return nil }
func (rs *resServer) Start()                                            {}
func (rs *resServer) Stop()                                             {}

func mustMarshalEvent(t *testing.T, ev *store.Event) string {
	b := new(bytes.Buffer)
	if err := json.NewEncoder(b).Encode(ev); err != nil {
		t.Fatalf("error marshalling event %#v: %v", ev, err)
	}
	return b.String()
}

func TestBadServeKeys(t *testing.T) {
	testBadCases := []struct {
		req    *http.Request
		server etcdserver.Server

		wcode int
	}{
		{
			// bad method
			&http.Request{
				Method: "CONNECT",
			},
			&resServer{},

			http.StatusMethodNotAllowed,
		},
		{
			// bad method
			&http.Request{
				Method: "TRACE",
			},
			&resServer{},

			http.StatusMethodNotAllowed,
		},
		{
			// parseRequest error
			&http.Request{
				Body:   nil,
				Method: "PUT",
			},
			&resServer{},

			http.StatusBadRequest,
		},
		{
			// etcdserver.Server error
			mustNewRequest(t, "foo"),
			&errServer{
				errors.New("blah"),
			},

			http.StatusInternalServerError,
		},
		{
			// timeout waiting for event (watcher never returns)
			mustNewRequest(t, "foo"),
			&resServer{
				etcdserver.Response{
					Watcher: &dummyWatcher{},
				},
			},

			http.StatusGatewayTimeout,
		},
		{
			// non-event/watcher response from etcdserver.Server
			mustNewRequest(t, "foo"),
			&resServer{
				etcdserver.Response{},
			},

			http.StatusInternalServerError,
		},
	}
	for i, tt := range testBadCases {
		h := &serverHandler{
			timeout: 0, // context times out immediately
			server:  tt.server,
		}
		rw := httptest.NewRecorder()
		h.serveKeys(rw, tt.req)
		if rw.Code != tt.wcode {
			t.Errorf("#%d: got code=%d, want %d", i, rw.Code, tt.wcode)
		}
	}
}

func TestServeKeysEvent(t *testing.T) {
	req := mustNewRequest(t, "foo")
	server := &resServer{
		etcdserver.Response{
			Event: &store.Event{
				Action: store.Get,
				Node:   &store.NodeExtern{},
			},
		},
	}
	h := &serverHandler{
		timeout: time.Hour,
		server:  server,
	}
	rw := httptest.NewRecorder()

	h.serveKeys(rw, req)

	wcode := http.StatusOK
	wbody := mustMarshalEvent(
		t,
		&store.Event{
			Action: store.Get,
			Node:   &store.NodeExtern{},
		},
	)

	if rw.Code != wcode {
		t.Errorf("got code=%d, want %d", rw.Code, wcode)
	}
	g := rw.Body.String()
	if g != wbody {
		t.Errorf("got body=%#v, want %#v", g, wbody)
	}
}

func TestServeKeysWatch(t *testing.T) {
	req := mustNewRequest(t, "/foo/bar")
	ec := make(chan *store.Event)
	dw := &dummyWatcher{
		echan: ec,
	}
	server := &resServer{
		etcdserver.Response{
			Watcher: dw,
		},
	}
	h := &serverHandler{
		timeout: time.Hour,
		server:  server,
	}
	go func() {
		ec <- &store.Event{
			Action: store.Get,
			Node:   &store.NodeExtern{},
		}
	}()
	rw := httptest.NewRecorder()

	h.serveKeys(rw, req)

	wcode := http.StatusOK
	wbody := mustMarshalEvent(
		t,
		&store.Event{
			Action: store.Get,
			Node:   &store.NodeExtern{},
		},
	)

	if rw.Code != wcode {
		t.Errorf("got code=%d, want %d", rw.Code, wcode)
	}
	g := rw.Body.String()
	if g != wbody {
		t.Errorf("got body=%#v, want %#v", g, wbody)
	}
}

type fakePeerGetter struct {
	peers []etcdserver.PeerInfo
}

func (p *fakePeerGetter) Get(id int64) etcdserver.PeerInfo {
	for _, info := range p.peers {
		if info.ID == id {
			return info
		}
	}
	return etcdserver.PeerInfo{}
}
func (p *fakePeerGetter) GetAll() []etcdserver.PeerInfo { return p.peers }
