// Copyright 2022 The etcd Authors
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

package traffic

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/time/rate"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/tests/v3/framework/e2e"
	"go.etcd.io/etcd/tests/v3/robustness/client"
	"go.etcd.io/etcd/tests/v3/robustness/identity"
	"go.etcd.io/etcd/tests/v3/robustness/model"
	"go.etcd.io/etcd/tests/v3/robustness/random"
	"go.etcd.io/etcd/tests/v3/robustness/report"
	"go.etcd.io/etcd/tests/v3/robustness/validate"
)

type Range struct {
	Min int64
	Max int64
}

func (r Range) Rand() int64 {
	if r.Min == r.Max {
		return r.Min
	}
	return random.RandRange(r.Min, r.Max+1)
}

var (
	DefaultLeaseTTL         int64 = 7200
	RequestTimeout                = 200 * time.Millisecond
	WatchTimeout                  = 500 * time.Millisecond
	MultiOpTxnOpCount             = 4
	MinimalCompactionPeriod       = 100 * time.Millisecond

	KeyValueVeryLow = KeyValue{
		MinimalQPS:                     50,
		MaximalQPS:                     100,
		BurstableQPS:                   100,
		MemberClientCount:              6,
		ClusterClientCount:             2,
		MaxNonUniqueRequestConcurrency: 3,
	}
	KeyValueMedium = KeyValue{
		MinimalQPS:                     100,
		MaximalQPS:                     200,
		BurstableQPS:                   1000,
		MemberClientCount:              6,
		ClusterClientCount:             2,
		MaxNonUniqueRequestConcurrency: 3,
	}
	KeyValueHigh = KeyValue{
		MinimalQPS:                     100,
		MaximalQPS:                     1000,
		BurstableQPS:                   1000,
		MemberClientCount:              6,
		ClusterClientCount:             2,
		MaxNonUniqueRequestConcurrency: 3,
	}
	WatchDefault = Watch{
		MemberClientCount:   6,
		ClusterClientCount:  2,
		RevisionOffsetRange: Range{Min: -100, Max: 100},
	}
	CompactionDefault = Compaction{
		Period: 200 * time.Millisecond,
	}
	CompactionFrequent = Compaction{
		Period: 100 * time.Millisecond,
	}
)

