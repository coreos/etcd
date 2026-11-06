// Copyright 2017 The etcd Authors
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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.etcd.io/etcd/client/pkg/v3/types"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/pkg/v3/expect"
	"go.etcd.io/etcd/tests/v3/framework/e2e"
)

// TestCtlV3MoveLeaderEnvVars ensures `etcdctl move-leader` works when
// ETCDCTL_ENDPOINTS is set alongside the --endpoints flag, guarding against
// a regression of the conflicting-environment-variable failure fixed by
// 3fc16608f. The behavioral move-leader coverage lives in
// tests/common/move_leader_test.go.
func TestCtlV3MoveLeaderEnvVars(t *testing.T) {
	e2e.BeforeTest(t)

	epc, err := e2e.NewEtcdProcessCluster(t.Context(), t, e2e.WithConfig(e2e.NewConfigNoTLS()))
	require.NoError(t, err)
	defer func() {
		require.NoError(t, epc.Close())
	}()

	var leadIdx int
	var leaderID, transferee uint64
	for i, ep := range epc.EndpointsGRPC() {
		cli, err := clientv3.New(clientv3.Config{
			Endpoints:   []string{ep},
			DialTimeout: 3 * time.Second,
		})
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		resp, err := cli.Status(ctx, ep)
		cancel()
		require.NoError(t, err)
		cli.Close()

		if resp.Header.GetMemberId() == resp.Leader {
			leadIdx = i
			leaderID = resp.Leader
		} else {
			transferee = resp.Header.GetMemberId()
		}
	}

	cx := ctlCtx{
		t:           t,
		cfg:         *e2e.NewConfigNoTLS(),
		dialTimeout: 7 * time.Second,
		epc:         epc,
		envMap:      map[string]string{"ETCDCTL_ENDPOINTS": "something-else-is-set"},
	}
	cmdArgs := append(cx.prefixArgs([]string{epc.EndpointsGRPC()[leadIdx]}), "move-leader", types.ID(transferee).String())
	require.NoError(t, e2e.SpawnWithExpectWithEnv(cmdArgs, cx.envMap, expect.ExpectedResponse{
		Value: fmt.Sprintf("Leadership transferred from %s to %s", types.ID(leaderID), types.ID(transferee)),
	}))
}
