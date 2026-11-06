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

func TestElectionCampaignSessionExpired(t *testing.T) {
	const prefix = "/session-expiry-election"

	cli, err := integration.NewClient(t, clientv3.Config{Endpoints: exampleEndpoints()})
	require.NoError(t, err)
	defer cli.Close()

	s1, err := concurrency.NewSession(cli)
	require.NoError(t, err)
	defer s1.Close()
	e1 := concurrency.NewElection(s1, prefix)
	require.NoError(t, e1.Campaign(t.Context(), "candidate1"))

	s2, err := concurrency.NewSession(cli)
	require.NoError(t, err)
	e2 := concurrency.NewElection(s2, prefix)

	campaignDone := make(chan error, 1)
	go func() {
		campaignDone <- e2.Campaign(t.Context(), "candidate2")
	}()

	// wait until candidate2 has joined the election before revoking its session
	require.Eventually(t, func() bool {
		resp, getErr := cli.Get(t.Context(), prefix+"/", clientv3.WithPrefix())
		return getErr == nil && len(resp.Kvs) == 2
	}, 5*time.Second, 10*time.Millisecond)

	// candidate2 should stop waiting even though candidate1 is still leader
	require.NoError(t, s2.Close())
	select {
	case err = <-campaignDone:
		require.ErrorIs(t, err, concurrency.ErrElectionNotLeader)
	case <-time.After(5 * time.Second):
		t.Fatal("Campaign did not return after its session expired")
	}

	leader, err := e1.Leader(t.Context())
	require.NoError(t, err)
	require.Len(t, leader.Kvs, 1)
	require.Equal(t, "candidate1", string(leader.Kvs[0].Value))
	require.NoError(t, e1.Resign(t.Context()))
}

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
