// Copyright 2015 The etcd Authors
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

package mvcc

import (
	"fmt"
	"testing"

	"go.etcd.io/etcd/server/v3/lease"
	betesting "go.etcd.io/etcd/server/v3/storage/backend/testing"

	"go.uber.org/zap"
)

func BenchmarkKVWatcherMemoryUsage(b *testing.B) {
	be, tmpPath := betesting.NewDefaultTmpBackend(b)
	watchable := newWatchableStore(zap.NewExample(), be, &lease.FakeLessor{}, StoreConfig{})

	defer cleanup(watchable, be, tmpPath)

	w := watchable.NewWatchStream()

	b.ReportAllocs()
	b.StartTimer()
	for i := 0; i < b.N; i++ {
		w.Watch(0, []byte(fmt.Sprint("foo", i)), nil, 0)
	}
}
