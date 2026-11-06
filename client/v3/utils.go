// Copyright 2018 The etcd Authors
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
	"math"
	"math/rand"
	"time"
)

// jitterUp adds random jitter to the duration.
//
// This adds or subtracts time from the duration within a given jitter fraction.
// For example for 10s and jitter 0.1, it will return a time within [9s, 11s])
//
// Reference: https://godoc.org/github.com/grpc-ecosystem/go-grpc-middleware/util/backoffutils
func jitterUp(duration time.Duration, jitter float64) time.Duration {
	multiplier := jitter * (rand.Float64()*2 - 1)
	return time.Duration(float64(duration) * (1 + multiplier))
}

// expBackoff returns an exponential backoff duration.
//
// This will calculate exponential backoff based upon generation and exponent. The backoff is within [minDelay, maxDelay].
// For example, an exponent of 2.0 will double the backoff duration every subsequent generation. A generation of 0 will
// return minDelay.
func expBackoff(generation uint, exponent float64, minDelay, maxDelay time.Duration) time.Duration {
	delay := math.Min(math.Pow(exponent, float64(generation))*float64(minDelay), float64(maxDelay))
	return time.Duration(delay)
}
