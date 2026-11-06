// Copyright 2023 The etcd Authors
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
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"

	"go.etcd.io/etcd/pkg/v3/expect"
	"go.etcd.io/etcd/tests/v3/framework/e2e"
)

func TestCurlV3MaintenanceAlarmMissiongAlarm(t *testing.T) {
	testCtl(t, testCurlV3MaintenanceAlarmMissiongAlarm, withCfg(*e2e.NewConfigNoTLS()))
}

func testCurlV3MaintenanceAlarmMissiongAlarm(cx ctlCtx) {
	require.NoErrorf(cx.t, e2e.CURLPost(cx.epc, e2e.CURLReq{
		Endpoint: "/v3/maintenance/alarm",
		Value:    `{"action": "ACTIVATE"}`,
	}), "failed post maintenance alarm")
}

func TestCurlV3MaintenanceStatus(t *testing.T) {
	testCtl(t, testCurlV3MaintenanceStatus, withCfg(*e2e.NewConfigNoTLS()))
}

func testCurlV3MaintenanceStatus(cx ctlCtx) {
	clus := cx.epc
	args := e2e.CURLPrefixArgsCluster(clus.Cfg, clus.Procs[rand.Intn(clus.Cfg.ClusterSize)], "POST", e2e.CURLReq{
		Endpoint: "/v3/maintenance/status",
		Value:    "{}",
	})
	resp, err := runCommandAndReadJSONOutput(args)
	require.NoError(cx.t, err)

	requiredFields := []string{"version", "dbSize", "leader", "raftIndex", "raftTerm", "raftAppliedIndex", "dbSizeInUse", "storageVersion"}
	for _, field := range requiredFields {
		if _, ok := resp[field]; !ok {
			cx.t.Fatalf("Field %q not found in (%v)", field, resp)
		}
	}

	require.Equal(cx.t, e2e.ServerVersion(), resp["version"])
}

func TestCurlV3MaintenanceDefragment(t *testing.T) {
	testCtl(t, testCurlV3MaintenanceDefragment, withCfg(*e2e.NewConfigNoTLS()))
}

func testCurlV3MaintenanceDefragment(cx ctlCtx) {
	require.NoErrorf(cx.t, e2e.CURLPost(cx.epc, e2e.CURLReq{
		Endpoint: "/v3/maintenance/defragment",
		Value:    "{}",
		Expected: expect.ExpectedResponse{
			// DefragmentResponse carries a populated response header
			// (including leader_id), so the body is no longer "{}".
			Value: `"leader_id"`,
		},
	}), "failed post maintenance defragment request")
}

func TestCurlV3MaintenanceHash(t *testing.T) {
	testCtl(t, testCurlV3MaintenanceHash, withCfg(*e2e.NewConfigNoTLS()))
}

func testCurlV3MaintenanceHash(cx ctlCtx) {
	clus := cx.epc
	args := e2e.CURLPrefixArgsCluster(clus.Cfg, clus.Procs[rand.Intn(clus.Cfg.ClusterSize)], "POST", e2e.CURLReq{
		Endpoint: "/v3/maintenance/hash",
		Value:    "{}",
	})
	resp, err := runCommandAndReadJSONOutput(args)
	require.NoError(cx.t, err)

	requiredFields := []string{"header", "hash"}
	for _, field := range requiredFields {
		if _, ok := resp[field]; !ok {
			cx.t.Fatalf("Field %q not found in (%v)", field, resp)
		}
	}
}

func TestCurlV3MaintenanceHashKV(t *testing.T) {
	testCtl(t, testCurlV3MaintenanceHashKV, withCfg(*e2e.NewConfigNoTLS()))
}

func testCurlV3MaintenanceHashKV(cx ctlCtx) {
	clus := cx.epc
	args := e2e.CURLPrefixArgsCluster(clus.Cfg, clus.Procs[rand.Intn(clus.Cfg.ClusterSize)], "POST", e2e.CURLReq{
		Endpoint: "/v3/maintenance/hashkv",
		Value:    "{}",
	})
	resp, err := runCommandAndReadJSONOutput(args)
	require.NoError(cx.t, err)

	requiredFields := []string{"header", "hash", "compact_revision", "hash_revision"}
	for _, field := range requiredFields {
		if _, ok := resp[field]; !ok {
			cx.t.Fatalf("Field %q not found in (%v)", field, resp)
		}
	}
}

func TestCurlV3MaintenanceSnapshot(t *testing.T) {
	testCtl(t, testCurlV3MaintenanceSnapshot, withCfg(*e2e.NewConfigNoTLS()))
}

func testCurlV3MaintenanceSnapshot(cx ctlCtx) {
	require.NoErrorf(cx.t, e2e.CURLPost(cx.epc, e2e.CURLReq{
		Endpoint: "/v3/maintenance/snapshot",
		Value:    "{}",
		Expected: expect.ExpectedResponse{
			Value:         `"result":\{"header":\{[^}]*"leader_id":"[1-9][0-9]*"[^}]*\},"blob":`,
			IsRegularExpr: true,
		},
	}), "failed post maintenance snapshot request")
}

func TestCurlV3MaintenanceMoveleader(t *testing.T) {
	testCtl(t, testCurlV3MaintenanceMoveleader, withCfg(*e2e.NewConfigNoTLS()))
}

func testCurlV3MaintenanceMoveleader(cx ctlCtx) {
	require.NoErrorf(cx.t, e2e.CURLPost(cx.epc, e2e.CURLReq{
		Endpoint: "/v3/maintenance/transfer-leadership",
		Value:    `{"targetID": 123}`,
		Expected: expect.ExpectedResponse{
			Value: `"message":"etcdserver: bad leader transferee"`,
		},
	}), "failed post maintenance moveleader request")
}

func TestCurlV3MaintenanceDowngrade(t *testing.T) {
	testCtl(t, testCurlV3MaintenanceDowngrade, withCfg(*e2e.NewConfigNoTLS()))
}

func testCurlV3MaintenanceDowngrade(cx ctlCtx) {
	require.NoErrorf(cx.t, e2e.CURLPost(cx.epc, e2e.CURLReq{
		Endpoint: "/v3/maintenance/downgrade",
		Value:    `{"action": 0, "version": "3.0"}`,
		Expected: expect.ExpectedResponse{
			Value: `"message":"etcdserver: invalid downgrade target version"`,
		},
	}), "failed post maintenance downgrade request")
}
