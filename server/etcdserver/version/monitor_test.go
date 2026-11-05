// Copyright 2022 The etcd Authors
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

package version

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/coreos/go-semver/semver"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap/zaptest"

	"go.etcd.io/etcd/api/v3/version"
)

func TestMemberMinimalVersion(t *testing.T) {
	tests := []struct {
		memberVersions map[string]*version.Versions
		wantVersion    *semver.Version
	}{
		{
			map[string]*version.Versions{"a": {Server: "2.0.0"}},
			semver.Must(semver.NewVersion("2.0.0")),
		},
		// unknown
		{
			map[string]*version.Versions{"a": nil},
			nil,
		},
		{
			map[string]*version.Versions{"a": {Server: "2.0.0"}, "b": {Server: "2.1.0"}, "c": {Server: "2.1.0"}},
			semver.Must(semver.NewVersion("2.0.0")),
		},
		{
			map[string]*version.Versions{"a": {Server: "2.1.0"}, "b": {Server: "2.1.0"}, "c": {Server: "2.1.0"}},
			semver.Must(semver.NewVersion("2.1.0")),
		},
		{
			map[string]*version.Versions{"a": nil, "b": {Server: "2.1.0"}, "c": {Server: "2.1.0"}},
			nil,
		},
	}

	for i, tt := range tests {
		monitor := NewMonitor(zaptest.NewLogger(t), &storageMock{
			memberVersions: tt.memberVersions,
		})
		minV := monitor.membersMinimalServerVersion()
		if !reflect.DeepEqual(minV, tt.wantVersion) {
			t.Errorf("#%d: ver = %+v, want %+v", i, minV, tt.wantVersion)
		}
	}
}

func TestDecideStorageVersion(t *testing.T) {
	tests := []struct {
		name                 string
		clusterVersion       *semver.Version
		storageVersion       *semver.Version
		expectStorageVersion *semver.Version
	}{
		{
			name: "No action if cluster version is nil",
		},
		{
			name:                 "Should set storage version if cluster version is set",
			clusterVersion:       &version.V3_5,
			expectStorageVersion: &version.V3_5,
		},
		{
			name:                 "No action if storage version was already set",
			storageVersion:       &version.V3_5,
			expectStorageVersion: &version.V3_5,
		},
		{
			name:                 "No action if storage version equals cluster version",
			clusterVersion:       &version.V3_5,
			storageVersion:       &version.V3_5,
			expectStorageVersion: &version.V3_5,
		},
		{
			name:                 "Should set storage version to cluster version",
			clusterVersion:       &version.V3_6,
			storageVersion:       &version.V3_5,
			expectStorageVersion: &version.V3_6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &storageMock{
				clusterVersion: tt.clusterVersion,
				storageVersion: tt.storageVersion,
			}
			monitor := NewMonitor(zaptest.NewLogger(t), s)
			monitor.UpdateStorageVersionIfNeeded()
			if !reflect.DeepEqual(s.storageVersion, tt.expectStorageVersion) {
				t.Errorf("Unexpected storage version value, got = %+v, want %+v", s.storageVersion, tt.expectStorageVersion)
			}
		})
	}
}

func TestVersionMatchTarget(t *testing.T) {
	tests := []struct {
		name             string
		targetVersion    *semver.Version
		versionMap       map[string]*version.Versions
		expectedFinished bool
	}{
		{
			"When downgrade finished",
			&semver.Version{Major: 3, Minor: 4},
			map[string]*version.Versions{
				"mem1": {Server: "3.4.1", Cluster: "3.4.0"},
				"mem2": {Server: "3.4.2-pre", Cluster: "3.4.0"},
				"mem3": {Server: "v3.4.2", Cluster: "3.4.0"},
			},
			true,
		},
		{
			"When cannot parse peer version",
			&semver.Version{Major: 3, Minor: 4},
			map[string]*version.Versions{
				"mem1": {Server: "3.4", Cluster: "3.4.0"},
				"mem2": {Server: "3.4.2-pre", Cluster: "3.4.0"},
				"mem3": {Server: "3.4.2", Cluster: "3.4.0"},
			},
			false,
		},
		{
			"When downgrade not finished",
			&semver.Version{Major: 3, Minor: 4},
			map[string]*version.Versions{
				"mem1": {Server: "3.4.1", Cluster: "3.4.0"},
				"mem2": {Server: "3.4.2-pre", Cluster: "3.4.0"},
				"mem3": {Server: "3.5.2", Cluster: "3.5.0"},
			},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			monitor := NewMonitor(zaptest.NewLogger(t), &storageMock{
				memberVersions: tt.versionMap,
			})
			actual := monitor.versionsMatchTarget(tt.targetVersion)
			if actual != tt.expectedFinished {
				t.Errorf("expected downgrade finished is %v; got %v", tt.expectedFinished, actual)
			}
		})
	}
}

