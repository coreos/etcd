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
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.etcd.io/etcd/server/v3/storage/backend"
	betesting "go.etcd.io/etcd/server/v3/storage/backend/testing"
)

// TestAuthEnabled ensures that UnsafeSaveAuthEnabled&UnsafeReadAuthEnabled work well together.
func TestAuthEnabled(t *testing.T) {
	tcs := []struct {
		enabled bool
	}{
		{
			enabled: true,
		},
		{
			enabled: false,
		},
	}
	for _, tc := range tcs {
		t.Run(fmt.Sprint(tc.enabled), func(t *testing.T) {
			be, tmpPath := betesting.NewTmpBackend(t, time.Microsecond, 10)
			tx := be.BatchTx()
			if tx == nil {
				t.Fatal("batch tx is nil")
			}
			tx.Lock()
			UnsafeCreateAuthBucket(tx)
			UnsafeSaveAuthEnabled(tx, tc.enabled)
			tx.Unlock()
			be.ForceCommit()
			be.Close()

			b := backend.NewDefaultBackend(tmpPath)
			defer b.Close()
			v := UnsafeReadAuthEnabled(b.BatchTx())

			assert.Equal(t, tc.enabled, v)
		})
	}
}

// TestAuthRevision ensures that UnsafeSaveAuthRevision&UnsafeReadAuthRevision work well together.
func TestAuthRevision(t *testing.T) {
	tcs := []struct {
		revision uint64
	}{
		{
			revision: 0,
		},
		{
			revision: 1,
		},
		{
			revision: math.MaxUint64,
		},
	}
	for _, tc := range tcs {
		t.Run(fmt.Sprint(tc.revision), func(t *testing.T) {
			be, tmpPath := betesting.NewTmpBackend(t, time.Microsecond, 10)
			tx := be.BatchTx()
			if tx == nil {
				t.Fatal("batch tx is nil")
			}
			tx.Lock()
			UnsafeCreateAuthBucket(tx)
			UnsafeSaveAuthRevision(tx, tc.revision)
			tx.Unlock()
			be.ForceCommit()
			be.Close()

			b := backend.NewDefaultBackend(tmpPath)
			defer b.Close()
			v := UnsafeReadAuthRevision(b.BatchTx())

			assert.Equal(t, tc.revision, v)
		})
	}
}
