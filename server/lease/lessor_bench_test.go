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

package lease

import (
	"math/rand"
	"testing"
	"time"

	betesting "go.etcd.io/etcd/server/v3/storage/backend/testing"
	"go.uber.org/zap"
)

func BenchmarkLessorGrant1000(b *testing.B)   { benchmarkLessorGrant(1000, b) }
func BenchmarkLessorGrant100000(b *testing.B) { benchmarkLessorGrant(100000, b) }

func BenchmarkLessorRevoke1000(b *testing.B)   { benchmarkLessorRevoke(1000, b) }
func BenchmarkLessorRevoke100000(b *testing.B) { benchmarkLessorRevoke(100000, b) }

func BenchmarkLessorRenew1000(b *testing.B)   { benchmarkLessorRenew(1000, b) }
func BenchmarkLessorRenew100000(b *testing.B) { benchmarkLessorRenew(100000, b) }

// Use findExpired10000 replace findExpired1000, which takes too long.
func BenchmarkLessorFindExpired10000(b *testing.B)  { benchmarkLessorFindExpired(10000, b) }
func BenchmarkLessorFindExpired100000(b *testing.B) { benchmarkLessorFindExpired(100000, b) }

func init() {
	rand.Seed(time.Now().UTC().UnixNano())
}

const (
	// minTTL keep lease will not auto expire in benchmark
	minTTL = 1000
	// maxTTL control repeat probability of ttls
	maxTTL = 2000
)

func randomTTL(n int, min, max int64) (out []int64) {
	for i := 0; i < n; i++ {
		out = append(out, rand.Int63n(max-min)+min)
	}
	return out
}

// demote lessor from being the primary, but don't change any lease's expiry
func demote(le *lessor) {
	le.mu.Lock()
	defer le.mu.Unlock()
	close(le.demotec)
	le.demotec = nil
}

// return new lessor and tearDown to release resource
func setUp(t testing.TB) (le *lessor, tearDown func()) {
	lg := zap.NewNop()
	be, _ := betesting.NewDefaultTmpBackend(t)
	// MinLeaseTTL is negative, so we can grant expired lease in benchmark.
	// ExpiredLeasesRetryInterval should small, so benchmark of findExpired will recheck expired lease.
	le = newLessor(lg, be, LessorConfig{MinLeaseTTL: -1000, ExpiredLeasesRetryInterval: 10 * time.Microsecond})
	le.SetRangeDeleter(func() TxnDelete {
		ftd := &FakeTxnDelete{be.BatchTx()}
		ftd.Lock()
		return ftd
	})
	le.Promote(0)

	return le, func() {
		le.Stop()
		be.Close()
	}
}

func benchmarkLessorGrant(benchSize int, b *testing.B) {
	ttls := randomTTL(benchSize, minTTL, maxTTL)

	var le *lessor
	var tearDown func()

	b.ResetTimer()
	for i := 0; i < b.N; {
		b.StopTimer()
		if tearDown != nil {
			tearDown()
			tearDown = nil
		}
		le, tearDown = setUp(b)
		b.StartTimer()

		for j := 1; j <= benchSize; j++ {
			le.Grant(LeaseID(j), ttls[j-1])
		}
		i += benchSize
	}
	b.StopTimer()

	if tearDown != nil {
		tearDown()
	}
}

func benchmarkLessorRevoke(benchSize int, b *testing.B) {
	ttls := randomTTL(benchSize, minTTL, maxTTL)

	var le *lessor
	var tearDown func()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		if tearDown != nil {
			tearDown()
			tearDown = nil
		}
		le, tearDown = setUp(b)
		for j := 1; j <= benchSize; j++ {
			le.Grant(LeaseID(j), ttls[j-1])
		}
		b.StartTimer()

		for j := 1; j <= benchSize; j++ {
			le.Revoke(LeaseID(j))
		}
		i += benchSize
	}
	b.StopTimer()

	if tearDown != nil {
		tearDown()
	}
}

func benchmarkLessorRenew(benchSize int, b *testing.B) {
	ttls := randomTTL(benchSize, minTTL, maxTTL)

	var le *lessor
	var tearDown func()

	b.ResetTimer()
	for i := 0; i < b.N; {
		b.StopTimer()
		if tearDown != nil {
			tearDown()
			tearDown = nil
		}
		le, tearDown = setUp(b)
		for j := 1; j <= benchSize; j++ {
			le.Grant(LeaseID(j), ttls[j-1])
		}
		b.StartTimer()

		for j := 1; j <= benchSize; j++ {
			le.Renew(LeaseID(j))
		}
		i += benchSize
	}
	b.StopTimer()

	if tearDown != nil {
		tearDown()
	}
}

func benchmarkLessorFindExpired(benchSize int, b *testing.B) {
	// 50% lease are expired.
	ttls := randomTTL(benchSize, -500, 500)
	findExpiredLimit := 50

	var le *lessor
	var tearDown func()

	b.ResetTimer()
	for i := 0; i < b.N; {
		b.StopTimer()
		if tearDown != nil {
			tearDown()
			tearDown = nil
		}
		le, tearDown = setUp(b)
		for j := 1; j <= benchSize; j++ {
			le.Grant(LeaseID(j), ttls[j-1])
		}
		// lessor's runLoop should not call findExpired
		demote(le)
		b.StartTimer()

		// refresh fixture after pop all expired lease
		for ; ; i++ {
			le.mu.Lock()
			ls := le.findExpiredLeases(findExpiredLimit)
			if len(ls) == 0 {
				le.mu.Unlock()
				break
			}
			le.mu.Unlock()

			// simulation: revoke lease after expired
			b.StopTimer()
			for _, lease := range ls {
				le.Revoke(lease.ID)
			}
			b.StartTimer()
		}
	}
	b.StopTimer()

	if tearDown != nil {
		tearDown()
	}
}