func TestUpdateClusterVersionIfNeeded(t *testing.T) {
	tests := []struct {
		name                 string
		clusterVersion       *semver.Version
		memberVersions       map[string]*version.Versions
		downgrade            *DowngradeInfo
		expectClusterVersion *semver.Version
		expectError          error
	}{
		{
			name:                 "Default to 3.0 if there are no members",
			expectClusterVersion: &version.V3_0,
		},
		{
			name: "Should pick lowest server version from members",
			memberVersions: map[string]*version.Versions{
				"a": {Server: "3.6.0"},
				"b": {Server: "3.5.0"},
			},
			expectClusterVersion: &version.V3_5,
		},
		{
			name: "Should support not full releases",
			memberVersions: map[string]*version.Versions{
				"b": {Server: "3.5.0-alpha.0"},
			},
			expectClusterVersion: &version.V3_5,
		},
		{
			name: "Sets minimal version when member has broken version",
			memberVersions: map[string]*version.Versions{
				"a": {Server: "3.6.0"},
				"b": {Server: "yyyy"},
			},
			expectClusterVersion: &version.V3_0,
		},
		{
			name: "Should not downgrade cluster version without explicit downgrade request",
			memberVersions: map[string]*version.Versions{
				"a": {Server: "3.5.0"},
				"b": {Server: "3.6.0"},
			},
			clusterVersion:       &version.V3_6,
			expectClusterVersion: &version.V3_6,
		},
		{
			name: "Should not upgrade cluster version if there is still member old member",
			memberVersions: map[string]*version.Versions{
				"a": {Server: "3.5.0"},
				"b": {Server: "3.6.0"},
			},
			clusterVersion:       &version.V3_5,
			expectClusterVersion: &version.V3_5,
		},
		{
			name: "Should upgrade cluster version if all members have upgraded (have higher server version)",
			memberVersions: map[string]*version.Versions{
				"a": {Server: "3.6.0"},
				"b": {Server: "3.6.0"},
			},
			clusterVersion:       &version.V3_5,
			expectClusterVersion: &version.V3_6,
		},
		{
			name: "Should downgrade cluster version if downgrade is set to allow older members to join",
			memberVersions: map[string]*version.Versions{
				"a": {Server: "3.6.0"},
				"b": {Server: "3.6.0"},
			},
			clusterVersion:       &version.V3_6,
			downgrade:            &DowngradeInfo{TargetVersion: "3.5.0", Enabled: true},
			expectClusterVersion: &version.V3_5,
		},
		{
			name: "Don't downgrade below supported range",
			memberVersions: map[string]*version.Versions{
				"a": {Server: "3.6.0"},
				"b": {Server: "3.6.0"},
			},
			clusterVersion:       &version.V3_5,
			downgrade:            &DowngradeInfo{TargetVersion: "3.4.0", Enabled: true},
			expectClusterVersion: &version.V3_5,
			expectError:          fmt.Errorf("invalid downgrade target"),
		},
		{
			name: "Don't downgrade above cluster version",
			memberVersions: map[string]*version.Versions{
				"a": {Server: "3.5.0"},
				"b": {Server: "3.5.0"},
			},
			clusterVersion:       &version.V3_5,
			downgrade:            &DowngradeInfo{TargetVersion: "3.6.0", Enabled: true},
			expectClusterVersion: &version.V3_5,
			expectError:          fmt.Errorf("invalid downgrade target"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &storageMock{
				clusterVersion: tt.clusterVersion,
				memberVersions: tt.memberVersions,
				downgradeInfo:  tt.downgrade,
			}
			monitor := NewMonitor(zaptest.NewLogger(t), s)

			err := monitor.UpdateClusterVersionIfNeeded()
			assert.Equal(t, tt.expectClusterVersion, s.clusterVersion)
			assert.Equal(t, tt.expectError, err)

			// Ensure results are stable
			newVersion, err := monitor.decideClusterVersion()
			assert.Nil(t, newVersion)
			assert.Equal(t, tt.expectError, err)
		})
	}
}

