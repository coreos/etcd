// Copyright 2017 The etcd Authors
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
	"math"
	"os"
	"time"

	"github.com/cheggaaa/pb/v3"
	"github.com/spf13/cobra"
	"golang.org/x/time/rate"

	v3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/pkg/v3/report"
)

// txnPutCmd represents the txnPut command
var txnPutCmd = &cobra.Command{
	Use:   "txn-put",
	Short: "Benchmark txn-put",

	Run: txnPutFunc,
}

var (
	txnPutTotal     int
	txnPutRate      int
	txnPutOpsPerTxn int
)

func init() {
	RootCmd.AddCommand(txnPutCmd)
	txnPutCmd.Flags().IntVar(&keySize, "key-size", 8, "Key size of txn put")
	txnPutCmd.Flags().IntVar(&valSize, "val-size", 8, "Value size of txn put")
	txnPutCmd.Flags().IntVar(&txnPutOpsPerTxn, "txn-ops", 1, "Number of puts per txn")
	txnPutCmd.Flags().IntVar(&txnPutRate, "rate", 0, "Maximum txns per second (0 is no limit)")

	txnPutCmd.Flags().IntVar(&txnPutTotal, "total", 10000, "Total number of txn requests")
	txnPutCmd.Flags().IntVar(&keySpaceSize, "key-space-size", 1, "Maximum possible keys")
	txnPutCmd.Flags().BoolVar(&defrag, "defrag", false, "'true' to trigger a one-time defragmentation at approximately --defrag-trigger-percent of --total requests")
	txnPutCmd.Flags().IntVar(&defragTriggerPercent, "defrag-trigger-percent", 40, "Percentage of --total requests at which --defrag triggers defragmentation")
}

func txnPutFunc(cmd *cobra.Command, _ []string) {
	if keySpaceSize <= 0 {
		fmt.Fprintf(os.Stderr, "expected positive --key-space-size, got (%v)", keySpaceSize)
		os.Exit(1)
	}

	if txnPutOpsPerTxn > keySpaceSize {
		fmt.Fprintf(os.Stderr, "expected --txn-ops no larger than --key-space-size, "+
			"got txn-ops(%v) key-space-size(%v)\n", txnPutOpsPerTxn, keySpaceSize)
		os.Exit(1)
	}

	requests := make(chan []v3.Op, totalClients)
	if txnPutRate == 0 {
		txnPutRate = math.MaxInt32
	}
	limit := rate.NewLimiter(rate.Limit(txnPutRate), 1)
	clients := mustCreateClients(totalClients, totalConns)
	k, v := make([]byte, keySize), string(mustRandBytes(valSize))

	bar = pb.New(txnPutTotal)
	bar.Start()

	reportName := cmd.Name()
	r := newReport(reportName)
	finish := printReport(r, reportName)
	for i := range clients {
		wg.Add(1)
		go func(c *v3.Client) {
			defer wg.Done()
			for ops := range requests {
				limit.Wait(context.Background())
				st := time.Now()
				_, err := c.Txn(context.TODO()).Then(ops...).Commit()
				r.Results() <- report.Result{Err: err, Start: st, End: time.Now()}
				bar.Increment()
			}
		}(clients[i])
	}

	go func() {
		for i := 0; i < txnPutTotal; i++ {
			ops := make([]v3.Op, txnPutOpsPerTxn)
			for j := 0; j < txnPutOpsPerTxn; j++ {
				binary.PutVarint(k, int64(((i*txnPutOpsPerTxn)+j)%keySpaceSize))
				ops[j] = v3.OpPut(string(k), v)
			}
			requests <- ops
			maybeTriggerDefrag(clients, i, txnPutTotal)
		}
		close(requests)
	}()

	wg.Wait()
	close(r.Results())
	bar.Finish()
	finish()
	printDefragDuration()
}
