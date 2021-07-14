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

package e2e

import (
	"syscall"
	"testing"
	"time"
)


// TestReleaseUpgrade ensures that stopping etcdserver creates wal snapshot
func TestEtcdServerStopCreatesSnapshot(t *testing.T) {
	BeforeTest(t)
	epc, err := newEtcdProcessCluster(t, &etcdProcessClusterConfig{clusterSize: 1})
	if err != nil {
		t.Fatalf("could not start etcd process cluster (%v)", err)
	}
	defer func() {
		if err := epc.Close(); err != nil {
			t.Fatalf("error closing etcd processes (%v)", err)
		}
	}()
	// Give some minimal time for server to start, even minimal sleep reduced flakiness to 0% on 100 runs
	time.Sleep(time.Millisecond)

	// Grap logs before process finishes
	logs := epc.procs[0].Logs()

	// Send SIGTERM instead SIGKILL to allow process shutdown
	epc.procs[0].WithStopSignal(syscall.SIGTERM)
	epc.procs[0].Stop()

	_, err = logs.Expect("saved snapshot")
	if err != nil {
		t.Errorf(`Expected that server stop create a snapshot and write log "saved snapshot", err: %s`, err)
	}
}
