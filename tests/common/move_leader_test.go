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

func TestMoveLeader(t *testing.T) {
	testRunner.BeforeTest(t)
	for _, tc := range clusterTestCases() {
		t.Run(tc.name, func(t *testing.T) {
			if tc.config.ClusterSize < 2 {
				t.Skip("Skipping test for single-member cluster")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()

			clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(tc.config))
			defer clus.Close()
			cc := testutils.MustClient(clus.Client())

			testutils.ExecuteUntil(ctx, t, func() {
				leaderID, transfereeID := leaderAndTransferee(ctx, t, cc)
				require.NotZero(t, leaderID)
				require.NotZero(t, transfereeID)

				require.NoError(t, cc.MoveLeader(ctx, transfereeID))

				// The transfer is complete when MoveLeader returns, but followers
				// may briefly report the old leader until they observe the new term.
				require.Eventually(t, func() bool {
					statuses, err := cc.Status(ctx)
					if err != nil {
						return false
					}
					for _, status := range statuses {
						if status.Leader != transfereeID {
							return false
						}
					}
					return true
				}, 15*time.Second, 100*time.Millisecond, "leadership was not transferred to the transferee")
			})
		})
	}
}

func TestMoveLeaderToNonexistentMember(t *testing.T) {
	testRunner.BeforeTest(t)
	for _, tc := range clusterTestCases() {
		t.Run(tc.name, func(t *testing.T) {
			if tc.config.ClusterSize < 2 {
				t.Skip("Skipping test for single-member cluster")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()

			clus := testRunner.NewCluster(ctx, t, config.WithClusterConfig(tc.config))
			defer clus.Close()
			cc := testutils.MustClient(clus.Client())

			testutils.ExecuteUntil(ctx, t, func() {
				statuses, err := cc.Status(ctx)
				require.NoError(t, err)
				memberIDs := make(map[uint64]struct{}, len(statuses))
				for _, status := range statuses {
					memberIDs[status.Header.GetMemberId()] = struct{}{}
				}
				transfereeID := uint64(1)
				for {
					if _, ok := memberIDs[transfereeID]; !ok {
						break
					}
					transfereeID++
				}

				require.Error(t, cc.MoveLeader(ctx, transfereeID))
			})
		})
	}
}

// leaderAndTransferee returns the leader's member ID and the ID of one
// follower from the cluster's current statuses.
func leaderAndTransferee(ctx context.Context, t *testing.T, cc intf.Client) (leaderID, transfereeID uint64) {
	statuses, err := cc.Status(ctx)
	require.NoError(t, err)
	for _, status := range statuses {
		if status.Header.GetMemberId() == status.Leader {
			leaderID = status.Leader
		} else {
			transfereeID = status.Header.GetMemberId()
		}
	}
	return leaderID, transfereeID
}
