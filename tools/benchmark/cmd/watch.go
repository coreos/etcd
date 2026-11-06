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

package cmd

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"sync/atomic"
	"time"

	"github.com/cheggaaa/pb/v3"
	"github.com/spf13/cobra"
	"golang.org/x/time/rate"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/pkg/v3/report"
)

// watchCmd represents the watch command
var watchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Benchmark watch",
	Long: `Benchmark watch tests the performance of processing watch requests and
sending events to watchers. It tests the sending performance by
changing the value of the watched keys with concurrent put
requests.

During the test, each watcher watches (--total/--watchers) keys

(a watcher might watch on the same key multiple times if
--watched-key-total is small).

Each key is watched by (--total/--watched-key-total) watchers.
`,
	Run: watchFunc,
}

var (
	watchStreams          int
	watchWatchesPerStream int
	watchedKeyTotal       int

	watchPutRate  int
	watchPutTotal int

	watchKeySize      int
	watchKeySpaceSize int
	watchSeqKeys      bool
)

type watchedKeys struct {
	watched     []string
	numWatchers map[string]int

	watches []clientv3.WatchChan

	// ctx to control all watches
	ctx    context.Context
	cancel context.CancelFunc
}

func init() {
	RootCmd.AddCommand(watchCmd)
	watchCmd.Flags().IntVar(&watchStreams, "streams", 10, "Total watch streams")
	watchCmd.Flags().IntVar(&watchWatchesPerStream, "watch-per-stream", 100, "Total watchers per stream")
	watchCmd.Flags().IntVar(&watchedKeyTotal, "watched-key-total", 1, "Total number of keys to be watched")

	watchCmd.Flags().IntVar(&watchPutRate, "put-rate", 0, "Number of keys to put per second")
	watchCmd.Flags().IntVar(&watchPutTotal, "put-total", 1000, "Number of put requests")

	watchCmd.Flags().IntVar(&watchKeySize, "key-size", 32, "Key size of watch request")
	watchCmd.Flags().IntVar(&watchKeySpaceSize, "key-space-size", 1, "Maximum possible keys")
	watchCmd.Flags().BoolVar(&watchSeqKeys, "sequential-keys", false, "Use sequential keys")
}

