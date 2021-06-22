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
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"sync"
)

// DumpInfo contains KV request dump information
type DumpInfo struct {
	// protect dump operation
	mu         sync.RWMutex
	inProgress bool
	// nextStreamId is used to assign a unique id for
	// each newly created watch stream
	nextStreamId int64
	// current dump file
	dumpFile *os.File
	// dump data channel
	entriesCh   chan *KVRequestDumpEntry
	entriesChMu sync.RWMutex
	// open streams
	streams []*wrappedStream
}

var dumpInfo DumpInfo

// dumpKVRequestEntries read entries from channel and write them to compressed
// http response
func dumpKVRequestEntries(w io.Writer, ch chan *KVRequestDumpEntry) {
	gzipWriter, err := gzip.NewWriterLevel(w, gzip.BestSpeed)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not enable gzip writer\n")
	}

	for entry := range ch {
		data, err := entryToData(entry)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to parse data")
		}
		if gzipWriter == nil {
			continue
		}
		if raw, err := json.Marshal(data); err == nil {
			_, err := gzipWriter.Write(append(raw, "\n"...))
			if err != nil {
				fmt.Fprintf(os.Stderr, "failed to write data\n")
			}
		}
	}

	// we have finished reading all the dump data
	if gzipWriter != nil && gzipWriter.Close() != nil {
		fmt.Fprintf(os.Stderr, "failed to close gzip writer")
	}
}

// DumpStart starts dumping grpc requests
func DumpStart() error {
	dumpInfo.mu.Lock()
	defer dumpInfo.mu.Unlock()

	if dumpInfo.inProgress == true {
		return fmt.Errorf("another dump is in progress, stop it first")
	}
	dumpInfo.inProgress = true

	// create a new channel to receive dump data
	// intercepted grpc calls will send dump data to
	// this newly created channel
	newCh := make(chan *KVRequestDumpEntry, entriesBufMax)
	dumpInfo.entriesChMu.Lock()
	oldCh := dumpInfo.entriesCh
	dumpInfo.entriesCh = newCh
	dumpInfo.entriesChMu.Unlock()
	if oldCh != nil {
		close(oldCh)
	}

	// open a temp file to save temporary data
	f, err := ioutil.TempFile("./", "etcd-tmp-dump.*.gz")
	if err != nil {
		return fmt.Errorf("could not open temp file")
	}
	if dumpInfo.dumpFile != nil {
		if dumpInfo.dumpFile.Close() != nil {
			return fmt.Errorf("failed to close dump file")
		}
	}
	dumpInfo.dumpFile = f

	// start capturing data
	go dumpKVRequestEntries(f, newCh)
	return nil
}

// DumpStop stops dumping grpc requests
func DumpStop() error {
	dumpInfo.mu.Lock()
	defer dumpInfo.mu.Unlock()

	if dumpInfo.inProgress == false {
		return fmt.Errorf("no dump is running, start it first")
	}

	// close existing channel
	dumpInfo.entriesChMu.Lock()
	oldCh := dumpInfo.entriesCh
	dumpInfo.entriesCh = nil
	dumpInfo.entriesChMu.Unlock()
	// close old entry dump channel
	if oldCh != nil {
		close(oldCh)
	}
	// stop all stream captures
	for _, stream := range dumpInfo.streams {
		stream.Finish()
	}
	dumpInfo.streams = []*wrappedStream{}

	// ready to accept next dump request
	dumpInfo.inProgress = false
	return nil
}

// DumpDownload sends dump file back to user
func DumpDownload(w io.Writer) error {
	dumpInfo.mu.RLock()
	defer dumpInfo.mu.RUnlock()
	if dumpInfo.inProgress {
		return fmt.Errorf("a dump is in progress, stop it before downloading")
	}
	// get current dumpfile
	f := dumpInfo.dumpFile
	if f == nil {
		return fmt.Errorf("no dump file available yet")
	}
	// get to the beginning of the file
	if _, err := f.Seek(0, 0); err != nil {
		return fmt.Errorf("cannot get to the beginning of the dump file %s", err)
	}
	// send the dump file back to client side
	if _, err := io.Copy(w, f); err != nil {
		return fmt.Errorf("send dump file back failed: %s", err)
	}
	return nil
}
