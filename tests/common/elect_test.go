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

package common

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.etcd.io/etcd/tests/v3/framework/config"
	intf "go.etcd.io/etcd/tests/v3/framework/interfaces"
	"go.etcd.io/etcd/tests/v3/framework/testutils"
)

type electResult struct {
	key    string
	resign func() error
	err    error
}

func TestElect(t *testing.T) {
	testRunner.BeforeTest(t)

	for _, tc := range clusterTestCases() {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(tc.config))
			defer clus.Close()
			cc := testutils.MustClient(clus.Client())

			name := "a"
			electCtx, electCancel := context.WithTimeout(ctx, 5*time.Second)
			defer electCancel()
			holderKey, holderResign, err := cc.Elect(electCtx, name, "p1")
			require.NoError(t, err)
			require.Contains(t, holderKey, name+"/")

			// campaigner that gives up before winning the election
			blockedCtx, blockedCancel := context.WithCancel(ctx)
			defer blockedCancel()
			blockedc := campaign(blockedCtx, cc, name, "p2")
			requireBlocked(t, blockedc)

			// overlap with a campaigner that will win the election
			waiterc := campaign(ctx, cc, name, "p2")
			requireBlocked(t, waiterc)

			blockedCancel()
			select {
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for the blocked campaign to stop")
			case r := <-blockedc:
				require.Error(t, r.err)
			}

			require.NoError(t, holderResign())

			select {
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for the waiter to win the election")
			case r := <-waiterc:
				require.NoError(t, r.err)
				require.Contains(t, r.key, name+"/")
				require.NotEqual(t, holderKey, r.key)
				require.NoError(t, r.resign())
			}
		})
	}
}

func campaign(ctx context.Context, cc intf.Client, name, proposal string) <-chan electResult {
	resc := make(chan electResult, 1)
	go func() {
		key, resign, err := cc.Elect(ctx, name, proposal)
		resc <- electResult{key: key, resign: resign, err: err}
	}()
	return resc
}

func requireBlocked(t *testing.T, resc <-chan electResult) {
	t.Helper()
	select {
	case <-time.After(100 * time.Millisecond):
	case r := <-resc:
		t.Fatalf("should block, got key %q, err %v", r.key, r.err)
	}
}
