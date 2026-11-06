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

	m2Locked := make(chan error, 1)
	go func() {
		// m2 blocks since m1 already acquired lock /my-lock/
		m2Locked <- m2.Lock(t.Context())
	}()

	// wait until m2 has joined the lock queue before revoking its session
	require.Eventually(t, func() bool {
		resp, getErr := cli.Get(t.Context(), "/my-lock/", clientv3.WithPrefix())
		return getErr == nil && len(resp.Kvs) == 2
	}, 5*time.Second, 10*time.Millisecond)

	// revoke m2's session while m1 still holds the lock
	require.NoError(t, s2.Close())

	select {
	case err = <-m2Locked:
		require.ErrorIs(t, err, concurrency.ErrSessionExpired)
	case <-time.After(5 * time.Second):
		t.Fatal("Lock did not return after its session expired")
	}

	// m1 still owns the lock when m2 returns
	require.NoError(t, m1.Unlock(t.Context()))
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
