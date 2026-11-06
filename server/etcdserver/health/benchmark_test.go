// Copyright 2026 The etcd Authors
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

package health

import (
	"context"
	"sync/atomic"
	"testing"

	"go.uber.org/zap"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/client/pkg/v3/types"
	"go.etcd.io/etcd/server/v3/auth"
)

// benchServer is a minimal in-memory implementation of the EtcdServer
// interface. It exists only so the benchmarks can measure the dispatcher
// and debounce overhead without paying for real Raft/MVCC.
type benchServer struct {
	lead      uint64
	committed uint64
	applied   uint64
	rangeErr  error
	rootCtx   context.Context
}

func newBenchServer() *benchServer {
	return &benchServer{
		lead:      1,
		committed: 100,
		applied:   100,
		rootCtx:   context.Background(),
	}
}

func (b *benchServer) IsLearner() bool           { return false }
func (b *benchServer) Lead() uint64              { return b.lead }
func (b *benchServer) MemberID() types.ID        { return 1 }
func (b *benchServer) Alarms() []*pb.AlarmMember { return nil }
func (b *benchServer) CommittedIndex() uint64 {
	return b.committed
}
func (b *benchServer) AppliedIndex() uint64 { return b.applied }
func (b *benchServer) Range(_ context.Context, _ *pb.RangeRequest) (*pb.RangeResponse, error) {
	return &pb.RangeResponse{}, b.rangeErr
}
func (b *benchServer) AuthStore() auth.AuthStore {
	return &benchAuthStore{rootCtx: b.rootCtx}
}

type benchAuthStore struct {
	auth.AuthStore
	rootCtx context.Context
}

func (a *benchAuthStore) WithRoot(ctx context.Context) context.Context {
	if a.rootCtx != nil {
		return a.rootCtx
	}
	return ctx
}

var _ auth.AuthStore = (*benchAuthStore)(nil)

// recordingNotifierBench captures every notifier call.
type recordingNotifierBench struct {
	failed    atomic.Int64
	recovered atomic.Int64
}

func (r *recordingNotifierBench) HealthCheckFailed(reason string) { r.failed.Add(1) }
func (r *recordingNotifierBench) HealthCheckRecovered(reason string) {
	r.recovered.Add(1)
}

// ----- 4 benchmark functions covering §6.4 -----

// BenchmarkApplyLagCheck measures the cost of one applyLagCheck call,
// including the aggregator bookkeeping.
func BenchmarkApplyLagCheck(b *testing.B) {
	s := newBenchServer()
	cfg := DefaultConfig()
	agg := NewAggregator(cfg, &recordingNotifierBench{})

	ctx := context.Background()
	checkFn := applyLagCheckWith(cfg.ApplyLagThreshold)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		agg.Evaluate("apply_lag", checkFn(ctx, s))
	}
}

// BenchmarkNoLeaderCheck measures the cost of one noLeaderCheck call.
func BenchmarkNoLeaderCheck(b *testing.B) {
	s := newBenchServer()
	cfg := DefaultConfig()
	agg := NewAggregator(cfg, &recordingNotifierBench{})

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		agg.Evaluate("no_leader", noLeaderCheck(ctx, s))
	}
}

// BenchmarkAggregatorEvaluate_NoStateChange measures the per-tick cost when
// checks succeed and the service is already SERVING (steady state). This
// is the dominant cost in the 500ms tick loop.
func BenchmarkAggregatorEvaluate_NoStateChange(b *testing.B) {
	cfg := DefaultConfig()
	agg := NewAggregator(cfg, &recordingNotifierBench{})
	// pre-warm so counters are stable.
	agg.Evaluate("apply_lag", nil)
	agg.Evaluate("no_leader", nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		agg.Evaluate("apply_lag", nil)
		agg.Evaluate("no_leader", nil)
	}
}

// BenchmarkCheckerTick_Path measures the cost of a full tick iteration,
// including the async probe spawns (data_corruption every tick,
// linear_read/serializable_read on alternate ticks). All probes are
// stubbed to return immediately, so the benchmark measures dispatcher
// overhead only.
func BenchmarkCheckerTick_Path(b *testing.B) {
	s := newBenchServer()
	cfg := DefaultConfig()
	c := NewChecker(s, zap.NewNop(), cfg, &recordingNotifierBench{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.tick(ctx)
	}
}
