// Copyright 2024 The etcd Authors
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

package e2e

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.etcd.io/etcd/tests/v3/framework/e2e"
)

var commonAllowedErrors = map[string]bool{
	"setting up serving from embedded etcd failed.": true,
	// See https://github.com/etcd-io/etcd/pull/19040#issuecomment-2539173800
	// TODO: Remove with etcd 3.7
	"cannot detect storage schema version: missing term information": true,
}

func TestNoErrorLogsDuringNormalOperations(t *testing.T) {
	tests := []struct {
		name          string
		options       []e2e.EPClusterOption
		allowedErrors map[string]bool
	}{
		{
			name: "single node cluster",
			options: []e2e.EPClusterOption{
				e2e.WithClusterSize(1),
				e2e.WithLogLevel("debug"),
			},
			allowedErrors: commonAllowedErrors,
		},
		{
			name: "three node cluster",
			options: []e2e.EPClusterOption{
				e2e.WithClusterSize(3),
				e2e.WithLogLevel("debug"),
			},
			allowedErrors: commonAllowedErrors,
		},
		{
			name: "three node cluster with auto tls (all)",
			options: []e2e.EPClusterOption{
				e2e.WithClusterSize(3),
				e2e.WithLogLevel("debug"),
				e2e.WithIsPeerTLS(true),
				e2e.WithIsPeerAutoTLS(true),
				e2e.WithClientAutoTLS(true),
				e2e.WithClientConnType(e2e.ClientTLS),
			},
			allowedErrors: commonAllowedErrors,
		},
		{
			name: "three node cluster with auto tls (peers)",
			options: []e2e.EPClusterOption{
				e2e.WithClusterSize(3),
				e2e.WithLogLevel("debug"),
				e2e.WithIsPeerTLS(true),
				e2e.WithIsPeerAutoTLS(true),
			},
			allowedErrors: commonAllowedErrors,
		},
		{
			name: "three node cluster with auto tls (client)",
			options: []e2e.EPClusterOption{
				e2e.WithClusterSize(3),
				e2e.WithLogLevel("debug"),
				e2e.WithClientAutoTLS(true),
				e2e.WithClientConnType(e2e.ClientTLS),
			},
			allowedErrors: commonAllowedErrors,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e2e.BeforeTest(t)
			ctx := t.Context()

			epc, err := e2e.NewEtcdProcessCluster(ctx, t, tc.options...)
			require.NoError(t, err)
			defer epc.Close()

			require.Lenf(
				t,
				epc.Procs,
				epc.Cfg.ClusterSize,
				"embedded etcd cluster process count is not as expected",
			)

			// Collect the handle of logs before closing the processes.
			var logHandles []e2e.LogsExpect
			for i := range epc.Cfg.ClusterSize {
				logHandles = append(logHandles, epc.Procs[i].Logs())
			}

			time.Sleep(time.Second)
			require.NoErrorf(t, epc.Close(), "closing etcd processes")

			// Now that the processes are closed we can collect all log lines. This must happen after closing, else we
			// might not get all log lines.
			var lines []string
			for _, h := range logHandles {
				lines = append(lines, h.Lines()...)
			}
			require.NotEmptyf(t, lines, "expected at least one log line")

			var entry logEntry
			for _, line := range lines {
				err := json.Unmarshal([]byte(line), &entry)
				require.NoErrorf(t, err, "parse log line as json, line: %s", line)

				if tc.allowedErrors[entry.Message] || tc.allowedErrors[entry.Error] {
					continue
				}

				require.NotEqualf(
					t,
					"error",
					entry.Level,
					"error level log message found: %s",
					line,
				)
			}
		})
	}
}
