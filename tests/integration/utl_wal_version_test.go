// Copyright 2021 The etcd Authors
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

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/coreos/go-semver/semver"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	"go.etcd.io/etcd/client/pkg/v3/testutil"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"
	"go.etcd.io/etcd/server/v3/etcdserver/version"
	"go.etcd.io/etcd/server/v3/wal"
	"go.etcd.io/etcd/server/v3/wal/walpb"
)

func TestEtcdVersionFromWAL(t *testing.T) {
	testutil.SkipTestIfShortMode(t,
		"Wal creation tests are depending on embedded etcServer so are integration-level tests.")
	cfg := NewEmbedConfig(t, "default")
	srv, err := embed.StartEtcd(cfg)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-srv.Server.ReadyNotify():
	case <-time.After(3 * time.Second):
		t.Fatalf("failed to start embed.Etcd for test")
	}

	ccfg := clientv3.Config{Endpoints: []string{cfg.ACUrls[0].String()}}
	cli, err := NewClient(t, ccfg)
	if err != nil {
		srv.Close()
		t.Fatal(err)
	}
	// Get auth status to increase etcd version of proto stored in wal
	ctx, cancel := context.WithTimeout(context.Background(), testutil.RequestTimeout)
	cli.AuthStatus(ctx)
	cancel()

	cli.Close()
	srv.Close()

	wal, err := wal.Open(zap.NewNop(), cfg.Dir+"/member/wal", walpb.Snapshot{})
	if err != nil {
		panic(err)
	}
	defer wal.Close()
	ver := version.EtcdVersionFromWAL(wal)
	assert.Equal(t, &semver.Version{Major: 3, Minor: 5}, ver)
}
