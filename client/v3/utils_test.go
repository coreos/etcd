// Copyright 2025 The etcd Authors
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
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestExpBackoff(t *testing.T) {
	testCases := []struct {
		generation      uint
		exponent        float64
		minDelay        time.Duration
		maxDelay        time.Duration
		expectedBackoff time.Duration
	}{
		// exponential backoff with 2.0 exponent
		{generation: 0, exponent: 2.0, minDelay: 100 * time.Millisecond, maxDelay: 500 * time.Millisecond, expectedBackoff: 100 * time.Millisecond},
		{generation: 1, exponent: 2.0, minDelay: 100 * time.Millisecond, maxDelay: 500 * time.Millisecond, expectedBackoff: 200 * time.Millisecond},
		{generation: 2, exponent: 2.0, minDelay: 100 * time.Millisecond, maxDelay: 500 * time.Millisecond, expectedBackoff: 400 * time.Millisecond},
		{generation: 3, exponent: 2.0, minDelay: 100 * time.Millisecond, maxDelay: 500 * time.Millisecond, expectedBackoff: 500 * time.Millisecond},
		{generation: math.MaxUint, exponent: 2.0, minDelay: 100 * time.Millisecond, maxDelay: 500 * time.Millisecond, expectedBackoff: 500 * time.Millisecond},

		// exponential backoff with 1.0 exponent
		{generation: 0, exponent: 1.0, minDelay: 100 * time.Millisecond, maxDelay: 500 * time.Millisecond, expectedBackoff: 100 * time.Millisecond},
		{generation: 1, exponent: 1.0, minDelay: 100 * time.Millisecond, maxDelay: 500 * time.Millisecond, expectedBackoff: 100 * time.Millisecond},
		{generation: 2, exponent: 1.0, minDelay: 100 * time.Millisecond, maxDelay: 500 * time.Millisecond, expectedBackoff: 100 * time.Millisecond},
		{generation: 3, exponent: 1.0, minDelay: 100 * time.Millisecond, maxDelay: 500 * time.Millisecond, expectedBackoff: 100 * time.Millisecond},
		{generation: math.MaxUint, exponent: 1.0, minDelay: 100 * time.Millisecond, maxDelay: 500 * time.Millisecond, expectedBackoff: 100 * time.Millisecond},
	}

	for _, testCase := range testCases {
		testName := fmt.Sprintf("%+v", testCase)
		t.Run(testName, func(t *testing.T) {
			backoff := expBackoff(testCase.generation, testCase.exponent, testCase.minDelay, testCase.maxDelay)
			require.InDelta(t, testCase.expectedBackoff, backoff, 0.01)
		})
	}
}