func SimulateTraffic(ctx context.Context, t *testing.T, lg *zap.Logger, clus *e2e.EtcdProcessCluster, profile Profile, traffic Traffic, failpointInjected <-chan report.FailpointInjection, clientSet *client.ClientSet) []report.ClientReport {
	endpoints := clus.EndpointsGRPC()

	lm := identity.NewLeaseIDStorage()
	// Use the highest MaximalQPS of all traffic profiles as burst otherwise actual traffic may be accidentally limited
	limiter := rate.NewLimiter(rate.Limit(profile.KeyValue.MaximalQPS), profile.KeyValue.BurstableQPS)

	err := CheckEmptyDatabaseAtStart(ctx, lg, endpoints, clientSet)
	require.NoError(t, err)

	wg := sync.WaitGroup{}
	nonUniqueWriteLimiter := NewConcurrencyLimiter(profile.KeyValue.MaxNonUniqueRequestConcurrency)
	finish := make(chan struct{})

	keyStore := NewKeyStore(10, "key")
	kubernetesStorage := NewKubernetesStorage()

	lg.Info("Start traffic")
	startTime := time.Since(clientSet.BaseTime())
	err = SimulateKeyValueTraffic(ctx, &wg, profile.KeyValue, endpoints, clientSet, traffic, RunTrafficLoopParam{
		QPSLimiter:                         limiter,
		IDs:                                clientSet.IdentityProvider(),
		LeaseIDStorage:                     lm,
		NonUniqueRequestConcurrencyLimiter: nonUniqueWriteLimiter,
		KeyStore:                           keyStore,
		Storage:                            kubernetesStorage,
		Finish:                             finish,
	})
	require.NoError(t, err)
	if profile.Watch != nil {
		err = SimulateWatchTraffic(ctx, &wg, profile.Watch, endpoints, clientSet, traffic, RunWatchLoopParam{
			Config:     *profile.Watch,
			QPSLimiter: limiter,
			KeyStore:   keyStore,
			Storage:    kubernetesStorage,
			Finish:     finish,
			Logger:     lg,
		})
		require.NoError(t, err)
	}
	if profile.Compaction != nil {
		if profile.Compaction.Period < MinimalCompactionPeriod {
			t.Fatalf("Compaction period %v below minimal %v", profile.Compaction.Period, MinimalCompactionPeriod)
		}
		err = SimulateCompactionTraffic(ctx, &wg, profile.Compaction, endpoints, clientSet, traffic, RunCompactLoopParam{
			Period: profile.Compaction.Period,
			Finish: finish,
		})
		require.NoError(t, err)
	}
	var fr *report.FailpointInjection
	select {
	case frp, ok := <-failpointInjected:
		require.Truef(t, ok, "Failed to collect failpoint report")
		fr = &frp
	case <-ctx.Done():
		t.Fatalf("Traffic finished before failure was injected: %s", ctx.Err())
	}
	close(finish)
	wg.Wait()
	lg.Info("Finished traffic")
	endTime := time.Since(clientSet.BaseTime())

	time.Sleep(time.Second)
	// Ensure that last operation succeeds
	cc, err := clientSet.NewClient(endpoints)
	require.NoError(t, err)
	defer cc.Close()
	_, err = cc.Put(ctx, "tombstone", "true")
	require.NoErrorf(t, err, "Last operation failed, validation requires last operation to succeed")
	reports := clientSet.Reports()

	totalStats := CalculateStats(reports, startTime, endTime)
	beforeFailpointStats := CalculateStats(reports, startTime, fr.Start)
	duringFailpointStats := CalculateStats(reports, fr.Start, fr.End)
	afterFailpointStats := CalculateStats(reports, fr.End, endTime)

	lg.Info("Reporting complete traffic", zap.Int("successes", totalStats.Successes), zap.Int("failures", totalStats.Failures), zap.Float64("successRate", totalStats.SuccessRate()), zap.Duration("period", totalStats.Period), zap.Float64("qps", totalStats.QPS()))
	lg.Info("Reporting traffic before failure injection", zap.Int("successes", beforeFailpointStats.Successes), zap.Int("failures", beforeFailpointStats.Failures), zap.Float64("successRate", beforeFailpointStats.SuccessRate()), zap.Duration("period", beforeFailpointStats.Period), zap.Float64("qps", beforeFailpointStats.QPS()))
	lg.Info("Reporting traffic during failure injection", zap.Int("successes", duringFailpointStats.Successes), zap.Int("failures", duringFailpointStats.Failures), zap.Float64("successRate", duringFailpointStats.SuccessRate()), zap.Duration("period", duringFailpointStats.Period), zap.Float64("qps", duringFailpointStats.QPS()))
	lg.Info("Reporting traffic after failure injection", zap.Int("successes", afterFailpointStats.Successes), zap.Int("failures", afterFailpointStats.Failures), zap.Float64("successRate", afterFailpointStats.SuccessRate()), zap.Duration("period", afterFailpointStats.Period), zap.Float64("qps", afterFailpointStats.QPS()))

	watchTotal := CalculateWatchStats(reports, startTime, endTime)
	lg.Info("Reporting complete watch", zap.Int("requests", watchTotal.Requests), zap.Int("events", watchTotal.Events), zap.Float64("eventsQPS", watchTotal.EventsQPS()), zap.Int("progressNotifies", watchTotal.ProgressNotifies), zap.Int("immediateClosures", watchTotal.ImmediateClosures), zap.Duration("period", watchTotal.Period), zap.Duration("avgDuration", watchTotal.AvgDuration()))

	if beforeFailpointStats.QPS() < profile.KeyValue.MinimalQPS {
		t.Errorf("Requiring minimal %f qps before failpoint injection for test results to be reliable, got %f qps", profile.KeyValue.MinimalQPS, beforeFailpointStats.QPS())
	}
	// TODO: Validate QPS post failpoint injection to ensure that we sufficiently cover the period when the cluster recovers.
	return reports
}

func SimulateKeyValueTraffic(ctx context.Context, wg *sync.WaitGroup, profile *KeyValue, endpoints []string, clientSet *client.ClientSet, tf Traffic, baseParam RunTrafficLoopParam) error {
	for i := range profile.MemberClientCount {
		c, err := clientSet.NewClient([]string{endpoints[i%len(endpoints)]})
		if err != nil {
			return err
		}
		wg.Add(1)
		go func(c *client.RecordingClient) {
			defer wg.Done()
			defer c.Close()

			tf.RunKeyValueLoop(ctx, c, baseParam)
		}(c)
	}
	for range profile.ClusterClientCount {
		c, err := clientSet.NewClient(endpoints)
		if err != nil {
			return err
		}
		wg.Add(1)
		go func(c *client.RecordingClient) {
			defer wg.Done()
			defer c.Close()

			tf.RunKeyValueLoop(ctx, c, baseParam)
		}(c)
	}
	return nil
}