func TestCancelDowngradeIfNeeded(t *testing.T) {
	tests := []struct {
		name            string
		memberVersions  map[string]*version.Versions
		downgrade       *DowngradeInfo
		expectDowngrade *DowngradeInfo
	}{
		{
			name: "No action if there no downgrade in progress",
		},
		{
			name:            "Cancel downgrade if there are no members",
			downgrade:       &DowngradeInfo{TargetVersion: "3.5.0", Enabled: true},
			expectDowngrade: nil,
		},
		// Next entries go through all states that should happen during downgrade
		{
			name: "No action if downgrade was not started",
			memberVersions: map[string]*version.Versions{
				"a": {Cluster: "3.6.0", Server: "3.6.1"},
				"b": {Cluster: "3.6.0", Server: "3.6.2"},
			},
		},
		{
			name: "Continue downgrade if just started",
			memberVersions: map[string]*version.Versions{
				"a": {Cluster: "3.5.0", Server: "3.6.1"},
				"b": {Cluster: "3.5.0", Server: "3.6.2"},
			},
			downgrade:       &DowngradeInfo{TargetVersion: "3.5.0", Enabled: true},
			expectDowngrade: &DowngradeInfo{TargetVersion: "3.5.0", Enabled: true},
		},
		{
			name: "Continue downgrade if there is at least one member with not matching",
			memberVersions: map[string]*version.Versions{
				"a": {Cluster: "3.5.0", Server: "3.5.1"},
				"b": {Cluster: "3.5.0", Server: "3.6.2"},
			},
			downgrade:       &DowngradeInfo{TargetVersion: "3.5.0", Enabled: true},
			expectDowngrade: &DowngradeInfo{TargetVersion: "3.5.0", Enabled: true},
		},
		{
			name: "Cancel downgrade if all members have downgraded",
			memberVersions: map[string]*version.Versions{
				"a": {Cluster: "3.5.0", Server: "3.5.1"},
				"b": {Cluster: "3.5.0", Server: "3.5.2"},
			},
			downgrade:       &DowngradeInfo{TargetVersion: "3.5.0", Enabled: true},
			expectDowngrade: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &storageMock{
				memberVersions: tt.memberVersions,
				downgradeInfo:  tt.downgrade,
			}
			monitor := NewMonitor(zaptest.NewLogger(t), s)

			// Run multiple times to ensure that results are stable
			for i := 0; i < 3; i++ {
				monitor.CancelDowngradeIfNeeded()
				assert.Equal(t, tt.expectDowngrade, s.downgradeInfo)
			}
		})
	}
}

func TestUpdateStorageVersionIfNeeded(t *testing.T) {
	tests := []struct {
		name                 string
		clusterVersion       *semver.Version
		storageVersion       *semver.Version
		expectStorageVersion *semver.Version
	}{
		{
			name: "No action if cluster version is nil",
		},
		{
			name:                 "Should set storage version if cluster version is set",
			clusterVersion:       &version.V3_5,
			expectStorageVersion: &version.V3_5,
		},
		{
			name:                 "No action if storage version was already set",
			storageVersion:       &version.V3_5,
			expectStorageVersion: &version.V3_5,
		},
		{
			name:                 "No action if storage version equals cluster version",
			clusterVersion:       &version.V3_5,
			storageVersion:       &version.V3_5,
			expectStorageVersion: &version.V3_5,
		},
		{
			name:                 "Should set storage version to cluster version",
			clusterVersion:       &version.V3_6,
			storageVersion:       &version.V3_5,
			expectStorageVersion: &version.V3_6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &storageMock{
				clusterVersion: tt.clusterVersion,
				storageVersion: tt.storageVersion,
			}
			monitor := NewMonitor(zaptest.NewLogger(t), s)

			// Run multiple times to ensure that results are stable
			for i := 0; i < 3; i++ {
				monitor.UpdateStorageVersionIfNeeded()
				assert.Equal(t, tt.expectStorageVersion, s.storageVersion)
			}
		})
	}
}

type storageMock struct {
	memberVersions map[string]*version.Versions
	clusterVersion *semver.Version
	storageVersion *semver.Version
	downgradeInfo  *DowngradeInfo
}

var _ Server = (*storageMock)(nil)

func (s *storageMock) UpdateClusterVersion(version string) {
	s.clusterVersion = semver.New(version)
}

func (s *storageMock) LinearizableReadNotify(ctx context.Context) error {
	return nil
}

func (s *storageMock) DowngradeEnable(ctx context.Context, targetVersion *semver.Version) error {
	return nil
}

func (s *storageMock) DowngradeCancel(ctx context.Context) error {
	s.downgradeInfo = nil
	return nil
}

func (s *storageMock) GetClusterVersion() *semver.Version {
	return s.clusterVersion
}

func (s *storageMock) GetDowngradeInfo() *DowngradeInfo {
	return s.downgradeInfo
}

func (s *storageMock) GetMembersVersions() map[string]*version.Versions {
	return s.memberVersions
}

func (s *storageMock) GetStorageVersion() *semver.Version {
	return s.storageVersion
}

func (s *storageMock) UpdateStorageVersion(v semver.Version) error {
	s.storageVersion = &v
	return nil
}
