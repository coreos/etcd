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
	"go.etcd.io/etcd/api/v3/authpb"
	"go.etcd.io/etcd/server/v3/mvcc/backend"
	"go.uber.org/zap"
)

func UnsafeCreateAuthUsersBucket(tx backend.BatchTx) {
	tx.UnsafeCreateBucket(AuthUsers)
}

func UnsafeGetUser(lg *zap.Logger, tx backend.BatchTx, username string) *authpb.User {
	_, vs := tx.UnsafeRange(AuthUsers, []byte(username), nil, 0)
	if len(vs) == 0 {
		return nil
	}

	user := &authpb.User{}
	err := user.Unmarshal(vs[0])
	if err != nil {
		lg.Panic(
			"failed to unmarshal 'authpb.User'",
			zap.String("user-name", username),
			zap.Error(err),
		)
	}
	return user
}

func UnsafeGetAllUsers(lg *zap.Logger, tx backend.BatchTx) []*authpb.User {
	_, vs := tx.UnsafeRange(AuthUsers, []byte{0}, []byte{0xff}, -1)
	if len(vs) == 0 {
		return nil
	}

	users := make([]*authpb.User, len(vs))
	for i := range vs {
		user := &authpb.User{}
		err := user.Unmarshal(vs[i])
		if err != nil {
			lg.Panic("failed to unmarshal 'authpb.User'", zap.Error(err))
		}
		users[i] = user
	}
	return users
}

func UnsafePutUser(lg *zap.Logger, tx backend.BatchTx, user *authpb.User) {
	b, err := user.Marshal()
	if err != nil {
		lg.Panic("failed to unmarshal 'authpb.User'", zap.Error(err))
	}
	tx.UnsafePut(AuthUsers, user.Name, b)
}

func UnsafeDeleteUser(tx backend.BatchTx, username string) {
	tx.UnsafeDelete(AuthUsers, []byte(username))
}
