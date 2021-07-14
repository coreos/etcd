// Copyright 2021 The etcd Authors
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

package etcdutl

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"go.etcd.io/etcd/pkg/v3/cobrautl"
	"go.etcd.io/etcd/server/v3/datadir"
	"go.etcd.io/etcd/server/v3/storage/backend"
)

var (
	defragDataDir string
)

// NewDefragCommand returns the cobra command for "Defrag".
func NewDefragCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "defrag",
		Short: "Defragments the storage of the etcd",
		Run:   defragCommandFunc,
	}
	cmd.Flags().StringVar(&defragDataDir, "data-dir", "", "Required. Defragments a data directory not in use by etcd.")
	cmd.MarkFlagRequired("data-dir")
	cmd.MarkFlagDirname("data-dir")
	return cmd
}

func defragCommandFunc(cmd *cobra.Command, args []string) {
	err := DefragData(defragDataDir)
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitError,
			fmt.Errorf("Failed to defragment etcd data[%s] (%v)", defragDataDir, err))
	}
}

func DefragData(dataDir string) error {
	var be backend.Backend
	lg := GetLogger()
	bch := make(chan struct{})
	dbDir := datadir.ToBackendFileName(dataDir)
	go func() {
		defer close(bch)
		cfg := backend.DefaultBackendConfig()
		cfg.Logger = lg
		cfg.Path = dbDir
		be = backend.New(cfg)
	}()
	select {
	case <-bch:
	case <-time.After(time.Second):
		fmt.Fprintf(os.Stderr, "waiting for etcd to close and release its lock on %q. "+
			"To defrag a running etcd instance, use `etcdctl defrag` instead.\n", dbDir)
		<-bch
	}
	return be.Defrag()
}
