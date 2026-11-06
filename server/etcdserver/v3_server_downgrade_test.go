// Copyright 2026 The etcd Authors
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

package etcdserver

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/server/v3/etcdserver/errors"
)

// newServerWithoutClusterVersion returns a server whose cluster version is not
// decided yet, as during bootstrap before the version has been set.
func newServerWithoutClusterVersion(t *testing.T) *EtcdServer {
	t.Helper()
	return &EtcdServer{
		lgMu: new(sync.RWMutex),
		lg:   zaptest.NewLogger(t),
	}
}

func TestDowngradeCancelNilClusterVersion(t *testing.T) {
	s := newServerWithoutClusterVersion(t)
	require.Nil(t, s.ClusterVersion())

	resp, err := s.downgradeCancel(context.Background())
	require.ErrorIs(t, err, errors.ErrClusterVersionUnavailable)
	require.Nil(t, resp)
}

func TestDowngradeEnableMalformedTargetNilClusterVersion(t *testing.T) {
	s := newServerWithoutClusterVersion(t)

	// a malformed target version is rejected before the cluster version is consulted
	_, err := s.downgradeEnable(context.Background(), &pb.DowngradeRequest{Version: "not-a-version"})
	require.ErrorIs(t, err, errors.ErrWrongDowngradeVersionFormat)
}
