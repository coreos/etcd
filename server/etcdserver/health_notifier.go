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

package etcdserver

// HealthNotifier is the abstract interface used by the server-side health
// checker to push state transitions to whatever component owns the
// gRPC health.v1 server (today: v3rpc.healthNotifier).
//
// The indirection avoids an import cycle: etcdserver cannot import
// server/v3rpc, and v3rpc already imports etcdserver.
//
// Concurrency: every method is safe for concurrent use. The
// checker goroutine and the maintenance RPC handler both invoke
// these methods on the same instance; implementations MUST
// serialize state transitions internally (e.g. with a mutex)
// and MUST tolerate the race where HealthCheckFailed("X") and
// HealthCheckRecovered("X") arrive in arbitrary order. Returning
// early on unknown reasons is a no-op and is allowed.
type HealthNotifier interface {
	// HealthCheckFailed marks the gRPC service NOT_SERVING. The reason is
	// recorded so that HealthCheckRecovered can clear it later; multiple
	// concurrent failure reasons are reference-counted.
	HealthCheckFailed(reason string)

	// HealthCheckRecovered clears the "health:<reason>" entry previously
	// registered by HealthCheckFailed(reason) and, if no other failure
	// reason remains, marks the gRPC service SERVING again. A call for
	// a reason that is not currently registered is a no-op; recovering
	// one reason never affects another concurrently-active reason.
	HealthCheckRecovered(reason string)

	// DefragStarted marks the gRPC service NOT_SERVING for the
	// duration of a defragmentation. The reason string and the
	// StopGRPCServiceOnDefrag gating are owned by the implementation;
	// the method carries no argument so the reason namespace stays out
	// of this package. Callers invoke it exactly once before a
	// Defragment starts.
	DefragStarted()

	// DefragFinished clears the defrag-driven NOT_SERVING state
	// registered by DefragStarted. It must be called exactly once
	// after every DefragStarted, including on the error path (the
	// production caller defers it); when the implementation gated the
	// Started call off, this is a no-op.
	DefragFinished()
}

// MultiNotifier fans a single state-transition out to every
// registered HealthNotifier. embed/serve.go calls v3rpc.Server
// once per listener (insecure, secure); each call produces a
// listener-local *v3rpc.healthNotifier that owns its own
// *health.Server. AddHealthNotifier appends each one, and
// GetHealthNotifier hands the checker a reloadingNotifier that
// rebuilds a MultiNotifier from the current registration set on
// every call, so a single apply_lag / no_leader /
// linear_read decision — or a Defragment via the maintenance
// server, which drives the same fan-out — reaches every
// listener's gRPC health.v1 state, including a listener that
// registered after the checker started.
//
// Concurrency: Safe for concurrent use. The notifiers slice is
// copy-on-build and read-only thereafter.
type MultiNotifier struct {
	notifiers []HealthNotifier
}

// NewMultiNotifier returns a MultiNotifier that fans out to every
// supplied notifier in registration order. A nil or empty slice
// yields a no-op MultiNotifier; the checker goroutine keeps polling
// GetHealthNotifier until a notifier shows up (see waitForHealthNotifier
// in server.go), logging a one-time Error and bumping
// healthcheck_exited_total{reason="notifier_unwired"} if the wiring
// is late.
func NewMultiNotifier(notifiers []HealthNotifier) *MultiNotifier {
	return &MultiNotifier{notifiers: notifiers}
}

func (m *MultiNotifier) HealthCheckFailed(reason string) {
	for _, n := range m.notifiers {
		n.HealthCheckFailed(reason)
	}
}

func (m *MultiNotifier) HealthCheckRecovered(reason string) {
	for _, n := range m.notifiers {
		n.HealthCheckRecovered(reason)
	}
}

func (m *MultiNotifier) DefragStarted() {
	for _, n := range m.notifiers {
		n.DefragStarted()
	}
}

func (m *MultiNotifier) DefragFinished() {
	for _, n := range m.notifiers {
		n.DefragFinished()
	}
}

// reloadingNotifier re-reads the registered notifier set on every
// call instead of holding a snapshot. The two v3rpc.Server
// constructions in a dual-listener deployment are not atomic with
// respect to the checker's first poll (the secure listener also loads
// TLS material before registering), so a snapshot taken at wiring
// time can permanently miss the second listener's notifier.
//
// This is also why the aggregator repeats a notifier call on every
// tick while a condition persists instead of calling only on the edge
// that flipped it: a listener that registers after that edge learns
// the current state from the next repeated call. The re-read is one
// atomic load plus a small allocation per call, so at the checker's
// 500ms cadence the repetition is free next to the checks themselves.
type reloadingNotifier struct {
	get func() HealthNotifier
}

func (r *reloadingNotifier) HealthCheckFailed(reason string) {
	if n := r.get(); n != nil {
		n.HealthCheckFailed(reason)
	}
}

func (r *reloadingNotifier) HealthCheckRecovered(reason string) {
	if n := r.get(); n != nil {
		n.HealthCheckRecovered(reason)
	}
}

func (r *reloadingNotifier) DefragStarted() {
	if n := r.get(); n != nil {
		n.DefragStarted()
	}
}

func (r *reloadingNotifier) DefragFinished() {
	if n := r.get(); n != nil {
		n.DefragFinished()
	}
}
