// Copyright 2023 The etcd Authors
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

// These tests are performance sensitive, addition of cluster proxy makes them unstable.
//go:build !cluster_proxy

package e2e

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"
	v3rpc "go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/tests/v3/framework/e2e"
)

const (
	watchResponsePeriod = 100 * time.Millisecond
	watchTestDuration   = 5 * time.Second
	readLoadConcurrency = 10
)

type testCase struct {
	name               string
	client             e2e.ClientConfig
	clientHTTPSeparate bool
	maxWatchDelay      time.Duration
	dbSizeBytes        int
}

const (
	Kilo = 1000
	Mega = 1000 * Kilo
)

// 10 MB is not a bottleneck of grpc server, but filling up etcd with data.
// Keeping it lower so tests don't take too long.
// If we implement reuse of db we could increase the dbSize.
var tcs = []testCase{
	{
		name:          "NoTLS",
		maxWatchDelay: 150 * time.Millisecond,
		dbSizeBytes:   5 * Mega,
	},
	{
		name:          "TLS",
		client:        e2e.ClientConfig{ConnectionType: e2e.ClientTLS},
		maxWatchDelay: 150 * time.Millisecond,
		dbSizeBytes:   5 * Mega,
	},
	{
		name:               "SeparateHTTPNoTLS",
		clientHTTPSeparate: true,
		maxWatchDelay:      150 * time.Millisecond,
		dbSizeBytes:        5 * Mega,
	},
	{
		name:               "SeparateHTTPTLS",
		client:             e2e.ClientConfig{ConnectionType: e2e.ClientTLS},
		clientHTTPSeparate: true,
		maxWatchDelay:      150 * time.Millisecond,
		dbSizeBytes:        5 * Mega,
	},
}

func TestWatchDelayForPeriodicProgressNotification(t *testing.T) {
	e2e.BeforeTest(t)
	for _, tc := range tcs {
		tc := tc
		cfg := e2e.DefaultConfig()
		cfg.ClusterSize = 1
		cfg.ServerConfig.WatchProgressNotifyInterval = watchResponsePeriod
		cfg.Client = tc.client
		cfg.ClientHTTPSeparate = tc.clientHTTPSeparate
		t.Run(tc.name, func(t *testing.T) {
			clus, err := e2e.NewEtcdProcessCluster(t.Context(), t, e2e.WithConfig(cfg))
			require.NoError(t, err)
			defer clus.Close()
			c := newClient(t, clus.EndpointsGRPC(), tc.client)
			require.NoError(t, fillEtcdWithData(t.Context(), c, tc.dbSizeBytes))

			ctx, cancel := context.WithTimeout(t.Context(), watchTestDuration)
			defer cancel()
			g := errgroup.Group{}
			continuouslyExecuteGetAll(ctx, t, &g, c)
			validateWatchDelay(t, c.Watch(ctx, "fake-key", clientv3.WithProgressNotify()), tc.maxWatchDelay)
			require.NoError(t, g.Wait())
		})
	}
}

func TestWatchDelayForManualProgressNotification(t *testing.T) {
	e2e.BeforeTest(t)
	for _, tc := range tcs {
		tc := tc
		cfg := e2e.DefaultConfig()
		cfg.ClusterSize = 1
		cfg.Client = tc.client
		cfg.ClientHTTPSeparate = tc.clientHTTPSeparate
		t.Run(tc.name, func(t *testing.T) {
			clus, err := e2e.NewEtcdProcessCluster(t.Context(), t, e2e.WithConfig(cfg))
			require.NoError(t, err)
			defer clus.Close()
			c := newClient(t, clus.EndpointsGRPC(), tc.client)
			require.NoError(t, fillEtcdWithData(t.Context(), c, tc.dbSizeBytes))

			ctx, cancel := context.WithTimeout(t.Context(), watchTestDuration)
			defer cancel()
			g := errgroup.Group{}
			continuouslyExecuteGetAll(ctx, t, &g, c)
			g.Go(func() error {
				for {
					err := c.RequestProgress(ctx)
					if err != nil {
						if strings.Contains(err.Error(), "context deadline exceeded") {
							return nil
						}
						return err
					}
					time.Sleep(watchResponsePeriod)
				}
			})
			validateWatchDelay(t, c.Watch(ctx, "fake-key"), tc.maxWatchDelay)
			require.NoError(t, g.Wait())
		})
	}
}

