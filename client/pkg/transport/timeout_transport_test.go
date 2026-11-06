// Copyright 2015 The etcd Authors
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

package transport

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestNewTimeoutTransport tests that NewTimeoutTransport returns a transport
// that can dial out timeout connections.
func TestNewTimeoutTransport(t *testing.T) {
	tr, err := NewTimeoutTransport(TLSInfo{}, time.Hour, time.Hour, time.Hour)
	require.NoError(t, err)

	remoteAddr := func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(r.RemoteAddr))
	}
	srv := httptest.NewServer(http.HandlerFunc(remoteAddr))

	defer srv.Close()
	conn, err := tr.DialContext(context.Background(), "tcp", srv.Listener.Addr().String())
	require.NoError(t, err)
	defer conn.Close()

	tconn, ok := conn.(*timeoutConn)
	require.Truef(t, ok, "failed to dial out *timeoutConn")
	if tconn.readTimeout != time.Hour {
		t.Errorf("read timeout = %s, want %s", tconn.readTimeout, time.Hour)
	}
	if tconn.writeTimeout != time.Hour {
		t.Errorf("write timeout = %s, want %s", tconn.writeTimeout, time.Hour)
	}

	// ensure not reuse timeout connection
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	resp, err := tr.RoundTrip(req)
	require.NoError(t, err)
	addr0, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.NoError(t, err)

	resp, err = tr.RoundTrip(req)
	require.NoError(t, err)
	addr1, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.NoError(t, err)

	if bytes.Equal(addr0, addr1) {
		t.Errorf("addr0 = %s addr1= %s, want not equal", addr0, addr1)
	}
}

// TestNewTimeoutTransportReadTimeout tests that connections dialed by the
// transport time out when the peer stops sending.
func TestNewTimeoutTransportReadTimeout(t *testing.T) {
	srv := httptest.NewServer(stallingHandler())
	defer srv.Close()
	requireReadTimeout(t, srv.URL)
}

// TestNewTimeoutTransportUnixReadTimeout tests the same for peers reached
// through a unix socket, which go through a transport of their own.
func TestNewTimeoutTransportUnixReadTimeout(t *testing.T) {
	// The unix transport dials the URL host as a socket path relative to the
	// working directory, as the integration framework does.
	t.Chdir(t.TempDir())
	ln, err := net.Listen("unix", "localhost:1")
	require.NoError(t, err)
	srv := httptest.NewUnstartedServer(stallingHandler())
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	defer srv.Close()
	requireReadTimeout(t, "unix://localhost:1")
}

// stallingHandler sends the response headers and then nothing, until the
// client goes away.
func stallingHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
}

func requireReadTimeout(t *testing.T, url string) {
	t.Helper()
	tr, err := NewTimeoutTransport(TLSInfo{}, time.Second, 100*time.Millisecond, 100*time.Millisecond)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := tr.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	readErr := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(resp.Body)
		readErr <- err
	}()
	select {
	case err := <-readErr:
		var netErr net.Error
		require.ErrorAs(t, err, &netErr)
		require.Truef(t, netErr.Timeout(), "read error = %v, want a timeout", err)
	case <-time.After(5 * time.Second):
		t.Fatal("reading from a stalled peer did not time out")
	}
}

// TestNewTimeoutTransportLocalAddr tests that connections dialed by the
// transport use TLSInfo.LocalAddr as their source address.
func TestNewTimeoutTransportLocalAddr(t *testing.T) {
	const localAddr = "127.0.0.2"
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	d := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(localAddr)}}
	conn, err := d.Dial("tcp", probe.Addr().String())
	probe.Close()
	if err != nil {
		t.Skipf("%s is not assignable on this host (Linux assigns all of 127.0.0.0/8 to loopback): %v", localAddr, err)
	}
	conn.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(r.RemoteAddr))
	}))
	defer srv.Close()

	tr, err := NewTimeoutTransport(TLSInfo{LocalAddr: localAddr}, time.Second, time.Hour, time.Hour)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	resp, err := tr.RoundTrip(req)
	require.NoError(t, err)
	remote, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.NoError(t, err)

	host, _, err := net.SplitHostPort(string(remote))
	require.NoError(t, err)
	require.Equal(t, localAddr, host)
}
