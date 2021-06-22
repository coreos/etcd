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
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.uber.org/zap"
	"io"
	"io/ioutil"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

type ReplayStats struct {
	Name                      string
	ReplayStartTime           time.Time
	ReplayStartTimeOffset     int64
	ReplayRealStartTime       time.Time
	ReplayRealStartTimeOffset int64
	FirstEntryTime            time.Time
	TotalOps                  uint64
	SuccessOps                uint64
	FailedOps                 uint64
}

// ReplayInfo contains KV request replay information
type ReplayInfo struct {
	mu         sync.RWMutex
	inProgress bool
	// replay data channel
	replayCh     chan *KVRequestDumpEntry
	// current replay result dump
	res         *os.File
	watchServer pb.WatchServer
	// current replay stats
	stats ReplayStats
}

var replayInfo ReplayInfo

// SetWatchServer is a watch server for us to replay watch data
func SetWatchServer(ws pb.WatchServer) {
	if replayInfo.watchServer == nil {
		replayInfo.watchServer = ws
	}
}

// wrappedSteam is used to intercept grpc streams
type replayStream struct {
	pb.Watch_WatchServer
	id     int64
	sendCh chan *pb.WatchResponse
	recvCh chan *pb.WatchRequest
	ctx    context.Context
	cancel context.CancelFunc
	f      *os.File
}

func (r *replayStream) Context() context.Context {
	return r.ctx
}

// Recv is for replay watch stream to receive data
func (r *replayStream) Recv() (*pb.WatchRequest, error) {
	req, ok := <-r.recvCh
	if !ok {
		return nil, io.EOF
	}
	return req, nil
}

// Send is for replay watch stream to send data
func (r *replayStream) Send(m *pb.WatchResponse) error {
	resp, err := m.Marshal()
	if err != nil {
		return fmt.Errorf("failed to marshal data")
	}
	data := &KVRequestDumpData{
		Time:  time.Now().UnixNano(),
		Type:  kvDumpDataTypeStream,
		Unary: UnaryData{},
		Stream: StreamData{
			SteamID: r.id,
			Recv:    nil,
			Send:    resp,
		},
	}

	if raw, err := json.Marshal(data); err == nil {
		if _, err := r.f.Write(append(raw, "\n"...)); err != nil {
			return fmt.Errorf("failed to write data\n")
		}
	}
	return nil
}

// newReplayStream creates a new replay stream for us to replay watch data
func newReplayStream(id int64, f *os.File) *replayStream {
	ret := &replayStream{id: id}
	ret.sendCh = make(chan *pb.WatchResponse, replayStreamChannelSize)
	ret.recvCh = make(chan *pb.WatchRequest, replayStreamChannelSize)
	ret.ctx, ret.cancel = context.WithCancel(context.Background())
	ret.f = f
	go func(f *os.File) {
		for range ret.sendCh {
			// we do nothing here
		}
	}(replayInfo.res)
	return ret
}

