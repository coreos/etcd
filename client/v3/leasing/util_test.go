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

package leasing

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	v3pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"
	v3 "go.etcd.io/etcd/client/v3"
)

func TestCopyHeader(t *testing.T) {
	t.Run("ResponseHeader should have 5 protobuf fields", func(t *testing.T) {
		require.Equal(t, 5, countProtobufFields(&v3pb.ResponseHeader{}))
	})

	t.Run("nil header", func(t *testing.T) {
		require.Nil(t, copyHeader(nil))
	})

	t.Run("copy header", func(t *testing.T) {
		want := &v3pb.ResponseHeader{
			ClusterId: 123,
			MemberId:  456,
			Revision:  789,
			RaftTerm:  101112,
			LeaderId:  131415,
		}
		actual := copyHeader(want)
		require.Equal(t, want, actual)

		actual.ClusterId = 999
		require.Equal(t, uint64(123), want.ClusterId)
	})
}

func TestCopyGetResponseMetadataOnly(t *testing.T) {
	t.Run("GetResponse should have 4 protobuf fields", func(t *testing.T) {
		require.Equal(t, 4, countProtobufFields(&v3.GetResponse{}))
	})

	t.Run("nil GetResponse", func(t *testing.T) {
		require.Nil(t, copyGetResponseMetadataOnly(nil))
	})

	t.Run("copy GetResponse metadata only", func(t *testing.T) {
		want := &v3.GetResponse{
			Header: &v3pb.ResponseHeader{
				ClusterId: 123,
				MemberId:  456,
				Revision:  789,
				RaftTerm:  101112,
				LeaderId:  131415,
			},
			Kvs: []*mvccpb.KeyValue{
				{
					Key:            []byte("key1"),
					Value:          []byte("value1"),
					CreateRevision: 1,
					ModRevision:    2,
					Version:        3,
				},
			},
			More:  true,
			Count: 1,
		}
		actual := copyGetResponseMetadataOnly(want)
		require.Equal(t, want.Header, actual.Header)
		require.Nil(t, actual.Kvs)
		require.True(t, actual.More)
		require.Equal(t, int64(1), actual.Count)

		actual.Header.ClusterId = 999
		require.Equal(t, uint64(123), want.Header.ClusterId)

		actual.Count = 2
		require.Equal(t, int64(1), want.Count)
	})
}

func TestCopyKeyValue(t *testing.T) {
	t.Run("KeyValue should have 6 protobuf fields", func(t *testing.T) {
		require.Equal(t, 6, countProtobufFields(&mvccpb.KeyValue{}))
	})

	t.Run("nil key-value", func(t *testing.T) {
		require.Nil(t, copyKeyValue(nil, false))
		require.Nil(t, copyKeyValue(nil, true))
	})

	t.Run("copy key and value", func(t *testing.T) {
		src := &mvccpb.KeyValue{
			Key:            []byte("key1"),
			Value:          []byte("value1"),
			CreateRevision: 1,
			ModRevision:    2,
			Version:        3,
			Lease:          4,
		}

		got := copyKeyValue(src, false)
		require.NotSame(t, src, got)
		require.Equal(t, src, got)

		got.Key[0] = 'K'
		got.Value[0] = 'V'
		require.Equal(t, byte('k'), src.Key[0])
		require.Equal(t, byte('v'), src.Value[0])

		src.Key[1] = 'X'
		src.Value[1] = 'Y'
		require.Equal(t, byte('e'), got.Key[1])
		require.Equal(t, byte('a'), got.Value[1])
	})

	t.Run("keys only", func(t *testing.T) {
		src := &mvccpb.KeyValue{
			Key:            []byte("key2"),
			Value:          []byte("value2"),
			CreateRevision: 5,
			ModRevision:    6,
			Version:        7,
			Lease:          8,
		}

		got := copyKeyValue(src, true)
		require.Equal(t, &mvccpb.KeyValue{
			Key:            []byte("key2"),
			Value:          nil,
			CreateRevision: 5,
			ModRevision:    6,
			Version:        7,
			Lease:          8,
		}, got)
		require.Nil(t, got.Value)

		got.Key[0] = 'K'
		require.Equal(t, byte('k'), src.Key[0])
	})
}

func TestDeleteIntersectsLeasingPrefix(t *testing.T) {
	tests := []struct {
		name string
		op   v3.Op
		want bool
	}{
		{
			name: "single key before prefix",
			op:   v3.OpDelete("a"),
		},
		{
			name: "single key in prefix",
			op:   v3.OpDelete("pfx/a"),
			want: true,
		},
		{
			name: "range ending at prefix",
			op:   v3.OpDelete("a", v3.WithRange("pfx/")),
		},
		{
			name: "range overlapping prefix",
			op:   v3.OpDelete("a", v3.WithRange("pfx0")),
			want: true,
		},
		{
			name: "range starting in prefix",
			op:   v3.OpDelete("pfx/a", v3.WithRange("z")),
			want: true,
		},
		{
			name: "range starting after prefix",
			op:   v3.OpDelete("pfx0", v3.WithRange("z")),
		},
		{
			name: "from key before prefix",
			op:   v3.OpDelete("b", v3.WithFromKey()),
			want: true,
		},
		{
			name: "from key after prefix",
			op:   v3.OpDelete("pfx0", v3.WithFromKey()),
		},
		{
			name: "matching prefix",
			op:   v3.OpDelete("pfx/", v3.WithPrefix()),
			want: true,
		},
		{
			name: "non-matching prefix",
			op:   v3.OpDelete("user/", v3.WithPrefix()),
		},
	}

	lkv := &leasingKV{pfx: "pfx/"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, lkv.deleteIntersectsLeasingPrefix(tt.op))
		})
	}
}

func countProtobufFields(v any) int {
	t := reflect.TypeOf(v)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return 0
	}

	count := 0
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if tag := f.Tag.Get("protobuf"); tag != "" {
			count++
		}
	}
	return count
}
