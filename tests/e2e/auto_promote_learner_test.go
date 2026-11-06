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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.etcd.io/etcd/tests/v3/framework/e2e"
	"go.etcd.io/etcd/tests/v3/framework/testutils"
)

// TestAutoPromoteLearner verifies that a learner added to a cluster
// running with --feature-gates=AutoPromoteLearners=true is promoted to
// a voting member by the leader without an explicit
// `etcdctl member promote` invocation.
func TestAutoPromoteLearner(t *testing.T) {
	e2e.BeforeTest(t)

	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()

	epc, err := e2e.NewEtcdProcessCluster(ctx, t,
		e2e.WithClusterSize(3),
		e2e.WithServerFeatureGate("AutoPromoteLearners", true),
	)
	require.NoError(t, err)
	defer func() {
		require.NoErrorf(t, epc.Close(), "failed to close etcd cluster")
	}()

	// Add a new member as a learner and start its process. The cluster
	// may not be fully healthy immediately after start, so retry on the
	// transient "unhealthy cluster" error (matching the pattern used by
	// other e2e tests that add learners).
	var learnerID uint64
	testutils.ExecuteWithTimeout(t, 1*time.Minute, func() {
		for {
			var addErr error
			learnerID, addErr = epc.StartNewProc(ctx, nil, t, true /* addAsLearner */)
			if addErr == nil {
				return
			}
			if strings.Contains(addErr.Error(), "etcdserver: unhealthy cluster") {
				time.Sleep(1 * time.Second)
				continue
			}
			require.NoError(t, addErr)
		}
	})

	// Poll MemberList until the leader auto-promotes the learner. The
	// auto-promote loop ticks at monitorVersionInterval (~4s); a 45s
	// deadline gives ~10 iterations of headroom on slow runners.
	require.Eventuallyf(t, func() bool {
		resp, lerr := epc.Etcdctl().MemberList(ctx, false)
		if lerr != nil {
			return false
		}
		for _, m := range resp.Members {
			if m.ID == learnerID {
				return !m.IsLearner
			}
		}
		return false
	}, 45*time.Second, 500*time.Millisecond, "learner %x was not auto-promoted within deadline", learnerID)

	// The new member should also be healthy after promotion.
	newMemberProc := epc.Procs[len(epc.Procs)-1]
	assert.NoError(t, newMemberProc.Etcdctl().Health(ctx))
}
