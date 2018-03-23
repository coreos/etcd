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

package integration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/coreos/etcd/pkg/testutil"
)

func TestV3UnsafeOverwriteDB(t *testing.T) {
	defer testutil.AfterTest(t)

	clus := NewClusterV3(t, &ClusterConfig{
		Size:          1,
		SnapshotCount: 1,
	})
	defer clus.Terminate(t)

	bepath := filepath.Join(clus.Members[0].SnapDir(), "db")
	clus.Members[0].Stop(t)

	os.RemoveAll(bepath)
	clus.Members[0].UnsafeOverwriteDB = true

	clus.Members[0].Restart(t)
	clus.WaitLeader(t)
}
