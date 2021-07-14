// Copyright 2016 The etcd Authors
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

package main

import (
	"encoding/binary"
	"fmt"
	"path/filepath"

	"go.etcd.io/etcd/api/v3/authpb"
	"go.etcd.io/etcd/server/v3/storage/schema"

	"go.etcd.io/etcd/api/v3/mvccpb"
	"go.etcd.io/etcd/server/v3/lease/leasepb"
	"go.etcd.io/etcd/server/v3/storage/backend"

	bolt "go.etcd.io/bbolt"
)

func snapDir(dataDir string) string {
	return filepath.Join(dataDir, "member", "snap")
}

func getBuckets(dbPath string) (buckets []string, err error) {
	db, derr := bolt.Open(dbPath, 0600, &bolt.Options{Timeout: flockTimeout})
	if derr != nil {
		return nil, fmt.Errorf("failed to open bolt DB %v", derr)
	}
	defer db.Close()

	err = db.View(func(tx *bolt.Tx) error {
		return tx.ForEach(func(b []byte, _ *bolt.Bucket) error {
			buckets = append(buckets, string(b))
			return nil
		})
	})
	return buckets, err
}

// TODO: import directly from packages, rather than copy&paste

type decoder func(k, v []byte)

var decoders = map[string]decoder{
	"key":       keyDecoder,
	"lease":     leaseDecoder,
	"auth":      authDecoder,
	"authRoles": authRolesDecoder,
	"authUsers": authUsersDecoder,
}

type revision struct {
	main int64
	sub  int64
}

func bytesToRev(bytes []byte) revision {
	return revision{
		main: int64(binary.BigEndian.Uint64(bytes[0:8])),
		sub:  int64(binary.BigEndian.Uint64(bytes[9:])),
	}
}

func keyDecoder(k, v []byte) {
	rev := bytesToRev(k)
	var kv mvccpb.KeyValue
	if err := kv.Unmarshal(v); err != nil {
		panic(err)
	}
	fmt.Printf("rev=%+v, value=[key %q | val %q | created %d | mod %d | ver %d]\n", rev, string(kv.Key), string(kv.Value), kv.CreateRevision, kv.ModRevision, kv.Version)
}

func bytesToLeaseID(bytes []byte) int64 {
	if len(bytes) != 8 {
		panic(fmt.Errorf("lease ID must be 8-byte"))
	}
	return int64(binary.BigEndian.Uint64(bytes))
}

func leaseDecoder(k, v []byte) {
	leaseID := bytesToLeaseID(k)
	var lpb leasepb.Lease
	if err := lpb.Unmarshal(v); err != nil {
		panic(err)
	}
	fmt.Printf("lease ID=%016x, TTL=%ds\n", leaseID, lpb.TTL)
}

func authDecoder(k, v []byte) {
	if string(k) == "authRevision" {
		rev := binary.BigEndian.Uint64(v)
		fmt.Printf("key=%q, value=%v\n", k, rev)
	} else {
		fmt.Printf("key=%q, value=%v\n", k, v)
	}
}

func authRolesDecoder(k, v []byte) {
	role := &authpb.Role{}
	err := role.Unmarshal(v)
	if err != nil {
		panic(err)
	}
	fmt.Printf("role=%q, keyPermission=%v\n", string(role.Name), role.KeyPermission)
}

func authUsersDecoder(k, v []byte) {
	user := &authpb.User{}
	err := user.Unmarshal(v)
	if err != nil {
		panic(err)
	}
	fmt.Printf("user=%q, roles=%q, password=%q, option=%v\n", user.Name, user.Roles, string(user.Password), user.Options)
}

func iterateBucket(dbPath, bucket string, limit uint64, decode bool) (err error) {
	db, err := bolt.Open(dbPath, 0600, &bolt.Options{Timeout: flockTimeout})
	if err != nil {
		return fmt.Errorf("failed to open bolt DB %v", err)
	}
	defer db.Close()

	err = db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return fmt.Errorf("got nil bucket for %s", bucket)
		}

		c := b.Cursor()

		// iterate in reverse order (use First() and Next() for ascending order)
		for k, v := c.Last(); k != nil; k, v = c.Prev() {
			// TODO: remove sensitive information
			// (https://github.com/etcd-io/etcd/issues/7620)
			if dec, ok := decoders[bucket]; decode && ok {
				dec(k, v)
			} else {
				fmt.Printf("key=%q, value=%q\n", k, v)
			}

			limit--
			if limit == 0 {
				break
			}
		}

		return nil
	})
	return err
}

func getHash(dbPath string) (hash uint32, err error) {
	b := backend.NewDefaultBackend(dbPath)
	return b.Hash(schema.DefaultIgnores)
}

// TODO: revert by revision and find specified hash value
// currently, it's hard because lease is in separate bucket
// and does not modify revision
