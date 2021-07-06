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
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	"go.etcd.io/etcd/server/v3/mvcc/backend"
	betesting "go.etcd.io/etcd/server/v3/mvcc/backend/testing"
)

// TestAuthEnabled ensures that UnsafeSaveAuthEnabled&UnsafeReadAuthEnabled work well together.
func TestAuthEnabled(t *testing.T) {
	tcs := []struct {
		name        string
		skipSetting bool
		setEnabled  bool
		wantEnabled bool
	}{
		{
			name:        "Returns true after setting true",
			setEnabled:  true,
			wantEnabled: true,
		},
		{
			name:        "Returns false after setting false",
			setEnabled:  false,
			wantEnabled: false,
		},
		{
			name:        "Returns false by default",
			skipSetting: true,
			wantEnabled: false,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			be, tmpPath := betesting.NewTmpBackend(t, time.Microsecond, 10)
			abe := NewAuthBackend(zap.NewNop(), be)
			tx := abe.BatchTx()
			abe.CreateAuthBuckets()

			tx.Lock()
			if !tc.skipSetting {
				tx.UnsafeSaveAuthEnabled(tc.setEnabled)
			}
			tx.Unlock()
			abe.ForceCommit()
			be.Close()

			be2 := backend.NewDefaultBackend(tmpPath)
			defer be2.Close()
			abe2 := NewAuthBackend(zap.NewNop(), be2)
			tx = abe2.BatchTx()
			tx.Lock()
			defer tx.Unlock()
			v := tx.UnsafeReadAuthEnabled()

			assert.Equal(t, tc.wantEnabled, v)
		})
	}
}

// TestAuthRevision ensures that UnsafeSaveAuthRevision&UnsafeReadAuthRevision work well together.
func TestAuthRevision(t *testing.T) {
	tcs := []struct {
		name         string
		setRevision  uint64
		wantRevision uint64
	}{
		{
			name:         "Returns 0 by default",
			wantRevision: 0,
		},
		{
			name:         "Returns 1 after setting 1",
			setRevision:  1,
			wantRevision: 1,
		},
		{
			name:         "Returns max int after setting max int",
			setRevision:  math.MaxUint64,
			wantRevision: math.MaxUint64,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			be, tmpPath := betesting.NewTmpBackend(t, time.Microsecond, 10)
			abe := NewAuthBackend(zap.NewNop(), be)
			abe.CreateAuthBuckets()

			if tc.setRevision != 0 {
				tx := abe.BatchTx()
				tx.Lock()
				tx.UnsafeSaveAuthRevision(tc.setRevision)
				tx.Unlock()
			}
			abe.ForceCommit()
			be.Close()

			be2 := backend.NewDefaultBackend(tmpPath)
			defer be2.Close()
			abe2 := NewAuthBackend(zap.NewNop(), be2)
			tx := abe2.BatchTx()
			tx.Lock()
			defer tx.Unlock()
			v := tx.UnsafeReadAuthRevision()

			assert.Equal(t, tc.wantRevision, v)
		})
	}
}
