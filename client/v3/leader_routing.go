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

package clientv3

import (
	"context"
	"math/rand"
	"slices"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	leaderbalancer "go.etcd.io/etcd/client/v3/internal/balancer/leader"
	endpointpkg "go.etcd.io/etcd/client/v3/internal/endpoint"
)

const (
	defaultLeaderRefreshInterval  = 30 * time.Second
	defaultLeaderRediscoveryDelay = 3 * time.Second
	defaultLeaderStatusTimeout    = 5 * time.Second
	leaderRefreshJitterFraction   = 0.10
)

// leaderUnaryInterceptor marks mutations before retrying and uses response
// headers to detect stale leader hints.
//
// One routing state covers all attempts, so a failed leader attempt sends later
// attempts to round_robin.
func (c *Client) leaderUnaryInterceptor(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	tracker := c.leaderTracker
	if tracker == nil {
		return invoker(ctx, method, req, reply, cc, opts...)
	}
	hint := tracker.current.Load()
	if isMutationRequest(req) {
		ctx = leaderbalancer.MarkMutation(ctx, tracker.invalidateHint)
	}
	err := invoker(ctx, method, req, reply, cc, opts...)
	if err == nil && hint != nil {
		if response, ok := reply.(interface{ GetHeader() *pb.ResponseHeader }); ok {
			leaderID := response.GetHeader().GetLeaderId()
			if leaderID != 0 && leaderID != hint.memberID {
				// The header is advisory, not an atomic (term, leader) pair.
				// Rediscover through Status rather than publishing it directly.
				// An empty address prevents a late response from invalidating a
				// newer hint, even if it points to the same endpoint.
				tracker.invalidateHint(hint.id, "")
			}
		}
	}
	return err
}

// isMutationRequest reports whether req uses leader-aware routing.
//
// It optimizes consensus writes because direct delivery skips the follower-to-
// leader proposal copy. MoveLeader also needs it because only the leader serves
// that RPC. All other requests use round_robin.
func isMutationRequest(req any) bool {
	switch req := req.(type) {
	case *pb.PutRequest, *pb.DeleteRangeRequest, *pb.CompactionRequest:
		return true
	case *pb.TxnRequest:
		// Match the transactions that the server proposes to Raft.
		//
		// The server evaluates compares and selects the branch at apply time, so
		// it proposes unless every operation in both branches is a Range
		// (etcdserver/txn.IsTxnReadonly).
		return requestOpsContainMutation(req.GetSuccess()) || requestOpsContainMutation(req.GetFailure())
	case *pb.LeaseGrantRequest, *pb.LeaseRevokeRequest:
		// Grant and revoke are consensus writes, so sessions, locks, and
		// elections built on them benefit.
		//
		// LeaseKeepAlive is a streaming RPC that this interceptor never sees.
		// Routing a long-lived stream to a leader would freeze its pick across
		// leader changes.
		return true
	case *pb.AuthenticateRequest,
		*pb.AuthEnableRequest, *pb.AuthDisableRequest,
		*pb.AuthUserAddRequest, *pb.AuthUserDeleteRequest, *pb.AuthUserChangePasswordRequest,
		*pb.AuthUserGrantRoleRequest, *pb.AuthUserRevokeRoleRequest,
		*pb.AuthRoleAddRequest, *pb.AuthRoleDeleteRequest,
		*pb.AuthRoleGrantPermissionRequest, *pb.AuthRoleRevokePermissionRequest:
		// Every auth administration request is a consensus write, including
		// Authenticate, which proposes to register the issued token.
		//
		// The auth queries (AuthStatus, UserGet, UserList, RoleGet, RoleList)
		// stay on round_robin. The server submits them to Raft as well, so
		// leader routing would save the same proposal copy, but they are too
		// rare to justify the extra routing logic, and routing writes alone
		// keeps the rule simple.
		return true
	case *pb.MoveLeaderRequest:
		// MoveLeader needs leader-aware routing even though it is not a consensus
		// write: only the leader serves it.
		//
		// A follower returns ErrNotLeader instead of forwarding. That error is
		// FailedPrecondition, which the retry policy does not retry, so a
		// round_robin attempt to a follower fails immediately. A stale hint fails
		// once, invalidates itself, and schedules rediscovery.
		return true
	case *pb.AlarmRequest:
		// The server submits every Alarm action, including GET, to Raft.
		// The whole type therefore uses leader-aware routing.
		return true
	default:
		// Everything else uses round_robin.
		//
		// This includes reads; Defragment, Status, and HashKV, which dial a
		// specific member; membership changes, which are fenced by endpoint
		// generation after MemberList; and DowngradeRequest, whose actions mix
		// member-local validation with cluster state changes.
		return false
	}
}

