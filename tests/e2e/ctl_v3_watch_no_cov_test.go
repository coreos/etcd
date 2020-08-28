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

// +build !cov

package e2e

import (
	"os"
	"testing"
)

func TestCtlV3Watch(t *testing.T)              { testCtl(t, watchTest) }
func TestCtlV3WatchNoTLS(t *testing.T)         { testCtl(t, watchTest, withCfg(configNoTLS)) }
func TestCtlV3WatchClientTLS(t *testing.T)     { testCtl(t, watchTest, withCfg(configClientTLS)) }
func TestCtlV3WatchPeerTLS(t *testing.T)       { testCtl(t, watchTest, withCfg(configPeerTLS)) }
func TestCtlV3WatchClientAutoTLS(t *testing.T) { testCtl(t, watchTest, withCfg(configAutoTLS)) }
func TestCtlV3WatchTimeout(t *testing.T)       { testCtl(t, watchTest, withDialTimeout(-1)) }

func TestCtlV3WatchInteractive(t *testing.T) {
	testCtl(t, watchTest, withInteractive())
}
func TestCtlV3WatchInteractiveNoTLS(t *testing.T) {
	testCtl(t, watchTest, withInteractive(), withCfg(configNoTLS))
}
func TestCtlV3WatchInteractiveClientTLS(t *testing.T) {
	testCtl(t, watchTest, withInteractive(), withCfg(configClientTLS))
}
func TestCtlV3WatchInteractivePeerTLS(t *testing.T) {
	testCtl(t, watchTest, withInteractive(), withCfg(configPeerTLS))
}
func TestCtlV3WatchInteractiveClientAutoTLS(t *testing.T) {
	testCtl(t, watchTest, withInteractive(), withCfg(configAutoTLS))
}
func TestCtlV3WatchInteractiveTimeout(t *testing.T) {
	testCtl(t, watchTest, withInteractive(), withDialTimeout(0), withCfg(configAutoTLS))
}