func SimulateWatchTraffic(ctx context.Context, wg *sync.WaitGroup, profile *Watch, endpoints []string, clientSet *client.ClientSet, tf Traffic, baseParam RunWatchLoopParam) error {
	for i := range profile.MemberClientCount {
		c, err := clientSet.NewClient([]string{endpoints[i%len(endpoints)]})
		if err != nil {
			return err
		}
		wg.Add(1)
		go func(c *client.RecordingClient) {
			defer wg.Done()
			defer c.Close()
			tf.RunWatchLoop(ctx, c, baseParam)
		}(c)

		if profile.EndpointSwitchPeriod != nil {
			wg.Go(func() {
				runEndpointSwitchLoop(ctx, c, endpoints, *profile.EndpointSwitchPeriod, baseParam.Finish)
			})
		}
	}
	for range profile.ClusterClientCount {
		c, err := clientSet.NewClient(endpoints)
		if err != nil {
			return err
		}
		wg.Add(1)
		go func(c *client.RecordingClient) {
			defer wg.Done()
			defer c.Close()
			tf.RunWatchLoop(ctx, c, baseParam)
		}(c)

		if profile.EndpointSwitchPeriod != nil {
			wg.Go(func() {
				runEndpointSwitchLoop(ctx, c, endpoints, *profile.EndpointSwitchPeriod, baseParam.Finish)
			})
		}
	}
	return nil
}

func SimulateCompactionTraffic(ctx context.Context, wg *sync.WaitGroup, profile *Compaction, endpoints []string, clientSet *client.ClientSet, tf Traffic, baseParam RunCompactLoopParam) error {
	c, err := clientSet.NewClient(endpoints)
	if err != nil {
		return err
	}
	wg.Add(1)
	go func(c *client.RecordingClient) {
		defer wg.Done()
		defer c.Close()
		tf.RunCompactLoop(ctx, c, baseParam)
	}(c)
	return nil
}

func CalculateWatchStats(reports []report.ClientReport, start, end time.Duration) (ws watchStats) {
	ws.Period = end - start
	if ws.Period <= 0 {
		return ws
	}

	for _, r := range reports {
		for _, w := range r.Watch {
			var (
				firstInWindow       time.Duration
				lastInWindow        time.Duration
				haveInWindow        bool
				noEventsYetInWindow = true
				closedCounted       = false
			)

			for _, resp := range w.Responses {
				if resp.Time < start || resp.Time > end {
					continue
				}
				if !haveInWindow {
					firstInWindow = resp.Time
					haveInWindow = true
				}
				lastInWindow = resp.Time
				if resp.IsProgressNotify {
					ws.ProgressNotifies++
				}
				if len(resp.Events) > 0 {
					ws.Events += len(resp.Events)
					noEventsYetInWindow = false
				}
				if resp.Error != "" && noEventsYetInWindow && !closedCounted {
					ws.ImmediateClosures++
					closedCounted = true
				}
			}

			if haveInWindow {
				ws.Requests++
				if lastInWindow > firstInWindow {
					ws.SumDuration += lastInWindow - firstInWindow
					ws.DurationsCount++
				}
			}
		}
	}

	return ws
}

type watchStats struct {
	Period            time.Duration
	Requests          int
	Events            int
	ProgressNotifies  int
	ImmediateClosures int
	SumDuration       time.Duration
	DurationsCount    int
}

func (ws *watchStats) AvgDuration() time.Duration {
	if ws.DurationsCount == 0 {
		return 0
	}
	return ws.SumDuration / time.Duration(ws.DurationsCount)
}

func (ws *watchStats) EventsQPS() float64 {
	if ws.Period <= 0 {
		return 0
	}
	return float64(ws.Events) / ws.Period.Seconds()
}

func CalculateStats(reports []report.ClientReport, start, end time.Duration) (ts trafficStats) {
	ts.Period = end - start

	for _, r := range reports {
		for _, op := range r.KeyValue {
			if op.Call < start.Nanoseconds() || op.Call > end.Nanoseconds() {
				continue
			}
			resp := op.Output.(model.MaybeEtcdResponse)
			if resp.Error == "" {
				ts.Successes++
			} else {
				ts.Failures++
			}
		}
	}
	return ts
}

