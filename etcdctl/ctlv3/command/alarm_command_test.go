// Copyright 2025 The etcd Authors
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

package command

import (
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/etcdctl/v3/ctlv3/command/fakeclient"
)

func newTestRoot() *cobra.Command {
	root := &cobra.Command{Use: "etcdctl"}
	root.AddGroup(NewKVGroup(), NewClusterMaintenanceGroup(), NewConcurrencyGroup(), NewAuthenticationGroup(), NewUtilityGroup())
	RegisterGlobalFlags(root)
	return root
}

func TestAlarmList_PrintsAlarms(t *testing.T) {
	var gotReq int
	fc := &fakeclient.Client{
		AlarmListFn: func(ctx context.Context) (*clientv3.AlarmResponse, error) {
			gotReq++
			resp := clientv3.AlarmResponse{
				Alarms: []*pb.AlarmMember{
					{MemberID: 1, Alarm: pb.AlarmType_NOSPACE},
					{MemberID: 2, Alarm: pb.AlarmType_CORRUPT},
				},
			}
			return &resp, nil
		},
	}
	root := newTestRoot()
	root.SetContext(WithClient(context.Background(), fakeclient.WrapAsClientV3(fc)))
	root.AddCommand(NewAlarmCommand())
	root.SetArgs([]string{"alarm", "list"})

	out := withStdoutCapture(t, func() { _ = root.Execute() })

	if gotReq != 1 {
		t.Fatalf("expected AlarmList to be called once, got %d", gotReq)
	}
	if !strings.Contains(out, "NOSPACE") || !strings.Contains(out, "CORRUPT") {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestAlarmDisarm_DisarmsAll(t *testing.T) {
	var disarmCalled int
	fc := &fakeclient.Client{
		AlarmDisarmFn: func(ctx context.Context, m *clientv3.AlarmMember) (*clientv3.AlarmResponse, error) {
			disarmCalled++
			return &clientv3.AlarmResponse{Alarms: []*pb.AlarmMember{}}, nil
		},
	}
	root := newTestRoot()
	root.SetContext(WithClient(context.Background(), fakeclient.WrapAsClientV3(fc)))
	root.AddCommand(NewAlarmCommand())
	root.SetArgs([]string{"alarm", "disarm"})

	_ = withStdoutCapture(t, func() { _ = root.Execute() })

	if disarmCalled != 1 {
		t.Fatalf("expected disarm to be called, got %d", disarmCalled)
	}
}
