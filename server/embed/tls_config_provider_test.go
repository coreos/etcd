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

package embed

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"go.etcd.io/etcd/client/pkg/v3/transport"
)

type embeddedTLSConfigProvider struct {
	server, client *tls.Config
	err            error
}

func (p embeddedTLSConfigProvider) ServerConfig() (*tls.Config, error) { return p.server, p.err }
func (p embeddedTLSConfigProvider) ClientConfig() (*tls.Config, error) { return p.client, p.err }

func TestConfigurePeerListenersTLSConfigProvider(t *testing.T) {
	fixture := httptest.NewTLSServer(http.NotFoundHandler())
	defer fixture.Close()
	cfg := NewConfig()
	cfg.logger = zaptest.NewLogger(t)
	cfg.ListenPeerUrls = []url.URL{{Scheme: "https", Host: "127.0.0.1:0"}}
	cfg.PeerTLSInfo.ConfigProvider = embeddedTLSConfigProvider{
		server: fixture.TLS,
		client: fixture.Client().Transport.(*http.Transport).TLSClientConfig,
	}
	peers, err := configurePeerListeners(cfg)
	require.NoError(t, err)
	require.Len(t, peers, 1)
	defer peers[0].close(context.Background())
	srv := &http.Server{Handler: http.NotFoundHandler(), ReadHeaderTimeout: time.Second}
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(peers[0].Listener) }()
	defer func() { _ = srv.Close(); <-done }()
	tr, err := transport.NewTransport(cfg.PeerTLSInfo, time.Second)
	require.NoError(t, err)
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	resp, err := client.Get("https://" + peers[0].Addr().String())
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestConfigurePeerListenersTLSConfigProviderError(t *testing.T) {
	sentinel := errors.New("provider failed")
	cfg := NewConfig()
	cfg.logger = zaptest.NewLogger(t)
	cfg.ListenPeerUrls = []url.URL{{Scheme: "https", Host: "127.0.0.1:0"}}
	cfg.PeerTLSInfo.ConfigProvider = embeddedTLSConfigProvider{err: sentinel}
	_, err := configurePeerListeners(cfg)
	require.ErrorIs(t, err, sentinel)
}
