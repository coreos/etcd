// Copyright 2021 The etcd Authors
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

package buckets

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap/zaptest"

	"go.etcd.io/etcd/api/v3/authpb"
	"go.etcd.io/etcd/server/v3/auth"
	"go.etcd.io/etcd/server/v3/mvcc/backend"
	betesting "go.etcd.io/etcd/server/v3/mvcc/backend/testing"
)

func TestGetAllRoles(t *testing.T) {
	tcs := []struct {
		name  string
		setup func(tx auth.AuthBatchTx)
		want  []*authpb.Role
	}{
		{
			name:  "Empty by default",
			setup: func(tx auth.AuthBatchTx) {},
			want:  nil,
		},
		{
			name: "Returns data put before",
			setup: func(tx auth.AuthBatchTx) {
				tx.UnsafePutRole(&authpb.Role{
					Name: []byte("readKey"),
					KeyPermission: []*authpb.Permission{
						{
							PermType: authpb.READ,
							Key:      []byte("key"),
							RangeEnd: []byte("end"),
						},
					},
				})
			},
			want: []*authpb.Role{
				{
					Name: []byte("readKey"),
					KeyPermission: []*authpb.Permission{
						{
							PermType: authpb.READ,
							Key:      []byte("key"),
							RangeEnd: []byte("end"),
						},
					},
				},
			},
		},
		{
			name: "Skips deleted",
			setup: func(tx auth.AuthBatchTx) {
				tx.UnsafePutRole(&authpb.Role{
					Name: []byte("role1"),
				})
				tx.UnsafePutRole(&authpb.Role{
					Name: []byte("role2"),
				})
				tx.UnsafeDeleteRole("role1")
			},
			want: []*authpb.Role{{Name: []byte("role2")}},
		},
		{
			name: "Returns data overriden by put",
			setup: func(tx auth.AuthBatchTx) {
				tx.UnsafePutRole(&authpb.Role{
					Name: []byte("role1"),
					KeyPermission: []*authpb.Permission{
						{
							PermType: authpb.READ,
						},
					},
				})
				tx.UnsafePutRole(&authpb.Role{
					Name: []byte("role2"),
				})
				tx.UnsafePutRole(&authpb.Role{
					Name: []byte("role1"),
					KeyPermission: []*authpb.Permission{
						{
							PermType: authpb.READWRITE,
						},
					},
				})
			},
			want: []*authpb.Role{
				{Name: []byte("role1"), KeyPermission: []*authpb.Permission{{PermType: authpb.READWRITE}}},
				{Name: []byte("role2")},
			},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			be, tmpPath := betesting.NewTmpBackend(t, time.Microsecond, 10)
			abe := NewAuthBackend(zaptest.NewLogger(t), be)
			abe.CreateAuthBuckets()

			tx := abe.BatchTx()
			tx.Lock()
			tc.setup(tx)
			tx.Unlock()

			abe.ForceCommit()
			be.Close()

			be2 := backend.NewDefaultBackend(tmpPath)
			defer be2.Close()
			abe2 := NewAuthBackend(zaptest.NewLogger(t), be2)
			users := abe2.GetAllRoles()

			assert.Equal(t, tc.want, users)
		})
	}
}

func TestGetRole(t *testing.T) {
	tcs := []struct {
		name  string
		setup func(tx auth.AuthBatchTx)
		want  *authpb.Role
	}{
		{
			name:  "Returns nil for missing",
			setup: func(tx auth.AuthBatchTx) {},
			want:  nil,
		},
		{
			name: "Returns data put before",
			setup: func(tx auth.AuthBatchTx) {
				tx.UnsafePutRole(&authpb.Role{
					Name: []byte("role1"),
					KeyPermission: []*authpb.Permission{
						{
							PermType: authpb.READ,
							Key:      []byte("key"),
							RangeEnd: []byte("end"),
						},
					},
				})
			},
			want: &authpb.Role{
				Name: []byte("role1"),
				KeyPermission: []*authpb.Permission{
					{
						PermType: authpb.READ,
						Key:      []byte("key"),
						RangeEnd: []byte("end"),
					},
				},
			},
		},
		{
			name: "Return nil for deleted",
			setup: func(tx auth.AuthBatchTx) {
				tx.UnsafePutRole(&authpb.Role{
					Name: []byte("role1"),
				})
				tx.UnsafeDeleteRole("role1")
			},
			want: nil,
		},
		{
			name: "Returns data overriden by put",
			setup: func(tx auth.AuthBatchTx) {
				tx.UnsafePutRole(&authpb.Role{
					Name: []byte("role1"),
					KeyPermission: []*authpb.Permission{
						{
							PermType: authpb.READ,
						},
					},
				})
				tx.UnsafePutRole(&authpb.Role{
					Name: []byte("role1"),
					KeyPermission: []*authpb.Permission{
						{
							PermType: authpb.READWRITE,
						},
					},
				})
			},
			want: &authpb.Role{
				Name:          []byte("role1"),
				KeyPermission: []*authpb.Permission{{PermType: authpb.READWRITE}},
			},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			be, tmpPath := betesting.NewTmpBackend(t, time.Microsecond, 10)
			abe := NewAuthBackend(zaptest.NewLogger(t), be)
			abe.CreateAuthBuckets()

			tx := abe.BatchTx()
			tx.Lock()
			tc.setup(tx)
			tx.Unlock()

			abe.ForceCommit()
			be.Close()

			be2 := backend.NewDefaultBackend(tmpPath)
			defer be2.Close()
			abe2 := NewAuthBackend(zaptest.NewLogger(t), be2)
			users := abe2.GetRole("role1")

			assert.Equal(t, tc.want, users)
		})
	}
}
