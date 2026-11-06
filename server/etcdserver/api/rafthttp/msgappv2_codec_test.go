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

package rafthttp

import (
	"bytes"
	"encoding/binary"
	"testing"

	"google.golang.org/protobuf/proto"

	"go.etcd.io/etcd/client/pkg/v3/types"
	stats "go.etcd.io/etcd/server/v3/etcdserver/api/v2stats"
	"go.etcd.io/raft/v3/raftpb"
)

func TestMsgAppV2(t *testing.T) {
	tests := []*raftpb.Message{
		&linkHeartbeatMessage,
		{
			Type:    raftpb.MsgApp.Enum(),
			From:    new(uint64(1)),
			To:      new(uint64(2)),
			Term:    new(uint64(1)),
			LogTerm: new(uint64(1)),
			Index:   new(uint64(0)),
			Entries: []*raftpb.Entry{
				{Term: new(uint64(1)), Index: new(uint64(1)), Data: []byte("some data")},
				{Term: new(uint64(1)), Index: new(uint64(2)), Data: []byte("some data")},
				{Term: new(uint64(1)), Index: new(uint64(3)), Data: []byte("some data")},
			},
		},
		// consecutive MsgApp
		{
			Type:    raftpb.MsgApp.Enum(),
			From:    new(uint64(1)),
			To:      new(uint64(2)),
			Term:    new(uint64(1)),
			LogTerm: new(uint64(1)),
			Index:   new(uint64(3)),
			Entries: []*raftpb.Entry{
				{Term: new(uint64(1)), Index: new(uint64(4)), Data: []byte("some data")},
			},
			Commit: new(uint64(0)),
		},
		&linkHeartbeatMessage,
		// consecutive MsgApp after linkHeartbeatMessage
		{
			Type:    raftpb.MsgApp.Enum(),
			From:    new(uint64(1)),
			To:      new(uint64(2)),
			Term:    new(uint64(1)),
			LogTerm: new(uint64(1)),
			Index:   new(uint64(4)),
			Entries: []*raftpb.Entry{
				{Term: new(uint64(1)), Index: new(uint64(5)), Data: []byte("some data")},
			},
			Commit: new(uint64(0)),
		},
		// MsgApp with higher term
		{
			Type:    raftpb.MsgApp.Enum(),
			From:    new(uint64(1)),
			To:      new(uint64(2)),
			Term:    new(uint64(3)),
			LogTerm: new(uint64(1)),
			Index:   new(uint64(5)),
			Entries: []*raftpb.Entry{
				{Term: new(uint64(3)), Index: new(uint64(6)), Data: []byte("some data")},
			},
		},
		&linkHeartbeatMessage,
		// consecutive MsgApp
		{
			Type:    raftpb.MsgApp.Enum(),
			From:    new(uint64(1)),
			To:      new(uint64(2)),
			Term:    new(uint64(3)),
			LogTerm: new(uint64(2)),
			Index:   new(uint64(6)),
			Entries: []*raftpb.Entry{
				{Term: new(uint64(3)), Index: new(uint64(7)), Data: []byte("some data")},
			},
		},
		// consecutive empty MsgApp
		{
			Type:    raftpb.MsgApp.Enum(),
			From:    new(uint64(1)),
			To:      new(uint64(2)),
			Term:    new(uint64(3)),
			LogTerm: new(uint64(2)),
			Index:   new(uint64(7)),
			Entries: nil,
		},
		&linkHeartbeatMessage,
	}
	b := &bytes.Buffer{}
	enc := newMsgAppV2Encoder(b, &stats.FollowerStats{})
	dec := newMsgAppV2Decoder(b, types.ID(2), types.ID(1))

	for i, tt := range tests {
		if err := enc.encode(tt); err != nil {
			t.Errorf("#%d: unexpected encode message error: %v", i, err)
			continue
		}
		m, err := dec.decode()
		if err != nil {
			t.Errorf("#%d: unexpected decode message error: %v", i, err)
			continue
		}
		if !proto.Equal(m, tt) {
			t.Errorf("#%d: message = %+v, want %+v", i, m, tt)
		}
	}
}

// TestMsgAppV2DecodeOversize ensures a peer cannot make the decoder allocate
// (or panic via uint64->int narrowing) by claiming an oversized entry count or
// entry/message length, matching the limit enforced by the message stream.
func TestMsgAppV2DecodeOversize(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{
			name: "entry count",
			data: appendUint64([]byte{msgTypeAppEntries}, ^uint64(0)),
		},
		{
			name: "entry size",
			data: appendUint64(appendUint64([]byte{msgTypeAppEntries}, 1), readBytesLimit+1),
		},
		{
			name: "message size",
			data: appendUint64([]byte{msgTypeApp}, readBytesLimit+1),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec := newMsgAppV2Decoder(bytes.NewReader(tt.data), types.ID(2), types.ID(1))
			if _, err := dec.decode(); err != ErrExceedSizeLimit {
				t.Fatalf("error = %v, want %v", err, ErrExceedSizeLimit)
			}
		})
	}
}

func appendUint64(b []byte, v uint64) []byte {
	return binary.BigEndian.AppendUint64(b, v)
}