func watchFunc(_ *cobra.Command, _ []string) {
	if err := validateWatchFlags(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	grpcConns := int(totalClients)
	if totalClients > totalConns {
		grpcConns = int(totalConns)
	}
	wantedConns := 1 + (watchStreams / 100)
	if grpcConns < wantedConns {
		fmt.Fprintf(os.Stderr, "warning: grpc limits 100 streams per client connection, have %d but need %d\n", grpcConns, wantedConns)
	}
	clients := mustCreateClients(totalClients, totalConns)
	wk := newWatchedKeys()
	benchMakeWatches(clients, wk)
	benchPutWatches(clients, wk)
}

func benchMakeWatches(clients []*clientv3.Client, wk *watchedKeys) {
	streams := make([]clientv3.Watcher, watchStreams)
	for i := range streams {
		streams[i] = clientv3.NewWatcher(clients[i%len(clients)])
	}

	keyc := make(chan string, watchStreams)
	bar = pb.New(watchStreams * watchWatchesPerStream)
	bar.Start()

	r := newReport("watch-make")
	rch := r.Results()

	wg.Add(len(streams) + 1)
	wc := make(chan []clientv3.WatchChan, len(streams))
	for _, s := range streams {
		go func(s clientv3.Watcher) {
			defer wg.Done()
			var ws []clientv3.WatchChan
			for i := 0; i < watchWatchesPerStream; i++ {
				k := <-keyc
				st := time.Now()
				wch := s.Watch(wk.ctx, k)
				rch <- report.Result{Start: st, End: time.Now()}
				ws = append(ws, wch)
				bar.Increment()
			}
			wc <- ws
		}(s)
	}
	go func() {
		defer func() {
			close(keyc)
			wg.Done()
		}()
		for i := 0; i < watchStreams*watchWatchesPerStream; i++ {
			key := wk.watched[i%len(wk.watched)]
			keyc <- key
			wk.numWatchers[key]++
		}
	}()

	rc := r.Run()
	wg.Wait()
	bar.Finish()
	close(r.Results())
	fmt.Printf("Watch creation summary:\n%s", <-rc)

	for i := 0; i < len(streams); i++ {
		wk.watches = append(wk.watches, (<-wc)...)
	}
}

// validateWatchFlags checks that the watch command's cardinality flags are
// positive. Non-positive values lead to a divide-by-zero panic in
// benchMakeWatches, or to a no-op benchmark that silently reports success.
func validateWatchFlags() error {
	if watchStreams <= 0 {
		return fmt.Errorf("expected positive --streams, got (%v)", watchStreams)
	}
	if watchWatchesPerStream <= 0 {
		return fmt.Errorf("expected positive --watch-per-stream, got (%v)", watchWatchesPerStream)
	}
	if watchedKeyTotal <= 0 {
		return fmt.Errorf("expected positive --watched-key-total, got (%v)", watchedKeyTotal)
	}
	if watchKeySpaceSize <= 0 {
		return fmt.Errorf("expected positive --key-space-size, got (%v)", watchKeySpaceSize)
	}
	return nil
}

func newWatchedKeys() *watchedKeys {
	watched := make([]string, watchedKeyTotal)
	for i := range watched {
		k := make([]byte, watchKeySize)
		if watchSeqKeys {
			binary.PutVarint(k, int64(i%watchKeySpaceSize))
		} else {
			binary.PutVarint(k, int64(rand.Intn(watchKeySpaceSize)))
		}
		watched[i] = string(k)
	}
	ctx, cancel := context.WithCancel(context.TODO())
	return &watchedKeys{
		watched:     watched,
		numWatchers: make(map[string]int),
		ctx:         ctx,
		cancel:      cancel,
	}
}

func benchPutWatches(clients []*clientv3.Client, wk *watchedKeys) {
	eventsTotal := 0
	for i := 0; i < watchPutTotal; i++ {
		eventsTotal += wk.numWatchers[wk.watched[i%len(wk.watched)]]
	}

	bar = pb.New(eventsTotal)
	bar.Start()

	r := newReport("watch-put")

	timeline := newPutTimeline(watchPutTotal)

	wg.Add(len(wk.watches))
	nrRxed := int32(eventsTotal)
	for _, w := range wk.watches {
		go func(wc clientv3.WatchChan) {
			defer wg.Done()
			recvWatchChan(wc, r.Results(), &nrRxed, timeline)
			wk.cancel()
		}(w)
	}

	putreqc := make(chan watchPutReq, len(clients))
	go func() {
		defer close(putreqc)
		for i := 0; i < watchPutTotal; i++ {
			key := wk.watched[i%(len(wk.watched))]
			putreqc <- watchPutReq{seq: i, op: clientv3.OpPut(key, encodePutSeq(i))}
		}
	}()

	watchPutLimit := rate.Inf
	if watchPutRate > 0 {
		watchPutLimit = rate.Limit(watchPutRate)
	}

	limit := rate.NewLimiter(watchPutLimit, 1)
	for _, cc := range clients {
		go func(c *clientv3.Client) {
			for req := range putreqc {
				if err := limit.Wait(context.TODO()); err != nil {
					panic(err)
				}
				// Record the issue time before the put is sent: an event can
				// reach a watcher before Do() returns here.
				timeline.markIssued(req.seq)
				if _, err := c.Do(context.TODO(), req.op); err != nil {
					panic(err)
				}
			}
		}(cc)
	}

	rc := r.Run()
	wg.Wait()
	bar.Finish()
	close(r.Results())
	fmt.Printf("Watch events received summary:\n%s", <-rc)
	if n := timeline.unattributed.Load(); n > 0 {
		fmt.Fprintf(os.Stderr, "warning: %d watch events could not be attributed to a put and are excluded from the summary\n", n)
	}
}

// watchPutReq pairs a put with its sequence number so the goroutine issuing the
// put can record when it did so.
type watchPutReq struct {
	op  clientv3.Op
	seq int
}

// putTimeline records when each put was issued, so a watch event is timed from its put.
// Times are offsets from base, which keeps the monotonic clock reading for base.Add.
type putTimeline struct {
	base         time.Time
	issuedAt     []atomic.Int64 // offset from base, or notIssued
	unattributed atomic.Int32
}

const notIssued = -1

func newPutTimeline(puts int) *putTimeline {
	t := &putTimeline{
		base:     time.Now(),
		issuedAt: make([]atomic.Int64, puts),
	}
	for i := range t.issuedAt {
		t.issuedAt[i].Store(notIssued)
	}
	return t
}

func (t *putTimeline) markIssued(seq int) {
	t.issuedAt[seq].Store(int64(time.Since(t.base)))
}

// issued reports when the put with sequence number seq was issued, or false if
// seq is out of range or that put has not been issued yet.
func (t *putTimeline) issued(seq int) (time.Time, bool) {
	if seq < 0 || seq >= len(t.issuedAt) {
		return time.Time{}, false
	}
	offset := t.issuedAt[seq].Load()
	if offset == notIssued {
		return time.Time{}, false
	}
	return t.base.Add(time.Duration(offset)), true
}

// encodePutSeq and decodePutSeq store a put's sequence number in its value, because
// with many watchers and concurrent putters per key, events can't be matched by position.
func encodePutSeq(seq int) string {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(seq))
	return string(b[:])
}

func decodePutSeq(value []byte) (int, bool) {
	if len(value) != 8 {
		return 0, false
	}
	return int(binary.BigEndian.Uint64(value)), true
}

func recvWatchChan(wch clientv3.WatchChan, results chan<- report.Result, nrRxed *int32, timeline *putTimeline) {
	for r := range wch {
		for _, ev := range r.Events {
			now := time.Now()
			// Time from the put, so the result doesn't depend on how many events
			// the server batched into this response.
			if seq, ok := decodePutSeq(ev.Kv.Value); !ok {
				timeline.unattributed.Add(1)
			} else if st, ok := timeline.issued(seq); !ok {
				timeline.unattributed.Add(1)
			} else {
				results <- report.Result{Start: st, End: now}
			}
			bar.Increment()
			if atomic.AddInt32(nrRxed, -1) <= 0 {
				return
			}
		}
	}
}
