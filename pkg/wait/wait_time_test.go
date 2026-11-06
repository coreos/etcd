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

import (
	"fmt"
	"testing"
	"time"
)

func TestWaitTime(t *testing.T) {
	wt := NewTimeList()
	ch1 := wt.Wait(1)
	wt.Trigger(2)
	select {
	case <-ch1:
	default:
		t.Fatalf("cannot receive from ch as expected")
	}

	ch2 := wt.Wait(4)
	wt.Trigger(3)
	select {
	case <-ch2:
		t.Fatalf("unexpected to receive from ch2")
	default:
	}
	wt.Trigger(4)
	select {
	case <-ch2:
	default:
		t.Fatalf("cannot receive from ch2 as expected")
	}

	select {
	// wait on a triggered deadline
	case <-wt.Wait(4):
	default:
		t.Fatalf("unexpected blocking when wait on triggered deadline")
	}
}

func TestWaitTestStress(t *testing.T) {
	chs := make([]<-chan struct{}, 0)
	wt := NewTimeList()
	for i := 0; i <= 10000; i++ {
		chs = append(chs, wt.Wait(uint64(i)))
	}
	wt.Trigger(10000)

	for _, ch := range chs {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatalf("cannot receive from ch as expected")
		}
	}
}

func BenchmarkWaitTime(b *testing.B) {
	wt := NewTimeList()
	for i := 0; i < b.N; i++ {
		wt.Wait(1)
	}
}

func BenchmarkTriggerAnd10KWaitTime(b *testing.B) {
	for i := 0; i < b.N; i++ {
		wt := NewTimeList()
		for j := 0; j <= 10000; j++ {
			wt.Wait(uint64(j))
		}
		wt.Trigger(10000)
	}
}

func TestWaitTimePending(t *testing.T) {
	wt := NewTimeList()
	if n := wt.Pending(); n != 0 {
		t.Fatalf("empty list: Pending()=%d, want 0", n)
	}
	wt.Wait(1)
	wt.Wait(2)
	wt.Wait(3)
	wt.Wait(3) // a duplicate deadline shares a channel and must not be counted twice
	if n := wt.Pending(); n != 3 {
		t.Fatalf("after 3 distinct deadlines: Pending()=%d, want 3", n)
	}
	wt.Trigger(2)
	if n := wt.Pending(); n != 1 {
		t.Fatalf("after Trigger(2): Pending()=%d, want 1", n)
	}
	wt.Trigger(3)
	if n := wt.Pending(); n != 0 {
		t.Fatalf("after Trigger(3): Pending()=%d, want 0", n)
	}
}

func TestWaitTimeTriggerReleasesOnlyEarlierDeadlines(t *testing.T) {
	const n = 100
	wt := NewTimeList()
	chs := make([]<-chan struct{}, n)
	for i := range chs {
		chs[i] = wt.Wait(uint64(i + 1))
	}

	assertReleasedUpTo := func(limit uint64) {
		t.Helper()
		for i, ch := range chs {
			deadline := uint64(i + 1)
			released := false
			select {
			case <-ch:
				released = true
			default:
			}
			if want := deadline <= limit; released != want {
				t.Fatalf("deadline %d after Trigger(%d): released=%v, want %v", deadline, limit, released, want)
			}
		}
	}

	wt.Trigger(50)
	assertReleasedUpTo(50)

	// repeating or lowering the trigger deadline must not release later waiters
	wt.Trigger(50)
	assertReleasedUpTo(50)
	wt.Trigger(25)
	assertReleasedUpTo(50)

	wt.Trigger(n)
	assertReleasedUpTo(n)
}

// Trigger with nothing to release must not get slower as the waiter backlog grows.
func BenchmarkTriggerNoRelease(b *testing.B) {
	for _, n := range []int{100, 10_000, 100_000, 1_000_000} {
		b.Run(fmt.Sprintf("waiters=%d", n), func(b *testing.B) {
			wt := NewTimeList()
			base := uint64(1) << 40
			for i := 0; i < n; i++ {
				wt.Wait(base + uint64(i))
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				wt.Trigger(uint64(i))
			}
		})
	}
}
