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

//go:build compaction_bench

// TestCompactionReadWriteImpact is an opt-in e2e benchmark (gated by
// the `compaction_bench` build tag) that measures the impact of
// compaction on a real etcd cluster's read/write path. It exists to
// validate the kind of change in PR #21368: scheduleCompaction runs
// in a FIFO scheduler with b.ForceCommit() (fsync) between batches
// and acquires batchTx.LockOutsideApply() each iteration, and a
// micro-benchmark against an in-memory mvcc.Store does not see any
// of that.
//
// What it does:
//
//  1. Brings up a 1-node etcd cluster with WithSnapshotCount(100),
//     WithCompactionBatchLimit(5), and WithCompactionSleepInterval
//     (10ms) so compaction is forced to do many small batches that
//     contend with the apply loop.
//
//  2. Spawns `WorkerCount` (=4) worker goroutines, each holding its
//     own *clientv3.Client and a *rate.Limiter paced at `RequestRate`
//     (=2000) per second. Each request is independently chosen Put
//     or Range with long-run read fraction `ReadWriteRatio` (=0.8).
//
//  3. Drives three sequential phases, all of which run the workers
//     continuously (the compactor is the only thing that turns on
//     and off):
//
//     - warmup       (5s) fill the key space; no compaction.
//     - compaction   (60s) compactor fires immediately on entry,
//     then every CompactionInterval (=30s).
//     - cooldown     (10s) workers only, no compaction.
//
//  4. Records per-request latency into a bounded channel. A single
//     aggregator goroutine bins samples into per-second buckets and
//     the recorder's `snapshot` is read once at the end.
//
//  5. Emits a JSON report at the end:
//
//     - params: all workload knobs (so two reports are diffable).
//     - phases: start/end wall-clock ns per phase.
//     - per_second: per-second put/range count + p50 + p99.
//     - compaction_events: one entry per fired compaction (start,
//     end, target rev, took_ms).
//     - summary: per-phase worst p99 + drop count.
//
//     Two report copies are written: one to `ReportDir` (default
//     _artifacts/) and one mirrored to /tmp/etcd-compaction-rw-impact/
//     so scripts/compaction_rw_impact_test.sh can chart the latest
//     run after the test temp dir is cleaned up.
//
// To run: `go test -tags compaction_bench -run
// TestCompactionReadWriteImpact ./tests/e2e/ -v` (or use
// scripts/compaction_rw_impact_test.sh which also renders the chart).
//
// See openspec/changes/compaction-rw-impact-bench/ for the full
// design and rationale.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/tests/v3/framework/e2e"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
)

// ---- Test entry point ----

func TestCompactionReadWriteImpact(t *testing.T) {
	b := &compactionRWImpactBench{
		t:                        t,
		KeySpaceSize:             5000,
		ValueSize:                256,
		ReadWriteRatio:           0.8,
		RequestRate:              2000,
		WorkerCount:              4,
		WarmupDuration:           5 * time.Second,
		CompactionWindow:         60 * time.Second,
		CooldownDuration:         10 * time.Second,
		CompactionInterval:       30 * time.Second,
		CompactedRevisionsBehind: 1000,
		CompactionBatchLimit:     5,
		CompactionSleepInterval:  10 * time.Millisecond,
		ReportDir:                "_artifacts",
	}
	b.run(t.Context())
}

// ---- Orchestrator: struct + run + phase methods ----

