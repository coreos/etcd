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

//go:build !cluster_proxy
// +build !cluster_proxy

// Keep the test in a separate package from other tests such that
// .setupLogging method does not race with other (previously running) servers (grpclog is global).

package embed_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.etcd.io/etcd/client/pkg/v3/testutil"
	"go.etcd.io/etcd/client/pkg/v3/transport"
	"go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"
	"go.etcd.io/etcd/tests/v3/integration"
)

var (
	testTLSInfo = transport.TLSInfo{
		KeyFile:        integration.MustAbsPath("../../fixtures/server.key.insecure"),
		CertFile:       integration.MustAbsPath("../../fixtures/server.crt"),
		TrustedCAFile:  integration.MustAbsPath("../../fixtures/ca.crt"),
		ClientCertAuth: true,
	}
)

func TestEmbedEtcd(t *testing.T) {
	testutil.SkipTestIfShortMode(t, "Cannot start embedded cluster in --short tests")

	tests := []struct {
		cfg embed.Config

		werr     string
		wpeers   int
		wclients int
	}{
		{werr: "multiple discovery"},
		{werr: "advertise-client-urls is required"},
		{werr: "should be at least"},
		{werr: "is too long"},
		{wpeers: 1, wclients: 1},
		{wpeers: 2, wclients: 1},
		{wpeers: 1, wclients: 2},
		{werr: "expected IP"},
		{werr: "expected IP"},
	}

	urls := newEmbedURLs(false, 10)

	// setup defaults
	for i := range tests {
		tests[i].cfg = *embed.NewConfig()
		tests[i].cfg.Logger = "zap"
		tests[i].cfg.LogFormat = "json"
		tests[i].cfg.LogOutputs = []string{"/dev/null"}
	}

	tests[0].cfg.Durl = "abc"
	setupEmbedCfg(&tests[1].cfg, []url.URL{urls[0]}, []url.URL{urls[1]})
	tests[1].cfg.ACUrls = nil
	tests[2].cfg.TickMs = tests[2].cfg.ElectionMs - 1
	tests[3].cfg.ElectionMs = 999999
	setupEmbedCfg(&tests[4].cfg, []url.URL{urls[2]}, []url.URL{urls[3]})
	setupEmbedCfg(&tests[5].cfg, []url.URL{urls[4]}, []url.URL{urls[5], urls[6]})
	setupEmbedCfg(&tests[6].cfg, []url.URL{urls[7], urls[8]}, []url.URL{urls[9]})

	dnsURL, _ := url.Parse("http://whatever.test:12345")
	tests[7].cfg.LCUrls = []url.URL{*dnsURL}
	tests[8].cfg.LPUrls = []url.URL{*dnsURL}

	dir := filepath.Join(t.TempDir(), fmt.Sprintf("embed-etcd"))

	for i, tt := range tests {
		tests[i].cfg.Dir = dir
		e, err := embed.StartEtcd(&tests[i].cfg)
		if e != nil {
			<-e.Server.ReadyNotify() // wait for e.Server to join the cluster
		}
		if tt.werr != "" {
			if err == nil || !strings.Contains(err.Error(), tt.werr) {
				t.Errorf("%d: expected error with %q, got %v", i, tt.werr, err)
			}
			if e != nil {
				e.Close()
			}
			continue
		}
		if err != nil {
			t.Errorf("%d: expected success, got error %v", i, err)
			continue
		}
		if len(e.Peers) != tt.wpeers {
			t.Errorf("%d: expected %d peers, got %d", i, tt.wpeers, len(e.Peers))
		}
		if len(e.Clients) != tt.wclients {
			t.Errorf("%d: expected %d clients, got %d", i, tt.wclients, len(e.Clients))
		}
		e.Close()
		select {
		case err := <-e.Err():
			if err != nil {
				t.Errorf("#%d: unexpected error on close (%v)", i, err)
			}
		}
	}
}

func TestEmbedEtcdGracefulStopSecure(t *testing.T)   { testEmbedEtcdGracefulStop(t, true) }
func TestEmbedEtcdGracefulStopInsecure(t *testing.T) { testEmbedEtcdGracefulStop(t, false) }

// testEmbedEtcdGracefulStop ensures embedded server stops
// cutting existing transports.
func testEmbedEtcdGracefulStop(t *testing.T, secure bool) {
	testutil.SkipTestIfShortMode(t, "Cannot start embedded cluster in --short tests")

	cfg := embed.NewConfig()
	if secure {
		cfg.ClientTLSInfo = testTLSInfo
		cfg.PeerTLSInfo = testTLSInfo
	}

	urls := newEmbedURLs(secure, 2)
	setupEmbedCfg(cfg, []url.URL{urls[0]}, []url.URL{urls[1]})

	cfg.Dir = filepath.Join(t.TempDir(), fmt.Sprintf("embed-etcd"))

	e, err := embed.StartEtcd(cfg)
	if err != nil {
		t.Fatal(err)
	}
	<-e.Server.ReadyNotify() // wait for e.Server to join the cluster

	clientCfg := clientv3.Config{
		Endpoints: []string{urls[0].String()},
	}
	if secure {
		clientCfg.TLS, err = testTLSInfo.ClientConfig()
		if err != nil {
			t.Fatal(err)
		}
	}
	cli, err := integration.NewClient(t, clientCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	// open watch connection
	cli.Watch(context.Background(), "foo")

	donec := make(chan struct{})
	go func() {
		e.Close()
		close(donec)
	}()
	select {
	case <-donec:
	case <-time.After(2*time.Second + e.Server.Cfg.ReqTimeout()):
		t.Fatalf("took too long to close server")
	}
	err = <-e.Err()
	if err != nil {
		t.Fatal(err)
	}
}

func newEmbedURLs(secure bool, n int) (urls []url.URL) {
	scheme := "unix"
	if secure {
		scheme = "unixs"
	}
	for i := 0; i < n; i++ {
		u, _ := url.Parse(fmt.Sprintf("%s://localhost:%d%06d", scheme, os.Getpid(), i))
		urls = append(urls, *u)
	}
	return urls
}

func setupEmbedCfg(cfg *embed.Config, curls []url.URL, purls []url.URL) {
	cfg.Logger = "zap"
	cfg.LogFormat = "json"
	cfg.LogOutputs = []string{"/dev/null"}

	cfg.ClusterState = "new"
	cfg.LCUrls, cfg.ACUrls = curls, curls
	cfg.LPUrls, cfg.APUrls = purls, purls
	cfg.InitialCluster = ""
	for i := range purls {
		cfg.InitialCluster += ",default=" + purls[i].String()
	}
	cfg.InitialCluster = cfg.InitialCluster[1:]
}
