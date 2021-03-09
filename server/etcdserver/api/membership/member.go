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

package membership

import (
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"math/rand"
	"sort"
	"time"

	"go.etcd.io/etcd/pkg/v3/types"
)

// RaftAttributes represents the raft related attributes of an etcd member.
type RaftAttributes struct {
	// PeerURLs is the list of peers in the raft cluster.
	// TODO(philips): ensure these are URLs
	PeerURLs []string `json:"peerURLs"`
	// IsLearner indicates if the member is raft learner.
	IsLearner bool `json:"isLearner,omitempty"`
}

// Attributes represents all the non-raft related attributes of an etcd member.
type Attributes struct {
	Name       string   `json:"name,omitempty"`
	ClientURLs []string `json:"clientURLs,omitempty"`
}

type Member struct {
	ID types.ID `json:"id"`
	RaftAttributes
	Attributes
	// PromoteRules govern if and when a raft member
	// may be automatically promoted.
	PromoteRules []PromoteRule `json:"promoteRules"`
}

type MonitorType string

const (
	Progress MonitorType = "progress"
)

type MonitorOp string

const (
	GreaterEqual MonitorOp = "greater-equal"
)

type monitorStatus uint8

const (
	// Monitor value meets the threshold for at least as long as the delay.
	inactive monitorStatus = iota
	// Monitor value meets the threshold, but delay has not yet elapsed.
	activeWait
	// Monitor value does not meet the threshold.
	active
)

type Monitor struct {
	Type            MonitorType `json:"type"`
	Op              MonitorOp   `json:"op"`
	Threshold       uint64      `json:"threshold"`
	Delay           uint32      `json:"delay"`
	status          monitorStatus
	statusChangedAt time.Time
	value           uint64
}

type PromoteRule struct {
	Auto     bool      `json:"auto"`
	Monitors []Monitor `json:"monitors"`
}

// NewMember creates a node Member without an ID and generates one based on the
// cluster name, peer URLs, and time. This is used for bootstrapping/adding new member.
func NewMember(name string, peerURLs types.URLs, clusterName string, now *time.Time) *Member {
	return newMember(name, peerURLs, clusterName, now, false /* isLearner */, nil)
}

// NewMemberAsLearnerWithPromoteRules creates a learner Member without an ID and generates
// one based on the cluster name, peer URLs, and time. This is used for bootstrapping/adding
// new member. Promote rules govern whether a learner may be manually or
// automatically promoted.
func NewMemberAsLearnerWithPromoteRules(name string, peerURLs types.URLs, promoteRules []PromoteRule, clusterName string, now *time.Time) *Member {
	return newMember(name, peerURLs, clusterName, now, true /* isLearner */, promoteRules)
}

// NewMemberAsLearner creates a learner Member without an ID and generates one based on the
// cluster name, peer URLs, and time. This is used for adding new learner member.
func NewMemberAsLearner(name string, peerURLs types.URLs, clusterName string, now *time.Time) *Member {
	rule := PromoteRule{
		Auto: false,
		Monitors: []Monitor{
			{
				Delay:     0,
				Op:        GreaterEqual,
				Type:      Progress,
				Threshold: 90,
			},
		},
	}
	return newMember(name, peerURLs, clusterName, now, true /* isLearner */, []PromoteRule{rule})
}

func newMember(name string, peerURLs types.URLs, clusterName string, now *time.Time, isLearner bool, promoteRules []PromoteRule) *Member {
	m := &Member{
		RaftAttributes: RaftAttributes{
			PeerURLs:  peerURLs.StringSlice(),
			IsLearner: isLearner,
		},
		Attributes: Attributes{
			Name: name,
		},
		PromoteRules: promoteRules,
	}

	var b []byte
	sort.Strings(m.PeerURLs)
	for _, p := range m.PeerURLs {
		b = append(b, []byte(p)...)
	}

	b = append(b, []byte(clusterName)...)
	if now != nil {
		b = append(b, []byte(fmt.Sprintf("%d", now.Unix()))...)
	}

	hash := sha1.Sum(b)
	m.ID = types.ID(binary.BigEndian.Uint64(hash[:8]))
	return m
}

// PickPeerURL chooses a random address from a given Member's PeerURLs.
// It will panic if there is no PeerURLs available in Member.
func (m *Member) PickPeerURL() string {
	if len(m.PeerURLs) == 0 {
		panic("member should always have some peer url")
	}
	return m.PeerURLs[rand.Intn(len(m.PeerURLs))]
}

