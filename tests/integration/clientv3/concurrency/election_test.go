// Copyright 2018 The etcd Authors
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
	"context"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
	"go.etcd.io/etcd/tests/v3/framework/integration"
)

func TestResumeElection(t *testing.T) {
	const prefix = "/resume-election/"

	cli, err := integration.NewClient(t, clientv3.Config{Endpoints: exampleEndpoints()})
	if err != nil {
		log.Fatal(err)
	}
	defer cli.Close()

	var s *concurrency.Session
	s, err = concurrency.NewSession(cli)
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()

	e := concurrency.NewElection(s, prefix)

	// entire test should never take more than 10 seconds
	ctx, cancel := context.WithTimeout(t.Context(), time.Second*10)
	defer cancel()

	// become leader
	require.NoErrorf(t, e.Campaign(ctx, "candidate1"), "Campaign() returned non nil err")

	// get the leadership details of the current election
	var leader *clientv3.GetResponse
	leader, err = e.Leader(ctx)
	require.NoErrorf(t, err, "Leader() returned non nil err")

	// Recreate the election
	e = concurrency.ResumeElection(s, prefix,
		string(leader.Kvs[0].Key), leader.Kvs[0].CreateRevision)

	respChan := make(chan *clientv3.GetResponse)
	go func() {
		defer close(respChan)
		o := e.Observe(ctx)
		respChan <- nil
		for resp := range o {
			// Ignore any observations that candidate1 was elected
			if string(resp.Kvs[0].Value) == "candidate1" {
				continue
			}
			respChan <- resp
			return
		}
		t.Error("Observe() channel closed prematurely")
	}()

	// wait until observe goroutine is running
	<-respChan

	// put some random data to generate a change event, this put should be
	// ignored by Observe() because it is not under the election prefix.
	_, err = cli.Put(ctx, "foo", "bar")
	require.NoErrorf(t, err, "Put('foo') returned non nil err")

	// resign as leader
	require.NoErrorf(t, e.Resign(ctx), "Resign() returned non nil err")

	// elect a different candidate
	require.NoErrorf(t, e.Campaign(ctx, "candidate2"), "Campaign() returned non nil err")

	// wait for observed leader change
	resp := <-respChan

	kv := resp.Kvs[0]
	if !strings.HasPrefix(string(kv.Key), prefix) {
		t.Errorf("expected observed election to have prefix '%s' got %q", prefix, string(kv.Key))
	}
	if string(kv.Value) != "candidate2" {
		t.Errorf("expected new leader to be 'candidate1' got %q", string(kv.Value))
	}
}

// TestElectionResignCanRetryAfterError reproduces
// https://github.com/etcd-io/etcd/issues/22123: if Resign's delete
// transaction fails (e.g. because its context is canceled before the
// request reaches etcd), Resign must not forget its local leadership
// state. Otherwise a later Resign call becomes a silent no-op and can
// never remove the still-existing leader key.
func TestElectionResignCanRetryAfterError(t *testing.T) {
	const prefix = "/resign-retry/"

	cli, err := integration.NewClient(t, clientv3.Config{Endpoints: exampleEndpoints()})
	require.NoError(t, err)
	defer cli.Close()

	s, err := concurrency.NewSession(cli)
	require.NoError(t, err)
	defer s.Close()

	e := concurrency.NewElection(s, prefix)

	ctx, cancel := context.WithTimeout(t.Context(), time.Second*10)
	defer cancel()

	require.NoError(t, e.Campaign(ctx, "candidate1"))
	leaderKey := e.Key()
	require.NotEmpty(t, leaderKey)

	canceledCtx, cancelNow := context.WithCancel(ctx)
	cancelNow()
	err = e.Resign(canceledCtx)
	require.ErrorIs(t, err, context.Canceled)

	// The failed Resign must retain enough state to retry: the leader
	// key must still be there, both locally and in etcd.
	require.Equal(t, leaderKey, e.Key())
	resp, err := cli.Get(ctx, leaderKey)
	require.NoError(t, err)
	require.Len(t, resp.Kvs, 1, "leader key should still exist in etcd after the failed Resign")

	// A retried Resign with a valid context should now actually succeed.
	require.NoError(t, e.Resign(ctx))
	require.Empty(t, e.Key())
	resp, err = cli.Get(ctx, leaderKey)
	require.NoError(t, err)
	require.Empty(t, resp.Kvs, "leader key should be gone after the successful retry")
}
