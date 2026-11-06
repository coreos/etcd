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

package lease_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
	"go.etcd.io/etcd/tests/v3/framework/integration"
)

func TestLeaseNotFoundError(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	kv := clus.RandClient()

	_, err := kv.Put(t.Context(), "foo", "bar", clientv3.WithLease(clientv3.LeaseID(500)))
	require.ErrorIsf(t, err, rpctypes.ErrLeaseNotFound, "expected %v, got %v", rpctypes.ErrLeaseNotFound, err)
}

func TestLeaseGrant(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	lapi := clus.RandClient()

	kv := clus.RandClient()

	_, merr := lapi.Grant(t.Context(), clientv3.MaxLeaseTTL+1)
	require.ErrorIsf(t, merr, rpctypes.ErrLeaseTTLTooLarge, "err = %v, want %v", merr, rpctypes.ErrLeaseTTLTooLarge)

	resp, err := lapi.Grant(t.Context(), 10)
	if err != nil {
		t.Errorf("failed to create lease %v", err)
	}

	_, err = kv.Put(t.Context(), "foo", "bar", clientv3.WithLease(resp.ID))
	require.NoErrorf(t, err, "failed to create key with lease %v", err)
}

func TestLeaseRevoke(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	lapi := clus.RandClient()

	kv := clus.RandClient()

	resp, err := lapi.Grant(t.Context(), 10)
	if err != nil {
		t.Errorf("failed to create lease %v", err)
	}

	_, err = lapi.Revoke(t.Context(), resp.ID)
	if err != nil {
		t.Errorf("failed to revoke lease %v", err)
	}

	_, err = kv.Put(t.Context(), "foo", "bar", clientv3.WithLease(resp.ID))
	require.ErrorIsf(t, err, rpctypes.ErrLeaseNotFound, "err = %v, want %v", err, rpctypes.ErrLeaseNotFound)
}

func TestLeaseKeepAliveOnce(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	lapi := clus.RandClient()

	resp, err := lapi.Grant(t.Context(), 10)
	if err != nil {
		t.Errorf("failed to create lease %v", err)
	}

	_, err = lapi.KeepAliveOnce(t.Context(), resp.ID)
	if err != nil {
		t.Errorf("failed to keepalive lease %v", err)
	}

	_, err = lapi.KeepAliveOnce(t.Context(), clientv3.LeaseID(0))
	if !errors.Is(err, rpctypes.ErrLeaseNotFound) {
		t.Errorf("expected %v, got %v", rpctypes.ErrLeaseNotFound, err)
	}
}

func TestLeaseKeepAlive(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	lapi := clus.Client(0)
	clus.TakeClient(0)

	resp, err := lapi.Grant(t.Context(), 10)
	if err != nil {
		t.Errorf("failed to create lease %v", err)
	}

	type uncomparableCtx struct {
		context.Context
		_ func()
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	rc, kerr := lapi.KeepAlive(uncomparableCtx{Context: ctx}, resp.ID)
	if kerr != nil {
		t.Errorf("failed to keepalive lease %v", kerr)
	}

	kresp, ok := <-rc
	if !ok {
		t.Errorf("chan is closed, want not closed")
	}

	require.NotNilf(t, kresp, "unexpected null response")

	if kresp.ID != resp.ID {
		t.Errorf("ID = %x, want %x", kresp.ID, resp.ID)
	}

	ctx2, cancel2 := context.WithCancel(t.Context())
	rc2, kerr2 := lapi.KeepAlive(uncomparableCtx{Context: ctx2}, resp.ID)
	if kerr2 != nil {
		t.Errorf("failed to keepalive lease %v", kerr2)
	}

	cancel2()

	_, ok = <-rc2
	if ok {
		t.Errorf("chan is not closed, want cancel stop keepalive")
	}

	select {
	case <-rc:
		// cancel2() should not affect first keepalive
		t.Errorf("chan is closed, want keepalive continue")
	default:
	}

	lapi.Close()

	_, ok = <-rc
	if ok {
		t.Errorf("chan is not closed, want lease Close() closes chan")
	}
}

func TestLeaseKeepAliveSeconds(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	cli := clus.Client(0)

	resp, err := cli.Grant(t.Context(), 3)
	if err != nil {
		t.Errorf("failed to create lease %v", err)
	}
	rc, kerr := cli.KeepAlive(t.Context(), resp.ID)
	if kerr != nil {
		t.Errorf("failed to keepalive lease %v", kerr)
	}

	for i := 0; i < 3; i++ {
		if _, ok := <-rc; !ok {
			t.Errorf("[%d] chan is closed, want not closed", i)
		}
	}
}

// TestLeaseKeepAliveHandleFailure tests lease keep alive handling faillure
// TODO: add a client that can connect to all the members of cluster via unix sock.
// TODO: test handle more complicated failures.
func TestLeaseKeepAliveHandleFailure(t *testing.T) {
	t.Skip("test it when we have a cluster client")

	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3, UseBridge: true})
	defer clus.Terminate(t)

	// TODO: change this line to get a cluster client
	lapi := clus.RandClient()

	resp, err := lapi.Grant(t.Context(), 10)
	if err != nil {
		t.Errorf("failed to create lease %v", err)
	}

	rc, kerr := lapi.KeepAlive(t.Context(), resp.ID)
	if kerr != nil {
		t.Errorf("failed to keepalive lease %v", kerr)
	}

	kresp := <-rc
	if kresp.ID != resp.ID {
		t.Errorf("ID = %x, want %x", kresp.ID, resp.ID)
	}

	// restart the connected member.
	clus.Members[0].Stop(t)

	select {
	case <-rc:
		t.Fatalf("unexpected keepalive")
	case <-time.After(10*time.Second/3 + 1):
	}

	// recover the member.
	clus.Members[0].Restart(t)

	kresp = <-rc
	if kresp.ID != resp.ID {
		t.Errorf("ID = %x, want %x", kresp.ID, resp.ID)
	}

	lapi.Close()

	_, ok := <-rc
	if ok {
		t.Errorf("chan is not closed, want lease Close() closes chan")
	}
}

