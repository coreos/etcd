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
	"fmt"
	"strings"

	"github.com/coreos/go-semver/semver"
	"github.com/golang/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/pkg/v3/pbutil"
	"go.etcd.io/etcd/raft/v3/raftpb"
	"go.etcd.io/etcd/server/v3/wal"
)

func EtcdVersionFromWAL(wal *wal.WAL) *semver.Version {
	_, _, ents, err := wal.ReadAll()
	if err != nil {
		panic(err)
	}
	var maxVer *semver.Version
	for _, ent := range ents {
		maxVer = maxVersion(maxVer, etcdVersionFromEntry(ent))
	}
	return maxVer
}

func etcdVersionFromEntry(ent raftpb.Entry) *semver.Version {
	msgVer := etcdVersionFromMessage(proto.MessageReflect(&ent))
	dataVer := etcdVersionFromData(ent.Type, ent.Data)
	return maxVersion(msgVer, dataVer)
}

func etcdVersionFromData(entryType raftpb.EntryType, data []byte) *semver.Version {
	var msg protoreflect.Message
	switch entryType {
	case raftpb.EntryNormal:
		var raftReq etcdserverpb.InternalRaftRequest
		err := pbutil.Unmarshaler(&raftReq).Unmarshal(data)
		if err != nil {
			return nil
		}
		msg = proto.MessageReflect(&raftReq)
	case raftpb.EntryConfChange:
		var confChange raftpb.ConfChange
		err := pbutil.Unmarshaler(&confChange).Unmarshal(data)
		if err != nil {
			return nil
		}
		msg = proto.MessageReflect(&confChange)
	case raftpb.EntryConfChangeV2:
		var confChange raftpb.ConfChangeV2
		err := pbutil.Unmarshaler(&confChange).Unmarshal(data)
		if err != nil {
			return nil
		}
		msg = proto.MessageReflect(&confChange)
	default:
		panic("unhandled")
	}
	return etcdVersionFromMessage(msg)
}

func etcdVersionFromMessage(m protoreflect.Message) *semver.Version {
	var maxVer *semver.Version
	md := m.Descriptor()
	opts := md.Options().(*descriptorpb.MessageOptions)
	if opts != nil {
		maxVer = maxVersion(maxVer, etcdVersionFromOptionsString(opts.String()))
	}

	m.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		fd := md.Fields().Get(field.Index())
		maxVer = maxVersion(maxVer, etcdVersionFromField(fd))
		switch m := value.Interface().(type) {
		case protoreflect.Message:
			maxVer = maxVersion(maxVer, etcdVersionFromMessage(m))
		case protoreflect.EnumNumber:
			maxVer = maxVersion(maxVer, etcdVersionFromEnum(field.Enum(), m))
		}
		return true
	})
	return maxVer
}

func etcdVersionFromEnum(enum protoreflect.EnumDescriptor, value protoreflect.EnumNumber) *semver.Version {
	var maxVer *semver.Version
	enumOpts := enum.Options().(*descriptorpb.EnumOptions)
	if enumOpts != nil {
		maxVer = maxVersion(maxVer, etcdVersionFromOptionsString(enumOpts.String()))
	}
	valueDesc := enum.Values().Get(int(value))
	valueOpts := valueDesc.Options().(*descriptorpb.EnumValueOptions)
	if valueOpts != nil {
		maxVer = maxVersion(maxVer, etcdVersionFromOptionsString(valueOpts.String()))
	}
	return maxVer
}

func maxVersion(a *semver.Version, b *semver.Version) *semver.Version {
	if a != nil && (b == nil || b.LessThan(*a)) {
		return a
	}
	return b
}

func etcdVersionFromField(fd protoreflect.FieldDescriptor) *semver.Version {
	opts := fd.Options().(*descriptorpb.FieldOptions)
	if opts == nil {
		return nil
	}
	return etcdVersionFromOptionsString(opts.String())
}

func etcdVersionFromOptionsString(opts string) *semver.Version {
	// TODO: Use proto.GetExtention when gogo/protobuf is usable with protoreflect
	msgs := []string{"[versionpb.etcd_version_msg]:", "[versionpb.etcd_version_field]:", "[versionpb.etcd_version_enum]:", "[versionpb.etcd_version_enum_value]:"}
	var end, index int
	for _, msg := range msgs {
		index = strings.Index(opts, msg)
		end = index + len(msg)
		if index != -1 {
			break
		}
	}
	if index == -1 {
		return nil
	}
	var verStr string
	_, err := fmt.Sscanf(opts[end:], "%q", &verStr)
	if err != nil {
		return nil
	}
	ver, err := semver.NewVersion(verStr + ".0")
	if err != nil {
		panic(err)
	}
	return ver
}
