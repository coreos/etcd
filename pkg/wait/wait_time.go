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

package wait

import "sync"

type WaitTime interface {
	// Wait returns a chan that waits on the given logical deadline.
	// The chan will be triggered when Trigger is called with a
	// deadline that is later than or equal to the one it is waiting for.
	Wait(deadline uint64) <-chan struct{}
	// Trigger triggers all the waiting chans with an equal or earlier logical deadline.
	Trigger(deadline uint64)
	// Pending returns the number of outstanding deadlines that have not been triggered yet.
	Pending() int
}

var closec chan struct{}

func init() { closec = make(chan struct{}); close(closec) }

type timeList struct {
	l                   sync.Mutex
	lastTriggerDeadline uint64
	m                   map[uint64]chan struct{}
	h                   deadlineHeap
}

func NewTimeList() *timeList {
	return &timeList{m: make(map[uint64]chan struct{})}
}

func (tl *timeList) Wait(deadline uint64) <-chan struct{} {
	tl.l.Lock()
	defer tl.l.Unlock()
	if tl.lastTriggerDeadline >= deadline {
		return closec
	}
	ch := tl.m[deadline]
	if ch == nil {
		ch = make(chan struct{})
		tl.m[deadline] = ch
		tl.h.push(deadline)
	}
	return ch
}

func (tl *timeList) Trigger(deadline uint64) {
	tl.l.Lock()
	defer tl.l.Unlock()
	tl.lastTriggerDeadline = deadline
	for len(tl.h) > 0 && tl.h[0] <= deadline {
		t := tl.h.pop()
		close(tl.m[t])
		delete(tl.m, t)
	}
}

func (tl *timeList) Pending() int {
	tl.l.Lock()
	defer tl.l.Unlock()
	return len(tl.m)
}

// deadlineHeap is a min-heap of uint64 deadlines, hand-rolled to avoid container/heap's boxing allocation.
type deadlineHeap []uint64

func (h *deadlineHeap) push(d uint64) {
	s := append(*h, d)
	i := len(s) - 1

	for i > 0 {
		p := (i - 1) / 2
		if s[p] <= s[i] {
			break
		}
		s[p], s[i] = s[i], s[p]
		i = p
	}
	*h = s
}

func (h *deadlineHeap) pop() uint64 {
	s := *h
	top := s[0]
	n := len(s) - 1
	s[0] = s[n]
	s = s[:n]
	i := 0

	for {
		l := 2*i + 1
		if l >= n {
			break
		}
		c := l
		if r := l + 1; r < n && s[r] < s[l] {
			c = r
		}
		if s[i] <= s[c] {
			break
		}
		s[i], s[c] = s[c], s[i]
		i = c
	}

	*h = s

	return top
}