// requestOpsContainMutation mirrors the server's IsTxnReadonly rule.
//
// Any operation that is not a Range — Put, DeleteRange, or a nested Txn —
// makes the enclosing transaction a Raft proposal. Like the server, this is a
// shallow check: a nested read-only Txn still uses the consensus path.
func requestOpsContainMutation(ops []*pb.RequestOp) bool {
	for _, op := range ops {
		if op.GetRequestRange() == nil {
			return true
		}
	}
	return false
}

type leaderTracker struct {
	client           *Client
	refreshInterval  time.Duration
	rediscoveryDelay time.Duration
	statusTimeout    time.Duration
	invalidatec      chan struct{}
	donec            chan struct{}

	// A hint is current only when hintEpoch equals epoch.
	//
	// Invalidation advances epoch, so an observation from an older Status round
	// cannot remain published.
	epoch atomic.Uint64
	// current is the active hint's publication and failure CAS token. Nil means
	// there is no active hint.
	//
	// The address accompanies the ID. If a newer publication repeats an address,
	// a failure against an older hint still triggers rediscovery because its Status
	// probes predate that failure.
	current atomic.Pointer[hintIdentity]
	// currentRef mirrors current for the tracker goroutine's CAS expectations.
	//
	// The tracker goroutine alone accesses these fields.
	currentRef          *hintIdentity
	hintEpoch           uint64
	nextHintID          uint64
	hintID              uint64
	hintAddress         string
	pendingInvalidation atomic.Bool
}

// hintIdentity pairs a hint's CAS identity with its resolver address and member ID.
type hintIdentity struct {
	id       uint64
	address  string
	memberID uint64
}

func newLeaderTracker(client *Client) *leaderTracker {
	refreshInterval := client.cfg.leaderAwareRefreshInterval
	if refreshInterval <= 0 {
		refreshInterval = defaultLeaderRefreshInterval
	}
	rediscoveryDelay := client.cfg.leaderAwareRediscoveryDelay
	if rediscoveryDelay <= 0 {
		rediscoveryDelay = defaultLeaderRediscoveryDelay
	}
	statusTimeout := client.cfg.leaderAwareStatusTimeout
	if statusTimeout <= 0 {
		statusTimeout = defaultLeaderStatusTimeout
	}
	return &leaderTracker{
		client:           client,
		refreshInterval:  refreshInterval,
		rediscoveryDelay: rediscoveryDelay,
		statusTimeout:    statusTimeout,
		invalidatec:      make(chan struct{}, 1),
		donec:            make(chan struct{}),
	}
}

// Status polling bootstraps discovery and supports older servers with no
// leader_id header. Successful unary responses reporting a different leader
// schedule prompt rediscovery, including after a leadership transfer where
// writes through the old leader still succeed.
func (tracker *leaderTracker) run() {
	defer close(tracker.donec)
	// Reuse the main connection's round_robin SubConns for endpoint Status probes.
	statusClient := pb.NewMaintenanceClient(tracker.client.conn)
	delay := tracker.promptRefreshDelay()
	promptScheduled := true
	for tracker.waitForRefresh(delay, promptScheduled) {
		if tracker.client.ctx.Err() != nil {
			return
		}
		// The timer has expired, so consume any concurrent invalidation without
		// adding another delay before probing the current endpoint generation.
		tracker.consumeInvalidation()
		if tracker.refresh(statusClient) {
			delay = tracker.promptRefreshDelay()
			promptScheduled = true
		} else {
			// Independent jitter spreads periodic Status rounds across clients.
			// It does not reduce aggregate probe QPS, which scales with
			// clients * endpoints / refreshInterval.
			delay = jitterUp(tracker.refreshInterval, leaderRefreshJitterFraction)
			promptScheduled = false
		}
	}
}