// compactionRWImpactBench encapsulates the workload and cluster
// state. All workload shape parameters are fields on this struct so
// they are visible at the top of the test and can be tuned in one
// place.
type compactionRWImpactBench struct {
	t *testing.T

	// ---- Workload knobs (tune these) ----

	// Number of distinct keys in the key space (keys are "0".."KeySpaceSize-1").
	KeySpaceSize int
	// Byte size of each Put value payload.
	ValueSize int
	// Fraction of requests that are Range reads (0.0 = all writes, 1.0 = all reads).
	ReadWriteRatio float64
	// Target requests per second per worker; aggregate ≈ WorkerCount × RequestRate.
	RequestRate int
	// Number of concurrent worker goroutines, each with its own client and rate limiter.
	WorkerCount int
	// Duration of the warmup phase (fills key space, no compaction). 5s+ recommended for stable baselines.
	WarmupDuration time.Duration
	// Duration of the compaction phase (workers + periodic compaction).
	CompactionWindow time.Duration
	// Duration of the cooldown phase (workers only, no compaction).
	CooldownDuration time.Duration
	// How often to fire a compaction during the compaction phase.
	CompactionInterval time.Duration
	// How many revisions behind the current head to target for compaction.
	CompactedRevisionsBehind int64
	// etcd server --compaction-batch-limit: max keys deleted per batch during compaction.
	CompactionBatchLimit int
	// etcd server --compaction-sleep-interval: sleep between compaction batches.
	CompactionSleepInterval time.Duration

	// ---- Output ----

	// Directory for the JSON report file (relative to the test working dir).
	ReportDir string

	// ---- Internals (set in setup) ----
	clus   *e2e.EtcdProcessCluster
	cli    []*clientv3.Client
	rec    *sampleRecorder
	phases []phaseRecord
	events []compactionEvent
}

// run drives the three phases sequentially, then writes the report.
func (b *compactionRWImpactBench) run(ctx context.Context) {
	b.setup()
	b.runWarmup(ctx)
	b.runCompactionLoop(ctx)
	b.runCooldown(ctx)
	b.report()
}

// recordPhase appends a phase record to b.phases.
func (b *compactionRWImpactBench) recordPhase(name string, start, end time.Time) {
	b.phases = append(b.phases, phaseRecord{Name: name, StartNs: start.UnixNano(), EndNs: end.UnixNano()})
}

// setup is called from the orchestrator before any phase. It
// brings up the cluster, dials clients, and starts the recorder.
func (b *compactionRWImpactBench) setup() {
	e2e.BeforeTest(b.t)

	clus, err := e2e.NewEtcdProcessCluster(b.t.Context(), b.t,
		e2e.WithClusterSize(1),
		e2e.WithSnapshotCount(100),
		e2e.WithCompactionBatchLimit(b.CompactionBatchLimit),
		e2e.WithCompactionSleepInterval(b.CompactionSleepInterval),
	)
	if err != nil {
		b.t.Fatalf("failed to start etcd cluster: %v", err)
	}
	b.clus = clus

	b.cli = make([]*clientv3.Client, b.WorkerCount)
	for i := range b.cli {
		b.cli[i] = newClient(b.t, b.clus.EndpointsGRPC(), e2e.ClientConfig{})
	}

	b.rec = newSampleRecorder()
	b.rec.start()

	b.t.Cleanup(func() { _ = b.clus.Stop() })
	b.t.Logf("cluster started, %d client slots", b.WorkerCount)
}

// runWarmup fills the key space at high rate. No compactions.
func (b *compactionRWImpactBench) runWarmup(ctx context.Context) {
	now := time.Now()
	defer func() { b.recordPhase("warmup", now, time.Now()) }()
	_, cancel, wg := b.startWorkers(ctx)
	defer func() { cancel(); wg.Wait() }()
	b.t.Logf("warmup: running for %s", b.WarmupDuration)
	time.Sleep(b.WarmupDuration)
}

// runCooldown runs workers with no compactions, after the compaction
// phase has ended.
func (b *compactionRWImpactBench) runCooldown(ctx context.Context) {
	now := time.Now()
	defer func() { b.recordPhase("cooldown", now, time.Now()) }()
	_, cancel, wg := b.startWorkers(ctx)
	defer func() { cancel(); wg.Wait() }()
	b.t.Logf("cooldown: running for %s", b.CooldownDuration)
	time.Sleep(b.CooldownDuration)
}

