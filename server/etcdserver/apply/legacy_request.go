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

package apply

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"
)

// requestV2 contains the part of the removed v2 WAL request needed to detect
// the metadata entries written by etcd 3.5. Such entries are dropped during
// replay. Keeping this type local to the apply package avoids bringing the
// removed public v2 request API back.
type requestV2 struct {
	ID     uint64
	Method string
	Path   string
	Val    string
}

func (r *requestV2) Unmarshal(data []byte) error {
	*r = requestV2{}
	for len(data) > 0 {
		number, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return protowire.ParseError(n)
		}
		data = data[n:]

		switch number {
		case 1:
			if typ != protowire.VarintType {
				return fmt.Errorf("request v2: field ID has wire type %d", typ)
			}
			value, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			r.ID = value
			data = data[n:]
		case 2, 3, 4:
			if typ != protowire.BytesType {
				return fmt.Errorf("request v2: field %d has wire type %d", number, typ)
			}
			value, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			switch number {
			case 2:
				r.Method = string(value)
			case 3:
				r.Path = string(value)
			case 4:
				r.Val = string(value)
			}
			data = data[n:]
		default:
			n := protowire.ConsumeFieldValue(number, typ, data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
		}
	}
	return nil
}

func (r *requestV2) String() string {
	return fmt.Sprintf("RequestV2{ID:%d Method:%q Path:%q}", r.ID, r.Method, r.Path)
}

// isMetadataRequest reports whether the legacy v2 request is one of the two
// metadata writes etcd 3.5 and earlier could persist to the WAL: member
// attribute updates and cluster version updates. Anything else that happens
// to parse as a v2 request is WAL corruption and must not be silently
// dropped.
func (r *requestV2) isMetadataRequest() bool {
	if r.Method != "PUT" {
		return false
	}
	if strings.HasSuffix(r.Path, "/version") && !strings.Contains(r.Path, "/members/") {
		return true
	}
	return strings.HasSuffix(r.Path, "/attributes") && strings.Contains(r.Path, "/members/")
}