func KvRequestReplayOneTask(s *EtcdServer, ch chan *KVRequestDumpEntry)  {
	var startTime int64
	var firstEntryTime int64
	streams := map[int64]*replayStream{}

	lg := s.Logger()
	wg := sync.WaitGroup{}

	for entry := range ch {
		if replayInfo.stats.TotalOps == 0 {
			// calculate the first entry sending time
			firstEntryTime = entry.Time
			startTime = firstEntryTime + replayInfo.stats.ReplayRealStartTimeOffset

			// initialize stats
			replayInfo.stats.ReplayStartTime = time.Now()
			replayInfo.stats.ReplayStartTimeOffset = replayInfo.stats.ReplayStartTime.Sub(replayInfo.stats.FirstEntryTime).Nanoseconds()
			replayInfo.stats.FailedOps = 0
			replayInfo.stats.SuccessOps = 0
			replayInfo.stats.TotalOps = 0
			replayInfo.stats.ReplayRealStartTime = time.Unix(0, startTime)
			lg.Info("replay in progress",
				zap.Time("first-entry-time", replayInfo.stats.FirstEntryTime),
				zap.Time("replay-start-time", replayInfo.stats.ReplayStartTime),
				zap.Time("replay-real-start-time", replayInfo.stats.ReplayRealStartTime))
		}
		replayInfo.stats.TotalOps++
		currentTimeOffset := time.Now().UnixNano() - startTime
		entrySendTimeOffset := entry.Time - firstEntryTime
		if entrySendTimeOffset > currentTimeOffset {
			select {
			case <-s.StoppingNotify():
				return
			case <-time.After(time.Duration(entrySendTimeOffset - currentTimeOffset)):
			}
		}
		switch entry.Type {
		case kvDumpDataTypeStream:
			// process stream dump data
			if _, ok := streams[entry.Stream.SteamID]; !ok {
				tmpStream := newReplayStream(entry.Stream.SteamID, replayInfo.res)
				wg.Add(1)
				go func(wg *sync.WaitGroup, stream *replayStream) {
					defer wg.Done()
					replayInfo.watchServer.Watch(stream)
				}(&wg, tmpStream)
				streams[entry.Stream.SteamID] = tmpStream
			}
			if entry.Stream.Recv != nil {
				streams[entry.Stream.SteamID].recvCh <- entry.Stream.Recv.(*pb.WatchRequest)
			}
			if entry.Stream.Send != nil {
				streams[entry.Stream.SteamID].sendCh <- entry.Stream.Send.(*pb.WatchResponse)
			}
		case kvDumpDataTypeUnary:
			// process unary dump data
			switch entry.Unary.RequestType {
			case kvReqTypePut:
				wg.Add(1)
				go func(entry *KVRequestDumpEntry, f *os.File, wg *sync.WaitGroup) {
					defer wg.Done()
					ctx := context.Background()
					resp, err := s.Put(ctx, entry.Unary.Request.(*pb.PutRequest))
					if err != nil {
						lg.Error("failed put request")
						atomic.AddUint64(&replayInfo.stats.FailedOps, 1)
					} else {
						atomic.AddUint64(&replayInfo.stats.SuccessOps, 1)
					}
					entry.Unary.ResponseType = kvRespTypePut
					entry.Unary.Response = resp
					data, err := entryToData(entry)
					if err != nil {
						lg.Error("failed to parse data")
						return
					}
					if raw, err := json.Marshal(data); err == nil {
						if _, err := f.Write(append(raw, "\n"...)); err != nil {
							lg.Error("failed to write data\n")
						}
					}
				}(entry, replayInfo.res, &wg)
			case kvReqTypeRange:
				wg.Add(1)
				go func(entry *KVRequestDumpEntry, f *os.File, wg *sync.WaitGroup) {
					defer wg.Done()
					ctx := context.Background()
					resp, err := s.Range(ctx, entry.Unary.Request.(*pb.RangeRequest))
					if err != nil {
						lg.Error("failed range request")
						atomic.AddUint64(&replayInfo.stats.FailedOps, 1)
					} else {
						atomic.AddUint64(&replayInfo.stats.SuccessOps, 1)
					}
					entry.Unary.ResponseType = kvRespTypeRange
					entry.Unary.Response = resp
					data, err := entryToData(entry)
					if err != nil {
						lg.Error("failed to parse data")
						return
					}
					if raw, err := json.Marshal(data); err == nil {
						if _, err := f.Write(append(raw, "\n"...)); err != nil {
							lg.Error("failed to write data\n")
						}
					}
				}(entry, replayInfo.res, &wg)
			case kvReqTypeTxn:
				wg.Add(1)
				go func(entry *KVRequestDumpEntry, f *os.File, wg *sync.WaitGroup) {
					defer wg.Done()
					ctx := context.Background()
					resp, err := s.Txn(ctx, entry.Unary.Request.(*pb.TxnRequest))
					if err != nil {
						lg.Error("failed txn request")
						atomic.AddUint64(&replayInfo.stats.FailedOps, 1)
					} else {
						atomic.AddUint64(&replayInfo.stats.SuccessOps, 1)
					}
					entry.Unary.ResponseType = kvRespTypeTxn
					entry.Unary.Response = resp
					data, err := entryToData(entry)
					if err != nil {
						lg.Error("failed to parse data")
						return
					}
					if raw, err := json.Marshal(data); err == nil {
						if _, err := f.Write(append(raw, "\n"...)); err != nil {
							lg.Error("failed to write data\n")
						}
					}
				}(entry, replayInfo.res, &wg)
			case kvReqTypeDelete:
				wg.Add(1)
				go func(entry *KVRequestDumpEntry, f *os.File, wg *sync.WaitGroup) {
					defer wg.Done()
					ctx := context.Background()
					resp, err := s.DeleteRange(ctx, entry.Unary.Request.(*pb.DeleteRangeRequest))
					if err != nil {
						lg.Error("failed delete request")
						atomic.AddUint64(&replayInfo.stats.FailedOps, 1)
					} else {
						atomic.AddUint64(&replayInfo.stats.SuccessOps, 1)
					}
					entry.Unary.ResponseType = kvRespTypeDelete
					entry.Unary.Response = resp
					data, err := entryToData(entry)
					if err != nil {
						lg.Error("failed to parse data")
						return
					}
					if raw, err := json.Marshal(data); err == nil {
						if _, err := f.Write(append(raw, "\n"...)); err != nil {
							lg.Error("failed to write data\n")
						}
					}
				}(entry, replayInfo.res, &wg)
			case kvReqTypeCompact:
				wg.Add(1)
				go func(entry *KVRequestDumpEntry, f *os.File, wg *sync.WaitGroup) {
					defer wg.Done()
					ctx := context.Background()
					resp, err := s.Compact(ctx, entry.Unary.Request.(*pb.CompactionRequest))
					if err != nil {
						lg.Error("failed compact request")
						atomic.AddUint64(&replayInfo.stats.FailedOps, 1)
					} else {
						atomic.AddUint64(&replayInfo.stats.SuccessOps, 1)
					}
					entry.Unary.ResponseType = kvRespTypeCompact
					entry.Unary.Response = resp
					data, err := entryToData(entry)
					if err != nil {
						lg.Error("failed to parse data")
						return
					}
					if raw, err := json.Marshal(data); err == nil {
						if _, err := f.Write(append(raw, "\n"...)); err != nil {
							lg.Error("failed to write data\n")
						}
					}
				}(entry, replayInfo.res, &wg)
			}
		}
	}

	// cleanup
	for _, stream := range streams {
		stream.cancel()
		close(stream.sendCh)
		close(stream.recvCh)
	}

	wg.Wait()
	lg.Info("finished grpc replay", zap.Uint64("success", replayInfo.stats.SuccessOps),
		zap.Uint64("failure", replayInfo.stats.FailedOps))

	// replay is done
	replayInfo.inProgress = false
}

