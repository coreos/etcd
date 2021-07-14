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
	"bytes"
	"encoding/binary"
	"go.etcd.io/etcd/server/v3/storage/backend"
)

const (
	revBytesLen = 8
)

var (
	authEnabled  = []byte{1}
	authDisabled = []byte{0}
)

func UnsafeCreateAuthBucket(tx backend.BatchTx) {
	tx.UnsafeCreateBucket(Auth)
}

func UnsafeSaveAuthEnabled(tx backend.BatchTx, enabled bool) {
	if enabled {
		tx.UnsafePut(Auth, AuthEnabledKeyName, authEnabled)
	} else {
		tx.UnsafePut(Auth, AuthEnabledKeyName, authDisabled)
	}
}

func UnsafeReadAuthEnabled(tx backend.ReadTx) bool {
	_, vs := tx.UnsafeRange(Auth, AuthEnabledKeyName, nil, 0)
	if len(vs) == 1 {
		if bytes.Equal(vs[0], authEnabled) {
			return true
		}
	}
	return false
}

func UnsafeSaveAuthRevision(tx backend.BatchTx, rev uint64) {
	revBytes := make([]byte, revBytesLen)
	binary.BigEndian.PutUint64(revBytes, rev)
	tx.UnsafePut(Auth, AuthRevisionKeyName, revBytes)
}

func UnsafeReadAuthRevision(tx backend.ReadTx) uint64 {
	_, vs := tx.UnsafeRange(Auth, AuthRevisionKeyName, nil, 0)
	if len(vs) != 1 {
		// this can happen in the initialization phase
		return 0
	}
	return binary.BigEndian.Uint64(vs[0])
}
