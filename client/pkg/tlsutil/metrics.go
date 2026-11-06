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

package tlsutil

import "github.com/prometheus/client_golang/prometheus"

var (
	caReloadSuccessTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "etcd",
		Subsystem: "tlsutil",
		Name:      "ca_reload_success_total",
		Help:      "Total number of successful CA certificate reloads.",
	})

	caReloadFailureTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "etcd",
		Subsystem: "tlsutil",
		Name:      "ca_reload_failure_total",
		Help:      "Total number of failed CA certificate reloads.",
	})

	caReloadDurationSeconds = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "etcd",
		Subsystem: "tlsutil",
		Name:      "ca_reload_duration_seconds",
		Help:      "Duration of CA certificate reload operations in seconds.",
		// Buckets from 1ms to ~1s (reload should be fast, it's just file reads)
		Buckets: prometheus.ExponentialBuckets(0.001, 2, 10),
	})
)

func init() {
	prometheus.MustRegister(caReloadSuccessTotal)
	prometheus.MustRegister(caReloadFailureTotal)
	prometheus.MustRegister(caReloadDurationSeconds)
}
