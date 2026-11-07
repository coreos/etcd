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

package betesting

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"

	"go.etcd.io/etcd/server/v3/storage/backend"
)

func NewTmpBackendFromCfg(tb testing.TB, bcfg backend.BackendConfig) (backend.Backend, string) {
	dir, err := os.MkdirTemp(tb.TempDir(), "etcd_backend_test")
	if err != nil {
		panic(err)
	}
	tmpPath := filepath.Join(dir, "database")
	bcfg.Path = tmpPath
	if bcfg.Logger == nil {
		bcfg.Logger = zaptest.NewLogger(tb)
	}
	return backend.New(bcfg), tmpPath
}

// NewTmpBackend creates a backend implementation for testing.
func NewTmpBackend(tb testing.TB, batchInterval time.Duration, batchLimit int) (backend.Backend, string) {
	bcfg := backend.DefaultBackendConfig(zaptest.NewLogger(tb))
	bcfg.BatchInterval, bcfg.BatchLimit = batchInterval, batchLimit
	return NewTmpBackendFromCfg(tb, bcfg)
}

func NewDefaultTmpBackend(tb testing.TB) (backend.Backend, string) {
	return NewTmpBackendFromCfg(tb, backend.DefaultBackendConfig(zaptest.NewLogger(tb)))
}

// NewDefaultTmpBackendWithNopLogger creates a default temporary backend with logging disabled.
func NewDefaultTmpBackendWithNopLogger(tb testing.TB) (backend.Backend, string) {
	return NewTmpBackendFromCfg(tb, backend.DefaultBackendConfig(zap.NewNop()))
}

func Close(tb testing.TB, b backend.Backend) {
	assert.NoError(tb, b.Close())
}