func TestWatchDelayForEvent(t *testing.T) {
	e2e.BeforeTest(t)
	for _, tc := range tcs {
		tc := tc
		cfg := e2e.DefaultConfig()
		cfg.ClusterSize = 1
		cfg.Client = tc.client
		cfg.ClientHTTPSeparate = tc.clientHTTPSeparate
		t.Run(tc.name, func(t *testing.T) {
			clus, err := e2e.NewEtcdProcessCluster(t.Context(), t, e2e.WithConfig(cfg))
			require.NoError(t, err)
			defer clus.Close()
			c := newClient(t, clus.EndpointsGRPC(), tc.client)
			require.NoError(t, fillEtcdWithData(t.Context(), c, tc.dbSizeBytes))

			ctx, cancel := context.WithTimeout(t.Context(), watchTestDuration)
			defer cancel()
			g := errgroup.Group{}
			g.Go(func() error {
				i := 0
				for {
					_, err := c.Put(ctx, "key", fmt.Sprintf("%d", i))
					if err != nil {
						if strings.Contains(err.Error(), "context deadline exceeded") {
							return nil
						}
						return err
					}
					time.Sleep(watchResponsePeriod)
				}
			})
			continuouslyExecuteGetAll(ctx, t, &g, c)
			validateWatchDelay(t, c.Watch(ctx, "key"), tc.maxWatchDelay)
			require.NoError(t, g.Wait())
		})
	}
}

func validateWatchDelay(t *testing.T, watch clientv3.WatchChan, maxWatchDelay time.Duration) {
	start := time.Now()
	var maxDelay time.Duration
	for range watch {
		sinceLast := time.Since(start)
		if sinceLast > watchResponsePeriod+maxWatchDelay {
			t.Errorf("Unexpected watch response delayed over allowed threshold %s, delay: %s", maxWatchDelay, sinceLast-watchResponsePeriod)
		} else {
			t.Logf("Got watch response, since last: %s", sinceLast)
		}
		if sinceLast > maxDelay {
			maxDelay = sinceLast
		}
		start = time.Now()
	}
	sinceLast := time.Since(start)
	if sinceLast > maxDelay && sinceLast > watchResponsePeriod+maxWatchDelay {
		t.Errorf("Unexpected watch response delayed over allowed threshold %s, delay: unknown", maxWatchDelay)
		t.Errorf("Test finished while in middle of delayed response, measured delay: %s", sinceLast-watchResponsePeriod)
		t.Logf("Please increase the test duration to measure delay")
	} else {
		t.Logf("Max delay: %s", maxDelay-watchResponsePeriod)
	}
}

func continuouslyExecuteGetAll(ctx context.Context, t *testing.T, g *errgroup.Group, c *clientv3.Client) {
	mux := sync.RWMutex{}
	size := 0
	for i := 0; i < readLoadConcurrency; i++ {
		g.Go(func() error {
			for {
				resp, err := c.Get(ctx, "", clientv3.WithPrefix())
				if err != nil {
					if strings.Contains(err.Error(), "context deadline exceeded") {
						return nil
					}
					return err
				}
				respSize := 0
				for _, kv := range resp.Kvs {
					respSize += proto.Size(kv)
				}
				mux.Lock()
				size += respSize
				mux.Unlock()
			}
		})
	}
	g.Go(func() error {
		lastSize := size
		for range time.Tick(time.Second) {
			select {
			case <-ctx.Done():
				return nil
			default:
			}
			mux.RLock()
			t.Logf("Generating read load around %.1f MB/s", float64(size-lastSize)/1000/1000)
			lastSize = size
			mux.RUnlock()
		}
		return nil
	})
}