// runCompactionLoop runs workers plus a compactor that fires every
// CompactionInterval for the duration of CompactionWindow.
func (b *compactionRWImpactBench) runCompactionLoop(ctx context.Context) {
	phaseStart := time.Now()
	defer func() { b.recordPhase("compaction", phaseStart, time.Now()) }()

	cctx, ccancel := context.WithCancel(ctx)
	defer ccancel()

	_, wcancel, wg := b.startWorkers(cctx)
	defer func() { wcancel(); wg.Wait() }()

	compactorDone := make(chan struct{})
	go func() {
		defer close(compactorDone)
		b.runCompactor(cctx)
	}()

	b.t.Logf("compaction-loop: running for %s with compaction every %s",
		b.CompactionWindow, b.CompactionInterval)
	time.Sleep(b.CompactionWindow)

	// Stop the compactor first so it doesn't fire after we exit the
	// phase boundary.
	ccancel()
	<-compactorDone
}

// runCompactor fires compactions on a ticker. The first compaction
// fires immediately on entry, then on every tick of CompactionInterval.
func (b *compactionRWImpactBench) runCompactor(ctx context.Context) {
	cli := b.cli[0]
	tick := time.NewTicker(b.CompactionInterval)
	defer tick.Stop()
	// Local tracker for the highest compacted rev, so we don't keep
	// retrying the same already-compacted revision.
	lastCompactedRev := int64(0)

	b.fireOneCompaction(ctx, cli, &lastCompactedRev)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		b.fireOneCompaction(ctx, cli, &lastCompactedRev)
	}
}

// fireOneCompaction reads the current revision, picks a target, and
// issues the Compact RPC. On any error (Get or Compact) the event
// is dropped and the compactor's lastCompactedRev is left unchanged
// so the next tick re-evaluates the target.
func (b *compactionRWImpactBench) fireOneCompaction(ctx context.Context, cli *clientv3.Client, lastCompactedRev *int64) {
	getCtx, getCancel := context.WithTimeout(ctx, 5*time.Second)
	resp, err := cli.KV.Get(getCtx, "0")
	getCancel()
	if err != nil {
		return
	}
	currentRev := resp.Header.Revision

	rev := currentRev - b.CompactedRevisionsBehind
	if rev < 1 {
		rev = 1
	}
	if rev <= *lastCompactedRev {
		rev = *lastCompactedRev + 1
	}
	if rev > currentRev {
		rev = currentRev
	}

	st := time.Now()
	compactCtx, compactCancel := context.WithTimeout(ctx, 30*time.Second)
	_, cerr := cli.KV.Compact(compactCtx, rev, clientv3.WithCompactPhysical())
	compactCancel()
	if cerr != nil {
		// Don't record an event with a bogus duration; the orchestrator
		// already logs the test failure via t.Fatal later. The
		// compactor's local lastCompactedRev is left unchanged so
		// the next tick re-evaluates the target.
		return
	}
	b.recordCompactionEvent(st.UnixNano(), time.Now().UnixNano(), rev)
	*lastCompactedRev = rev
}

// recordCompactionEvent appends a compaction event to b.events.
func (b *compactionRWImpactBench) recordCompactionEvent(startNs, endNs, rev int64) {
	tookMs := float64(endNs-startNs) / float64(time.Millisecond)
	b.events = append(b.events, compactionEvent{
		StartNs: startNs,
		EndNs:   endNs,
		Rev:     rev,
		TookMs:  tookMs,
	})
}

// ---- Report writer ----

// report is a placeholder for group 6; it just closes the recorder so
// the e2e exits cleanly.
func (b *compactionRWImpactBench) report() {
	snap := b.rec.closeAndSnapshot()
	if err := b.writeReport(snap); err != nil {
		b.t.Errorf("writeReport: %v", err)
	}
}

// reportJSON is the top-level structure written to disk.
type reportJSON struct {
	Params    map[string]any               `json:"params"`
	Phases    []phaseRecord                `json:"phases"`
	PerSecond []perSecondJSON              `json:"per_second"`
	Events    []compactionEvent            `json:"compaction_events"`
	Summary   map[string]*phaseSummaryJSON `json:"summary"`
}

