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

package e2e

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.etcd.io/etcd/tests/v3/framework/e2e"
)

func TestCompactLatestRevision(t *testing.T) {
	testCases := []struct {
		name                       string
		hasWriteBetweenCompactions bool
		expectError                bool
	}{
		{
			name:                       "No write between compactions",
			hasWriteBetweenCompactions: false,
			expectError:                false,
		},
		{
			name:                       "Has write between compactions",
			hasWriteBetweenCompactions: true,
			expectError:                true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			e2e.BeforeTest(t)

			ctx := t.Context()
			clus, cerr := e2e.NewEtcdProcessCluster(ctx, t,
				e2e.WithClusterSize(1),
			)
			require.NoError(t, cerr)

			defer func() {
				assert.NoError(t, clus.Close())
			}()

			cli := newClient(t, clus.EndpointsGRPC(), e2e.ClientConfig{})

			for i := 0; i <= 5; i++ {
				_, werr := cli.Put(ctx, "foo", "bar")
				require.NoError(t, werr)
			}

			resp, err := cli.Get(ctx, "foo")
			require.NoError(t, err)

			latestRevision := resp.Header.Revision
			t.Logf("Latest Revision: %d", latestRevision)

			_, err = cli.Compact(ctx, latestRevision)
			require.NoError(t, err)

			if tc.hasWriteBetweenCompactions {
				_, werr := cli.Put(ctx, "foo", "bar")
				require.NoError(t, werr)
			}

			_, err = cli.Compact(ctx, latestRevision)
			if tc.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
