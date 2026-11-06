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

package cache

import (
	"testing"

	"github.com/stretchr/testify/require"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
)

func TestInvalidateOpenEndedRanges(t *testing.T) {
	rangeReq := func(key, end string) *pb.RangeRequest {
		return &pb.RangeRequest{Key: []byte(key), RangeEnd: []byte(end), Serializable: true}
	}
	tests := []struct {
		name            string
		cached          *pb.RangeRequest
		writeKey        string
		writeEnd        string
		wantInvalidated bool
	}{
		{"finite range, put inside", rangeReq("a", "c"), "b", "", true},
		{"finite range, put outside", rangeReq("a", "c"), "d", "", false},
		{"finite range, finite delete over it", rangeReq("a", "c"), "a", "z", true},
		{"from-key range, put above the start key", rangeReq("a", "\x00"), "z", "", true},
		{"from-key range, put below the start key", rangeReq("b", "\x00"), "a", "", false},
		{"from-key range, finite delete inside", rangeReq("a", "\x00"), "b", "d", true},
		{"whole keyspace range, put", rangeReq("\x00", "\x00"), "b", "", true},
		{"finite range above the key, from-key delete", rangeReq("b", "c"), "a", "\x00", true},
		{"single key above the key, from-key delete", rangeReq("z", ""), "a", "\x00", true},
		{"single key below the key, from-key delete", rangeReq("a", ""), "b", "\x00", false},
		{"finite range, whole keyspace delete", rangeReq("a", "c"), "\x00", "\x00", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewCache(16)
			c.Add(tt.cached, &pb.RangeResponse{})
			_, err := c.Get(tt.cached)
			require.NoError(t, err)

			var end []byte
			if tt.writeEnd != "" {
				end = []byte(tt.writeEnd)
			}
			c.Invalidate([]byte(tt.writeKey), end)

			_, err = c.Get(tt.cached)
			if tt.wantInvalidated {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}