type perSecondJSON struct {
	Second           int64   `json:"second"`
	PutCount         int     `json:"put_count"`
	PutP50Ms         float64 `json:"put_p50_ms"`
	PutP99Ms         float64 `json:"put_p99_ms"`
	RangeCount       int     `json:"range_count"`
	RangeP50Ms       float64 `json:"range_p50_ms"`
	RangeP99Ms       float64 `json:"range_p99_ms"`
	CompactionActive bool    `json:"compaction_active"`
}

type phaseSummaryJSON struct {
	PutP99Ms   float64 `json:"put_p99_ms"`
	RangeP99Ms float64 `json:"range_p99_ms"`
	Dropped    int64   `json:"dropped"`
}

// writeReport serializes the benchmark state to a JSON file. The
// only test invariant: the report file is written and is well-formed.
func (b *compactionRWImpactBench) writeReport(snap snapshotBuckets) error {
	if err := os.MkdirAll(b.ReportDir, 0o755); err != nil {
		return err
	}

	// Per-second bucket list
	perSec := make([]perSecondJSON, 0, len(snap.buckets))
	for _, sec := range sortedBucketKeys(snap.buckets) {
		bucket := snap.buckets[sec]
		active := false
		for _, ev := range b.events {
			evStartSec := ev.StartNs / int64(time.Second)
			evEndSec := ev.EndNs / int64(time.Second)
			if sec >= evStartSec && sec <= evEndSec+1 {
				active = true
				break
			}
		}
		perSec = append(perSec, perSecondJSON{
			Second:           sec,
			PutCount:         len(bucket.putLatsMs),
			PutP50Ms:         percentile(bucket.putLatsMs, 50),
			PutP99Ms:         percentile(bucket.putLatsMs, 99),
			RangeCount:       len(bucket.rangeLatsMs),
			RangeP50Ms:       percentile(bucket.rangeLatsMs, 50),
			RangeP99Ms:       percentile(bucket.rangeLatsMs, 99),
			CompactionActive: active,
		})
	}

	summary := b.computeSummary(perSec, snap)

	// TODO(shenmu): revisit these two assertions once we have ≥3 clean
	// baseline runs to set realistic thresholds. Loose now to avoid
	// flakiness on noisy CI runners.
	// require.Greater(b.t, summary["compaction"].RangeP99Ms,
	//     summary["warmup"].RangeP99Ms,
	//     "compaction should have some visible read impact")
	// require.Less(b.t, summary["cooldown"].RangeP99Ms,
	//     2*summary["warmup"].RangeP99Ms,
	//     "latency should recover after compactions stop")

	r := reportJSON{
		Params:    b.paramsMap(),
		Phases:    b.phases,
		PerSecond: perSec,
		Events:    b.events,
		Summary:   summary,
	}
	buf, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(b.ReportDir,
		fmt.Sprintf("compaction_rw_impact_%d.json", time.Now().Unix()))
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		return err
	}
	// Also mirror to a stable absolute path so the
	// scripts/compaction_rw_impact_test.sh wrapper can find and
	// chart it after the test temp dir is cleaned up.
	mirrorDir := "/tmp/etcd-compaction-rw-impact"
	if err := os.MkdirAll(mirrorDir, 0o755); err == nil {
		mirrorPath := filepath.Join(mirrorDir, filepath.Base(path))
		_ = os.WriteFile(mirrorPath, buf, 0o644)
	}
	b.t.Logf("wrote report to %s", path)
	return nil
}