type leaseCh struct {
	lid clientv3.LeaseID
	ch  <-chan *clientv3.LeaseKeepAliveResponse
}

// TestLeaseKeepAliveNotFound ensures a revoked lease won't halt other leases.
func TestLeaseKeepAliveNotFound(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	cli := clus.RandClient()
	var lchs []leaseCh
	for i := 0; i < 3; i++ {
		resp, rerr := cli.Grant(t.Context(), 5)
		require.NoError(t, rerr)
		kach, kaerr := cli.KeepAlive(t.Context(), resp.ID)
		require.NoError(t, kaerr)
		lchs = append(lchs, leaseCh{resp.ID, kach})
	}

	_, err := cli.Revoke(t.Context(), lchs[1].lid)
	require.NoError(t, err)

	<-lchs[0].ch
	if _, ok := <-lchs[0].ch; !ok {
		t.Fatal("closed keepalive on wrong lease")
	}
	if _, ok := <-lchs[1].ch; ok {
		t.Fatal("expected closed keepalive")
	}
}

func TestLeaseGrantErrConnClosed(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	cli := clus.Client(0)
	clus.TakeClient(0)
	require.NoError(t, cli.Close())

	donec := make(chan struct{})
	go func() {
		defer close(donec)
		_, err := cli.Grant(t.Context(), 5)
		if !clientv3.IsConnCanceled(err) {
			// context.Canceled if grpc-go balancer calls 'Get' with an inflight client.Close.
			t.Errorf("expected %v, or server unavailable, got %v", context.Canceled, err)
		}
	}()

	select {
	case <-time.After(integration.RequestWaitTimeout):
		t.Fatal("le.Grant took too long")
	case <-donec:
	}
}

// TestLeaseKeepAliveFullResponseQueue ensures when response
// queue is full thus dropping keepalive response sends,
// keepalive request is sent with the same rate of TTL / 3.
func TestLeaseKeepAliveFullResponseQueue(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	lapi := clus.Client(0)

	// expect lease keepalive every 10-second
	lresp, err := lapi.Grant(t.Context(), 30)
	require.NoErrorf(t, err, "failed to create lease %v", err)
	id := lresp.ID

	old := clientv3.LeaseResponseChSize
	defer func() {
		clientv3.LeaseResponseChSize = old
	}()
	clientv3.LeaseResponseChSize = 0

	// never fetch from response queue, and let it become full
	_, err = lapi.KeepAlive(t.Context(), id)
	require.NoErrorf(t, err, "failed to keepalive lease %v", err)

	// TTL should not be refreshed after 3 seconds
	// expect keepalive to be triggered after TTL/3
	time.Sleep(3 * time.Second)

	tr, terr := lapi.TimeToLive(t.Context(), id)
	require.NoErrorf(t, terr, "failed to get lease information %v", terr)
	if tr.TTL >= 29 {
		t.Errorf("unexpected kept-alive lease TTL %d", tr.TTL)
	}
}

