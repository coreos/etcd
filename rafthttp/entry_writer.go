/*
   Copyright 2014 CoreOS, Inc.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package rafthttp

import (
	"encoding/binary"
	"io"

	"github.com/coreos/etcd/pkg/metrics"
	"github.com/coreos/etcd/raft/raftpb"
)

type entryWriter struct {
	w             io.Writer
	entriesSent   metrics.Counter
	bytesSent     metrics.Counter
	lastIndexSent metrics.Gauge
}

func newEntryWriter(w io.Writer, mg *metrics.Group) *entryWriter {
	return &entryWriter{
		w:             w,
		entriesSent:   mg.Counter("entries_sent"),
		bytesSent:     mg.Counter("bytes_sent"),
		lastIndexSent: mg.Gauge("last_index_sent"),
	}
}

func (ew *entryWriter) writeEntries(ents []raftpb.Entry) error {
	l := len(ents)
	if err := binary.Write(ew.w, binary.BigEndian, uint64(l)); err != nil {
		return err
	}
	ew.bytesSent.Add(8)
	for i := 0; i < l; i++ {
		if err := ew.writeEntry(&ents[i]); err != nil {
			return err
		}
	}
	ew.entriesSent.Add(int64(len(ents)))
	ew.lastIndexSent.Set(int64(ents[len(ents)-1].Index))
	return nil
}

func (ew *entryWriter) writeEntry(ent *raftpb.Entry) error {
	size := ent.Size()
	if err := binary.Write(ew.w, binary.BigEndian, uint64(size)); err != nil {
		return err
	}
	b, err := ent.Marshal()
	if err != nil {
		return err
	}
	_, err = ew.w.Write(b)
	ew.bytesSent.Add(8 + int64(size))
	return err
}