func (m *Member) Clone() *Member {
	if m == nil {
		return nil
	}
	mm := &Member{
		ID: m.ID,
		RaftAttributes: RaftAttributes{
			IsLearner: m.IsLearner,
		},
		Attributes: Attributes{
			Name: m.Name,
		},
	}
	if m.PeerURLs != nil {
		mm.PeerURLs = make([]string, len(m.PeerURLs))
		copy(mm.PeerURLs, m.PeerURLs)
	}
	if m.ClientURLs != nil {
		mm.ClientURLs = make([]string, len(m.ClientURLs))
		copy(mm.ClientURLs, m.ClientURLs)
	}
	if m.PromoteRules != nil {
		mm.PromoteRules = make([]PromoteRule, len(m.PromoteRules))
		for ridx := range m.PromoteRules {
			mm.PromoteRules[ridx] = PromoteRule{
				Auto:     m.PromoteRules[ridx].Auto,
				Monitors: make([]Monitor, len(m.PromoteRules[ridx].Monitors)),
			}
			for midx := range m.PromoteRules[ridx].Monitors {
				mm.PromoteRules[ridx].Monitors[midx] = Monitor{
					Op:              m.PromoteRules[ridx].Monitors[midx].Op,
					Type:            m.PromoteRules[ridx].Monitors[midx].Type,
					Threshold:       m.PromoteRules[ridx].Monitors[midx].Threshold,
					Delay:           m.PromoteRules[ridx].Monitors[midx].Delay,
					status:          m.PromoteRules[ridx].Monitors[midx].status,
					statusChangedAt: m.PromoteRules[ridx].Monitors[midx].statusChangedAt,
					value:           m.PromoteRules[ridx].Monitors[midx].value,
				}
			}
		}
	}
	return mm
}

func (m *Member) CanPromote() (bool, error) {
	if !m.IsLearner {
		return false, ErrMemberNotLearner
	}
	if len(m.PromoteRules) == 0 {
		return false, ErrNoPromoteRules
	}
	var errors []error
	for idx := range m.PromoteRules {
		if satisfied, reason := m.PromoteRules[idx].IsSatisfied(); !satisfied {
			errors = append(errors, reason)
		} else {
			return true, nil
		}
	}
	// TODO: combine errors
	return false, errors[0]
}

func (m *Member) IsStarted() bool {
	return len(m.Name) != 0
}

// AddValue adds a value to the monitor's history of values. Update monitor status.
func (m *Monitor) AddValue(value uint64) {
	// Set some values that will be used to calculate new monitor status.
	meetsThreshold := false
	now := time.Now()
	var elapsed uint64
	if !m.statusChangedAt.IsZero() {
		elapsed = uint64(now.Sub(m.statusChangedAt).Seconds() * 1e3)
	}

	// Determine whether the value meets the threshold.
	switch m.Op {
	case GreaterEqual:
		meetsThreshold = value >= m.Threshold
		break
	}

	newStatus := m.status
	// Update monitor status according to the following truth table
	//
	// meetsThreshold && m.status && m.delay && elapsed => newStatus
	// ============================================================
	// true            | *         | 0        | *        | active
	// true            | !inactive | > 0      | >= delay | active
	// true            | inactive  | > 0      | *        | activeWait
	// false           | *         | *        | *        | inactive
	//
	// For all other possibilities, keep current status.
	if meetsThreshold && m.Delay == 0 {
		newStatus = active
	} else if meetsThreshold && m.status != inactive && m.Delay > 0 && elapsed >= uint64(m.Delay) {
		newStatus = active
	} else if meetsThreshold && m.status == inactive && m.Delay > 0 {
		newStatus = activeWait
	} else if !meetsThreshold {
		newStatus = inactive
	}

	// If status has changed, record the change and update time of last change.
	if newStatus != m.status {
		m.status = newStatus
		m.statusChangedAt = now
	}
}

func (m *Monitor) IsActive() bool {
	return m.status == active
}

func (r *PromoteRule) IsSatisfied() (bool, error) {
	if len(r.Monitors) == 0 {
		return false, ErrNoMonitors
	}
	for idx := range r.Monitors {
		if !r.Monitors[idx].IsActive() {
			switch r.Monitors[idx].Type {
			case Progress:
				return false, ErrLearnerNotReady
			}
			return false, ErrInactiveMonitor
		}
	}
	return true, nil
}

// MembersByID implements sort by ID interface
type MembersByID []*Member

func (ms MembersByID) Len() int           { return len(ms) }
func (ms MembersByID) Less(i, j int) bool { return ms[i].ID < ms[j].ID }
func (ms MembersByID) Swap(i, j int)      { ms[i], ms[j] = ms[j], ms[i] }

// MembersByPeerURLs implements sort by peer urls interface
type MembersByPeerURLs []*Member

func (ms MembersByPeerURLs) Len() int { return len(ms) }
func (ms MembersByPeerURLs) Less(i, j int) bool {
	return ms[i].PeerURLs[0] < ms[j].PeerURLs[0]
}
func (ms MembersByPeerURLs) Swap(i, j int) { ms[i], ms[j] = ms[j], ms[i] }
