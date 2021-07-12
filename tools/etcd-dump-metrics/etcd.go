// Copyright 2018 The etcd Authors
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

package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/url"
	"os"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"

	"go.uber.org/zap"
)

func newEmbedURLs(n int) (urls []url.URL) {
	urls = make([]url.URL, n)
	for i := 0; i < n; i++ {
		u, _ := url.Parse(fmt.Sprintf("unix://localhost:%d%06d", os.Getpid(), i))
		urls[i] = *u
	}
	return urls
}

func setupEmbedCfg(cfg *embed.Config, curls, purls, ics []url.URL) {
	cfg.Logger = "zap"
	cfg.LogFormat = "json"
	cfg.LogOutputs = []string{"/dev/null"}
	// []string{"stderr"} to enable server logging

	var err error
	cfg.Dir, err = ioutil.TempDir(os.TempDir(), fmt.Sprintf("%016X", time.Now().UnixNano()))
	if err != nil {
		panic(err)
	}
	os.RemoveAll(cfg.Dir)

	cfg.ClusterState = "new"
	cfg.LCUrls, cfg.ACUrls = curls, curls
	cfg.LPUrls, cfg.APUrls = purls, purls

	cfg.InitialCluster = ""
	for i := range ics {
		cfg.InitialCluster += fmt.Sprintf(",%d=%s", i, ics[i].String())
	}
	cfg.InitialCluster = cfg.InitialCluster[1:]
}

func getCommand(exec, name, dir, cURL, pURL, cluster string) (args []string) {
	if !strings.Contains(exec, "etcd") {
		panic(fmt.Errorf("%q doesn't seem like etcd binary", exec))
	}
	return []string{
		exec,
		"--name", name,
		"--data-dir", dir,
		"--listen-client-urls", cURL,
		"--advertise-client-urls", cURL,
		"--listen-peer-urls", pURL,
		"--initial-advertise-peer-urls", pURL,
		"--initial-cluster", cluster,
		"--initial-cluster-token=tkn",
		"--initial-cluster-state=new",
	}
}

func write(ep string) {
	cli, err := clientv3.New(clientv3.Config{Endpoints: []string{strings.Replace(ep, "/metrics", "", 1)}})
	if err != nil {
		lg.Panic("failed to create client", zap.Error(err))
	}
	defer cli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = cli.Put(ctx, "____test", "")
	if err != nil {
		lg.Panic("failed to write test key", zap.Error(err))
	}
	_, err = cli.Get(ctx, "____test")
	if err != nil {
		lg.Panic("failed to read test key", zap.Error(err))
	}
	_, err = cli.Delete(ctx, "____test")
	if err != nil {
		lg.Panic("failed to delete test key", zap.Error(err))
	}
	cli.Watch(ctx, "____test", clientv3.WithCreatedNotify())
}