func TestLeaseGrantNewAfterClose(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	cli := clus.Client(0)
	clus.TakeClient(0)
	require.NoError(t, cli.Close())

	donec := make(chan struct{})
	go func() {
		_, err := cli.Grant(t.Context(), 5)
		if !clientv3.IsConnCanceled(err) {
			t.Errorf("expected %v or server unavailable, got %v", context.Canceled, err)
		}
		close(donec)
	}()
	select {
	case <-time.After(integration.RequestWaitTimeout):
		t.Fatal("le.Grant took too long")
	case <-donec:
	}
}

func TestLeaseRevokeNewAfterClose(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	cli := clus.Client(0)
	resp, err := cli.Grant(t.Context(), 5)
	require.NoError(t, err)
	leaseID := resp.ID

	clus.TakeClient(0)
	require.NoError(t, cli.Close())

	errMsgCh := make(chan string, 1)
	go func() {
		_, err := cli.Revoke(t.Context(), leaseID)
		if !clientv3.IsConnCanceled(err) {
			errMsgCh <- fmt.Sprintf("expected %v or server unavailable, got %v", context.Canceled, err)
		} else {
			errMsgCh <- ""
		}
	}()
	select {
	case <-time.After(integration.RequestWaitTimeout):
		t.Fatal("le.Revoke took too long")
	case errMsg := <-errMsgCh:
		require.Empty(t, errMsg)
	}
}

// TestLeaseKeepAliveCloseAfterDisconnectRevoke ensures the keep alive channel is closed
// following a disconnection, lease revoke, then reconnect.
func TestLeaseKeepAliveCloseAfterDisconnectRevoke(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3, UseBridge: true})
	defer clus.Terminate(t)

	cli := clus.Client(0)

	// setup lease and do a keepalive
	resp, err := cli.Grant(t.Context(), 10)
	require.NoError(t, err)
	rc, kerr := cli.KeepAlive(t.Context(), resp.ID)
	require.NoError(t, kerr)
	kresp := <-rc
	require.Equalf(t, kresp.ID, resp.ID, "ID = %x, want %x", kresp.ID, resp.ID)

	// keep client disconnected
	clus.Members[0].Stop(t)
	time.Sleep(time.Second)
	clus.WaitLeader(t)

	_, err = clus.Client(1).Revoke(t.Context(), resp.ID)
	require.NoError(t, err)

	clus.Members[0].Restart(t)

	// some responses may still be buffered; drain until close
	timer := time.After(time.Duration(kresp.TTL) * time.Second)
	for kresp != nil {
		select {
		case kresp = <-rc:
		case <-timer:
			t.Fatalf("keepalive channel did not close")
		}
	}
}

// TestLeaseKeepAliveInitTimeout ensures the keep alive channel closes if
// the initial keep alive request never gets a response.
func TestLeaseKeepAliveInitTimeout(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1, UseBridge: true})
	defer clus.Terminate(t)

	cli := clus.Client(0)

	// setup lease and do a keepalive
	resp, err := cli.Grant(t.Context(), 5)
	require.NoError(t, err)
	// keep client disconnected
	clus.Members[0].Stop(t)
	rc, kerr := cli.KeepAlive(t.Context(), resp.ID)
	require.NoError(t, kerr)
	select {
	case ka, ok := <-rc:
		require.Falsef(t, ok, "unexpected keepalive %v, expected closed channel", ka)
	case <-time.After(10 * time.Second):
		t.Fatalf("keepalive channel did not close")
	}

	clus.Members[0].Restart(t)
}

// TestLeaseKeepAliveTTLTimeout ensures the keep alive channel closes if
// a keep alive request after the first never gets a response.
func TestLeaseKeepAliveTTLTimeout(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1, UseBridge: true})
	defer clus.Terminate(t)

	cli := clus.Client(0)

	// setup lease and do a keepalive
	resp, err := cli.Grant(t.Context(), 5)
	require.NoError(t, err)
	rc, kerr := cli.KeepAlive(t.Context(), resp.ID)
	require.NoError(t, kerr)
	kresp := <-rc
	require.Equalf(t, kresp.ID, resp.ID, "ID = %x, want %x", kresp.ID, resp.ID)

	// keep client disconnected
	clus.Members[0].Stop(t)
	select {
	case ka, ok := <-rc:
		require.Falsef(t, ok, "unexpected keepalive %v, expected closed channel", ka)
	case <-time.After(10 * time.Second):
		t.Fatalf("keepalive channel did not close")
	}

	clus.Members[0].Restart(t)
}

