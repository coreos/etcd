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

package features

import (
	"testing"

	"go.etcd.io/etcd/pkg/v3/featuregate"
)

// TestServerHealthCheckDefault pins the gate contract while the
// ServerHealthCheck chain is still under development:
//
//   - default-off: the chain must not run in deployments that did not
//     explicitly opt in — enabling is a conscious act
//     (--feature-gates=ServerHealthCheck=true, or an operator rendering
//     that flag for the clusters it manages), so upgrading the binary
//     alone activates nothing;
//   - opt-in: --feature-gates=ServerHealthCheck=true must enable it;
//   - Alpha prerelease: --feature-gates=AllAlpha=true turns it on along
//     with every other Alpha gate — pinning PreRelease=Alpha documents
//     that graduating to default-on is a deliberate future step.
func TestServerHealthCheckDefault(t *testing.T) {
	spec, ok := DefaultEtcdServerFeatureGates[ServerHealthCheck]
	if !ok {
		t.Fatalf("ServerHealthCheck not registered in DefaultEtcdServerFeatureGates")
	}
	if spec.Default {
		t.Errorf("ServerHealthCheck must default to disabled while under development, got Default=true")
	}
	if spec.PreRelease != featuregate.Alpha {
		t.Errorf("ServerHealthCheck must stay Alpha (default-off under development), got %q", spec.PreRelease)
	}

	fg := NewDefaultServerFeatureGate(t.Name(), nil)
	if fg.Enabled(ServerHealthCheck) {
		t.Errorf("default-constructed gate must report ServerHealthCheck disabled without any Set()")
	}
	mfg, ok := fg.(featuregate.MutableFeatureGate)
	if !ok {
		t.Fatalf("NewDefaultServerFeatureGate must return a MutableFeatureGate to support opt-in")
	}
	if err := mfg.Set("ServerHealthCheck=true"); err != nil {
		t.Fatalf("explicit opt-in failed: %v", err)
	}
	if !fg.Enabled(ServerHealthCheck) {
		t.Errorf("ServerHealthCheck=true must enable the gate (opt-in path)")
	}

	fgAllAlpha := NewDefaultServerFeatureGate(t.Name(), nil)
	if err := fgAllAlpha.(featuregate.MutableFeatureGate).Set("AllAlpha=true"); err != nil {
		t.Fatalf("setting AllAlpha=true failed: %v", err)
	}
	if !fgAllAlpha.Enabled(ServerHealthCheck) {
		t.Errorf("AllAlpha=true must enable ServerHealthCheck (Alpha gate)")
	}
}
