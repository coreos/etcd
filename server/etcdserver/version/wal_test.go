// Copyright 2021 The etcd Authors
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
	"go.etcd.io/etcd/api/v3/membershippb"
	"testing"

	"github.com/coreos/go-semver/semver"
	"github.com/golang/protobuf/proto"
	"github.com/stretchr/testify/assert"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/pkg/v3/pbutil"
	"go.etcd.io/etcd/raft/v3/raftpb"
)

var (
	V3_0 = semver.Version{Major: 3, Minor: 0}
	V3_1 = semver.Version{Major: 3, Minor: 1}
	V3_3 = semver.Version{Major: 3, Minor: 3}
	V3_4 = semver.Version{Major: 3, Minor: 4}
)

func TestEtcdVersionFromEntry(t *testing.T) {
	raftReq := etcdserverpb.InternalRaftRequest{Header: &etcdserverpb.RequestHeader{AuthRevision: 1}}
	normalRequestData := pbutil.MustMarshal(&raftReq)

	confChange := raftpb.ConfChange{Type: raftpb.ConfChangeAddLearnerNode}
	confChangeData := pbutil.MustMarshal(&confChange)

	confChangeV2 := raftpb.ConfChangeV2{Transition: raftpb.ConfChangeTransitionJointExplicit}
	confChangeV2Data := pbutil.MustMarshal(&confChangeV2)

	tcs := []struct {
		name   string
		input  raftpb.Entry
		expect *semver.Version
	}{
		{
			name: "Using RequestHeader AuthRevision in NormalEntry implies v3.1",
			input: raftpb.Entry{
				Term:  1,
				Index: 2,
				Type:  raftpb.EntryNormal,
				Data:  normalRequestData,
			},
			expect: &V3_1,
		},
		{
			name: "Using ConfigChange implies v3.4",
			input: raftpb.Entry{
				Term:  1,
				Index: 2,
				Type:  raftpb.EntryConfChange,
				Data:  confChangeData,
			},
			expect: &V3_0,
		},
		{
			name: "Using ConfigChangeV2 implies v3.4",
			input: raftpb.Entry{
				Term:  1,
				Index: 2,
				Type:  raftpb.EntryConfChangeV2,
				Data:  confChangeV2Data,
			},
			expect: &V3_4,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			ver := etcdVersionFromEntry(tc.input)
			assert.Equal(t, tc.expect, ver)
		})
	}
}

func TestEtcdVersionFromMessage(t *testing.T) {
	tcs := []struct {
		name   string
		input  proto.Message
		expect *semver.Version
	}{
		{
			name:   "Empty RequestHeader impies v3.0",
			input:  &etcdserverpb.RequestHeader{},
			expect: &V3_0,
		},
		{
			name:   "RequestHeader AuthRevision field set implies v3.5",
			input:  &etcdserverpb.RequestHeader{AuthRevision: 1},
			expect: &V3_1,
		},
		{
			name:   "RequestHeader Username set implies v3.0",
			input:  &etcdserverpb.RequestHeader{Username: "Alice"},
			expect: &V3_0,
		},
		{
			name:   "When two fields are set take higher version",
			input:  &etcdserverpb.RequestHeader{AuthRevision: 1, Username: "Alice"},
			expect: &V3_1,
		},
		{
			name:   "Setting a RequestHeader AuthRevision in subfield implies v3.1",
			input:  &etcdserverpb.InternalRaftRequest{Header: &etcdserverpb.RequestHeader{AuthRevision: 1}},
			expect: &V3_1,
		},
		{
			name:   "Setting a DowngradeInfoSetRequest implies v3.5",
			input:  &etcdserverpb.InternalRaftRequest{DowngradeInfoSet: &membershippb.DowngradeInfoSetRequest{}},
			expect: &V3_5,
		},
		{
			name:   "Enum CompareResult set to EQUAL implies v3.0",
			input:  &etcdserverpb.Compare{Result: etcdserverpb.Compare_EQUAL},
			expect: &V3_0,
		},
		{
			name:   "Enum CompareResult set to NOT_EQUAL implies v3.1",
			input:  &etcdserverpb.Compare{Result: etcdserverpb.Compare_NOT_EQUAL},
			expect: &V3_1,
		},
		{
			name:   "Oneof Compare version set implies v3.1",
			input:  &etcdserverpb.Compare{TargetUnion: &etcdserverpb.Compare_Version{}},
			expect: &V3_0,
		},
		{
			name:   "Oneof Compare lease set implies v3.3",
			input:  &etcdserverpb.Compare{TargetUnion: &etcdserverpb.Compare_Lease{}},
			expect: &V3_3,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			ver := etcdVersionFromMessage(proto.MessageReflect(tc.input))
			assert.Equal(t, tc.expect, ver)
		})
	}
}

func TestEtcdVersionFromFieldOptionsString(t *testing.T) {
	tcs := []struct {
		input  string
		expect *semver.Version
	}{
		{
			input: "65001:0",
		},
		{
			input: `65001:0  65004:"NodeID"`,
		},
		{
			input: `[versionpb.XXX]:"3.5"`,
		},
		{
			input:  `[versionpb.etcd_version_msg]:"3.5"`,
			expect: &V3_5,
		},
		{
			input:  `[versionpb.etcd_version_enum]:"3.5"`,
			expect: &V3_5,
		},
		{
			input:  `[versionpb.etcd_version_field]:"3.5"`,
			expect: &V3_5,
		},
		{
			input:  `[versionpb.etcd_version_enum_value]:"3.5"`,
			expect: &V3_5,
		},
		{
			input:  `65001:0 [versionpb.etcd_version_msg]:"3.5"`,
			expect: &V3_5,
		},
		{
			input:  `65004:"NodeID" [versionpb.etcd_version_msg]:"3.5"`,
			expect: &V3_5,
		},
		{
			input:  `65004:"NodeID" [versionpb.etcd_version_enum]:"3.5"`,
			expect: &V3_5,
		},
		{
			input:  `[versionpb.other_field]:"NodeID" [versionpb.etcd_version_msg]:"3.5"`,
			expect: &V3_5,
		},
		{
			input:  `[versionpb.etcd_version_msg]:"3.5" 65001:0`,
			expect: &V3_5,
		},
		{
			input:  `[versionpb.etcd_version_msg]:"3.5" 65004:"NodeID"`,
			expect: &V3_5,
		},
		{
			input:  `[versionpb.etcd_version_msg]:"3.5" [versionpb.other_field]:"NodeID"`,
			expect: &V3_5,
		},
		{
			input:  `[versionpb.other_field]:"NodeID" [versionpb.etcd_version_msg]:"3.5" [versionpb.another_field]:"NodeID"`,
			expect: &V3_5,
		},
		{
			input:  `65001:0 [versionpb.etcd_version_msg]:"3.5" 65001:0"`,
			expect: &V3_5,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.input, func(t *testing.T) {
			ver := etcdVersionFromOptionsString(tc.input)
			assert.Equal(t, ver, tc.expect)
		})
	}
}
