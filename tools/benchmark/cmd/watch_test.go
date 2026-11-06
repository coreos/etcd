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

package cmd

import (
	"testing"
	"time"
)

func TestValidateWatchFlags(t *testing.T) {
	tests := []struct {
		name             string
		streams          int
		watchesPerStream int
		watchedKeyTotal  int
		keySpaceSize     int
		expectErr        bool
	}{
		{"all positive", 1, 1, 1, 1, false},
		{"zero streams", 0, 1, 1, 1, true},
		{"negative streams", -1, 1, 1, 1, true},
		{"zero watch-per-stream", 1, 0, 1, 1, true},
		{"negative watch-per-stream", 1, -1, 1, 1, true},
		{"zero watched-key-total", 1, 1, 0, 1, true},
		{"negative watched-key-total", 1, 1, -1, 1, true},
		{"zero key-space-size", 1, 1, 1, 0, true},
		{"negative key-space-size", 1, 1, 1, -1, true},
	}

	// Save and restore the package-level flag variables since they are
	// shared global state read by validateWatchFlags.
	origStreams, origWatchesPerStream := watchStreams, watchWatchesPerStream
	origWatchedKeyTotal, origKeySpaceSize := watchedKeyTotal, watchKeySpaceSize
	defer func() {
		watchStreams, watchWatchesPerStream = origStreams, origWatchesPerStream
		watchedKeyTotal, watchKeySpaceSize = origWatchedKeyTotal, origKeySpaceSize
	}()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			watchStreams = tt.streams
			watchWatchesPerStream = tt.watchesPerStream
			watchedKeyTotal = tt.watchedKeyTotal
			watchKeySpaceSize = tt.keySpaceSize

			err := validateWatchFlags()
			if tt.expectErr && err == nil {
				t.Errorf("validateWatchFlags() = nil, want error")
			}
			if !tt.expectErr && err != nil {
				t.Errorf("validateWatchFlags() = %v, want nil", err)
			}
		})
	}
}

func TestPutSeqRoundTrip(t *testing.T) {
	for _, seq := range []int{0, 1, 42, 1000, 1 << 20} {
		got, ok := decodePutSeq([]byte(encodePutSeq(seq)))
		if !ok {
			t.Fatalf("decodePutSeq(encodePutSeq(%d)) reported failure", seq)
		}
		if got != seq {
			t.Fatalf("decodePutSeq(encodePutSeq(%d)) = %d", seq, got)
		}
	}
}

func TestDecodePutSeqRejectsForeignValues(t *testing.T) {
	// Values not written by this benchmark must not be decoded into an
	// arbitrary sequence number and silently skew the report.
	for _, value := range [][]byte{nil, {}, []byte("data"), []byte("too long to be a sequence")} {
		if _, ok := decodePutSeq(value); ok {
			t.Fatalf("decodePutSeq(%q) accepted a value it did not write", value)
		}
	}
}

func TestPutTimelineIssued(t *testing.T) {
	timeline := newPutTimeline(4)

	if _, ok := timeline.issued(0); ok {
		t.Fatal("issued() succeeded for a put that was never issued")
	}
	for _, seq := range []int{-1, 4, 100} {
		if _, ok := timeline.issued(seq); ok {
			t.Fatalf("issued(%d) succeeded for an out-of-range sequence", seq)
		}
	}

	before := time.Now()
	timeline.markIssued(2)
	after := time.Now()

	st, ok := timeline.issued(2)
	if !ok {
		t.Fatal("issued() failed for a put that was issued")
	}
	if st.Before(before) || st.After(after) {
		t.Fatalf("issued() = %v, want within [%v, %v]", st, before, after)
	}

	// Recording one put must not make the others look issued.
	if _, ok := timeline.issued(1); ok {
		t.Fatal("issued(1) succeeded after only put 2 was issued")
	}
}

func TestPutTimelineIssuedAtBase(t *testing.T) {
	timeline := newPutTimeline(1)
	// A put issued in the same clock tick as base has offset 0.
	timeline.issuedAt[0].Store(0)
	if _, ok := timeline.issued(0); !ok {
		t.Fatal("issued() failed for a put issued at the base time")
	}
}

// Latency must be measured from when the put was issued, so time that passes
// before the receiver handles the event still counts toward it.
func TestPutTimelineMeasuresFromPut(t *testing.T) {
	timeline := newPutTimeline(1)
	timeline.markIssued(0)

	st, ok := timeline.issued(0)
	if !ok {
		t.Fatal("issued() failed for a put that was issued")
	}

	time.Sleep(20 * time.Millisecond)
	if elapsed := time.Since(st); elapsed < 20*time.Millisecond {
		t.Fatalf("elapsed since put = %v, want >= 20ms", elapsed)
	}
}
