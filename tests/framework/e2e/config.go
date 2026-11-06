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

package e2e

import (
	"fmt"
	"strings"

	"github.com/Masterminds/semver/v3"

	"go.etcd.io/etcd/api/v3/version"
	"go.etcd.io/etcd/tests/v3/framework/config"
)

type ClusterVersion string

const (
	CurrentVersion      ClusterVersion = ""
	MinorityLastVersion ClusterVersion = "minority-last-version"
	QuorumLastVersion   ClusterVersion = "quorum-last-version"
	LastVersion         ClusterVersion = "last-version"
)

func (cv ClusterVersion) String() string {
	if cv == CurrentVersion {
		return "current-version"
	}
	return string(cv)
}

type ClusterContext struct {
	Version  ClusterVersion
	EnvVars  map[string]string
	UseUnix  bool
	BasePort int
}

func WithClusterOptionBasePort(port int) config.ClusterOption {
	return func(c *config.ClusterConfig) {
		ctx := ensureE2EClusterContext(c)
		ctx.BasePort = port
		c.ClusterContext = ctx
	}
}

func WithHTTP2Debug() config.ClusterOption {
	return func(c *config.ClusterConfig) {
		ctx := ensureE2EClusterContext(c)
		if ctx.EnvVars == nil {
			ctx.EnvVars = map[string]string{}
		}
		// Enable debug mode to get logs with http2 headers (including authority)
		ctx.EnvVars["GODEBUG"] = "http2debug=2"
		c.ClusterContext = ctx
	}
}

func WithUnixClient() config.ClusterOption {
	return func(c *config.ClusterConfig) {
		ctx := ensureE2EClusterContext(c)
		ctx.UseUnix = true
		c.ClusterContext = ctx
	}
}

func WithTCPClient() config.ClusterOption {
	return func(c *config.ClusterConfig) {
		ctx := ensureE2EClusterContext(c)
		ctx.UseUnix = false
		c.ClusterContext = ctx
	}
}

func ensureE2EClusterContext(c *config.ClusterConfig) *ClusterContext {
	ctx, _ := c.ClusterContext.(*ClusterContext)
	if ctx == nil {
		ctx = &ClusterContext{}
	}
	return ctx
}

var experimentalFlags = map[string]struct{}{
	"compact-hash-check-time":              {},
	"corrupt-check-time":                   {},
	"compaction-batch-limit":               {},
	"watch-progress-notify-interval":       {},
	"warning-apply-duration":               {},
	"bootstrap-defrag-threshold-megabytes": {},
	"memory-mlock":                         {},
	"snapshot-catchup-entries":             {},
	"compaction-sleep-interval":            {},
	"downgrade-check-time":                 {},
	"peer-skip-client-san-verification":    {},
	"enable-distributed-tracing":           {},
	"distributed-tracing-address":          {},
	"distributed-tracing-service-name":     {},
	"distributed-tracing-instance-id":      {},
	"distributed-tracing-sampling-rate":    {},
}

// convertFlag converts between experimental and non-experimental flags.
// All experimental flags have been removed in version 3.7, but versions prior
// to 3.6 only support experimental flags. The robustness tests use the same
// code from the main branch to test all previously supported releases, so we
// need to convert flags accordingly based on the binary version.
func convertFlag(name, value string, ver *semver.Version) string {
	// For versions >= 3.6, use the normal (non-experimental) flag.
	if ver == nil || !ver.LessThan(&version.V3_6) {
		return fmt.Sprintf("--%s=%s", name, value)
	}

	// For versions < 3.6, use the experimental flag if it exists in `experimentalFlags`
	if _, ok := experimentalFlags[name]; ok {
		return fmt.Sprintf("--experimental-%s=%s", name, value)
	}

	return fmt.Sprintf("--%s=%s", name, value)
}

func convertFlags(args []string, ver *semver.Version) []string {
	var retArgs []string

	for _, arg := range args {
		kv := strings.Split(arg, "=")
		if len(kv) != 2 {
			retArgs = append(retArgs, arg)
			continue
		}

		name := strings.TrimPrefix(kv[0], "--")
		name = strings.TrimPrefix(name, "experimental-")

		retArgs = append(retArgs, convertFlag(name, kv[1], ver))
	}

	return retArgs
}