func TestLeaseTimeToLive(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3, UseBridge: true})
	defer clus.Terminate(t)

	c := clus.RandClient()
	lapi := c

	resp, err := lapi.Grant(t.Context(), 10)
	if err != nil {
		t.Errorf("failed to create lease %v", err)
	}

	kv := clus.RandClient()
	keys := []string{"foo1", "foo2"}
	for i := range keys {
		_, err = kv.Put(t.Context(), keys[i], "bar", clientv3.WithLease(resp.ID))
		require.NoError(t, err)
	}

	// linearized read to ensure Puts propagated to server backing lapi
	_, err = c.Get(t.Context(), "abc")
	require.NoError(t, err)

	lresp, lerr := lapi.TimeToLive(t.Context(), resp.ID, clientv3.WithAttachedKeys())
	require.NoError(t, lerr)
	require.Equalf(t, lresp.ID, resp.ID, "leaseID expected %d, got %d", resp.ID, lresp.ID)
	require.Equalf(t, int64(10), lresp.GrantedTTL, "GrantedTTL expected %d, got %d", 10, lresp.GrantedTTL)
	if lresp.TTL == 0 || lresp.TTL > lresp.GrantedTTL {
		t.Fatalf("unexpected TTL %d (granted %d)", lresp.TTL, lresp.GrantedTTL)
	}
	ks := make([]string, len(lresp.Keys))
	for i := range lresp.Keys {
		ks[i] = string(lresp.Keys[i])
	}
	sort.Strings(ks)
	require.Truef(t, reflect.DeepEqual(ks, keys), "keys expected %v, got %v", keys, ks)

	lresp, lerr = lapi.TimeToLive(t.Context(), resp.ID)
	require.NoError(t, lerr)
	require.Emptyf(t, lresp.Keys, "unexpected keys %+v", lresp.Keys)
}

func TestLeaseTimeToLiveLeaseNotFound(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	cli := clus.RandClient()
	resp, err := cli.Grant(t.Context(), 10)
	if err != nil {
		t.Errorf("failed to create lease %v", err)
	}
	_, err = cli.Revoke(t.Context(), resp.ID)
	if err != nil {
		t.Errorf("failed to Revoke lease %v", err)
	}

	lresp, err := cli.TimeToLive(t.Context(), resp.ID)
	// TimeToLive() should return a response with TTL=-1.
	require.NoErrorf(t, err, "expected err to be nil")
	require.NotNilf(t, lresp, "expected lresp not to be nil")
	require.NotNilf(t, lresp.ResponseHeader, "expected ResponseHeader not to be nil")
	require.Equalf(t, lresp.ID, resp.ID, "expected Lease ID %v, but got %v", resp.ID, lresp.ID)
	require.Equalf(t, int64(-1), lresp.TTL, "expected TTL %v, but got %v", int64(-1), lresp.TTL)
}

func TestLeaseLeases(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	cli := clus.RandClient()

	var ids []clientv3.LeaseID
	for i := 0; i < 5; i++ {
		resp, err := cli.Grant(t.Context(), 10)
		if err != nil {
			t.Errorf("failed to create lease %v", err)
		}
		ids = append(ids, resp.ID)
	}

	resp, err := cli.Leases(t.Context())
	require.NoError(t, err)
	require.Lenf(t, resp.Leases, 5, "len(resp.Leases) expected 5, got %d", len(resp.Leases))
	for i := range resp.Leases {
		require.Equalf(t, ids[i], resp.Leases[i].ID, "#%d: lease ID expected %d, got %d", i, ids[i], resp.Leases[i].ID)
	}
}