// TestDeleteEventDrop_Issue18089 is an e2e test to reproduce the issue reported in: https://github.com/etcd-io/etcd/issues/18089
//
// The goal is to reproduce a DELETE event being dropped in a watch after a compaction
// occurs on the revision where the deletion took place. In order to reproduce this, we
// perform the following sequence (steps for reproduction thanks to @ahrtr):
//   - PUT k v2 (assume returned revision = r2)
//   - PUT k v3 (assume returned revision = r3)
//   - PUT k v4 (assume returned revision = r4)
//   - DELETE k (assume returned revision = r5)
//   - PUT k v6 (assume returned revision = r6)
//   - COMPACT r5
//   - WATCH rev=r5
//
// We should get the DELETE event (r5) followed by the PUT event (r6).
func TestDeleteEventDrop_Issue18089(t *testing.T) {
	e2e.BeforeTest(t)
	cfg := e2e.DefaultConfig()
	cfg.ClusterSize = 1
	cfg.Client = e2e.ClientConfig{ConnectionType: e2e.ClientTLS}
	clus, err := e2e.NewEtcdProcessCluster(t.Context(), t, e2e.WithConfig(cfg))
	require.NoError(t, err)
	defer clus.Close()

	c := newClient(t, clus.EndpointsGRPC(), cfg.Client)
	defer c.Close()

	ctx := t.Context()
	const (
		key = "k"
		v2  = "v2"
		v3  = "v3"
		v4  = "v4"
		v6  = "v6"
	)

	t.Logf("PUT key=%s, val=%s", key, v2)
	_, err = c.KV.Put(ctx, key, v2)
	require.NoError(t, err)

	t.Logf("PUT key=%s, val=%s", key, v3)
	_, err = c.KV.Put(ctx, key, v3)
	require.NoError(t, err)

	t.Logf("PUT key=%s, val=%s", key, v4)
	_, err = c.KV.Put(ctx, key, v4)
	require.NoError(t, err)

	t.Logf("DELTE key=%s", key)
	deleteResp, err := c.KV.Delete(ctx, key)
	require.NoError(t, err)

	t.Logf("PUT key=%s, val=%s", key, v6)
	_, err = c.KV.Put(ctx, key, v6)
	require.NoError(t, err)

	t.Logf("COMPACT rev=%d", deleteResp.Header.Revision)
	_, err = c.KV.Compact(ctx, deleteResp.Header.Revision, clientv3.WithCompactPhysical())
	require.NoError(t, err)

	watchChan := c.Watch(ctx, key, clientv3.WithRev(deleteResp.Header.Revision))
	select {
	case watchResp := <-watchChan:
		require.Len(t, watchResp.Events, 2)

		require.Equal(t, mvccpb.Event_DELETE, watchResp.Events[0].Type)
		deletedKey := string(watchResp.Events[0].Kv.Key)
		require.Equal(t, key, deletedKey)

		require.Equal(t, mvccpb.Event_PUT, watchResp.Events[1].Type)

		updatedKey := string(watchResp.Events[1].Kv.Key)
		require.Equal(t, key, updatedKey)

		require.Equal(t, v6, string(watchResp.Events[1].Kv.Value))
	case <-time.After(100 * time.Millisecond):
		// we care only about the first response, but have an
		// escape hatch in case the watch response is delayed.
		t.Fatal("timed out getting watch response")
	}
}

func TestStartWatcherFromCompactedRevision(t *testing.T) {
	t.Run("compaction on tombstone revision", func(t *testing.T) {
		testStartWatcherFromCompactedRevision(t, true)
	})
	t.Run("compaction on normal revision", func(t *testing.T) {
		testStartWatcherFromCompactedRevision(t, false)
	})
}

