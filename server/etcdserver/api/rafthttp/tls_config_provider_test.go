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

package rafthttp

import (
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"go.etcd.io/etcd/client/pkg/v3/transport"
)

type peerTLSConfigProvider struct {
	config *tls.Config
	calls  int
	failAt int
	err    error
}

func (p *peerTLSConfigProvider) ServerConfig() (*tls.Config, error) {
	return nil, errors.New("unexpected server configuration request")
}

func (p *peerTLSConfigProvider) ClientConfig() (*tls.Config, error) {
	p.calls++
	if p.calls == p.failAt {
		return nil, p.err
	}
	return p.config, nil
}

func TestTransportTLSConfigProvider(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer srv.Close()
	provider := &peerTLSConfigProvider{config: srv.Client().Transport.(*http.Transport).TLSClientConfig}
	tr := &Transport{Logger: zaptest.NewLogger(t), TLSInfo: transport.TLSInfo{ConfigProvider: provider}, DialTimeout: time.Second}
	require.NoError(t, tr.Start())
	defer tr.Stop()
	require.Equal(t, 2, provider.calls)
	for name, rt := range map[string]http.RoundTripper{"stream": tr.streamRt, "pipeline": tr.pipelineRt} {
		t.Run(name, func(t *testing.T) {
			client := &http.Client{Transport: rt, Timeout: 5 * time.Second}
			resp, err := client.Get(srv.URL)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusNoContent, resp.StatusCode)
			require.NotSame(t, provider.config, rt.(*http.Transport).TLSClientConfig)
		})
	}
}

func TestTransportTLSConfigProviderError(t *testing.T) {
	sentinel := errors.New("provider failed")
	for _, failAt := range []int{1, 2} {
		provider := &peerTLSConfigProvider{config: &tls.Config{}, failAt: failAt, err: sentinel}
		tr := &Transport{TLSInfo: transport.TLSInfo{ConfigProvider: provider}}
		require.ErrorIs(t, tr.Start(), sentinel)
		require.Equal(t, failAt, provider.calls)
	}
}
