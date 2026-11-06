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

package e2e

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"go.etcd.io/etcd/pkg/v3/expect"
)

type CURLReq struct {
	Username string
	Password string

	IsTLS   bool
	Timeout int

	Endpoint string

	Value    string
	Expected expect.ExpectedResponse
	Header   string

	Ciphers     string
	Curves      string
	HTTPVersion string

	OutputFile string
}

func (r CURLReq) timeoutDuration() time.Duration {
	if r.Timeout != 0 {
		return time.Duration(r.Timeout) * time.Second
	}

	// assume a sane default to finish a curl request
	return 5 * time.Second
}

// CURLPrefixArgsCluster builds the beginning of a curl command for a given key
// addressed to a random URL in the given cluster.
func CURLPrefixArgsCluster(cfg *EtcdProcessClusterConfig, member EtcdProcess, method string, req CURLReq) []string {
	return CURLPrefixArgs(member.Config().ClientURL, cfg.Client, cfg.CN, method, req)
}

func CURLPrefixArgs(clientURL string, cfg ClientConfig, CN bool, method string, req CURLReq) []string {
	cmdArgs := []string{"curl"}
	if req.HTTPVersion != "" {
		cmdArgs = append(cmdArgs, "--http"+req.HTTPVersion)
	}
	if req.IsTLS {
		if cfg.ConnectionType != ClientTLSAndNonTLS {
			panic("should not use cURLPrefixArgsUseTLS when serving only TLS or non-TLS")
		}
		cmdArgs = append(cmdArgs, "--cacert", CaPath, "--cert", CertPath, "--key", PrivateKeyPath)
		clientURL = ToTLS(clientURL)
	} else if cfg.ConnectionType == ClientTLS {
		if CN {
			cmdArgs = append(cmdArgs, "--cacert", CaPath, "--cert", CertPath, "--key", PrivateKeyPath)
		} else {
			cmdArgs = append(cmdArgs, "--cacert", CaPath, "--cert", CertPath3, "--key", PrivateKeyPath3)
		}
	}
	ep := clientURL + req.Endpoint

	if req.Username != "" || req.Password != "" {
		cmdArgs = append(cmdArgs, "-L", "-u", fmt.Sprintf("%s:%s", req.Username, req.Password), ep)
	} else {
		cmdArgs = append(cmdArgs, "-L", ep)
	}
	if req.Timeout != 0 {
		cmdArgs = append(cmdArgs, "-m", fmt.Sprintf("%d", req.Timeout))
	}

	if req.Header != "" {
		cmdArgs = append(cmdArgs, "-H", req.Header)
	}

	if req.Ciphers != "" {
		cmdArgs = append(cmdArgs, "--ciphers", req.Ciphers)
	}

	if req.Curves != "" {
		cmdArgs = append(cmdArgs, "--curves", req.Curves)
	}

	if req.OutputFile != "" {
		cmdArgs = append(cmdArgs, "--output", req.OutputFile)
	}

	switch method {
	case "POST", "PUT":
		dt := req.Value
		if !strings.HasPrefix(dt, "{") { // for non-JSON value
			dt = "value=" + dt
		}
		cmdArgs = append(cmdArgs, "-X", method, "-d", dt)
	}
	return cmdArgs
}

func CURLPost(clus *EtcdProcessCluster, req CURLReq) error {
	ctx, cancel := context.WithTimeout(context.Background(), req.timeoutDuration())
	defer cancel()
	return SpawnWithExpectsContext(ctx, CURLPrefixArgsCluster(clus.Cfg, clus.Procs[rand.Intn(clus.Cfg.ClusterSize)], "POST", req), nil, req.Expected)
}

func CURLPut(clus *EtcdProcessCluster, req CURLReq) error {
	ctx, cancel := context.WithTimeout(context.Background(), req.timeoutDuration())
	defer cancel()
	return SpawnWithExpectsContext(ctx, CURLPrefixArgsCluster(clus.Cfg, clus.Procs[rand.Intn(clus.Cfg.ClusterSize)], "PUT", req), nil, req.Expected)
}

func CURLGet(clus *EtcdProcessCluster, req CURLReq) error {
	member := clus.Procs[rand.Intn(clus.Cfg.ClusterSize)]
	return CURLGetFromMember(clus, member, req)
}

func CURLGetFromMember(clus *EtcdProcessCluster, member EtcdProcess, req CURLReq) error {
	ctx, cancel := context.WithTimeout(context.Background(), req.timeoutDuration())
	defer cancel()

	return SpawnWithExpectsContext(ctx, CURLPrefixArgsCluster(clus.Cfg, member, "GET", req), nil, req.Expected)
}
