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
	"io"
	"net/http"
	"testing"

	"github.com/Masterminds/semver/v3"
	"google.golang.org/protobuf/proto"

	"go.etcd.io/etcd/api/v3/version"
	"go.etcd.io/raft/v3/raftpb"
)

func TestEntry(t *testing.T) {
	tests := []*raftpb.Entry{
		{},
		{Term: new(uint64(1)), Index: new(uint64(1))},
		{Term: new(uint64(1)), Index: new(uint64(1)), Data: []byte("some data")},
	}
	for i, tt := range tests {
		b := &bytes.Buffer{}
		if err := writeEntryTo(b, tt); err != nil {
			t.Errorf("#%d: unexpected write ents error: %v", i, err)
			continue
		}
		var ent raftpb.Entry
		if err := readEntryFrom(b, &ent); err != nil {
			t.Errorf("#%d: unexpected read ents error: %v", i, err)
			continue
		}
		if !proto.Equal(&ent, tt) {
			t.Errorf("#%d: ent = %+v, want %+v", i, &ent, tt)
		}
	}
}

func TestCompareMajorMinorVersion(t *testing.T) {
	tests := []struct {
		va, vb *semver.Version
		w      int
	}{
		// equal to
		{
			semver.MustParse("2.1.0"),
			semver.MustParse("2.1.0"),
			0,
		},
		// smaller than
		{
			semver.MustParse("2.0.0"),
			semver.MustParse("2.1.0"),
			-1,
		},
		// bigger than
		{
			semver.MustParse("2.2.0"),
			semver.MustParse("2.1.0"),
			1,
		},
		// ignore patch
		{
			semver.MustParse("2.1.1"),
			semver.MustParse("2.1.0"),
			0,
		},
		// ignore prerelease
		{
			semver.MustParse("2.1.0-alpha.0"),
			semver.MustParse("2.1.0"),
			0,
		},
	}
	for i, tt := range tests {
		if g := compareMajorMinorVersion(tt.va, tt.vb); g != tt.w {
			t.Errorf("#%d: compare = %d, want %d", i, g, tt.w)
		}
	}
}

func TestServerVersion(t *testing.T) {
	tests := []struct {
		h  http.Header
		wv *semver.Version
	}{
		// backward compatibility with etcd 2.0
		{
			http.Header{},
			semver.MustParse("2.0.0"),
		},
		{
			http.Header{"X-Server-Version": []string{"2.1.0"}},
			semver.MustParse("2.1.0"),
		},
		{
			http.Header{"X-Server-Version": []string{"v2.1.0"}},
			semver.MustParse("v2.1.0"),
		},
		{
			http.Header{"X-Server-Version": []string{"2.1.0-alpha.0+git"}},
			semver.MustParse("2.1.0-alpha.0+git"),
		},
	}
	for i, tt := range tests {
		v := serverVersion(tt.h)
		if v.String() != tt.wv.String() {
			t.Errorf("#%d: version = %s, want %s", i, v, tt.wv)
		}
	}
}

func TestMinClusterVersion(t *testing.T) {
	tests := []struct {
		h  http.Header
		wv *semver.Version
	}{
		// backward compatibility with etcd 2.0
		{
			http.Header{},
			semver.MustParse("2.0.0"),
		},
		{
			http.Header{"X-Min-Cluster-Version": []string{"2.1.0"}},
			semver.MustParse("2.1.0"),
		},
		{
			http.Header{"X-Min-Cluster-Version": []string{"2.1.0-alpha.0+git"}},
			semver.MustParse("2.1.0-alpha.0+git"),
		},
	}
	for i, tt := range tests {
		v := minClusterVersion(tt.h)
		if v.String() != tt.wv.String() {
			t.Errorf("#%d: version = %s, want %s", i, v, tt.wv)
		}
	}
}

func TestCheckVersionCompatibility(t *testing.T) {
	ls := semver.MustParse(version.Version)
	lmc := semver.MustParse(version.MinClusterVersion)
	tests := []struct {
		server     *semver.Version
		minCluster *semver.Version
		wok        bool
	}{
		// the same version as local
		{
			ls,
			lmc,
			true,
		},
		// one version lower
		{
			lmc,
			semver.New(0, 0, 0, "", ""),
			true,
		},
		// one version higher
		{
			semver.New(ls.Major()+1, 0, 0, "", ""),
			ls,
			true,
		},
		// too low version
		{
			semver.New(lmc.Major()-1, 0, 0, "", ""),
			semver.New(0, 0, 0, "", ""),
			false,
		},
		// too high version
		{
			semver.New(ls.Major()+1, 1, 0, "", ""),
			semver.New(ls.Major()+1, 0, 0, "", ""),
			false,
		},
	}
	for i, tt := range tests {
		_, _, err := checkVersionCompatibility("", tt.server, tt.minCluster)
		if ok := err == nil; ok != tt.wok {
			t.Errorf("#%d: ok = %v, want %v", i, ok, tt.wok)
		}
	}
}

func writeEntryTo(w io.Writer, ent *raftpb.Entry) error {
	size := proto.Size(ent)
	if err := binary.Write(w, binary.BigEndian, uint64(size)); err != nil {
		return err
	}
	b, err := proto.Marshal(ent)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

func readEntryFrom(r io.Reader, ent *raftpb.Entry) error {
	var l uint64
	if err := binary.Read(r, binary.BigEndian, &l); err != nil {
		return err
	}
	buf := make([]byte, int(l))
	if _, err := io.ReadFull(r, buf); err != nil {
		return err
	}
	return proto.Unmarshal(buf, ent)
}
