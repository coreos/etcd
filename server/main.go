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

//go:debug multipathtcp=0

// Package main is a simple wrapper of the real etcd entrypoint package
// (located at go.etcd.io/etcd/etcdmain) to ensure that etcd is still
// "go getable"; e.g. `go get go.etcd.io/etcd` works as expected and
// builds a binary in $GOBIN/etcd
//
// This package should NOT be extended or modified in any way; to modify the
// etcd binary, work in the `go.etcd.io/etcd/etcdmain` package.
package main

import (
	"os"

	"go.etcd.io/etcd/server/v3/etcdmain"
)

func main() {
	etcdmain.Main(os.Args)
}
