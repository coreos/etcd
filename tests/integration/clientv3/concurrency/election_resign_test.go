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

package concurrency_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
	"go.etcd.io/etcd/tests/v3/framework/integration"
)

func TestElectionResignCanRetryAfterError(t *testing.T) {
	cli, err := integration.NewClient(t, clientv3.Config{Endpoints: exampleEndpoints()})
	require.NoError(t, err)
	defer cli.Close()

	session, err := concurrency.NewSession(cli)
	require.NoError(t, err)
	defer session.Close()

	election := concurrency.NewElection(session, "/resign-retry")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	require.NoError(t, election.Campaign(ctx, "candidate"))
	leaderKey := election.Key()

	canceledCtx, cancelResign := context.WithCancel(t.Context())
	cancelResign()
	require.ErrorIs(t, election.Resign(canceledCtx), context.Canceled)
	require.Equal(t, leaderKey, election.Key())
	resp, err := cli.Get(ctx, leaderKey)
	require.NoError(t, err)
	require.Len(t, resp.Kvs, 1)

	require.NoError(t, election.Resign(ctx))
	resp, err = cli.Get(ctx, leaderKey)
	require.NoError(t, err)
	require.Empty(t, resp.Kvs)
}
