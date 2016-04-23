// Copyright 2015 CoreOS, Inc.
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

// +build linux,386

package backend

import (
	"math"
	"syscall"

	"github.com/coreos/etcd/Godeps/_workspace/src/github.com/boltdb/bolt"
)

var (
	// InitialMmapSize is the initial size of the mmapped region. Setting this larger than
	// the potential max db size can prevent writer from blocking reader.
	// This only works for linux and only on arm 32-bit
	// This sets InitialMmapSize to 2GB, or (2 * 1024 * 1024 * 1024) - 1
	InitialMmapSize = math.MaxInt32
)

// syscall.MAP_POPULATE on linux 2.6.23+ does sequential read-ahead
// which can speed up entire-database read with boltdb. We want to
// enable MAP_POPULATE for faster key-value store recovery in storage
// package. If your kernel version is lower than 2.6.23
// (https://github.com/torvalds/linux/releases/tag/v2.6.23), mmap might
// silently ignore this flag. Please update your kernel to prevent this.
var boltOpenOptions = &bolt.Options{
	MmapFlags:       syscall.MAP_POPULATE,
	InitialMmapSize: InitialMmapSize,
}
