// Copyright 2022 The etcd Authors
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

package config

import (
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// ClientOption configures the client with additional parameter.
// For example, if Auth is enabled, the common test cases just need to
// use `WithAuth` to return a ClientOption. Note that the common `WithAuth`
// function calls `e2e.WithAuth` or `integration.WithAuth`, depending on the
// build tag (either "e2e" or "integration").
type ClientOption func(any)

type GetOptions struct {
	Revision          int
	End               string
	CountOnly         bool
	Serializable      bool
	Prefix            bool
	FromKey           bool
	Limit             int
	Order             clientv3.SortOrder
	SortBy            clientv3.SortTarget
	Timeout           time.Duration
	KeysOnly          bool
	MinModRevision    int
	MaxModRevision    int
	MinCreateRevision int
	MaxCreateRevision int
	Stream            bool
}

type PutOptions struct {
	LeaseID     clientv3.LeaseID
	Timeout     time.Duration
	IgnoreValue bool
	IgnoreLease bool
}

type DeleteOptions struct {
	Prefix  bool
	FromKey bool
	End     string
}

type TxnOptions struct {
	Interactive bool
}

type CompactOption struct {
	Physical bool
	Timeout  time.Duration
}

type DefragOption struct {
	Timeout time.Duration
}

type LeaseOption struct {
	WithAttachedKeys bool
}

type UserAddOptions struct {
	NoPassword bool
}

type WatchOptions struct {
	Prefix        bool
	Revision      int64
	RangeEnd      string
	FromKey       bool
	CreatedNotify bool
}
