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

package etcdserver

import (
	"fmt"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc/metadata"
	"net/http"
)

const (
	entriesBufMax           = 10000
	httpPrefixDebug         = "/debug"
	hoursBeforeStartMax     = 24
	replayStreamChannelSize = 1000
)

type kvRequestType int
type kvResponseType int

const (
	kvReqTypeUnknown = kvRequestType(iota)
	kvReqTypePut
	kvReqTypeRange
	kvReqTypeTxn
	kvReqTypeDelete
	kvReqTypeCompact
)

const (
	kvRespTypeUnknown = kvResponseType(iota)
	kvRespTypePut
	kvRespTypeRange
	kvRespTypeTxn
	kvRespTypeDelete
	kvRespTypeCompact
)

type kvDataType int

const (
	kvDumpDataTypeUnknown = kvDataType(iota)
	kvDumpDataTypeUnary
	kvDumpDataTypeStream
)

// StreamEntry is used by KVRequestDumpEntry to save stream request entry
type StreamEntry struct {
	SteamID int64
	Recv    interface{}
	Send    interface{}
}

// UnaryEntry is used by KVRequestDumpEntry to save unary request entry
type UnaryEntry struct {
	End          int64
	Method       string
	Metadata     metadata.MD
	RequestType  kvRequestType
	Request      interface{}
	ResponseType kvResponseType
	Response     interface{}
}

// KVRequestDumpEntry is the data inside entriesCh
type KVRequestDumpEntry struct {
	Time   int64
	Type   kvDataType
	Unary  UnaryEntry
	Stream StreamEntry
}

// StreamData is used by KVRequestDumpData to save stream request data
type StreamData struct {
	SteamID int64
	Recv    []byte
	Send    []byte
}

// UnaryData is used by KVRequestDumpData to save unary request data
type UnaryData struct {
	End          int64
	Method       string
	Metadata     metadata.MD
	RequestType  kvRequestType
	Request      []byte
	ResponseType kvResponseType
	Response     []byte
}

// KVRequestDumpData is the actual type of data saved in
// dump file in json format
type KVRequestDumpData struct {
	Time   int64
	Type   kvDataType
	Unary  UnaryData
	Stream StreamData
}

func serveError(w http.ResponseWriter, status int, txt string) {
	// failure response
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Del("Content-Disposition")
	w.WriteHeader(status)
	fmt.Fprintln(w, txt)
}

// dataToEntry converts KVRequestDumpData to KVRequestDumpEntry
func dataToEntry(raw *KVRequestDumpData) (*KVRequestDumpEntry, error) {
	switch raw.Type {
	case kvDumpDataTypeStream:
		entry := KVRequestDumpEntry{
			Time:  raw.Time,
			Type:  kvDumpDataTypeStream,
			Unary: UnaryEntry{},
			Stream: StreamEntry{
				SteamID: raw.Stream.SteamID,
			},
		}
		if len(raw.Stream.Recv) != 0 {
			req := pb.WatchRequest{}
			if req.Unmarshal(raw.Stream.Recv) != nil {
				return nil, fmt.Errorf("failed to unmarshal data")
			}
			entry.Stream.Recv = &req
		} else {
			resp := pb.WatchResponse{}
			if resp.Unmarshal(raw.Stream.Send) != nil {
				return nil, fmt.Errorf("failed to unmarshal data")
			}
			entry.Stream.Send = &resp
		}
		return &entry, nil
	case kvDumpDataTypeUnary:
		entry := KVRequestDumpEntry{
			Time: raw.Time,
			Type: kvDumpDataTypeUnary,
			Unary: UnaryEntry{
				End:          raw.Unary.End,
				Method:       raw.Unary.Method,
				Metadata:     raw.Unary.Metadata,
				RequestType:  raw.Unary.RequestType,
				Request:      nil,
				ResponseType: raw.Unary.ResponseType,
				Response:     nil,
			},
		}
		switch raw.Unary.RequestType {
		case kvReqTypePut:
			req := pb.PutRequest{}
			if req.Unmarshal(raw.Unary.Request) != nil {
				return nil, fmt.Errorf("failed to unmarshal data")
			}
			entry.Unary.Request = &req
		case kvReqTypeRange:
			req := pb.RangeRequest{}
			if req.Unmarshal(raw.Unary.Request) != nil {
				return nil, fmt.Errorf("failed to unmarshal data")
			}
			entry.Unary.Request = &req
		case kvReqTypeTxn:
			req := pb.TxnRequest{}
			if req.Unmarshal(raw.Unary.Request) != nil {
				return nil, fmt.Errorf("failed to unmarshal data")
			}
			entry.Unary.Request = &req
		case kvReqTypeDelete:
			req := pb.DeleteRangeRequest{}
			if req.Unmarshal(raw.Unary.Request) != nil {
				return nil, fmt.Errorf("failed to unmarshal data")
			}
			entry.Unary.Request = &req
		case kvReqTypeCompact:
			req := pb.CompactionRequest{}
			if req.Unmarshal(raw.Unary.Request) != nil {
				return nil, fmt.Errorf("failed to unmarshal data")
			}
			entry.Unary.Request = &req
		}
		switch raw.Unary.ResponseType {
		case kvRespTypePut:
			resp := pb.PutResponse{}
			if resp.Unmarshal(raw.Unary.Response) != nil {
				return nil, fmt.Errorf("failed to unmarshal data")
			}
			entry.Unary.Response = &resp
		case kvRespTypeRange:
			resp := pb.RangeResponse{}
			if resp.Unmarshal(raw.Unary.Response) != nil {
				return nil, fmt.Errorf("failed to unmarshal data")
			}
			entry.Unary.Response = &resp
		case kvRespTypeTxn:
			resp := pb.TxnResponse{}
			if resp.Unmarshal(raw.Unary.Response) != nil {
				return nil, fmt.Errorf("failed to unmarshal data")
			}
			entry.Unary.Response = &resp
		case kvRespTypeDelete:
			resp := pb.DeleteRangeResponse{}
			if resp.Unmarshal(raw.Unary.Response) != nil {
				return nil, fmt.Errorf("failed to unmarshal data")
			}
			entry.Unary.Response = &resp
		case kvRespTypeCompact:
			resp := pb.CompactionResponse{}
			if resp.Unmarshal(raw.Unary.Response) != nil {
				return nil, fmt.Errorf("failed to unmarshal data")
			}
			entry.Unary.Response = &resp
		}
		return &entry, nil
	}
	return nil, fmt.Errorf("invalid dump data type")
}