func (tracker *leaderTracker) promptRefreshDelay() time.Duration {
	// Full jitter spreads clients that start or observe the same failure together.
	return time.Duration(rand.Float64() * float64(tracker.rediscoveryDelay))
}

// invalidate rejects the current hint epoch after an endpoint change and wakes
// the tracker; the tracker goroutine owns resolver hint updates.
func (tracker *leaderTracker) invalidate() {
	tracker.current.Store(nil)
	tracker.signalInvalidation()
}

func (tracker *leaderTracker) invalidateHint(hintID uint64, address string) {
	if hintID == 0 {
		return
	}
	current := tracker.current.Load()
	if current == nil {
		return
	}
	if current.id == hintID {
		// Only the failed hint may clear itself. The ID also distinguishes
		// A -> B -> A leader changes whose addresses compare equal again.
		if tracker.current.CompareAndSwap(current, nil) {
			tracker.signalInvalidation()
			return
		}
		// The CAS failed because the hint changed underfoot: another failure
		// cleared it, or a newer publication replaced it.
		//
		// Reload once. If the newer hint repeats the failed address, the
		// staleness check still applies because its probes predate this failure.
		current = tracker.current.Load()
		if current == nil {
			return
		}
	}
	// The failed hint is no longer current.
	//
	// If a newer publication repeats the failed address, its Status probes
	// predate this failure, so rediscover instead of trusting it. A late,
	// unrelated failure can clear a healthy hint, but that costs one coalesced
	// probe round and republishes the same leader.
	if address != "" && current.address == address {
		tracker.signalInvalidation()
	}
}

func (tracker *leaderTracker) signalInvalidation() {
	tracker.epoch.Add(1)
	if !tracker.pendingInvalidation.CompareAndSwap(false, true) {
		return
	}
	// Pick and Done must not block.
	// The flag and buffered channel coalesce concurrent failures into one wake-up.
	select {
	case tracker.invalidatec <- struct{}{}:
	default:
	}
}

func (tracker *leaderTracker) consumeInvalidation() bool {
	// The flag is authoritative; drain its coalesced wake-up when a caller
	// consumes directly instead of receiving from invalidatec.
	select {
	case <-tracker.invalidatec:
	default:
	}
	if !tracker.pendingInvalidation.Swap(false) {
		return false
	}
	tracker.clear()
	return true
}

func (tracker *leaderTracker) waitForRefresh(delay time.Duration, promptScheduled bool) bool {
	deadline := time.Now().Add(delay)
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	for {
		select {
		case <-tracker.client.ctx.Done():
			return false
		case <-tracker.invalidatec:
			if tracker.consumeInvalidation() {
				if promptScheduled {
					continue
				}
				promptScheduled = true
				// The first failure moves a periodic deadline earlier.
				// Later failures share it instead of replacing the jitter sample.
				delay = tracker.promptRefreshDelay()
				newDeadline := time.Now().Add(delay)
				if !newDeadline.Before(deadline) {
					continue
				}
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(delay)
				deadline = newDeadline
			}
		case <-timer.C:
			return true
		}
	}
}

func (tracker *leaderTracker) clear() {
	epoch := tracker.epoch.Load()
	if tracker.hintEpoch == epoch {
		return
	}
	tracker.current.Store(nil)
	tracker.currentRef = nil
	_, generation := tracker.client.endpointSnapshot()
	changed := tracker.hintAddress != ""
	tracker.client.resolver.SetLeader("", generation, 0)
	tracker.hintAddress = ""
	tracker.hintID = 0
	tracker.hintEpoch = epoch
	if changed {
		tracker.client.GetLogger().Debug("invalidated etcd leader")
	}
}

