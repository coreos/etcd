// Copyright 2024 The etcd Authors
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

// Reproduction test for https://github.com/etcd-io/etcd/issues/21673
// Run with: GODEBUG=fips140=only go test -v -run TestFIPS ./server/etcdserver/api/membership/

package membership

import (
	"testing"

	"go.etcd.io/etcd/client/pkg/v3/types"
)

// TestFIPSMemberIDComputation reproduces issue #21673:
// computeMemberID uses crypto/sha1 which panics under GODEBUG=fips140=only.
func TestFIPSMemberIDComputation(t *testing.T) {
	urls, err := types.NewURLs([]string{"http://127.0.0.1:2380"})
	if err != nil {
		t.Fatalf("failed to parse URLs: %v", err)
	}
	// Under GODEBUG=fips140=only this call panics:
	// "crypto/sha1: use of SHA-1 is not allowed in FIPS 140-only mode"
	id := computeMemberID(urls, "etcd-cluster-fips-repro", nil)
	t.Logf("memberID = %s (only reached when NOT in fips140=only mode)", id)
}

// TestFIPSClusterIDGeneration reproduces issue #21673:
// RaftCluster.genID() uses crypto/sha1 which panics under GODEBUG=fips140=only.
func TestFIPSClusterIDGeneration(t *testing.T) {
	membs := []*Member{
		newTestMember(1, []string{"http://127.0.0.1:2380"}, "node1", nil),
		newTestMember(2, []string{"http://127.0.0.2:2380"}, "node2", nil),
		newTestMember(3, []string{"http://127.0.0.3:2380"}, "node3", nil),
	}
	c := newTestCluster(t, membs)
	// Under GODEBUG=fips140=only this call panics:
	// "crypto/sha1: use of SHA-1 is not allowed in FIPS 140-only mode"
	c.genID()
	t.Logf("clusterID = %s (only reached when NOT in fips140=only mode)", c.ID())
}
