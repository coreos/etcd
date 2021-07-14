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

package integration

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	"go.etcd.io/etcd/pkg/v3/traceutil"
	"go.etcd.io/etcd/server/v3/storage/backend"
	"go.etcd.io/etcd/server/v3/storage/mvcc"
	"go.uber.org/zap/zaptest"
)

// TestV3StorageQuotaApply tests the V3 server respects quotas during apply
func TestV3StorageQuotaApply(t *testing.T) {
	BeforeTest(t)
	quotasize := int64(16 * os.Getpagesize())

	clus := NewClusterV3(t, &ClusterConfig{Size: 2})
	defer clus.Terminate(t)
	kvc0 := toGRPC(clus.Client(0)).KV
	kvc1 := toGRPC(clus.Client(1)).KV

	// Set a quota on one node
	clus.Members[0].QuotaBackendBytes = quotasize
	clus.Members[0].Stop(t)
	clus.Members[0].Restart(t)
	clus.waitLeader(t, clus.Members)
	waitForRestart(t, kvc0)

	key := []byte("abc")

	// test small put still works
	smallbuf := make([]byte, 1024)
	_, serr := kvc0.Put(context.TODO(), &pb.PutRequest{Key: key, Value: smallbuf})
	if serr != nil {
		t.Fatal(serr)
	}

	// test big put
	bigbuf := make([]byte, quotasize)
	_, err := kvc1.Put(context.TODO(), &pb.PutRequest{Key: key, Value: bigbuf})
	if err != nil {
		t.Fatal(err)
	}

	// quorum get should work regardless of whether alarm is raised
	_, err = kvc0.Range(context.TODO(), &pb.RangeRequest{Key: []byte("foo")})
	if err != nil {
		t.Fatal(err)
	}

	// wait until alarm is raised for sure-- poll the alarms
	stopc := time.After(5 * time.Second)
	for {
		req := &pb.AlarmRequest{Action: pb.AlarmRequest_GET}
		resp, aerr := clus.Members[0].s.Alarm(context.TODO(), req)
		if aerr != nil {
			t.Fatal(aerr)
		}
		if len(resp.Alarms) != 0 {
			break
		}
		select {
		case <-stopc:
			t.Fatalf("timed out waiting for alarm")
		case <-time.After(10 * time.Millisecond):
		}
	}

	ctx, cancel := context.WithTimeout(context.TODO(), RequestWaitTimeout)
	defer cancel()

	// small quota machine should reject put
	if _, err := kvc0.Put(ctx, &pb.PutRequest{Key: key, Value: smallbuf}); err == nil {
		t.Fatalf("past-quota instance should reject put")
	}

	// large quota machine should reject put
	if _, err := kvc1.Put(ctx, &pb.PutRequest{Key: key, Value: smallbuf}); err == nil {
		t.Fatalf("past-quota instance should reject put")
	}

	// reset large quota node to ensure alarm persisted
	clus.Members[1].Stop(t)
	clus.Members[1].Restart(t)
	clus.waitLeader(t, clus.Members)

	if _, err := kvc1.Put(context.TODO(), &pb.PutRequest{Key: key, Value: smallbuf}); err == nil {
		t.Fatalf("alarmed instance should reject put after reset")
	}
}

// TestV3AlarmDeactivate ensures that space alarms can be deactivated so puts go through.
func TestV3AlarmDeactivate(t *testing.T) {
	BeforeTest(t)

	clus := NewClusterV3(t, &ClusterConfig{Size: 3})
	defer clus.Terminate(t)
	kvc := toGRPC(clus.RandClient()).KV
	mt := toGRPC(clus.RandClient()).Maintenance

	alarmReq := &pb.AlarmRequest{
		MemberID: 123,
		Action:   pb.AlarmRequest_ACTIVATE,
		Alarm:    pb.AlarmType_NOSPACE,
	}
	if _, err := mt.Alarm(context.TODO(), alarmReq); err != nil {
		t.Fatal(err)
	}

	key := []byte("abc")
	smallbuf := make([]byte, 512)
	_, err := kvc.Put(context.TODO(), &pb.PutRequest{Key: key, Value: smallbuf})
	if err == nil && !eqErrGRPC(err, rpctypes.ErrGRPCNoSpace) {
		t.Fatalf("put got %v, expected %v", err, rpctypes.ErrGRPCNoSpace)
	}

	alarmReq.Action = pb.AlarmRequest_DEACTIVATE
	if _, err = mt.Alarm(context.TODO(), alarmReq); err != nil {
		t.Fatal(err)
	}

	if _, err = kvc.Put(context.TODO(), &pb.PutRequest{Key: key, Value: smallbuf}); err != nil {
		t.Fatal(err)
	}
}

func TestV3CorruptAlarm(t *testing.T) {
	BeforeTest(t)
	clus := NewClusterV3(t, &ClusterConfig{Size: 3})
	defer clus.Terminate(t)

	var wg sync.WaitGroup
	wg.Add(10)
	for i := 0; i < 10; i++ {
		go func() {
			defer wg.Done()
			if _, err := clus.Client(0).Put(context.TODO(), "k", "v"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	// Corrupt member 0 by modifying backend offline.
	clus.Members[0].Stop(t)
	fp := filepath.Join(clus.Members[0].DataDir, "member", "snap", "db")
	be := backend.NewDefaultBackend(fp)
	s := mvcc.NewStore(zaptest.NewLogger(t), be, nil, mvcc.StoreConfig{})
	// NOTE: cluster_proxy mode with namespacing won't set 'k', but namespace/'k'.
	s.Put([]byte("abc"), []byte("def"), 0)
	s.Put([]byte("xyz"), []byte("123"), 0)
	s.Compact(traceutil.TODO(), 5)
	s.Commit()
	s.Close()
	be.Close()

	clus.Members[1].WaitOK(t)
	clus.Members[2].WaitOK(t)
	time.Sleep(time.Second * 2)

	// Wait for cluster so Puts succeed in case member 0 was the leader.
	if _, err := clus.Client(1).Get(context.TODO(), "k"); err != nil {
		t.Fatal(err)
	}
	if _, err := clus.Client(1).Put(context.TODO(), "xyz", "321"); err != nil {
		t.Fatal(err)
	}
	if _, err := clus.Client(1).Put(context.TODO(), "abc", "fed"); err != nil {
		t.Fatal(err)
	}

	// Restart with corruption checking enabled.
	clus.Members[1].Stop(t)
	clus.Members[2].Stop(t)
	for _, m := range clus.Members {
		m.CorruptCheckTime = time.Second
		m.Restart(t)
	}
	clus.WaitLeader(t)
	time.Sleep(time.Second * 2)

	clus.Members[0].WaitStarted(t)
	resp0, err0 := clus.Client(0).Get(context.TODO(), "abc")
	if err0 != nil {
		t.Fatal(err0)
	}
	clus.Members[1].WaitStarted(t)
	resp1, err1 := clus.Client(1).Get(context.TODO(), "abc")
	if err1 != nil {
		t.Fatal(err1)
	}

	if resp0.Kvs[0].ModRevision == resp1.Kvs[0].ModRevision {
		t.Fatalf("matching ModRevision values")
	}

	for i := 0; i < 5; i++ {
		presp, perr := clus.Client(0).Put(context.TODO(), "abc", "aaa")
		if perr != nil {
			if !eqErrGRPC(perr, rpctypes.ErrCorrupt) {
				t.Fatalf("expected %v, got %+v (%v)", rpctypes.ErrCorrupt, presp, perr)
			} else {
				return
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("expected error %v after %s", rpctypes.ErrCorrupt, 5*time.Second)
}