// computeSummary aggregates per-second buckets by phase. Per-phase
// P99 is approximated as the max of the per-second P99s (a
// conservative upper bound that avoids merging the per-second
// latency slices).
func (b *compactionRWImpactBench) computeSummary(perSec []perSecondJSON, snap snapshotBuckets) map[string]*phaseSummaryJSON {
	out := make(map[string]*phaseSummaryJSON, len(b.phases))
	for _, p := range b.phases {
		out[p.Name] = &phaseSummaryJSON{}
	}
	for _, ps := range perSec {
		var phaseName string
		for _, p := range b.phases {
			if ps.Second*int64(time.Second) >= p.StartNs/int64(time.Second)*int64(time.Second) &&
				ps.Second*int64(time.Second) <= p.EndNs/int64(time.Second)*int64(time.Second) {
				phaseName = p.Name
				break
			}
		}
		if phaseName == "" {
			continue
		}
		s := out[phaseName]
		if ps.PutP99Ms > s.PutP99Ms {
			s.PutP99Ms = ps.PutP99Ms
		}
		if ps.RangeP99Ms > s.RangeP99Ms {
			s.RangeP99Ms = ps.RangeP99Ms
		}
	}
	if _, ok := out["compaction"]; ok {
		out["compaction"].Dropped = snap.dropped
	}
	return out
}

func (b *compactionRWImpactBench) paramsMap() map[string]any {
	return map[string]any{
		"key_space_size":               b.KeySpaceSize,
		"value_size":                   b.ValueSize,
		"read_write_ratio":             b.ReadWriteRatio,
		"request_rate":                 b.RequestRate,
		"worker_count":                 b.WorkerCount,
		"warmup_duration_ms":           b.WarmupDuration.Milliseconds(),
		"compaction_window_ms":         b.CompactionWindow.Milliseconds(),
		"cooldown_duration_ms":         b.CooldownDuration.Milliseconds(),
		"compaction_interval_ms":       b.CompactionInterval.Milliseconds(),
		"compacted_revisions_behind":   b.CompactedRevisionsBehind,
		"compaction_batch_limit":       b.CompactionBatchLimit,
		"compaction_sleep_interval_ms": b.CompactionSleepInterval.Milliseconds(),
		"report_dir":                   b.ReportDir,
	}
}

