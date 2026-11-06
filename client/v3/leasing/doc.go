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

// Package leasing serves linearizable reads from a local cache by acquiring
// exclusive write access to keys through a client-side leasing protocol. This
// leasing layer can either directly wrap the etcd client or it can be exposed
// through the etcd grpc proxy server, granting multiple clients write access.
//
// First, create a leasing KV from a clientv3.Client 'cli':
//
//	lkv, err := leasing.NewKV(cli, "leasing-prefix")
//	if err != nil {
//	    // handle error
//	}
//
// The supplied prefix reserves a key range for leasing protocol state. Delete
// operations that intersect this range fail with a gRPC InvalidArgument status.
//
// A range request for a key "abc" tries to acquire a leasing key so it can cache the range's
// key locally. On the server, the leasing key is stored to "leasing-prefix/abc":
//
//	resp, err := lkv.Get(context.TODO(), "abc")
//
// Future linearized read requests using 'lkv' will be served locally for the lease's lifetime:
//
//	resp, err = lkv.Get(context.TODO(), "abc")
//
// If another leasing client writes to a leased key, then the owner relinquishes its exclusive
// access, permitting the writer to modify the key:
//
//	lkv2, err := leasing.NewKV(cli, "leasing-prefix")
//	if err != nil {
//	    // handle error
//	}
//	lkv2.Put(context.TODO(), "abc", "456")
//	resp, err = lkv.Get("abc")
package leasing
