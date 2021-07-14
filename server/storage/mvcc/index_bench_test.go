// Copyright 2018 The etcd Authors
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
	"testing"

	"go.uber.org/zap"
)

func BenchmarkIndexCompact1(b *testing.B)       { benchmarkIndexCompact(b, 1) }
func BenchmarkIndexCompact100(b *testing.B)     { benchmarkIndexCompact(b, 100) }
func BenchmarkIndexCompact10000(b *testing.B)   { benchmarkIndexCompact(b, 10000) }
func BenchmarkIndexCompact100000(b *testing.B)  { benchmarkIndexCompact(b, 100000) }
func BenchmarkIndexCompact1000000(b *testing.B) { benchmarkIndexCompact(b, 1000000) }

func benchmarkIndexCompact(b *testing.B, size int) {
	log := zap.NewNop()
	kvindex := newTreeIndex(log)

	bytesN := 64
	keys := createBytesSlice(bytesN, size)
	for i := 1; i < size; i++ {
		kvindex.Put(keys[i], revision{main: int64(i), sub: int64(i)})
	}
	b.ResetTimer()
	for i := 1; i < b.N; i++ {
		kvindex.Compact(int64(i))
	}
}
