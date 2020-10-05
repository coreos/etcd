// Copyright 2015 The etcd Authors
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
	"go.etcd.io/etcd/v3/clientv3"
)

var (
	getStreamConsistency string
	getStreamLimit       int64
	getStreamPrefix      bool
	getStreamFromKey     bool
	getStreamRev         int64
	getStreamKeysOnly    bool
	printStreamValueOnly bool
)

// NewGetStreamCommand returns the cobra command for "getstream".
func NewGetStreamCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "getstream [options] <key> [range_end]",
		Short: "Gets the key or a range of keys by stream",
		Run:   getStreamCommandFunc,
	}

	cmd.Flags().StringVar(&getStreamConsistency, "consistency", "l", "Linearizable(l) or Serializable(s)")
	cmd.Flags().Int64Var(&getStreamLimit, "limit", 0, "Maximum number of results")
	cmd.Flags().BoolVar(&getStreamPrefix, "prefix", false, "Get keys with matching prefix")
	cmd.Flags().BoolVar(&getStreamFromKey, "from-key", false, "Get keys that are greater than or equal to the given key using byte compare")
	cmd.Flags().Int64Var(&getStreamRev, "rev", 0, "Specify the kv revision")
	cmd.Flags().BoolVar(&getStreamKeysOnly, "keys-only", false, "Get only the keys")
	cmd.Flags().BoolVar(&printStreamValueOnly, "print-value-only", false, `Only write values when using the "simple" output format`)
	return cmd
}

// getStreamCommandFunc executes the "getstream" command.
func getStreamCommandFunc(cmd *cobra.Command, args []string) {
	key, opts := getGetStreamOp(args)
	ctx, cancel := commandCtx(cmd)
	resp, err := mustClientFromCmd(cmd).GetStream(ctx, key, opts...)
	cancel()
	if err != nil {
		ExitWithError(ExitError, err)
	}

	if printStreamValueOnly {
		dp, simple := (display).(*simplePrinter)
		if !simple {
			ExitWithError(ExitBadArgs, fmt.Errorf("print-value-only is only for `--write-out=simple`"))
		}
		dp.valueOnly = true
	}
	display.GetStream(*resp)
}

func getGetStreamOp(args []string) (string, []clientv3.OpOption) {
	if len(args) == 0 {
		ExitWithError(ExitBadArgs, fmt.Errorf("get command needs one argument as key and an optional argument as range_end"))
	}

	if getStreamPrefix && getStreamFromKey {
		ExitWithError(ExitBadArgs, fmt.Errorf("`--prefix` and `--from-key` cannot be set at the same time, choose one"))
	}

	opts := []clientv3.OpOption{}
	switch getStreamConsistency {
	case "s":
		opts = append(opts, clientv3.WithSerializable())
	case "l":
	default:
		ExitWithError(ExitBadFeature, fmt.Errorf("unknown consistency flag %q", getStreamConsistency))
	}

	key := args[0]
	if len(args) > 1 {
		if getStreamPrefix || getStreamFromKey {
			ExitWithError(ExitBadArgs, fmt.Errorf("too many arguments, only accept one argument when `--prefix` or `--from-key` is set"))
		}
		opts = append(opts, clientv3.WithRange(args[1]))
	}

	opts = append(opts, clientv3.WithLimit(getStreamLimit))
	if getStreamRev > 0 {
		opts = append(opts, clientv3.WithRev(getStreamRev))
	}

	if getStreamPrefix {
		if len(key) == 0 {
			key = "\x00"
			opts = append(opts, clientv3.WithFromKey())
		} else {
			opts = append(opts, clientv3.WithPrefix())
		}
	}

	if getStreamFromKey {
		if len(key) == 0 {
			key = "\x00"
		}
		opts = append(opts, clientv3.WithFromKey())
	}

	if getStreamKeysOnly {
		opts = append(opts, clientv3.WithKeysOnly())
	}

	return key, opts
}
