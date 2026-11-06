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

//go:build !cluster_proxy

package e2e

import (
	"context"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.etcd.io/etcd/server/v3/etcdserver"
	"go.etcd.io/etcd/tests/v3/framework/e2e"
)

// TestIssue21178 reproduces https://github.com/etcd-io/etcd/issues/21178.
//
// Re-adding a learner with an empty data-dir should not panic during bootstrap.
func TestIssue21178(t *testing.T) {
	e2e.BeforeTest(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	epc, err := e2e.NewEtcdProcessCluster(ctx, t)
	require.NoError(t, err)
	defer epc.Close()

	// Pick a random member
	memberIdx := rand.Int() % len(epc.Procs)
	member := epc.Procs[memberIdx]
	memberName := member.Config().Name

	// Build etcdctl against other members
	var endpoints []string
	for i := 1; i < len(epc.Procs); i++ {
		endpoints = append(endpoints, epc.Procs[(memberIdx+i)%len(epc.Procs)].EndpointsGRPC()...)
	}
	cc, err := e2e.NewEtcdctl(epc.Cfg.Client, endpoints)
	require.NoError(t, err)

	memberID, found, err := getMemberIDByName(ctx, cc, memberName)
	require.NoError(t, err)
	require.True(t, found)

	// Wait for cluster to accept member changes
	time.Sleep(etcdserver.HealthInterval)

	t.Logf("Removing member %s", memberName)
	_, err = cc.MemberRemove(ctx, memberID)
	require.NoError(t, err)

	// Wait for process to exit
	for member.IsRunning() {
		err = member.Wait(ctx)
		if err != nil && !strings.Contains(err.Error(), "unexpected exit code") {
			t.Fatalf("member didn't exit as expected: %v", err)
		}
	}

	t.Logf("Removing member %s data-dir", memberName)
	require.NoError(t, os.RemoveAll(member.Config().DataDirPath))

	t.Logf("Re-adding member %s as learner", memberName)
	peerURL := member.Config().PeerURL.String()
	_, err = cc.MemberAddAsLearner(ctx, memberName, []string{peerURL})
	require.NoError(t, err)

	err = e2e.PatchArgs(member.Config().Args, "initial-cluster-state", "existing")
	require.NoError(t, err)

	// Small delay to avoid known race (see issue #16687)
	time.Sleep(100 * time.Millisecond)

	t.Logf("Starting member %s (should not panic)", memberName)
	err = member.Start(ctx)

	// The only assertion that matters:
	// startup must NOT panic or return error.
	require.NoError(t, err)
}
