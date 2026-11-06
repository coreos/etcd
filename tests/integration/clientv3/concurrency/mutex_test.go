// Copyright 2019 The etcd Authors
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

package concurrency_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
	"go.etcd.io/etcd/tests/v3/framework/integration"
)

func TestMutexLockSessionExpired(t *testing.T) {
	cli, err := integration.NewClient(t, clientv3.Config{Endpoints: exampleEndpoints()})
	require.NoError(t, err)
	defer cli.Close()

	// create two separate sessions for lock competition
	s1, err := concurrency.NewSession(cli)
	require.NoError(t, err)
	defer s1.Close()
	m1 := concurrency.NewMutex(s1, "/my-lock/")

	s2, err := concurrency.NewSession(cli)
	require.NoError(t, err)
	m2 := concurrency.NewMutex(s2, "/my-lock/")

	// acquire lock for s1
	require.NoError(t, m1.Lock(t.Context()))

	m2Locked := make(chan struct{})
	var err2 error
	go func() {
		defer close(m2Locked)
		// m2 blocks since m1 already acquired lock /my-lock/
		if err2 = m2.Lock(t.Context()); err2 == nil {
			t.Error("expect session expired error")
		}
	}()

	// revoke the session of m2 before unlock m1
	require.NoError(t, s2.Close())
	require.NoError(t, m1.Unlock(t.Context()))

	<-m2Locked
}

func TestMutexUnlock(t *testing.T) {
	cli, err := integration.NewClient(t, clientv3.Config{Endpoints: exampleEndpoints()})
	require.NoError(t, err)
	defer cli.Close()

	s1, err := concurrency.NewSession(cli)
	require.NoError(t, err)
	defer s1.Close()

	m1 := concurrency.NewMutex(s1, "/my-lock/")
	err = m1.Unlock(t.Context())
	require.Errorf(t, err, "expect lock released error")
	if !errors.Is(err, concurrency.ErrLockReleased) {
		t.Fatal(err)
	}

	require.NoError(t, m1.Lock(t.Context()))

	require.NoError(t, m1.Unlock(t.Context()))

	err = m1.Unlock(t.Context())
	if err == nil {
		t.Fatal("expect lock released error")
	}
	if !errors.Is(err, concurrency.ErrLockReleased) {
		t.Fatal(err)
	}
}

// TestResumeMutex verifies that a Mutex can be reconstructed from a known
// owner key and revision (e.g. after a process restart with a surviving
// lease, via Session's WithLease option), mirroring TestResumeElection.
func TestResumeMutex(t *testing.T) {
	const prefix = "/resume-mutex/"

	cli, err := integration.NewClient(t, clientv3.Config{Endpoints: exampleEndpoints()})
	require.NoError(t, err)
	defer cli.Close()

	s1, err := concurrency.NewSession(cli)
	require.NoError(t, err)
	defer s1.Close()

	m1 := concurrency.NewMutex(s1, prefix)
	require.NoError(t, m1.Lock(t.Context()))

	myKey := m1.Key()
	getResp, err := cli.Get(t.Context(), myKey)
	require.NoError(t, err)
	require.Lenf(t, getResp.Kvs, 1, "lock key %q should exist on the server after Lock()", myKey)
	myRev := getResp.Kvs[0].CreateRevision

	// Recreate the mutex, as if this were a different process that only
	// knows the session, the key it previously wrote, and that key's
	// creation revision.
	m2 := concurrency.ResumeMutex(s1, prefix, myKey, myRev)

	// The resumed mutex must still be recognized as the lock owner.
	txnResp, err := cli.Txn(t.Context()).If(m2.IsOwner()).Then(clientv3.OpGet(myKey)).Commit()
	require.NoError(t, err)
	require.Truef(t, txnResp.Succeeded, "resumed Mutex should still be recognized as lock owner via IsOwner()")

	// Unlock() through the resumed handle must release the real key.
	require.NoError(t, m2.Unlock(t.Context()))

	getResp, err = cli.Get(t.Context(), myKey)
	require.NoError(t, err)
	require.Emptyf(t, getResp.Kvs, "Unlock() via resumed Mutex should have deleted the real lock key")
}
