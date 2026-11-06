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

// Package health implements the server-side health self-check loop that
// reports NOT_SERVING via the gRPC health.v1 service when linear reads
// fail, apply lag exceeds a threshold, or the cluster has no leader.
//
// The package is gated by the ServerHealthCheck feature gate. When the
// gate is disabled the Checker goroutine is never started and the
// package's only side effect is the registration of Prometheus metrics.
package health

import "time"

// Default tunables. They are unexported on purpose: callers must
// reach them through DefaultConfig() so the package owns the only
// writable copy of the defaults. Tests inside this package (which
// share the unexported namespace) compare against the same
// constants; external callers should never need to read or
// mutate them.
const (
	defaultCheckInterval     = 500 * time.Millisecond
	defaultCheckTimeout      = 2 * time.Second
	defaultApplyLagThreshold = 10000
	defaultFailureThreshold  = 3
	defaultSuccessThreshold  = 2
)

// Config holds tunables for the health check loop. Values come from
// command-line flags (--experimental-health-*) or DefaultConfig.
type Config struct {
	// Interval is the tick period of the check loop. 500ms is a good
	// default: it is short enough to detect failure within 1.5s (3
	// consecutive failures) but long enough to avoid wasting CPU.
	Interval time.Duration

	// CheckTimeout caps a single check's wall-clock duration. A
	// linear-read check that exceeds this is considered failed.
	//
	// The package default is only a fallback: a linear read that the
	// server itself would still be waiting on must not be scored as
	// a failure by the checker. etcdserver.healthCheckConfig
	// therefore sets this to ServerConfig.ReqTimeout() (5s + 2
	// election timeouts, the same budget a real linearizable read
	// gets) unless the operator pinned
	// --experimental-health-check-timeout. Keeping the two in step is
	// what makes the checker's verdict agree with what clients
	// actually experience.
	CheckTimeout time.Duration

	// ApplyLagThreshold is the maximum allowed gap between the
	// committed index and the applied index. The default of
	// 10000 is derived from
	// server/etcdserver/v3_server.go as
	// `maxPriorityGap = 2 * maxGapBetweenApplyAndCommitIndex`
	// (with the latter = 5000), so a node that has applied at
	// least one entry for every committed index is never
	// reported unhealthy by the check itself.
	//
	// NOTE: this duplication is intentional. v3_server.go keeps
	// the constants unexported and lives in a package the health
	// package cannot import (it would create a cycle). If
	// maxGapBetweenApplyAndCommitIndex or maxPriorityGap is ever
	// changed in v3_server.go, this default MUST be updated in
	// the same patch or the apply_lag check will start firing
	// for healthy nodes.
	//
	// The condition is cluster-correlated, not member-local:
	// CommittedIndex advances raft-wide while AppliedIndex lags
	// locally, so under sustained write pressure every member
	// crosses the threshold together. That is why apply_lag
	// drives only the etcd.health/apply_lag observation entry and
	// never the aggregate entry a client's endpoint selection
	// consumes; a client acting on this entry must keep at least
	// one endpoint (or fall back when all report NOT_SERVING) so
	// a full-cluster backlog does not become "no address
	// available".
	ApplyLagThreshold uint64

	// FailureThreshold is the number of consecutive failed ticks
	// before the gRPC service is reported NOT_SERVING.
	FailureThreshold int

	// SuccessThreshold is the number of consecutive successful ticks
	// before the gRPC service is reported SERVING again. It is
	// intentionally lower than FailureThreshold (asymmetric debounce)
	// to favor fast recovery.
	SuccessThreshold int
}

// DefaultConfig returns a Config populated with the package defaults.
// The embed layer wraps this with flag overrides.
func DefaultConfig() Config {
	return Config{
		Interval:          defaultCheckInterval,
		CheckTimeout:      defaultCheckTimeout,
		ApplyLagThreshold: defaultApplyLagThreshold,
		FailureThreshold:  defaultFailureThreshold,
		SuccessThreshold:  defaultSuccessThreshold,
	}
}
