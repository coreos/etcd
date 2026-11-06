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

package apply

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
	"google.golang.org/protobuf/encoding/protowire"

	"go.etcd.io/etcd/pkg/v3/wait"
	"go.etcd.io/etcd/server/v3/etcdserver/api/membership"
	"go.etcd.io/raft/v3/raftpb"
)

func legacyRequestBytes(id uint64, method, requestPath, value string) []byte {
	var data []byte
	data = protowire.AppendTag(data, 1, protowire.VarintType)
	data = protowire.AppendVarint(data, id)
	data = protowire.AppendTag(data, 2, protowire.BytesType)
	data = protowire.AppendString(data, method)
	data = protowire.AppendTag(data, 3, protowire.BytesType)
	data = protowire.AppendString(data, requestPath)
	data = protowire.AppendTag(data, 4, protowire.BytesType)
	data = protowire.AppendString(data, value)
	return data
}

// TestApplyDropsLegacyV2Requests verifies that metadata requests written by
// the removed v2 store are dropped instead of being applied.
func TestApplyDropsLegacyV2Requests(t *testing.T) {
	tests := []struct {
		name        string
		requestPath string
		value       string
	}{
		{
			name:        "member attributes",
			requestPath: "/0/members/1a2b/attributes",
			value:       `{"name":"member-1","clientURLs":["https://127.0.0.1:2379"]}`,
		},
		{
			name:        "cluster version",
			requestPath: "/0/version",
			value:       "3.6.15",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := &raftpb.Entry{Data: legacyRequestBytes(42, "PUT", tt.requestPath, tt.value)}
			ar, id := Apply(zaptest.NewLogger(t), entry, nil, wait.New(), membership.ApplyV2storeOnly)

			require.Nil(t, ar)
			require.Equal(t, uint64(42), id)
		})
	}
}

// TestApplyPanicsOnUnparseableEntry verifies that an entry which is neither a
// valid InternalRaftRequest nor a legacy v2 request still fails loudly.
func TestApplyPanicsOnUnparseableEntry(t *testing.T) {
	entry := &raftpb.Entry{Data: []byte{0x12, 0x02, 'P'}}
	require.Panics(t, func() {
		Apply(zaptest.NewLogger(t), entry, nil, wait.New(), membership.ApplyV2storeOnly)
	})
}

// TestApplyPanicsOnNonMetadataV2Request verifies that a v2-shaped entry that
// is not one of the expected metadata requests still fails loudly instead of
// being dropped.
func TestApplyPanicsOnNonMetadataV2Request(t *testing.T) {
	entry := &raftpb.Entry{Data: legacyRequestBytes(7, "PUT", "/0/keys/foo", "value")}
	require.Panics(t, func() {
		Apply(zaptest.NewLogger(t), entry, nil, wait.New(), membership.ApplyV2storeOnly)
	})
}

func TestRequestV2RejectsMalformedInput(t *testing.T) {
	var request requestV2
	require.Error(t, request.Unmarshal([]byte{0x12, 0x02, 'P'}))
}
