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

package nonblock

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"

	"go.etcd.io/etcd/client/pkg/v3/fileutil"
	"go.uber.org/zap"
)

const Scheme = "nonblock"
const DefaultMode = os.FileMode(0666)

var DefaultTimeout = time.Millisecond

func init() {
	if err := zap.RegisterSink(Scheme, NewSink); err != nil {
		panic("Failed to register zap sink for scheme " + Scheme + ": " + err.Error())
	}
}

type nonblock struct {
	sync.Mutex

	file     *os.File
	timeout  time.Duration
	errCount uint64
	warn     bool
	must     bool
}

func NewSink(u *url.URL) (zap.Sink, error) {
	if u == nil || u.Scheme != Scheme {
		return nil, errors.New("invalid url or scheme")
	}

	n := &nonblock{timeout: DefaultTimeout}

	if timeout := u.Query().Get("timeout"); timeout != "" {
		dur, err := time.ParseDuration(timeout)
		if err != nil {
			return nil, fmt.Errorf("failed to parse timeout parameter: %w", err)
		}
		if dur < 1 {
			return nil, errors.New("timeout must be greater than zero")
		}
		n.timeout = dur
	}

	if warn := u.Query().Get("warn"); warn != "" {
		b, err := strconv.ParseBool(warn)
		if err != nil {
			return nil, fmt.Errorf("failed to parse warn parameter: %w", err)
		}
		n.warn = b
	}

	if must := u.Query().Get("must"); must != "" {
		b, err := strconv.ParseBool(must)
		if err != nil {
			return nil, fmt.Errorf("failed to parse must parameter: %w", err)
		}
		n.must = b
	}

	fileMode := DefaultMode
	if mode := u.Query().Get("mode"); mode != "" {
		u, err := strconv.ParseUint(mode, 0, 32)
		if err != nil {
			return nil, fmt.Errorf("failed to parse mode parameter: %w", err)
		}
		fileMode = os.FileMode(u)
	}

	var err error
	switch u.Opaque {
	case "stdout":
		n.file, err = fileutil.Dup(os.Stdout)
	case "stderr":
		n.file, err = fileutil.Dup(os.Stderr)
	default:
		n.file, err = os.OpenFile(u.Opaque, os.O_WRONLY|os.O_APPEND|os.O_CREATE|syscall.O_NONBLOCK, fileMode)
	}

	if err != nil {
		return nil, err
	}

	return n, nil
}

func (n *nonblock) Write(p []byte) (int, error) {
	n.Lock()
	defer n.Unlock()

	if err := n.file.SetWriteDeadline(time.Now().Add(n.timeout)); err != nil && n.must {
		n.errCount++
		return len(p), nil
	}
	i, err := n.file.Write(p)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		n.errCount++
		return len(p), nil
	}
	if err == nil && n.errCount != 0 {
		if n.warn {
			fmt.Fprintf(n.file, `\n{"level":"error","msg":"Sink dropped %d writes prior to the last entry"}\n`, n.errCount)
		}
		n.errCount = 0
	}
	return i, err
}

func (n *nonblock) Sync() error {
	n.Lock()
	defer n.Unlock()

	if n.errCount == 0 {
		return n.file.Sync()
	}
	return nil
}

func (n *nonblock) Close() error {
	n.Lock()
	defer n.Unlock()

	return n.file.Close()
}