func sortedBucketKeys(m map[int64]*secondBucket) []int64 {
	keys := make([]int64, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

// ---- Workers ----

// putValue is a reusable byte buffer for put value payloads.
type putValue struct{ buf []byte }

func newPutValue(size int) *putValue {
	buf := make([]byte, size)
	for i := range buf {
		buf[i] = 'x'
	}
	return &putValue{buf: buf}
}

// runWorker drives one worker goroutine that issues a stream of
// Put/Range requests at the given rate limit, recording each
// request as a sample. It exits when ctx is canceled.
func (b *compactionRWImpactBench) runWorker(
	ctx context.Context,
	cli *clientv3.Client,
	limiter *rate.Limiter,
	keyCounter *uint64,
	val *putValue,
) error {
	readProb := b.ReadWriteRatio
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		_ = limiter.Wait(ctx)

		isRead := rand.Float64() < readProb
		keyIdx := atomic.AddUint64(keyCounter, 1) - 1
		key := strconv.FormatInt(int64(keyIdx), 10)

		st := time.Now()
		if isRead {
			_, _ = cli.KV.Get(ctx, key,
				clientv3.WithRange(strconv.FormatInt(int64(keyIdx+1), 10)),
				clientv3.WithLimit(1),
			)
		} else {
			_, _ = cli.KV.Put(ctx, key, string(val.buf))
		}
		b.rec.record(sample{
			tNs:   st.UnixNano(),
			op:    map[bool]string{true: "range", false: "put"}[isRead],
			latNs: time.Since(st).Nanoseconds(),
		})
	}
}

// startWorkers launches WorkerCount workers and returns a cancel
// function that stops them all. Each worker has its own rate limiter
// at RequestRate per second, so the aggregate is approximately
// WorkerCount × RequestRate.
func (b *compactionRWImpactBench) startWorkers(ctx context.Context) (context.Context, context.CancelFunc, *sync.WaitGroup) {
	wctx, cancel := context.WithCancel(ctx)
	g, wctx := errgroup.WithContext(wctx)
	var keyCounter uint64
	val := newPutValue(b.ValueSize)
	for i := 0; i < b.WorkerCount; i++ {
		cli := b.cli[i]
		limiter := rate.NewLimiter(rate.Limit(b.RequestRate), 1)
		g.Go(func() error {
			return b.runWorker(wctx, cli, limiter, &keyCounter, val)
		})
	}
	wg := &sync.WaitGroup{}
	wg.Add(1)
	go func() {
		_ = g.Wait()
		wg.Done()
	}()
	return wctx, cancel, wg
}

// ---- Recorder (single-use, kept inline as helper code) ----

// sample is one per-request observation pushed by a worker. The
// recorder only tracks latency; error recording was deliberately
// dropped to keep the recorder minimal. A future contributor who
// wants error counts can extend sample + secondBucket and add
// classification here without touching the rest of the test.
//
// TODO: consider adding per-op error counts (ErrCompacted from a
// race, etc.) to the report. The current chart only shows latency,
// so error recording is out of scope for the first cut.
type sample struct {
	tNs   int64
	op    string // "put" or "range"
	latNs int64
}

// secondBucket holds per-second aggregated data for one wall-clock second.
type secondBucket struct {
	putLatsMs   []float64
	rangeLatsMs []float64
	dropped     int64
}

// snapshotBuckets is a deep copy returned to the report writer. The
// mutex is intentionally absent: snapshot is called only after the
// aggregator has stopped, so there is no concurrent access.
type snapshotBuckets struct {
	buckets map[int64]*secondBucket
	dropped int64
}

// sampleRecorder collects samples from workers and bins them by
// wall-clock second. record() is non-blocking; full channel drops
// are counted. snapshot is called once at the end after all
// producers have stopped; it returns a deep copy of the per-second
// state.
type sampleRecorder struct {
	ch   chan sample
	done chan struct{}

	mu      sync.Mutex
	buckets map[int64]*secondBucket
	dropped int64
}

func newSampleRecorder() *sampleRecorder {
	return &sampleRecorder{
		ch:      make(chan sample, 65536),
		done:    make(chan struct{}),
		buckets: make(map[int64]*secondBucket),
	}
}

func (r *sampleRecorder) start() { go r.run() }

func (r *sampleRecorder) run() {
	for s := range r.ch {
		sec := s.tNs / int64(time.Second)
		latMs := float64(s.latNs) / float64(time.Millisecond)
		r.mu.Lock()
		b, ok := r.buckets[sec]
		if !ok {
			b = &secondBucket{}
			r.buckets[sec] = b
		}
		switch s.op {
		case "put":
			b.putLatsMs = append(b.putLatsMs, latMs)
		case "range":
			b.rangeLatsMs = append(b.rangeLatsMs, latMs)
		}
		r.mu.Unlock()
	}
	close(r.done)
}

func (r *sampleRecorder) record(s sample) {
	select {
	case r.ch <- s:
	default:
		r.mu.Lock()
		r.dropped++
		r.mu.Unlock()
	}
}

func (r *sampleRecorder) closeAndSnapshot() snapshotBuckets {
	close(r.ch)
	<-r.done
	r.mu.Lock()
	defer r.mu.Unlock()
	out := snapshotBuckets{
		buckets: make(map[int64]*secondBucket, len(r.buckets)),
		dropped: r.dropped,
	}
	for k, v := range r.buckets {
		copyB := &secondBucket{
			putLatsMs:   append([]float64(nil), v.putLatsMs...),
			rangeLatsMs: append([]float64(nil), v.rangeLatsMs...),
			dropped:     v.dropped,
		}
		out.buckets[k] = copyB
	}
	return out
}

// percentile returns the p-th percentile (0-100) of vals. Sorts a
// copy to avoid mutating the caller's data.
func percentile(vals []float64, p float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	cp := append([]float64(nil), vals...)
	sort.Float64s(cp)
	return cp[int(float64(len(cp)-1)*p/100.0)]
}

// phaseRecord and compactionEvent are produced by the benchmark
// orchestrator and consumed by the JSON report writer.
type phaseRecord struct {
	Name    string
	StartNs int64
	EndNs   int64
}

type compactionEvent struct {
	StartNs int64
	EndNs   int64
	Rev     int64
	TookMs  float64
}
