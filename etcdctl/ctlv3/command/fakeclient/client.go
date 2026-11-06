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

package fakeclient

import (
	"bytes"
	"context"
	"io"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// Client is a lightweight fake that satisfies the small subset of etcdctl usage.
// It embeds function fields for behaviors; unimplemented methods return error.
// This keeps the fake reusable across most commands by stubbing only used calls.
type Client struct {
	AlarmListFn   func(ctx context.Context) (*clientv3.AlarmResponse, error)
	AlarmDisarmFn func(ctx context.Context, m *clientv3.AlarmMember) (*clientv3.AlarmResponse, error)
}

// WrapAsClientV3 constructs a minimal *clientv3.Client using NewCtxClient and attaches fake Maintenance.
func WrapAsClientV3(fc *Client) *clientv3.Client {
	c := clientv3.NewCtxClient(context.Background())
	c.Maintenance = &maintenance{fake: fc}
	return c
}

type maintenance struct{ fake *Client }

func (m *maintenance) AlarmList(ctx context.Context) (*clientv3.AlarmResponse, error) {
	if m.fake.AlarmListFn != nil {
		return m.fake.AlarmListFn(ctx)
	}
	return &clientv3.AlarmResponse{}, nil
}

func (m *maintenance) AlarmDisarm(ctx context.Context, am *clientv3.AlarmMember) (*clientv3.AlarmResponse, error) {
	if m.fake.AlarmDisarmFn != nil {
		return m.fake.AlarmDisarmFn(ctx, am)
	}
	return &clientv3.AlarmResponse{}, nil
}

func (m *maintenance) Defragment(ctx context.Context, endpoint string) (*clientv3.DefragmentResponse, error) {
	var resp clientv3.DefragmentResponse
	return &resp, nil
}

func (m *maintenance) Status(ctx context.Context, endpoint string) (*clientv3.StatusResponse, error) {
	var resp clientv3.StatusResponse
	return &resp, nil
}

func (m *maintenance) HashKV(ctx context.Context, endpoint string, rev int64) (*clientv3.HashKVResponse, error) {
	var resp clientv3.HashKVResponse
	return &resp, nil
}

func (m *maintenance) SnapshotWithVersion(ctx context.Context) (*clientv3.SnapshotResponse, error) {
	return &clientv3.SnapshotResponse{Snapshot: io.NopCloser(bytes.NewReader(nil))}, nil
}

func (m *maintenance) Snapshot(ctx context.Context) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}

func (m *maintenance) MoveLeader(ctx context.Context, transfereeID uint64) (*clientv3.MoveLeaderResponse, error) {
	var resp clientv3.MoveLeaderResponse
	return &resp, nil
}

func (m *maintenance) Downgrade(ctx context.Context, action clientv3.DowngradeAction, version string) (*clientv3.DowngradeResponse, error) {
	var resp clientv3.DowngradeResponse
	return &resp, nil
}
