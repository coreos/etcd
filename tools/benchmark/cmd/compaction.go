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

package cmd

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"sync"
	"time"

	"github.com/cheggaaa/pb/v3"
	"github.com/spf13/cobra"
	"golang.org/x/time/rate"

	v3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/pkg/v3/report"
)

// Example (run from the repository root; etcd auto-compaction is disabled by default):
//
//     make build
//     go build -o ./bin/benchmark ./tools/benchmark
//     PATH="$PWD/bin:$PATH" ./scripts/benchmark_test.sh compaction \
//       --clients=16 \
//       --conns=4 \
//       --rate=1000 \
//       --total=60000 \
//       --read-ratio=0.8 \
//       --compact-interval=10s \
//       --compact-index-delta=1000
//
// Use --enable-compaction=false for a no-compaction baseline.
// compactionCmd represents the compaction command
var compactionCmd = &cobra.Command{
	Use:   "compaction",
	Short: "Benchmark the impact of compaction on a running read/write workload",

	Run: compactionFunc,
}

var (
	compactionKeySize      int
	compactionValSize      int
	compactionRate         int
	compactionTotal        int
	compactionKeySpaceSize int
	compactionReadRatio    float64
	compactionEnabled      bool
	compactionInterval     time.Duration
	compactionRevDelta     int64
)

func init() {
	RootCmd.AddCommand(compactionCmd)
	compactionCmd.Flags().IntVar(&compactionKeySize, "key-size", 8, "Key size of request")
	compactionCmd.Flags().IntVar(&compactionValSize, "val-size", 256, "Value size of put request")
	compactionCmd.Flags().IntVar(&compactionRate, "rate", 1000, "Maximum requests per second (0 is no limit)")
	compactionCmd.Flags().IntVar(&compactionTotal, "total", 60000, "Total number of read/write requests")
	compactionCmd.Flags().IntVar(&compactionKeySpaceSize, "key-space-size", 5000, "Maximum possible keys")
	compactionCmd.Flags().Float64Var(&compactionReadRatio, "read-ratio", 0.5, "Ratio of RANGE requests in the workload, remainder are PUT requests (0.0 - 1.0)")
	compactionCmd.Flags().BoolVar(&compactionEnabled, "enable-compaction", true, "Whether to issue compactions during the workload; disable to measure a no-compaction baseline of the same mixed PUT/RANGE load")
	compactionCmd.Flags().DurationVar(&compactionInterval, "compact-interval", 10*time.Second, `Interval between compactions issued during the workload (do not duplicate this with etcd's 'auto-compaction-retention' flag)`)
	compactionCmd.Flags().Int64Var(&compactionRevDelta, "compact-index-delta", 1000, "Delta between current revision and compact revision (e.g. current revision 10000, compact at 9000)")
}

func compactionFunc(_ *cobra.Command, _ []string) {
	if compactionKeySpaceSize <= 0 {
		fmt.Fprintf(os.Stderr, "expected positive --key-space-size, got (%v)\n", compactionKeySpaceSize)
		os.Exit(1)
	}
	if compactionReadRatio < 0 || compactionReadRatio > 1 {
		fmt.Fprintf(os.Stderr, "expected --read-ratio between 0.0 and 1.0, got (%v)\n", compactionReadRatio)
		os.Exit(1)
	}
	if compactionEnabled && compactionInterval <= 0 {
		fmt.Fprintf(os.Stderr, "expected positive --compact-interval, got (%v)\n", compactionInterval)
		os.Exit(1)
	}

	requests := make(chan v3.Op, totalClients)
	limit := rate.NewLimiter(rate.Limit(compactionRate), 1)
	clients := mustCreateClients(totalClients, totalConns)
	k, v := make([]byte, compactionKeySize), string(mustRandBytes(compactionValSize))

	bar = pb.New(compactionTotal)
	bar.Start()

	putName, rangeName := "put", "range"
	putLabel, rangeLabel := "PUT", "RANGE"
	if compactionEnabled {
		putName, rangeName = "putDuringCompaction", "rangeDuringCompaction"
		putLabel, rangeLabel = "PUT during compaction", "RANGE during compaction"
	}
	putReport := newReport(putName)
	rangeReport := newReport(rangeName)
	compactReport := newReport("compaction")

	for i := range clients {
		wg.Add(1)
		go func(c *v3.Client) {
			defer wg.Done()
			for op := range requests {
				limit.Wait(context.Background())

				st := time.Now()
				_, err := c.Do(context.Background(), op)
				res := report.Result{Err: err, Start: st, End: time.Now()}
				if op.IsGet() {
					rangeReport.Results() <- res
				} else {
					putReport.Results() <- res
				}
				bar.Increment()
			}
		}(clients[i])
	}

	go func() {
		for i := 0; i < compactionTotal; i++ {
			binary.PutVarint(k, int64(rand.Intn(compactionKeySpaceSize)))
			if rand.Float64() < compactionReadRatio {
				requests <- v3.OpGet(string(k))
			} else {
				requests <- v3.OpPut(string(k), v)
			}
		}
		close(requests)
	}()

	stopCompactor := make(chan struct{})
	var compactorWg sync.WaitGroup
	if compactionEnabled {
		compactorWg.Add(1)
		go func() {
			defer compactorWg.Done()
			ticker := time.NewTicker(compactionInterval)
			defer ticker.Stop()
			var lastCompactRev int64
			for {
				select {
				case <-stopCompactor:
					return
				case <-ticker.C:
					lastCompactRev = compactAndMeasure(clients[0], compactReport, lastCompactRev)
				}
			}
		}()
	}

	putRC := putReport.Run()
	rangeRC := rangeReport.Run()
	compactRC := compactReport.Run()

	wg.Wait()
	close(stopCompactor)
	compactorWg.Wait()

	close(putReport.Results())
	close(rangeReport.Results())
	close(compactReport.Results())
	bar.Finish()

	fmt.Printf("%s:\n%s\n", putLabel, <-putRC)
	fmt.Printf("%s:\n%s\n", rangeLabel, <-rangeRC)
	if compactionEnabled {
		fmt.Printf("COMPACTION:\n%s\n", <-compactRC)
	}
}

func compactAndMeasure(c *v3.Client, r report.Report, lastCompactRev int64) int64 {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	resp, err := c.Get(ctx, "compaction-probe")
	cancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to fetch current revision: %v\n", err)
		return lastCompactRev
	}
	revToCompact := resp.Header.Revision - compactionRevDelta
	if revToCompact <= lastCompactRev {
		return lastCompactRev
	}

	st := time.Now()
	_, err = c.Compact(context.Background(), revToCompact, v3.WithCompactPhysical())
	r.Results() <- report.Result{Err: err, Start: st, End: time.Now()}
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to compact revision %d: %v\n", revToCompact, err)
		return lastCompactRev
	}
	return revToCompact
}