func (tracker *leaderTracker) publish(epoch, generation uint64, leader string, memberID uint64) bool {
	if tracker.pendingInvalidation.Load() || tracker.epoch.Load() != epoch {
		return false
	}
	changed := tracker.hintAddress != leader
	hintID := uint64(0)
	var next *hintIdentity
	if leader != "" {
		tracker.nextHintID++
		hintID = tracker.nextHintID
		// The identity carries the interpreted address because picker failures
		// compare resolver addresses.
		address, _ := endpointpkg.Interpret(leader)
		next = &hintIdentity{id: hintID, address: address, memberID: memberID}
	}
	// Every nonempty publication gets a new identity.
	// The CAS decides whether the old hint's failure or this publication wins.
	if !tracker.current.CompareAndSwap(tracker.currentRef, next) {
		return false
	}
	if !tracker.client.resolver.SetLeader(leader, generation, hintID) {
		tracker.current.CompareAndSwap(next, tracker.currentRef)
		return false
	}
	if tracker.pendingInvalidation.Load() || tracker.epoch.Load() != epoch {
		// Invalidation raced with publication; remove the older hint before
		// returning to the refresh loop.
		tracker.clear()
		return false
	}
	tracker.hintAddress = leader
	tracker.hintID = hintID
	tracker.currentRef = next
	tracker.hintEpoch = epoch
	return changed
}

// refresh polls all endpoints concurrently and publishes a leader hint only
// while the endpoint generation and routing epoch remain current.
//
// It reports whether invalidation interrupted the round and needs a prompt
// retry.
func (tracker *leaderTracker) refresh(statusClient pb.MaintenanceClient) bool {
	endpoints, generation := tracker.client.endpointSnapshot()
	if len(endpoints) == 0 {
		tracker.clear()
		return false
	}

	ctx, cancel := context.WithTimeout(tracker.client.ctx, tracker.statusTimeout)
	defer cancel()
	epoch := tracker.epoch.Load()
	// Use one attempt per endpoint; the next refresh handles transient failures.
	callOpts := append(slices.Clone(tracker.client.callOpts), withMax(0))
	results := make(chan leaderStatus, len(endpoints))
	for _, endpoint := range endpoints {
		go func(endpoint string) {
			address, _ := endpointpkg.Interpret(endpoint)
			response, err := statusClient.Status(
				leaderbalancer.PinEndpoint(ctx, address),
				&pb.StatusRequest{},
				callOpts...,
			)
			if err != nil || response == nil || response.Header == nil {
				results <- leaderStatus{}
				return
			}
			results <- leaderStatus{
				endpoint:  endpoint,
				memberID:  response.Header.MemberId,
				leaderID:  response.Leader,
				isLearner: response.IsLearner,
			}
		}(endpoint)
	}

	statuses := make([]leaderStatus, 0, len(endpoints))
	for received := 0; received < len(endpoints); {
		select {
		case <-tracker.client.ctx.Done():
			return false
		case <-tracker.invalidatec:
			// Do not cancel for a stale notification or an epoch already published.
			if tracker.consumeInvalidation() {
				return true
			}
		case status := <-results:
			received++
			if status.memberID != 0 {
				statuses = append(statuses, status)
			}
		}
	}
	if tracker.client.ctx.Err() != nil {
		return false
	}

	leader := selectLeader(statuses)
	var memberID uint64
	for _, status := range statuses {
		if status.endpoint == leader {
			memberID = status.memberID
			break
		}
	}
	if tracker.publish(epoch, generation, leader, memberID) && leader != "" {
		tracker.client.GetLogger().Debug("refreshed etcd leader", zap.String("endpoint", leader))
	}
	return false
}

type leaderStatus struct {
	endpoint  string
	memberID  uint64
	leaderID  uint64
	isLearner bool
}

// selectLeader returns a leader only when all responding voting members agree
// and the candidate reports itself as leader.
func selectLeader(statuses []leaderStatus) string {
	reports := make(map[uint64]leaderStatus, len(statuses))
	for _, status := range statuses {
		if status.isLearner {
			continue
		}
		if previous, ok := reports[status.memberID]; ok && previous.leaderID != status.leaderID {
			// Multiple endpoints may reach the same member. Conflicting views are
			// not a safe basis for leader-aware routing.
			return ""
		}
		reports[status.memberID] = status
	}
	var leaderID uint64
	for _, status := range reports {
		if status.leaderID == 0 {
			return ""
		}
		if leaderID == 0 {
			leaderID = status.leaderID
			continue
		}
		if status.leaderID != leaderID {
			// Status replies can straddle an election, so disagreement falls
			// back to round_robin.
			return ""
		}
	}
	if leaderID == 0 {
		return ""
	}
	leader, ok := reports[leaderID]
	if !ok || leader.leaderID != leaderID {
		// Peer reports alone are insufficient; the candidate must confirm itself.
		return ""
	}
	return leader.endpoint
}