// entryToData converts KVRequestDumpEntry to KVRequestDumpData
func entryToData(entry *KVRequestDumpEntry) (*KVRequestDumpData, error) {
	switch entry.Type {
	case kvDumpDataTypeStream:
		// stream data
		data := KVRequestDumpData{
			Time:  entry.Time,
			Type:  kvDumpDataTypeStream,
			Unary: UnaryData{},
			Stream: StreamData{
				SteamID: entry.Stream.SteamID,
				Recv:    nil,
				Send:    nil,
			},
		}
		if entry.Stream.Send != nil {
			wr := entry.Stream.Send.(*pb.WatchResponse)
			val, err := wr.Marshal()
			if err != nil {
				return nil, fmt.Errorf("failed to marshal data")
			}
			data.Stream.Send = val
		} else {
			wr := entry.Stream.Recv.(*pb.WatchRequest)
			val, err := wr.Marshal()
			if err != nil {
				return nil, fmt.Errorf("failed to marshal data")
			}
			data.Stream.Recv = val
		}
		return &data, nil
	case kvDumpDataTypeUnary:
		// unary data
		data := KVRequestDumpData{
			Time: entry.Time,
			Type: kvDumpDataTypeUnary,
			Unary: UnaryData{
				End:      entry.Unary.End,
				Method:   entry.Unary.Method,
				Metadata: entry.Unary.Metadata,
				Request:  nil,
				Response: nil,
			},
		}
		switch tmp := entry.Unary.Request.(type) {
		case *pb.PutRequest:
			data.Unary.RequestType = kvReqTypePut
			val, err := tmp.Marshal()
			if err != nil {
				return nil, fmt.Errorf("failed to marshal data")
			}
			data.Unary.Request = val
		case *pb.RangeRequest:
			data.Unary.RequestType = kvReqTypeRange
			val, err := tmp.Marshal()
			if err != nil {
				return nil, fmt.Errorf("failed to marshal data")
			}
			data.Unary.Request = val
		case *pb.TxnRequest:
			data.Unary.RequestType = kvReqTypeTxn
			val, err := tmp.Marshal()
			if err != nil {
				return nil, fmt.Errorf("failed to marshal data")
			}
			data.Unary.Request = val
		case *pb.DeleteRangeRequest:
			data.Unary.RequestType = kvReqTypeDelete
			val, err := tmp.Marshal()
			if err != nil {
				return nil, fmt.Errorf("failed to marshal data")
			}
			data.Unary.Request = val
		case *pb.CompactionRequest:
			data.Unary.RequestType = kvReqTypeCompact
			val, err := tmp.Marshal()
			if err != nil {
				return nil, fmt.Errorf("failed to marshal data")
			}
			data.Unary.Request = val
		}
		switch tmp := entry.Unary.Response.(type) {
		case *pb.PutResponse:
			data.Unary.ResponseType = kvRespTypePut
			if tmp == nil {
				break
			}
			val, err := tmp.Marshal()
			if err != nil {
				return nil, fmt.Errorf("failed to marshal data")
			}
			data.Unary.Response = val
		case *pb.RangeResponse:
			data.Unary.ResponseType = kvRespTypeRange
			if tmp == nil {
				break
			}
			val, err := tmp.Marshal()
			if err != nil {
				return nil, fmt.Errorf("failed to marshal data")
			}
			data.Unary.Response = val
		case *pb.TxnResponse:
			data.Unary.ResponseType = kvRespTypeTxn
			if tmp == nil {
				break
			}
			val, err := tmp.Marshal()
			if err != nil {
				return nil, fmt.Errorf("failed to marshal data")
			}
			data.Unary.Response = val
		case *pb.DeleteRangeResponse:
			data.Unary.ResponseType = kvRespTypeDelete
			if tmp == nil {
				break
			}
			val, err := tmp.Marshal()
			if err != nil {
				return nil, fmt.Errorf("failed to marshal data")
			}
			data.Unary.Response = val
		case *pb.CompactionResponse:
			data.Unary.ResponseType = kvRespTypeCompact
			if tmp == nil {
				break
			}
			val, err := tmp.Marshal()
			if err != nil {
				return nil, fmt.Errorf("failed to marshal data")
			}
			data.Unary.Response = val
		}
		return &data, nil
	}
	return nil, fmt.Errorf("invalid data type")
}
