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
	"time"

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

// TestMutexLockKeyRecreated ensures that Lock does not succeed while another
// session holds the lock, after another Mutex on the same session deleted and
// recreated the key Lock waits on.
func TestMutexLockKeyRecreated(t *testing.T) {
	cli, err := integration.NewClient(t, clientv3.Config{Endpoints: exampleEndpoints()})
	require.NoError(t, err)
	defer cli.Close()

	s1, err := concurrency.NewSession(cli)
	require.NoError(t, err)
	defer s1.Close()
	s2, err := concurrency.NewSession(cli)
	require.NoError(t, err)
	defer s2.Close()
	s3, err := concurrency.NewSession(cli)
	require.NoError(t, err)
	defer s3.Close()
	m1 := concurrency.NewMutex(s1, "/my-lock/")
	m2 := concurrency.NewMutex(s2, "/my-lock/")
	m2b := concurrency.NewMutex(s2, "/my-lock/")
	m3 := concurrency.NewMutex(s3, "/my-lock/")

	require.NoError(t, m1.Lock(t.Context()))
	wch := cli.Watch(t.Context(), "/my-lock/", clientv3.WithPrefix(), clientv3.WithRev(m1.Header().Revision+1))

	// m2 puts the key of s2 and waits for m1.
	m2Locked := make(chan error, 1)
	go func() { m2Locked <- m2.Lock(t.Context()) }()
	<-wch
	// m2b fails to lock and deletes the key of s2.
	require.ErrorIs(t, m2b.TryLock(t.Context()), concurrency.ErrLocked)
	<-wch
	// m3 puts the key of s3 and waits for m1.
	m3Locked := make(chan error, 1)
	go func() { m3Locked <- m3.Lock(t.Context()) }()
	<-wch
	// m2b puts the key of s2 again and waits for m3.
	m2bLocked := make(chan error, 1)
	go func() { m2bLocked <- m2b.Lock(t.Context()) }()
	<-wch

	require.NoError(t, m1.Unlock(t.Context()))
	require.NoError(t, <-m3Locked)
	// m2 must not lock while m3 holds the lock.
	select {
	case err := <-m2Locked:
		require.Error(t, err, "m2 locked while m3 holds the lock")
	case <-time.After(time.Second):
	}
	require.NoError(t, m3.Unlock(t.Context()))
	<-m2bLocked
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
