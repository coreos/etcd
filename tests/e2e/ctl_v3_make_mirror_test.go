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

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.etcd.io/etcd/tests/v3/framework/e2e"
)

func TestCtlV3MakeMirror(t *testing.T)                 { testCtl(t, makeMirrorTest) }
func TestCtlV3MakeMirrorModifyDestPrefix(t *testing.T) { testCtl(t, makeMirrorModifyDestPrefixTest) }
func TestCtlV3MakeMirrorNoDestPrefix(t *testing.T)     { testCtl(t, makeMirrorNoDestPrefixTest) }

func TestCtlV3MakeMirrorWithWatchRev(t *testing.T) {
	for _, tc := range []struct {
		name       string
		flags      []string
		destprefix string
	}{
		{name: "preserve-prefix", destprefix: "o_"},
		{name: "replace-prefix", flags: []string{"--dest-prefix", "d_"}, destprefix: "d_"},
		{name: "remove-prefix", flags: []string{"--no-dest-prefix"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testCtl(t, func(cx ctlCtx) {
				flags := append([]string{"--prefix", "o_", "--rev", "4"}, tc.flags...)
				kvs := []kv{{"o_key1", "val1"}, {"o_key2", "val2"}, {"o_key3", "val3"}, {"o_key4", "val4"}}
				kvs2 := []kvExec{{key: tc.destprefix + "key3", val: "val3"}, {key: tc.destprefix + "key4", val: "val4"}}
				testMirrorCommand(cx, flags, kvs, kvs2, "o_", tc.destprefix+"key")
			})
		})
	}
}

func makeMirrorTest(cx ctlCtx) {
	var (
		flags  []string
		kvs    = []kv{{"key1", "val1"}, {"key2", "val2"}, {"key3", "val3"}}
		kvs2   = []kvExec{{key: "key1", val: "val1"}, {key: "key2", val: "val2"}, {key: "key3", val: "val3"}}
		prefix = "key"
	)
	testMirrorCommand(cx, flags, kvs, kvs2, prefix, prefix)
}

func makeMirrorModifyDestPrefixTest(cx ctlCtx) {
	var (
		flags      = []string{"--prefix", "o_", "--dest-prefix", "d_"}
		kvs        = []kv{{"o_key1", "val1"}, {"o_key2", "val2"}, {"o_key3", "val3"}}
		kvs2       = []kvExec{{key: "d_key1", val: "val1"}, {key: "d_key2", val: "val2"}, {key: "d_key3", val: "val3"}}
		srcprefix  = "o_"
		destprefix = "d_"
	)
	testMirrorCommand(cx, flags, kvs, kvs2, srcprefix, destprefix)
}

func makeMirrorNoDestPrefixTest(cx ctlCtx) {
	var (
		flags      = []string{"--prefix", "o_", "--no-dest-prefix"}
		kvs        = []kv{{"o_key1", "val1"}, {"o_key2", "val2"}, {"o_key3", "val3"}}
		kvs2       = []kvExec{{key: "key1", val: "val1"}, {key: "key2", val: "val2"}, {key: "key3", val: "val3"}}
		srcprefix  = "o_"
		destprefix = "key"
	)

	testMirrorCommand(cx, flags, kvs, kvs2, srcprefix, destprefix)
}

func testMirrorCommand(cx ctlCtx, flags []string, sourcekvs []kv, destkvs []kvExec, srcprefix, destprefix string) {
	// set up another cluster to mirror with
	mirrorcfg := e2e.NewConfigAutoTLS()
	mirrorcfg.ClusterSize = 1
	mirrorcfg.BasePort = 10000
	mirrorctx := ctlCtx{
		t:           cx.t,
		cfg:         *mirrorcfg,
		dialTimeout: 7 * time.Second,
	}

	mirrorepc, err := e2e.NewEtcdProcessCluster(context.TODO(), cx.t, e2e.WithConfig(&mirrorctx.cfg))
	if err != nil {
		cx.t.Fatalf("could not start etcd process cluster (%v)", err)
	}
	mirrorctx.epc = mirrorepc

	defer func() {
		if err = mirrorctx.epc.Close(); err != nil {
			cx.t.Fatalf("error closing etcd processes (%v)", err)
		}
	}()

	cmdArgs := append(cx.PrefixArgs(), "make-mirror")
	cmdArgs = append(cmdArgs, flags...)
	cmdArgs = append(cmdArgs, fmt.Sprintf("localhost:%d", mirrorcfg.BasePort))
	proc, err := e2e.SpawnCmd(cmdArgs, cx.envMap)
	require.NoError(cx.t, err)
	defer func() {
		require.NoError(cx.t, proc.Stop())
	}()

	for i := range sourcekvs {
		require.NoError(cx.t, ctlV3Put(cx, sourcekvs[i].key, sourcekvs[i].val, ""))
	}
	require.NoError(cx.t, ctlV3Get(cx, []string{srcprefix, "--prefix"}, sourcekvs...))
	require.NoError(cx.t, ctlV3Watch(mirrorctx, []string{destprefix, "--rev", "1", "--prefix"}, destkvs...))
}
