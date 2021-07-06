package buckets

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	"go.etcd.io/etcd/api/v3/authpb"
	"go.etcd.io/etcd/server/v3/auth"
	"go.etcd.io/etcd/server/v3/mvcc/backend"
	betesting "go.etcd.io/etcd/server/v3/mvcc/backend/testing"
)

func TestGetAllUsers(t *testing.T) {
	tcs := []struct {
		name  string
		setup func(tx auth.AuthBatchTx)
		want  []*authpb.User
	}{
		{
			name:  "Empty by default",
			setup: func(tx auth.AuthBatchTx) {},
			want:  nil,
		},
		{
			name: "Returns user put before",
			setup: func(tx auth.AuthBatchTx) {
				tx.UnsafePutUser(&authpb.User{
					Name:     []byte("alice"),
					Password: []byte("alicePassword"),
					Roles:    []string{"aliceRole1", "aliceRole2"},
					Options: &authpb.UserAddOptions{
						NoPassword: true,
					},
				})
			},
			want: []*authpb.User{
				{
					Name:     []byte("alice"),
					Password: []byte("alicePassword"),
					Roles:    []string{"aliceRole1", "aliceRole2"},
					Options: &authpb.UserAddOptions{
						NoPassword: true,
					},
				},
			},
		},
		{
			name: "Skips deleted user",
			setup: func(tx auth.AuthBatchTx) {
				tx.UnsafePutUser(&authpb.User{
					Name: []byte("alice"),
				})
				tx.UnsafePutUser(&authpb.User{
					Name: []byte("bob"),
				})
				tx.UnsafeDeleteUser("alice")
			},
			want: []*authpb.User{{Name: []byte("bob")}},
		},
		{
			name: "Returns data overriden by put",
			setup: func(tx auth.AuthBatchTx) {
				tx.UnsafePutUser(&authpb.User{
					Name:     []byte("alice"),
					Password: []byte("oldPassword"),
				})
				tx.UnsafePutUser(&authpb.User{
					Name: []byte("bob"),
				})
				tx.UnsafePutUser(&authpb.User{
					Name:     []byte("alice"),
					Password: []byte("newPassword"),
				})
			},
			want: []*authpb.User{
				{Name: []byte("alice"), Password: []byte("newPassword")},
				{Name: []byte("bob")},
			},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			be, tmpPath := betesting.NewTmpBackend(t, time.Microsecond, 10)
			abe := NewAuthBackend(zap.NewNop(), be)
			abe.CreateAuthBuckets()

			tx := abe.BatchTx()
			tx.Lock()
			tc.setup(tx)
			tx.Unlock()

			abe.ForceCommit()
			be.Close()

			be2 := backend.NewDefaultBackend(tmpPath)
			defer be2.Close()
			abe2 := NewAuthBackend(zap.NewNop(), be2)
			users := abe2.GetAllUsers()

			assert.Equal(t, tc.want, users)
		})
	}
}

func TestGetUser(t *testing.T) {
	tcs := []struct {
		name  string
		setup func(tx auth.AuthBatchTx)
		want  *authpb.User
	}{
		{
			name:  "Returns nil for missing user",
			setup: func(tx auth.AuthBatchTx) {},
			want:  nil,
		},
		{
			name: "Returns data put before",
			setup: func(tx auth.AuthBatchTx) {
				tx.UnsafePutUser(&authpb.User{
					Name:     []byte("alice"),
					Password: []byte("alicePassword"),
					Roles:    []string{"aliceRole1", "aliceRole2"},
					Options: &authpb.UserAddOptions{
						NoPassword: true,
					},
				})
			},
			want: &authpb.User{
				Name:     []byte("alice"),
				Password: []byte("alicePassword"),
				Roles:    []string{"aliceRole1", "aliceRole2"},
				Options: &authpb.UserAddOptions{
					NoPassword: true,
				},
			},
		},
		{
			name: "Skips deleted",
			setup: func(tx auth.AuthBatchTx) {
				tx.UnsafePutUser(&authpb.User{
					Name: []byte("alice"),
				})
				tx.UnsafeDeleteUser("alice")
			},
			want: nil,
		},
		{
			name: "Returns data overriden by put",
			setup: func(tx auth.AuthBatchTx) {
				tx.UnsafePutUser(&authpb.User{
					Name:     []byte("alice"),
					Password: []byte("oldPassword"),
				})
				tx.UnsafePutUser(&authpb.User{
					Name:     []byte("alice"),
					Password: []byte("newPassword"),
				})
			},
			want: &authpb.User{
				Name:     []byte("alice"),
				Password: []byte("newPassword"),
			},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			be, tmpPath := betesting.NewTmpBackend(t, time.Microsecond, 10)
			abe := NewAuthBackend(zap.NewNop(), be)
			abe.CreateAuthBuckets()

			tx := abe.BatchTx()
			tx.Lock()
			tc.setup(tx)
			tx.Unlock()

			abe.ForceCommit()
			be.Close()

			be2 := backend.NewDefaultBackend(tmpPath)
			defer be2.Close()
			abe2 := NewAuthBackend(zap.NewNop(), be2)
			users := abe2.GetUser("alice")

			assert.Equal(t, tc.want, users)
		})
	}
}
