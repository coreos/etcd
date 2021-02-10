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
	"os"
	"strconv"
)

var (
	getKillStreamType bool
)

func NewKillCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kill [options] <ID>",
		Short: "kill the connection from show processlist",
		Run:   killCommandFunc,
	}
	cmd.Flags().BoolVar(&getKillStreamType, "stream", false, "kill stream type connection from processlist")
	return cmd
}

func killCommandFunc(cmd *cobra.Command, args []string) {
	if len(args) == 0 {
		ExitWithError(ExitBadArgs, fmt.Errorf("Must input an ID. \n"))
	}

	idStr := args[0]
	id, err := strconv.Atoi(idStr)
	if err != nil {
		ExitWithError(ExitBadArgs, fmt.Errorf("ID must a number type. \n"))
	}

	c := mustClientFromCmd(cmd)

	endpoints := endpointsFromCluster(cmd)
	if len(endpoints) > 0 {
		fmt.Printf("send kill command to towards IP is %s, id=%d \n", endpoints[0], id)
		ctx, cancel := commandCtx(cmd)
		_, serr := c.Kill(ctx, endpoints[0], getKillStreamType, int64(id))
		cancel()
		if serr != nil {
			fmt.Fprintf(os.Stderr, "Failed to kill of endpoint %s (%v)\n", endpoints[0], serr)
			os.Exit(ExitError)
		}
	}
}
