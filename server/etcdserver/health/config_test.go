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

package health

import (
	"testing"
	"time"
)

// TestDefaultConfig_ExpectedValues pins the literal defaults
// documented in the design doc, not the package consts: comparing
// DefaultConfig() against the consts it is populated from cannot
// fail (both sides move together), so the literals here are the
// only guard. Operators depend on the exact values when tuning
// dashboards and alerts, so a "typo" in defaultApplyLagThreshold
// must not go unnoticed.
func TestDefaultConfig_ExpectedValues(t *testing.T) {
	cfg := DefaultConfig()

	cases := []struct {
		name string
		got  any
		want any
	}{
		{"Interval", cfg.Interval, 500 * time.Millisecond},
		{"CheckTimeout", cfg.CheckTimeout, 2 * time.Second},
		{"ApplyLagThreshold", cfg.ApplyLagThreshold, uint64(10000)},
		{"FailureThreshold", cfg.FailureThreshold, 3},
		{"SuccessThreshold", cfg.SuccessThreshold, 2},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, c.got, c.want)
		}
	}
}

// TestDefaultConfig_AsymmetricDebounce ensures the recovery threshold
// stays strictly below the failure threshold. Asymmetric debounce
// (slow to fail, fast to recover) is an explicit design property;
// flipping the two by accident would degrade availability.
func TestDefaultConfig_AsymmetricDebounce(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.SuccessThreshold >= cfg.FailureThreshold {
		t.Errorf("SuccessThreshold (%d) must be < FailureThreshold (%d) for asymmetric debounce",
			cfg.SuccessThreshold, cfg.FailureThreshold)
	}
}

// TestDefaultConfig_ZeroValueFallback: a caller that uses
// `var cfg Config` (zero value) is asking for "everything default".
// Document the precise behavior so embed.Config authors know they
// must use DefaultConfig explicitly.
func TestDefaultConfig_ZeroValueIsNotDefault(t *testing.T) {
	var zero Config
	if zero.Interval == defaultCheckInterval {
		t.Fatal("zero-value Config.Interval unexpectedly equals defaultCheckInterval; " +
			"callers must use DefaultConfig() to opt into package defaults")
	}
}