// TestLeaseRenewLostQuorum ensures keepalives work after losing quorum
// for a while.
func TestLeaseRenewLostQuorum(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3, UseBridge: true})
	defer clus.Terminate(t)

	cli := clus.Client(0)
	r, err := cli.Grant(t.Context(), 4)
	require.NoError(t, err)

	kctx, kcancel := context.WithCancel(t.Context())
	defer kcancel()
	ka, err := cli.KeepAlive(kctx, r.ID)
	require.NoError(t, err)
	// consume first keepalive so next message sends when cluster is down
	<-ka
	lastKa := time.Now()

	// force keepalive stream message to timeout
	clus.Members[1].Stop(t)
	clus.Members[2].Stop(t)
	// Use TTL-2 since the client closes the keepalive channel if no
	// keepalive arrives before the lease deadline; the client will
	// try to resend a keepalive after TTL/3 seconds, so for a TTL of 4,
	// sleeping for 2s should be sufficient time for issuing a retry.
	// The cluster has two seconds to recover and reply to the keepalive.
	time.Sleep(time.Duration(r.TTL-2) * time.Second)
	clus.Members[1].Restart(t)
	clus.Members[2].Restart(t)

	if time.Since(lastKa) > time.Duration(r.TTL)*time.Second {
		t.Skip("waited too long for server stop and restart")
	}

	select {
	case _, ok := <-ka:
		require.Truef(t, ok, "keepalive closed")
	case <-time.After(time.Duration(r.TTL) * time.Second):
		t.Fatalf("timed out waiting for keepalive")
	}
}

func TestLeaseKeepAliveLoopExit(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	ctx := t.Context()
	cli := clus.Client(0)
	clus.TakeClient(0)

	resp, err := cli.Grant(ctx, 5)
	require.NoError(t, err)
	cli.Close()

	_, err = cli.KeepAlive(ctx, resp.ID)
	var keepAliveHaltedErr clientv3.ErrKeepAliveHalted
	require.ErrorAsf(t, err, &keepAliveHaltedErr, "expected %T, got %v(%T)", clientv3.ErrKeepAliveHalted{}, err, err)
}

// TestV3LeaseFailureOverlap issues Grant and KeepAlive requests to a cluster
// before, during, and after quorum loss to confirm Grant/KeepAlive tolerates
// transient cluster failure.
func TestV3LeaseFailureOverlap(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 2, UseBridge: true})
	defer clus.Terminate(t)

	numReqs := 5
	cli := clus.Client(0)

	// bring up a session, tear it down
	updown := func(i int) error {
		sess, err := concurrency.NewSession(cli)
		if err != nil {
			return err
		}
		ch := make(chan struct{})
		go func() {
			defer close(ch)
			sess.Close()
		}()
		select {
		case <-ch:
		case <-time.After(time.Minute / 4):
			t.Fatalf("timeout %d", i)
		}
		return nil
	}

	var wg sync.WaitGroup
	mkReqs := func(n int) {
		wg.Add(numReqs)
		for i := 0; i < numReqs; i++ {
			go func() {
				defer wg.Done()
				err := updown(n)
				if err == nil || errors.Is(err, rpctypes.ErrTimeoutDueToConnectionLost) {
					return
				}
				t.Error(err)
			}()
		}
	}

	mkReqs(1)
	clus.Members[1].Stop(t)
	mkReqs(2)
	time.Sleep(time.Second)
	mkReqs(3)
	clus.Members[1].Restart(t)
	mkReqs(4)
	wg.Wait()
}

