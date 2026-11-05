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
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/pkg/v3/cobrautl"
)

var txnInteractive bool

// NewTxnCommand returns the cobra command for "txn".
func NewTxnCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "txn [options]",
		Short: "Txn processes all the requests in one transaction",
		Long: `Txn reads multiple etcd requests from standard input and applies them as a single atomic transaction.

A transaction consists of three components:
1) a list of conditions,
2) a list of requests to apply if all the conditions are true,
3) a list of requests to apply if any condition is false.

Example interactive stdin usage:

---
etcdctl txn -i
# compares:
mod("key1") > "0"

# success requests (get, put, delete):
put key1 "overwrote-key1"

# failure requests (get, put, delete):
put key1 "created-key1"
put key2 "some extra key"
---

Refer to https://github.com/etcd-io/etcd/blob/main/etcdctl/README.md#txn-options.`,
		Run:     txnCommandFunc,
		GroupID: groupKVID,
	}
	cmd.Flags().BoolVarP(&txnInteractive, "interactive", "i", false, "Input transaction in interactive mode")
	return cmd
}

// txnCommandFunc executes the "txn" command.
func txnCommandFunc(cmd *cobra.Command, args []string) {
	if len(args) != 0 {
		cobrautl.ExitWithError(cobrautl.ExitBadArgs, fmt.Errorf("txn command does not accept argument"))
	}

	reader := bufio.NewReader(os.Stdin)

	txn := mustClientFromCmd(cmd).Txn(context.Background())
	promptInteractive("compares:")
	txn.If(readCompares(reader)...)
	promptInteractive("success requests (get, put, del):")
	txn.Then(readOps(reader)...)
	promptInteractive("failure requests (get, put, del):")
	txn.Else(readOps(reader)...)

	resp, err := txn.Commit()
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitError, err)
	}

	display.Txn(resp)
}

func promptInteractive(s string) {
	if txnInteractive {
		fmt.Println(s)
	}
}

func readCompares(r *bufio.Reader) (cmps []clientv3.Cmp) {
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			cobrautl.ExitWithError(cobrautl.ExitInvalidInput, err)
		}

		// remove space from the line
		line = strings.TrimSpace(line)
		if len(line) == 0 {
			break
		}

		cmp, err := ParseCompare(line)
		if err != nil {
			cobrautl.ExitWithError(cobrautl.ExitInvalidInput, err)
		}
		cmps = append(cmps, *cmp)
	}

	return cmps
}

func readOps(r *bufio.Reader) (ops []clientv3.Op) {
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			cobrautl.ExitWithError(cobrautl.ExitInvalidInput, err)
		}

		// remove space from the line
		line = strings.TrimSpace(line)
		if len(line) == 0 {
			break
		}

		op, err := parseRequestUnion(line)
		if err != nil {
			cobrautl.ExitWithError(cobrautl.ExitInvalidInput, err)
		}
		ops = append(ops, *op)
	}

	return ops
}

func parseRequestUnion(line string) (*clientv3.Op, error) {
	args := Argify(line)
	if len(args) < 2 {
		return nil, fmt.Errorf("invalid txn compare request: %s", line)
	}

	opc := make(chan clientv3.Op, 1)

	put := NewPutCommand()
	put.GroupID = ""
	put.Run = func(cmd *cobra.Command, args []string) {
		key, value, opts := getPutOp(args)
		opc <- clientv3.OpPut(key, value, opts...)
	}
	get := NewGetCommand()
	get.GroupID = ""
	get.Run = func(cmd *cobra.Command, args []string) {
		key, opts := getGetOp(args)
		opc <- clientv3.OpGet(key, opts...)
	}
	del := NewDelCommand()
	del.GroupID = ""
	del.Run = func(cmd *cobra.Command, args []string) {
		key, opts := getDelOp(args)
		opc <- clientv3.OpDelete(key, opts...)
	}
	cmds := &cobra.Command{SilenceErrors: true}
	cmds.AddCommand(put, get, del)

	cmds.SetArgs(args)
	if err := cmds.Execute(); err != nil {
		return nil, fmt.Errorf("invalid txn request: %s", line)
	}

	op := <-opc
	return &op, nil
}

func ParseCompare(line string) (*clientv3.Cmp, error) {
	var (
		key string
		op  string
		val string
	)

	lparenSplit := strings.SplitN(line, "(", 2)
	if len(lparenSplit) != 2 {
		return nil, fmt.Errorf("malformed comparison: %s", line)
	}

	target := lparenSplit[0]
	n, serr := fmt.Sscanf(lparenSplit[1], "%q) %s %q", &key, &op, &val)
	if n != 3 {
		return nil, fmt.Errorf("malformed comparison: %s; got %s(%q) %s %q", line, target, key, op, val)
	}
	if serr != nil {
		return nil, fmt.Errorf("malformed comparison: %s (%w)", line, serr)
	}

	var (
		v   int64
		err error
		cmp clientv3.Cmp
	)
	switch target {
	case "ver", "version":
		if v, err = strconv.ParseInt(val, 10, 64); err == nil {
			cmp = clientv3.Compare(clientv3.Version(key), op, v)
		}
	case "c", "create":
		if v, err = strconv.ParseInt(val, 10, 64); err == nil {
			cmp = clientv3.Compare(clientv3.CreateRevision(key), op, v)
		}
	case "m", "mod":
		if v, err = strconv.ParseInt(val, 10, 64); err == nil {
			cmp = clientv3.Compare(clientv3.ModRevision(key), op, v)
		}
	case "val", "value":
		cmp = clientv3.Compare(clientv3.Value(key), op, val)
	case "lease":
		if v, err = strconv.ParseInt(val, 16, 64); err == nil {
			cmp = clientv3.Compare(clientv3.LeaseValue(key), op, v)
		}
	default:
		return nil, fmt.Errorf("malformed comparison: %s (unknown target %s)", line, target)
	}

	if err != nil {
		return nil, fmt.Errorf("invalid txn compare request: %s", line)
	}

	return &cmp, nil
}
