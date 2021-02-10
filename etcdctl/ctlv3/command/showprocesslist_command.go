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

package command

import (
	"fmt"
	"github.com/spf13/cobra"
	v3 "go.etcd.io/etcd/client/v3"
	"os"
)

type showProcessList struct {
	Ep   string                      `json:"Endpoint"`
	Resp *v3.ShowProcessListResponse `json:"Status"`
}

var (
	getShowProcessListCountOnly  bool
	getShowProcessListStreamType bool
	getShowProcessListID         *int64
)

// NewShowProcessListCommand returns the cobra command for "ShowProcessList".
func NewShowProcessListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "showprocesslist [options]",
		Short: "ShowProcessList return the current user's connections on this node",
		Run:   showProcessListCommandFunc,
	}
	cmd.PersistentFlags().BoolVar(&epClusterEndpoints, "cluster", false, "use all endpoints from the cluster member list")
	cmd.Flags().BoolVar(&getShowProcessListCountOnly, "count-only", false, "Get only the count")
	cmd.Flags().BoolVar(&getShowProcessListStreamType, "stream", false, "Get stream type processlist")
	getShowProcessListID = cmd.Flags().Int64("id", 0, "ID")
	return cmd
}

func showProcessListCommandFunc(cmd *cobra.Command, args []string) {
	if getCountOnly {
		if _, fields := display.(*fieldsPrinter); !fields {
			ExitWithError(ExitBadArgs, fmt.Errorf("--count-only is only for `--write-out=fields`"))
		}
	}

	c := mustClientFromCmd(cmd)
	var err error
	showProcessLists := []showProcessList{}
	for _, ep := range endpointsFromCluster(cmd) {
		ctx, cancel := commandCtx(cmd)
		resp, serr := c.ShowProcessList(ctx, ep, getShowProcessListCountOnly, getShowProcessListStreamType, *getShowProcessListID)
		cancel()
		if serr != nil {
			err = serr
			fmt.Fprintf(os.Stderr, "Failed to get the showProcessList of endpoint %s (%v)\n", ep, serr)
			continue
		}
		showProcessLists = append(showProcessLists, showProcessList{Ep: ep, Resp: resp})
	}

	display.ShowProcessList(showProcessLists)

	if err != nil {
		os.Exit(ExitError)
	}
}