func watchTest(cx ctlCtx) {
	tests := []struct {
		puts     []kv
		envKey   string
		envRange string
		args     []string

		wkv []kvExec
	}{
		{ // watch 1 key
			puts: []kv{{"sample", "value"}},
			args: []string{"sample", "--rev", "1"},
			wkv:  []kvExec{{key: "sample", val: "value"}},
		},
		{ // watch 1 key with env
			puts:   []kv{{"sample", "value"}},
			envKey: "sample",
			args:   []string{"--rev", "1"},
			wkv:    []kvExec{{key: "sample", val: "value"}},
		},
		{ // watch 1 key with ${ETCD_WATCH_VALUE}
			puts: []kv{{"sample", "value"}},
			args: []string{"sample", "--rev", "1", "--", "env"},
			wkv:  []kvExec{{key: "sample", val: "value", execOutput: `ETCD_WATCH_VALUE="value"`}},
		},
		{ // watch 1 key with "echo watch event received", with env
			puts:   []kv{{"sample", "value"}},
			envKey: "sample",
			args:   []string{"--rev", "1", "--", "echo", "watch event received"},
			wkv:    []kvExec{{key: "sample", val: "value", execOutput: "watch event received"}},
		},
		{ // watch 1 key with "echo watch event received"
			puts: []kv{{"sample", "value"}},
			args: []string{"--rev", "1", "sample", "--", "echo", "watch event received"},
			wkv:  []kvExec{{key: "sample", val: "value", execOutput: "watch event received"}},
		},
		{ // watch 1 key with "echo \"Hello World!\""
			puts: []kv{{"sample", "value"}},
			args: []string{"--rev", "1", "sample", "--", "echo", "\"Hello World!\""},
			wkv:  []kvExec{{key: "sample", val: "value", execOutput: "Hello World!"}},
		},
		{ // watch 1 key with "echo watch event received"
			puts: []kv{{"sample", "value"}},
			args: []string{"sample", "samplx", "--rev", "1", "--", "echo", "watch event received"},
			wkv:  []kvExec{{key: "sample", val: "value", execOutput: "watch event received"}},
		},
		{ // watch 1 key with "echo watch event received"
			puts:     []kv{{"sample", "value"}},
			envKey:   "sample",
			envRange: "samplx",
			args:     []string{"--rev", "1", "--", "echo", "watch event received"},
			wkv:      []kvExec{{key: "sample", val: "value", execOutput: "watch event received"}},
		},
		{ // watch 1 key with "echo watch event received"
			puts: []kv{{"sample", "value"}},
			args: []string{"sample", "--rev", "1", "samplx", "--", "echo", "watch event received"},
			wkv:  []kvExec{{key: "sample", val: "value", execOutput: "watch event received"}},
		},
		{ // watch 3 keys by prefix
			puts: []kv{{"key1", "val1"}, {"key2", "val2"}, {"key3", "val3"}},
			args: []string{"key", "--rev", "1", "--prefix"},
			wkv:  []kvExec{{key: "key1", val: "val1"}, {key: "key2", val: "val2"}, {key: "key3", val: "val3"}},
		},
		{ // watch 3 keys by prefix, with env
			puts:   []kv{{"key1", "val1"}, {"key2", "val2"}, {"key3", "val3"}},
			envKey: "key",
			args:   []string{"--rev", "1", "--prefix"},
			wkv:    []kvExec{{key: "key1", val: "val1"}, {key: "key2", val: "val2"}, {key: "key3", val: "val3"}},
		},
		{ // watch by revision
			puts: []kv{{"etcd", "revision_1"}, {"etcd", "revision_2"}, {"etcd", "revision_3"}},
			args: []string{"etcd", "--rev", "2"},
			wkv:  []kvExec{{key: "etcd", val: "revision_2"}, {key: "etcd", val: "revision_3"}},
		},
		{ // watch 3 keys by range
			puts: []kv{{"key1", "val1"}, {"key3", "val3"}, {"key2", "val2"}},
			args: []string{"key", "key3", "--rev", "1"},
			wkv:  []kvExec{{key: "key1", val: "val1"}, {key: "key2", val: "val2"}},
		},
		{ // watch 3 keys by range, with env
			puts:     []kv{{"key1", "val1"}, {"key3", "val3"}, {"key2", "val2"}},
			envKey:   "key",
			envRange: "key3",
			args:     []string{"--rev", "1"},
			wkv:      []kvExec{{key: "key1", val: "val1"}, {key: "key2", val: "val2"}},
		},
	}

	for i, tt := range tests {
		donec := make(chan struct{})
		go func(i int, puts []kv) {
			for j := range puts {
				if err := ctlV3Put(cx, puts[j].key, puts[j].val, ""); err != nil {
					cx.t.Errorf("watchTest #%d-%d: ctlV3Put error (%v)", i, j, err)
				}
			}
			close(donec)
		}(i, tt.puts)

		unsetEnv := func() {}
		if tt.envKey != "" || tt.envRange != "" {
			if tt.envKey != "" {
				os.Setenv("ETCDCTL_WATCH_KEY", tt.envKey)
				unsetEnv = func() { os.Unsetenv("ETCDCTL_WATCH_KEY") }
			}
			if tt.envRange != "" {
				os.Setenv("ETCDCTL_WATCH_RANGE_END", tt.envRange)
				unsetEnv = func() { os.Unsetenv("ETCDCTL_WATCH_RANGE_END") }
			}
			if tt.envKey != "" && tt.envRange != "" {
				unsetEnv = func() {
					os.Unsetenv("ETCDCTL_WATCH_KEY")
					os.Unsetenv("ETCDCTL_WATCH_RANGE_END")
				}
			}
		}
		if err := ctlV3Watch(cx, tt.args, tt.wkv...); err != nil {
			if cx.dialTimeout > 0 && !isGRPCTimedout(err) {
				cx.t.Errorf("watchTest #%d: ctlV3Watch error (%v)", i, err)
			}
		}
		unsetEnv()
		<-donec
	}
}
