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
	"net/http"
	"os"
	"strconv"
)

// KVRequestReplayHandlers accepts a dump file and replay the grpc requests
// to replay a dump, an example command is:
//     curl -F 'file=@path/to/local/dump/file' http://localhost:2379/debug/replay
// to get the stats in json format
//     curl http://localhost:2379/debug/replay/stats
// to get the replay reply result dump file
//     wget -O result.gz http://localhost:2379/debug/replay/result
func KVRequestReplayHandlers() map[string]http.Handler {
	m := make(map[string]http.Handler)
	m[httpPrefixDebug+"/replay/result"] = http.HandlerFunc(kvRequestReplayResult)
	m[httpPrefixDebug+"/replay/stats"] = http.HandlerFunc(kvRequestReplayStats)
	m[httpPrefixDebug+"/replay"] = http.HandlerFunc(kvRequestReplay)
	return m
}

func kvRequestReplayResult(w http.ResponseWriter, r *http.Request) {
	// no browser MIME-type sniffing and plain text response
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// response is a file stream
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment;filename="result.gz"`)

	replayInfo.mu.Lock()
	defer replayInfo.mu.Unlock()
	if replayInfo.inProgress {
		serveError(w, http.StatusInternalServerError, "Replay in progress")
		return
	}

	if replayInfo.res == nil {
		serveError(w, http.StatusInternalServerError, "No result available yet")
		return
	}
	if _, err := replayInfo.res.Seek(0, 0); err != nil {
		serveError(w, http.StatusInternalServerError,
			fmt.Sprintf("Cannot get to the begining of file: %s", err))
		return
	}

	gzipWriter, err := gzip.NewWriterLevel(w, gzip.BestSpeed)
	if err != nil {
		serveError(w, http.StatusInternalServerError,
			fmt.Sprintf("Cannot open gzip writer: %s", err))
		return
	}
	defer gzipWriter.Close()

	if _, err := io.Copy(gzipWriter, replayInfo.res); err != nil {
		serveError(w, http.StatusInternalServerError,
			fmt.Sprintf("Cannot copy data to client: %s", err))
		return
	}
}

func kvRequestReplayStats(w http.ResponseWriter, r *http.Request) {
	// no browser MIME-type sniffing and plain text response
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	data, err := json.Marshal(replayInfo.stats)
	if err != nil {
		return
	}
	w.Write(data)
}

func uploadFile(file io.Reader, size int64) (*os.File, error){
	tmpFile, err := ioutil.TempFile("", "upload-dump.*.gz")
	if err != nil {
		return nil, fmt.Errorf("could not create temp file: %s", err)
	}
	// we cannot close tmpFile and it is used by goroutine inside replayStart

	sz, err := io.Copy(tmpFile, file)
	if err != nil || sz != size {
		return nil, fmt.Errorf("failed to write temp file: %s", err)
	}

	if _, err := tmpFile.Seek(0, 0); err != nil {
		return nil, fmt.Errorf("failed to reach file beginning: %s", err)
	}
	return tmpFile, nil
}

// kvRequestReplay simulate grpc requests by using a dump file
func kvRequestReplay(w http.ResponseWriter, r *http.Request) {
	// no browser MIME-type sniffing and plain text response
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")

	timeMinuteOffset, err := strconv.ParseInt(r.FormValue("minuteOffset"), 10, 64)
	if err != nil {
		timeMinuteOffset = 0
	}

	timeNanoOffset := timeMinuteOffset * 60 * 1000000000

	file, handler, err := r.FormFile("file")
	if err != nil {
		serveError(w, http.StatusInternalServerError,
			fmt.Sprintf("Could not get uploaded file: %s", err))
		return
	}
	defer file.Close()

	tmpFile, err := uploadFile(file, handler.Size)
	if err != nil {
		serveError(w, http.StatusInternalServerError,
			fmt.Sprintf("Error uploading dump file: %s", err))
		return
	}

	if err := replayStart(tmpFile, timeNanoOffset, handler.Filename); err != nil {
		serveError(w, http.StatusInternalServerError,
			fmt.Sprintf("Could not replay dump file: %s", err))
		return
	}
}

// KVRequestDumpHandlers returns a map of grpc dump handlers keyed by the HTTP path.
// To get a dump file, example commands are:
// to start dumping
//    wget http://localhost:2379/debug/dump/start
// to stop dumping
//    wget http://localhost:2379/debug/dump/stop
// to download the dump data
//    wget -O dump.gz http://localhost:2379/debug/dump
func KVRequestDumpHandlers() map[string]http.Handler {
	m := make(map[string]http.Handler)
	m[httpPrefixDebug+"/dump"] = http.HandlerFunc(kvRequestDumpDownload)
	m[httpPrefixDebug+"/dump/start"] = http.HandlerFunc(kvRequestDumpStart)
	m[httpPrefixDebug+"/dump/stop"] = http.HandlerFunc(kvRequestDumpStop)
	return m
}

// kvRequestDumpDownload sends the temp dump file back to user
func kvRequestDumpDownload(w http.ResponseWriter, _ *http.Request) {
	// no browser MIME-type sniffing
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// response is a file stream
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment;filename="dump.gz"`)
	if err := DumpDownload(w); err != nil {
		serveError(w, http.StatusInternalServerError,
			fmt.Sprintf("Downloading dump failed: %s", err))
	}
}

// kvRequestDumpStart starts dumping grpc requests/responses to a local file
func kvRequestDumpStart(w http.ResponseWriter, r *http.Request) {
	// no browser MIME-type sniffing and plain text response
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")

	if err := DumpStart(); err != nil {
		serveError(w, http.StatusInternalServerError,
			fmt.Sprintf("Starting dump failed: %s", err))
	}
}

// kvRequestDumpStop stops dumping grpc requests/responses and download the file
func kvRequestDumpStop(w http.ResponseWriter, _ *http.Request) {
	// no browser MIME-type sniffing and plain text response
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if err := DumpStop(); err != nil {
		serveError(w, http.StatusInternalServerError,
			fmt.Sprintf("Stopping dump failed: %s", err))
	}
}