// KvRequestReplayWorker is launched by EtcdServer. It will wait for incoming replay requests
func KvRequestReplayWorker(s *EtcdServer) {
	for {
		// wait till new replay is coming
		select {
		case <-s.StoppingNotify():
			return
		case <-time.After(time.Second):
			if replayInfo.inProgress {
				KvRequestReplayOneTask(s, replayInfo.replayCh)
			}
		}
	}
}

// replayStart start processing the dump data and replay the requests
func replayStart(f *os.File, timeNanoOffset int64, name string) error {
	replayInfo.mu.Lock()
	defer replayInfo.mu.Unlock()

	if replayInfo.inProgress {
		return fmt.Errorf("replay already in progress")
	}
	replayInfo.inProgress = true

	newCh := make(chan *KVRequestDumpEntry, entriesBufMax)
	oldCh := replayInfo.replayCh
	replayInfo.replayCh = newCh
	if oldCh != nil {
		close(oldCh)
	}

	// save the uploaded filename
	replayInfo.stats.Name = name
	replayInfo.stats.ReplayRealStartTimeOffset = timeNanoOffset
	// open a temp file to write replay result
	tmpFile, err := ioutil.TempFile("", "replay_result.*.gz")
	if err != nil {
		return fmt.Errorf("cannot open result file for saving result")
	}
	if replayInfo.res != nil {
		replayInfo.res.Close()
	}
	replayInfo.res = tmpFile

	go kvRequestReplayDataReader(f, timeNanoOffset, newCh)
	return nil
}

// kvRequestReplayDataReader reads data from dump file and send loaded
// data to replay goroutine.
// kvRequestReplayDataReader -> KvRequestReplayWorker
func kvRequestReplayDataReader(f *os.File, timeNanoOffset int64, ch chan *KVRequestDumpEntry) {
	defer f.Close()
	defer close(ch)
	r, err := gzip.NewReader(f)
	if err != nil {
		// wrong file format
		fmt.Fprintf(os.Stderr, "failed to open reader")
		return
	}
	defer r.Close()

	var counter int64
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		var raw KVRequestDumpData
		if err := json.Unmarshal(scanner.Bytes(), &raw); err != nil {
			fmt.Fprintf(os.Stderr, "invalid data")
			continue
		}

		if counter == 0 {
			if timeNanoOffset <= 0 {
				timeNanoOffset = time.Now().UnixNano() - raw.Time
			}
			replayInfo.stats.FirstEntryTime = time.Unix(0, raw.Time)
		}
		counter++

		entry, err:= dataToEntry(&raw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to parse dump data")
			break
		}

		switch entry.Type {
		case kvDumpDataTypeStream:
			entry.Time += timeNanoOffset
		case kvDumpDataTypeUnary:
			entry.Time += timeNanoOffset
			entry.Unary.End += timeNanoOffset
		}
		ch <- entry
	}
}