func testStartWatcherFromCompactedRevision(t *testing.T, performCompactOnTombstone bool) {
	e2e.BeforeTest(t)
	cfg := e2e.DefaultConfig()
	cfg.Client = e2e.ClientConfig{ConnectionType: e2e.ClientTLS}
	clus, err := e2e.NewEtcdProcessCluster(t.Context(), t, e2e.WithConfig(cfg), e2e.WithClusterSize(1))
	require.NoError(t, err)
	defer clus.Close()

	c := newClient(t, clus.EndpointsGRPC(), cfg.Client)
	defer c.Close()

	ctx := t.Context()
	key := "foo"
	totalRev := 100

	type valueEvent struct {
		value string
		typ   mvccpb.Event_EventType
	}

	var (
		// requestedValues records all requested change
		requestedValues = make([]valueEvent, 0)
		// revisionChan sends each compacted revision via this channel
		compactionRevChan = make(chan int64)
		// compactionStep means that client performs a compaction on every 7 operations
		compactionStep = 7
	)

	// This goroutine will submit changes on $key $totalRev times. It will
	// perform compaction after every $compactedAfterChanges changes.
	// Except for first time, the watcher always receives the compacted
	// revision as start.
	go func() {
		defer close(compactionRevChan)

		lastRevision := int64(1)

		compactionRevChan <- lastRevision
		for vi := 1; vi <= totalRev; vi++ {
			var respHeader *etcdserverpb.ResponseHeader

			if vi%compactionStep == 0 && performCompactOnTombstone {
				t.Logf("DELETE key=%s", key)

				resp, derr := c.KV.Delete(ctx, key)
				assert.NoError(t, derr)
				respHeader = resp.Header

				requestedValues = append(requestedValues, valueEvent{value: "", typ: mvccpb.Event_DELETE})
			} else {
				value := fmt.Sprintf("%d", vi)

				t.Logf("PUT key=%s, val=%s", key, value)
				resp, perr := c.KV.Put(ctx, key, value)
				assert.NoError(t, perr)
				respHeader = resp.Header

				requestedValues = append(requestedValues, valueEvent{value: value, typ: mvccpb.Event_PUT})
			}

			lastRevision = respHeader.Revision

			if vi%compactionStep == 0 {
				compactionRevChan <- lastRevision

				t.Logf("COMPACT rev=%d", lastRevision)
				_, err = c.KV.Compact(ctx, lastRevision, clientv3.WithCompactPhysical())
				assert.NoError(t, err)
			}
		}
	}()

	receivedEvents := make([]*clientv3.Event, 0)

	fromCompactedRev := false
	for fromRev := range compactionRevChan {
		watchChan := c.Watch(ctx, key, clientv3.WithRev(fromRev))

		prevEventCount := len(receivedEvents)

		// firstReceived represents this is first watch response.
		// Just in case that ETCD sends event one by one.
		firstReceived := true

		t.Logf("Start to watch key %s starting from revision %d", key, fromRev)
	watchLoop:
		for {
			currentEventCount := len(receivedEvents)
			if currentEventCount-prevEventCount == compactionStep || currentEventCount == totalRev {
				break
			}

			select {
			case watchResp := <-watchChan:
				t.Logf("Receive the number of events: %d", len(watchResp.Events))
				for i := range watchResp.Events {
					ev := watchResp.Events[i]

					// If the $fromRev is the compacted revision,
					// the first event should be the same as the last event receives in last watch response.
					if firstReceived && fromCompactedRev {
						firstReceived = false

						last := receivedEvents[prevEventCount-1]

						assert.Equalf(t, last.Type, ev.Type,
							"last received event type %s, but got event type %s", last.Type, ev.Type)
						assert.Equalf(t, string(last.Kv.Key), string(ev.Kv.Key),
							"last received event key %s, but got event key %s", string(last.Kv.Key), string(ev.Kv.Key))
						assert.Equalf(t, string(last.Kv.Value), string(ev.Kv.Value),
							"last received event value %s, but got event value %s", string(last.Kv.Value), string(ev.Kv.Value))
						continue
					}
					receivedEvents = append(receivedEvents, ev)
				}

				if len(watchResp.Events) == 0 {
					require.Equal(t, v3rpc.ErrCompacted, watchResp.Err())
					break watchLoop
				}

			case <-time.After(10 * time.Second):
				t.Fatal("timed out getting watch response")
			}
		}

		fromCompactedRev = true
	}

	t.Logf("Received total number of events: %d", len(receivedEvents))
	require.Len(t, requestedValues, totalRev)
	require.Lenf(t, receivedEvents, totalRev, "should receive %d events", totalRev)
	for idx, expected := range requestedValues {
		ev := receivedEvents[idx]

		require.Equalf(t, expected.typ, ev.Type, "#%d expected event %s", idx, expected.typ)

		updatedKey := string(ev.Kv.Key)

		require.Equal(t, key, updatedKey)
		if expected.typ == mvccpb.Event_PUT {
			updatedValue := string(ev.Kv.Value)
			require.Equal(t, expected.value, updatedValue)
		}
	}
}

