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

package schema

import (
	"go.etcd.io/etcd/api/v3/authpb"
	"go.etcd.io/etcd/server/v3/storage/backend"
	"go.uber.org/zap"
)

func UnsafeGetRole(lg *zap.Logger, tx backend.BatchTx, roleName string) *authpb.Role {
	_, vs := tx.UnsafeRange(AuthRoles, []byte(roleName), nil, 0)
	if len(vs) == 0 {
		return nil
	}

	role := &authpb.Role{}
	err := role.Unmarshal(vs[0])
	if err != nil {
		lg.Panic("failed to unmarshal 'authpb.Role'", zap.Error(err))
	}
	return role
}

func UnsafeGetAllRoles(lg *zap.Logger, tx backend.BatchTx) []*authpb.Role {
	_, vs := tx.UnsafeRange(AuthRoles, []byte{0}, []byte{0xff}, -1)
	if len(vs) == 0 {
		return nil
	}

	roles := make([]*authpb.Role, len(vs))
	for i := range vs {
		role := &authpb.Role{}
		err := role.Unmarshal(vs[i])
		if err != nil {
			lg.Panic("failed to unmarshal 'authpb.Role'", zap.Error(err))
		}
		roles[i] = role
	}
	return roles
}

func UnsafePutRole(lg *zap.Logger, tx backend.BatchTx, role *authpb.Role) {
	b, err := role.Marshal()
	if err != nil {
		lg.Panic(
			"failed to marshal 'authpb.Role'",
			zap.String("role-name", string(role.Name)),
			zap.Error(err),
		)
	}

	tx.UnsafePut(AuthRoles, role.Name, b)
}

func UnsafeDeleteRole(tx backend.BatchTx, rolename string) {
	tx.UnsafeDelete(AuthRoles, []byte(rolename))
}
