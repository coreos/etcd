// Copyright 2017 The etcd Authors
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

// Package ordering is a clientv3 wrapper that caches response header revisions
// to detect ordering violations from stale responses. Users may define a
// policy on how to handle the ordering violation, but typically the client
// should connect to another endpoint and reissue the request.
// The wrapper only reissues read-only transactions. If a transaction that may
// write has an ordering violation and the policy returns nil, the wrapper returns
// the original response rather than risk applying the mutation twice.
//
// The most common situation where an ordering violation happens is a client
// reconnects to a partitioned member and issues a serializable read. Since the
// partitioned member is likely behind the last member, it may return a Get
// response based on a store revision older than the store revision used to
// service a prior Get on the former endpoint.
//
// First, create a client:
//
//	cli, err := clientv3.New(clientv3.Config{Endpoints: []string{"localhost:2379"}})
//	if err != nil {
//		// handle error!
//	}
//
// Next, override the client interface with the ordering wrapper:
//
//	vf := func(op clientv3.Op, resp clientv3.OpResponse, prevRev int64) error {
//		return fmt.Errorf("ordering: issued %+v, got %+v, expected rev=%v", op, resp, prevRev)
//	}
//	cli.KV = ordering.NewKV(cli.KV, vf)
//
// Now calls using 'cli' will reject order violations with an error.
package ordering
