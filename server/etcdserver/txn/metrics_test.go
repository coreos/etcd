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

package txn

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestRangeSecObserve(t *testing.T) {
	// Enforce state isolation by wiping the global metric vector after execution.
	// This prevents data leakage across iterations when running with -count > 1.
	defer rangeSec.Reset()

	// Simulate a range operation taking 500 milliseconds.
	latency := 500 * time.Millisecond
	RangeSecObserve(true, latency)

	// Use testutil to collect the results and check against expected value
	expected := `
# HELP etcd_server_range_duration_seconds The latency distributions of txn.Range
# TYPE etcd_server_range_duration_seconds histogram
etcd_server_range_duration_seconds_bucket{success="true",le="0.0001"} 0
etcd_server_range_duration_seconds_bucket{success="true",le="0.0002"} 0
etcd_server_range_duration_seconds_bucket{success="true",le="0.0004"} 0
etcd_server_range_duration_seconds_bucket{success="true",le="0.0008"} 0
etcd_server_range_duration_seconds_bucket{success="true",le="0.0016"} 0
etcd_server_range_duration_seconds_bucket{success="true",le="0.0032"} 0
etcd_server_range_duration_seconds_bucket{success="true",le="0.0064"} 0
etcd_server_range_duration_seconds_bucket{success="true",le="0.0128"} 0
etcd_server_range_duration_seconds_bucket{success="true",le="0.0256"} 0
etcd_server_range_duration_seconds_bucket{success="true",le="0.0512"} 0
etcd_server_range_duration_seconds_bucket{success="true",le="0.1024"} 0
etcd_server_range_duration_seconds_bucket{success="true",le="0.2048"} 0
etcd_server_range_duration_seconds_bucket{success="true",le="0.4096"} 0
etcd_server_range_duration_seconds_bucket{success="true",le="0.8192"} 1
etcd_server_range_duration_seconds_bucket{success="true",le="1.6384"} 1
etcd_server_range_duration_seconds_bucket{success="true",le="3.2768"} 1
etcd_server_range_duration_seconds_bucket{success="true",le="6.5536"} 1
etcd_server_range_duration_seconds_bucket{success="true",le="13.1072"} 1
etcd_server_range_duration_seconds_bucket{success="true",le="26.2144"} 1
etcd_server_range_duration_seconds_bucket{success="true",le="52.4288"} 1
etcd_server_range_duration_seconds_bucket{success="true",le="+Inf"} 1
etcd_server_range_duration_seconds_sum{success="true"} 0.5
etcd_server_range_duration_seconds_count{success="true"} 1
`

	err := testutil.CollectAndCompare(rangeSec, strings.NewReader(expected))
	require.NoErrorf(t, err, "Collected metrics did not match expected metrics")
}
