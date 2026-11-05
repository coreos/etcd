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

package command

import (
	"testing"

	"github.com/stretchr/testify/require"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
)

func TestParseCompare(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		wantResult pb.Compare_CompareResult
		wantLease  int64
		wantErr    bool
	}{
		{
			name:       "issue 20773 regression",
			line:       `lease("foo1") > "0"`,
			wantResult: pb.Compare_GREATER,
			wantLease:  0,
		},
		{
			name:       "hex lease lowercase",
			line:       `lease("foo1") = "f"`,
			wantResult: pb.Compare_EQUAL,
			wantLease:  15,
		},
		{
			name:       "hex lease uppercase",
			line:       `lease("foo1") = "AF"`,
			wantResult: pb.Compare_EQUAL,
			wantLease:  175,
		},
		{
			name:       "long lease id from etcdctl output",
			line:       `lease("foo1") = "2d8257079fa1bc0c"`,
			wantResult: pb.Compare_EQUAL,
			wantLease:  3279279168933706764,
		},
		{
			name:    "invalid hex character",
			line:    `lease("foo1") > "g"`,
			wantErr: true,
		},
		{
			name:    "overflow int64 range",
			line:    `lease("foo1") > "ffffffffffffffff"`,
			wantErr: true,
		},
		{
			name:    "missing quoted lease value",
			line:    `lease("foo1") > 10`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmp, err := ParseCompare(tt.line)
			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)

			pbCmp := cmp.GetCompare()
			require.Equal(t, []byte("foo1"), pbCmp.Key)
			require.Equal(t, pb.Compare_LEASE, pbCmp.Target)
			require.Equal(t, tt.wantResult, pbCmp.Result)

			leaseCmp, ok := pbCmp.TargetUnion.(*pb.Compare_Lease)
			require.True(t, ok)
			require.Equal(t, tt.wantLease, leaseCmp.Lease)
		})
	}
}