type trafficStats struct {
	Successes, Failures int
	Period              time.Duration
}

func (ts *trafficStats) SuccessRate() float64 {
	return float64(ts.Successes) / float64(ts.Successes+ts.Failures)
}

func (ts *trafficStats) QPS() float64 {
	return float64(ts.Successes) / ts.Period.Seconds()
}

type Profile struct {
	KeyValue   *KeyValue
	Watch      *Watch
	Compaction *Compaction
}

type KeyValue struct {
	MinimalQPS                     float64
	MaximalQPS                     float64
	BurstableQPS                   int
	MaxNonUniqueRequestConcurrency int
	MemberClientCount              int
	ClusterClientCount             int
}

type Watch struct {
	MemberClientCount    int
	ClusterClientCount   int
	RevisionOffsetRange  Range
	EndpointSwitchPeriod *time.Duration
}

type Compaction struct {
	Period time.Duration
}

type RunTrafficLoopParam struct {
	QPSLimiter                         *rate.Limiter
	IDs                                identity.Provider
	LeaseIDStorage                     identity.LeaseIDStorage
	NonUniqueRequestConcurrencyLimiter ConcurrencyLimiter
	KeyStore                           *keyStore
	Storage                            *storage
	Finish                             <-chan struct{}
}

type RunCompactLoopParam struct {
	Period time.Duration
	Finish <-chan struct{}
}

type RunWatchLoopParam struct {
	Config     Watch
	QPSLimiter *rate.Limiter
	// TODO: merge 2 key stores into 1
	KeyStore *keyStore
	Storage  *storage
	Finish   <-chan struct{}
	Logger   *zap.Logger
}

type Traffic interface {
	RunKeyValueLoop(ctx context.Context, c *client.RecordingClient, param RunTrafficLoopParam)
	RunWatchLoop(ctx context.Context, c *client.RecordingClient, param RunWatchLoopParam)
	RunCompactLoop(ctx context.Context, c *client.RecordingClient, param RunCompactLoopParam)
	ExpectUniqueRevision() bool
}

func runWatchLoop(ctx context.Context, c *client.RecordingClient, p RunWatchLoopParam, cfg watchLoopConfig) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.Finish:
			return
		default:
		}
		err := p.QPSLimiter.Wait(ctx)
		if err != nil {
			return
		}
		// Client.get may fail when the blackhole is injected.
		// We suppress logging for these expected failures to avoid polluting the logs.
		_ = runWatch(ctx, c, p, cfg)
	}
}

func runWatch(ctx context.Context, c *client.RecordingClient, p RunWatchLoopParam, cfg watchLoopConfig) error {
	getCtx, getCancel := context.WithTimeout(ctx, RequestTimeout)
	defer getCancel()

	resp, err := c.Get(getCtx, cfg.key)
	if err != nil {
		return err
	}
	rev := resp.Header.Revision + p.Config.RevisionOffsetRange.Rand()

	watchCtx, watchCancel := context.WithTimeout(ctx, WatchTimeout)
	defer watchCancel()

	if cfg.requireLeader {
		watchCtx = clientv3.WithRequireLeader(watchCtx)
	}
	w := c.Watch(watchCtx, cfg.key, rev, true, true, true)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-p.Finish:
			return nil
		case _, ok := <-w:
			if !ok {
				return nil
			}
		}
	}
}

type watchLoopConfig struct {
	key           string
	requireLeader bool
}

func CheckEmptyDatabaseAtStart(ctx context.Context, lg *zap.Logger, endpoints []string, cs *client.ClientSet) error {
	c, err := cs.NewClient(endpoints)
	if err != nil {
		return err
	}
	defer c.Close()
	for {
		rCtx, cancel := context.WithTimeout(ctx, RequestTimeout)
		resp, err := c.Get(rCtx, "key")
		cancel()
		if err != nil {
			lg.Warn("Failed to check if database empty at start, retrying", zap.Error(err))
			continue
		}
		if resp.Header.Revision != 1 {
			return validate.ErrNotEmptyDatabase
		}
		break
	}
	return nil
}

func runEndpointSwitchLoop(ctx context.Context, c *client.RecordingClient, endpoints []string, period time.Duration, finish <-chan struct{}) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-finish:
			return
		case <-time.After(period):
			c.SetEndpoints(endpoints...)
		}
	}
}