func TestLeaseKeepAliveAndExpirationWithNoLeader(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3, UseBridge: true})
	defer clus.Terminate(t)

	c := clus.Client(0)
	leaseTTL := 9 * time.Second

	t.Run("lease stays alive with mixed keepAlives", func(t *testing.T) {
		leaseRsp, err := c.Grant(t.Context(), int64(leaseTTL.Seconds()))
		require.NoError(t, err)

		kaReqLeader, err := c.KeepAlive(clientv3.WithRequireLeader(t.Context()), leaseRsp.ID)
		require.NoError(t, err)
		kaWait, err := c.KeepAlive(t.Context(), leaseRsp.ID)
		require.NoError(t, err)

		for _, ch := range []<-chan *clientv3.LeaseKeepAliveResponse{kaReqLeader, kaWait} {
			select {
			case <-ch:
			case <-time.After(5 * time.Second):
				t.Fatal("didn't receive first keep-alive within timeout")
			}
		}

		prevUnavailable := getLeaseKeepAliveMetric(t, clus.Members[0], "Unavailable")
		prevCanceled := getLeaseKeepAliveMetric(t, clus.Members[0], "Canceled")
		clus.Members[1].Stop(t)
		clus.Members[2].Stop(t)
		waitKeepAliveChannelClose(t, kaReqLeader)

		require.Eventuallyf(t, func() bool {
			return getLeaseKeepAliveMetric(t, clus.Members[0], "Unavailable") > prevUnavailable
		}, 3*time.Second, 100*time.Millisecond, "expected Unavailable metric to increment after leader loss")
		curCanceled := getLeaseKeepAliveMetric(t, clus.Members[0], "Canceled")
		require.Equalf(t, prevCanceled, curCanceled,
			"Canceled metric should not change, expected %d, got %d", prevCanceled, curCanceled)

		// This ensures we don't mistake old responses as fresh ones after restore
	drainLoop:
		for {
			select {
			case _, ok := <-kaWait:
				if !ok {
					t.Fatal("kaWait channel unexpectedly closed while draining")
				}
			default:
				break drainLoop
			}
		}

		require.NoError(t, clus.Members[1].Restart(t))
		clus.WaitLeader(t)
		select {
		case resp, ok := <-kaWait:
			require.Truef(t, ok, "keepAlive channel should still be open")
			require.NotNilf(t, resp, "expected keepAlive response after leader restore")
		case <-time.After(5 * time.Second):
			t.Fatal("keepAlive response timed out after leader restored")
		}

		ttl, err := c.TimeToLive(t.Context(), leaseRsp.ID)
		require.NoError(t, err)
		require.Positivef(t, ttl.TTL, "lease should still be alive (TTL>0), got TTL=%d", ttl.TTL)
	})

	t.Run("lease expires when both keepAlives require leader", func(t *testing.T) {
		leaseRsp, err := c.Grant(t.Context(), int64(leaseTTL.Seconds()))
		require.NoError(t, err)

		kaChan1, err := c.KeepAlive(clientv3.WithRequireLeader(t.Context()), leaseRsp.ID)
		require.NoError(t, err)
		kaChan2, err := c.KeepAlive(clientv3.WithRequireLeader(t.Context()), leaseRsp.ID)
		require.NoError(t, err)

		for _, ch := range []<-chan *clientv3.LeaseKeepAliveResponse{kaChan1, kaChan2} {
			select {
			case <-ch:
			case <-time.After(5 * time.Second):
				t.Fatal("didn't receive first keep-alive within timeout")
			}
		}

		prevUnavailable := getLeaseKeepAliveMetric(t, clus.Members[0], "Unavailable")
		prevCanceled := getLeaseKeepAliveMetric(t, clus.Members[0], "Canceled")
		clus.Members[1].Stop(t)
		clus.Members[2].Stop(t)
		waitKeepAliveChannelClose(t, kaChan1)
		waitKeepAliveChannelClose(t, kaChan2)

		require.Eventuallyf(t, func() bool {
			return getLeaseKeepAliveMetric(t, clus.Members[0], "Unavailable") > prevUnavailable
		}, 3*time.Second, 100*time.Millisecond, "expected Unavailable metric to increment after leader loss")
		curCanceled := getLeaseKeepAliveMetric(t, clus.Members[0], "Canceled")
		require.Equalf(t, prevCanceled, curCanceled,
			"Canceled metric should not change, expected %d, got %d", prevCanceled, curCanceled)

		require.NoError(t, clus.Members[1].Restart(t))
		clus.WaitLeader(t)

		// Add 1 second buffer to ensure lease is fully expired
		time.Sleep(leaseTTL + time.Second)

		ttl, err := c.TimeToLive(t.Context(), leaseRsp.ID)
		require.NoError(t, err)
		require.Equalf(t, int64(-1), ttl.TTL, "lease should have expired (TTL=-1), got TTL=%d", ttl.TTL)
	})
}

func getLeaseKeepAliveMetric(t *testing.T, member *integration.Member, grpcCode string) int64 {
	t.Helper()
	metricVal, err := member.Metric(
		"grpc_server_handled_total",
		`grpc_method="LeaseKeepAlive"`,
		fmt.Sprintf(`grpc_code="%v"`, grpcCode),
	)
	require.NoError(t, err)
	count, err := strconv.ParseInt(metricVal, 10, 64)
	require.NoError(t, err)
	return count
}

func waitKeepAliveChannelClose(t *testing.T, ch <-chan *clientv3.LeaseKeepAliveResponse) {
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("keepAlive with require leader took too long to close")
		}
	}
}
