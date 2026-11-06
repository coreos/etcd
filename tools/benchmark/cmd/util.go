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
	"crypto/rand"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bgentry/speakeasy"
	"google.golang.org/grpc/grpclog"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/pkg/v3/report"
)

var (
	// cache the username and password for multiple connections
	globalUserName string
	globalPassword string

	// defrag is the shared backing var for each benchmark command's --defrag flag.
	defrag bool

	// defragTriggerPercent is the shared backing var for each benchmark command's
	// --defrag-trigger-percent flag.
	defragTriggerPercent int

	// defragOnce ensures the Defragment RPC triggered by --defrag fires exactly once per
	// run, even though maybeTriggerDefrag is called from a tight per-request loop.
	defragOnce sync.Once

	// defragDuration is how long the Defragment RPC triggered by --defrag took.
	defragDuration time.Duration
)

// maybeTriggerDefrag issues a single, asynchronous Defragment RPC against the first
// --endpoints entry once a command's request-producer loop reaches approximately
// --defrag-trigger-percent of total, when --defrag is set. current/total are that loop's
// own progress counters (e.g. i and putTotal), so it should be called once per iteration
// from that loop. The RPC runs in its own goroutine, tracked via the shared wg, so the
// producer keeps issuing requests at full rate while defrag runs concurrently.
func maybeTriggerDefrag(clients []*clientv3.Client, current, total int) {
	if !defrag || total <= 0 || current != total*defragTriggerPercent/100 {
		return
	}
	defragOnce.Do(func() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ep := clients[0].Endpoints()[0]
			fmt.Printf("defrag: triggering defragmentation on %s (%d/%d requests issued)\n", ep, current, total)
			st := time.Now()
			if _, err := clients[0].Defragment(context.Background(), ep); err != nil {
				fmt.Fprintf(os.Stderr, "defrag: failed to defragment %s: %v\n", ep, err)
				return
			}
			defragDuration = time.Since(st)
			fmt.Printf("defrag: defragmentation of %s completed in %v\n", ep, defragDuration)
		}()
	})
}

// printDefragDuration prints a --defrag run's duration as part of a command's final summary.
func printDefragDuration() {
	if defrag && defragDuration > 0 {
		fmt.Printf("Defrag duration: %v\n", defragDuration)
	}
}

func getUsernamePassword(usernameFlag string) (string, string, error) {
	if globalUserName != "" && globalPassword != "" {
		return globalUserName, globalPassword, nil
	}
	var ok bool
	globalUserName, globalPassword, ok = strings.Cut(usernameFlag, ":")
	if !ok {
		// Prompt for the password.
		password, err := speakeasy.Ask("Password: ")
		if err != nil {
			return "", "", err
		}
		globalUserName = usernameFlag
		globalPassword = password
	}
	return globalUserName, globalPassword, nil
}

func mustCreateConn() *clientv3.Client {
	cfg := clientv3.Config{
		AutoSyncInterval: autoSyncInterval,
		Endpoints:        endpoints,
		DialTimeout:      dialTimeout,
	}
	if !tls.Empty() || tls.TrustedCAFile != "" {
		cfgtls, err := tls.ClientConfig()
		if err != nil {
			fmt.Fprintf(os.Stderr, "bad tls config: %v\n", err)
			os.Exit(1)
		}
		cfg.TLS = cfgtls
	}

	if len(user) != 0 {
		username, password, err := getUsernamePassword(user)
		if err != nil {
			fmt.Fprintf(os.Stderr, "bad user information: %s %v\n", user, err)
			os.Exit(1)
		}
		cfg.Username = username
		cfg.Password = password
	}

	client, err := clientv3.New(cfg)
	grpclog.SetLoggerV2(grpclog.NewLoggerV2(os.Stderr, os.Stderr, os.Stderr))

	if err != nil {
		fmt.Fprintf(os.Stderr, "dial error: %v\n", err)
		os.Exit(1)
	}

	return client
}

func mustCreateClients(totalClients, totalConns uint) []*clientv3.Client {
	conns := make([]*clientv3.Client, totalConns)
	for i := range conns {
		conns[i] = mustCreateConn()
	}

	clients := make([]*clientv3.Client, totalClients)
	for i := range clients {
		clients[i] = conns[i%int(totalConns)]
	}
	return clients
}

func mustRandBytes(n int) []byte {
	rb := make([]byte, n)
	_, err := rand.Read(rb)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to generate value: %v\n", err)
		os.Exit(1)
	}
	return rb
}

func newReport(benchmarkOp string) report.Report {
	p := "%4.4f"
	if precise {
		p = "%g"
	}
	opts, sampler := reportOptions(benchmarkOp)
	var r report.Report
	if sample {
		r = report.NewReportSampleWithOptions(p, benchmarkOp, opts)
	} else {
		r = report.NewReportWithOptions(p, benchmarkOp, opts)
	}
	if sampler != nil {
		return newBenchmarkReport(r, sampler)
	}
	return r
}

func newWeightedReport(benchmarkOp string) report.Report {
	p := "%4.4f"
	if precise {
		p = "%g"
	}
	opts, sampler := reportOptions(benchmarkOp)
	var r report.Report
	if sample {
		r = report.NewReportSampleWithOptions(p, benchmarkOp, opts)
	} else {
		base := report.NewReportWithOptions(p, benchmarkOp, opts)
		r = report.NewWeightedReport(base, p, benchmarkOp, generatePerfReport)
	}
	if sampler != nil {
		return newBenchmarkReport(r, sampler)
	}
	return r
}

func reportOptions(benchmarkOp string) (report.Options, *metricSampler) {
	opts := report.Options{GeneratePerfReport: generatePerfReport}
	metricsURL := metricsEndpointURL()
	if metricsURL == "" || len(metrics) == 0 {
		return opts, nil
	}
	sampler := newMetricSampler(metricsURL, metrics)
	opts.MetricSummaries = func() []report.MetricSummary {
		summaries, timeSeries := sampler.stop()
		if len(timeSeries) > 0 {
			writeTimeSeriesReport(benchmarkOp, "resource", timeSeries)
		}
		return summaries
	}
	return opts, sampler
}

func metricsEndpointURL() string {
	if len(endpoints) == 0 {
		return ""
	}
	endpoint := strings.TrimSpace(endpoints[0])
	if endpoint == "" {
		return ""
	}
	scheme := "http"
	if !tls.Empty() {
		scheme = "https"
	}
	if strings.Contains(endpoint, "://") {
		u, err := url.Parse(endpoint)
		if err != nil {
			return ""
		}
		u.Path = "/metrics"
		u.RawQuery = ""
		u.Fragment = ""
		return u.String()
	}
	return (&url.URL{
		Scheme: scheme,
		Host:   endpoint,
		Path:   "/metrics",
	}).String()
}