// TestResumeCompactionOnTombstone verifies whether a deletion event is preserved
// when etcd restarts and resumes compaction on a key that only has a tombstone revision.
func TestResumeCompactionOnTombstone(t *testing.T) {
	e2e.BeforeTest(t)

	ctx := t.Context()
	compactBatchLimit := 5

	cfg := e2e.DefaultConfig()
	clus, err := e2e.NewEtcdProcessCluster(t.Context(),
		t,
		e2e.WithConfig(cfg),
		e2e.WithClusterSize(1),
		e2e.WithCompactionBatchLimit(compactBatchLimit),
		e2e.WithGoFailEnabled(true),
		e2e.WithWatchProcessNotifyInterval(100*time.Millisecond),
	)
	require.NoError(t, err)
	defer clus.Close()

	c1 := newClient(t, clus.EndpointsGRPC(), cfg.Client)
	defer c1.Close()

	keyPrefix := "/key-"
	for i := 0; i < compactBatchLimit; i++ {
		key := fmt.Sprintf("%s%d", keyPrefix, i)
		value := fmt.Sprintf("%d", i)

		t.Logf("PUT key=%s, val=%s", key, value)
		_, err = c1.KV.Put(ctx, key, value)
		require.NoError(t, err)
	}

	firstKey := keyPrefix + "0"
	t.Logf("DELETE key=%s", firstKey)
	deleteResp, err := c1.KV.Delete(ctx, firstKey)
	require.NoError(t, err)

	var deleteEvent *clientv3.Event
	select {
	case watchResp := <-c1.Watch(ctx, firstKey, clientv3.WithRev(deleteResp.Header.Revision)):
		require.Len(t, watchResp.Events, 1)

		require.Equal(t, mvccpb.Event_DELETE, watchResp.Events[0].Type)
		deletedKey := string(watchResp.Events[0].Kv.Key)
		require.Equal(t, firstKey, deletedKey)

		deleteEvent = watchResp.Events[0]
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out getting watch response")
	}

	require.NoError(t, clus.Procs[0].Failpoints().SetupHTTP(ctx, "compactBeforeSetFinishedCompact", `panic`))

	t.Logf("COMPACT rev=%d", deleteResp.Header.Revision)
	_, err = c1.KV.Compact(ctx, deleteResp.Header.Revision, clientv3.WithCompactPhysical())
	require.Error(t, err)

	require.NoError(t, clus.Restart(ctx))

	c2 := newClient(t, clus.EndpointsGRPC(), cfg.Client)
	defer c2.Close()

	watchChan := c2.Watch(ctx, firstKey, clientv3.WithRev(deleteResp.Header.Revision))
	select {
	case watchResp := <-watchChan:
		require.Equal(t, []*clientv3.Event{deleteEvent}, watchResp.Events)
	case <-time.After(100 * time.Millisecond):
		// we care only about the first response, but have an
		// escape hatch in case the watch response is delayed.
		t.Fatal("timed out getting watch response")
	}
}
