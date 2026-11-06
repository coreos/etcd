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

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/pkg/v3/cobrautl"
)

var (
	rolePermPrefix  bool
	rolePermFromKey bool
)

// NewRoleCommand returns the cobra command for "role".
func NewRoleCommand() *cobra.Command {
	ac := &cobra.Command{
		Use:     "role <subcommand>",
		Short:   "Role related commands. Use `etcdctl role --help` to see subcommands",
		Long:    "Role related commands",
		GroupID: groupAuthenticationID,
	}

	ac.AddCommand(newRoleAddCommand())
	ac.AddCommand(newRoleDeleteCommand())
	ac.AddCommand(newRoleGetCommand())
	ac.AddCommand(newRoleListCommand())
	ac.AddCommand(newRoleGrantPermissionCommand())
	ac.AddCommand(newRoleRevokePermissionCommand())

	return ac
}

func newRoleAddCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "add <role name>",
		Short: "Adds a new role",
		Run:   roleAddCommandFunc,
	}
}

func newRoleDeleteCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <role name>",
		Short: "Deletes a role",
		Run:   roleDeleteCommandFunc,
	}
}

func newRoleGetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "get <role name>",
		Short: "Gets detailed information of a role",
		Run:   roleGetCommandFunc,
	}
}

func newRoleListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Lists all roles",
		Run:   roleListCommandFunc,
	}
}

func newRoleGrantPermissionCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "grant-permission [options] <role name> <permission type> <key> [endkey]",
		Short: "Grants a key to a role",
		Run:   roleGrantPermissionCommandFunc,
	}

	cmd.Flags().BoolVar(&rolePermPrefix, "prefix", false, "grant a prefix permission")
	cmd.Flags().BoolVar(&rolePermFromKey, "from-key", false, "grant a permission of keys that are greater than or equal to the given key using byte compare")

	return cmd
}

func newRoleRevokePermissionCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "revoke-permission <role name> <key> [endkey]",
		Short: "Revokes a key from a role",
		Run:   roleRevokePermissionCommandFunc,
	}

	cmd.Flags().BoolVar(&rolePermPrefix, "prefix", false, "revoke a prefix permission")
	cmd.Flags().BoolVar(&rolePermFromKey, "from-key", false, "revoke a permission of keys that are greater than or equal to the given key using byte compare")

	return cmd
}

// roleAddCommandFunc executes the "role add" command.
func roleAddCommandFunc(cmd *cobra.Command, args []string) {
	if len(args) != 1 {
		cobrautl.ExitWithError(cobrautl.ExitBadArgs, fmt.Errorf("role add command requires role name as its argument"))
	}
	ctx, cancel := commandCtx(cmd)
	resp, err := mustClientFromCmd(cmd).Auth.RoleAdd(ctx, args[0])
	cancel()
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitError, err)
	}

	display.RoleAdd(args[0], resp)
}

// roleDeleteCommandFunc executes the "role delete" command.
func roleDeleteCommandFunc(cmd *cobra.Command, args []string) {
	if len(args) != 1 {
		cobrautl.ExitWithError(cobrautl.ExitBadArgs, fmt.Errorf("role delete command requires role name as its argument"))
	}
	ctx, cancel := commandCtx(cmd)
	resp, err := mustClientFromCmd(cmd).Auth.RoleDelete(ctx, args[0])
	cancel()
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitError, err)
	}

	display.RoleDelete(args[0], resp)
}

// roleGetCommandFunc executes the "role get" command.
func roleGetCommandFunc(cmd *cobra.Command, args []string) {
	if len(args) != 1 {
		cobrautl.ExitWithError(cobrautl.ExitBadArgs, fmt.Errorf("role get command requires role name as its argument"))
	}

	name := args[0]
	ctx, cancel := commandCtx(cmd)
	resp, err := mustClientFromCmd(cmd).Auth.RoleGet(ctx, name)
	cancel()
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitError, err)
	}

	display.RoleGet(name, resp)
}

// roleListCommandFunc executes the "role list" command.
func roleListCommandFunc(cmd *cobra.Command, args []string) {
	if len(args) != 0 {
		cobrautl.ExitWithError(cobrautl.ExitBadArgs, fmt.Errorf("role list command requires no arguments"))
	}
	ctx, cancel := commandCtx(cmd)
	resp, err := mustClientFromCmd(cmd).Auth.RoleList(ctx)
	cancel()
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitError, err)
	}

	display.RoleList(resp)
}

// roleGrantPermissionCommandFunc executes the "role grant-permission" command.
func roleGrantPermissionCommandFunc(cmd *cobra.Command, args []string) {
	if len(args) < 3 {
		cobrautl.ExitWithError(cobrautl.ExitBadArgs, fmt.Errorf("role grant command requires role name, permission type, and key [endkey] as its argument"))
	}

	perm, err := clientv3.StrToPermissionType(args[1])
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitBadArgs, err)
	}
	ctx, cancel := commandCtx(cmd)
	key, rangeEnd := permRange(args[2:])
	resp, err := mustClientFromCmd(cmd).Auth.RoleGrantPermission(ctx, args[0], key, rangeEnd, perm)
	cancel()
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitError, err)
	}

	display.RoleGrantPermission(args[0], resp)
}

// roleRevokePermissionCommandFunc executes the "role revoke-permission" command.
func roleRevokePermissionCommandFunc(cmd *cobra.Command, args []string) {
	if len(args) < 2 {
		cobrautl.ExitWithError(cobrautl.ExitBadArgs, fmt.Errorf("role revoke-permission command requires role name and key [endkey] as its argument"))
	}
	ctx, cancel := commandCtx(cmd)
	key, rangeEnd := permRange(args[1:])
	resp, err := mustClientFromCmd(cmd).Auth.RoleRevokePermission(ctx, args[0], key, rangeEnd)
	cancel()
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitError, err)
	}
	display.RoleRevokePermission(args[0], args[1], rangeEnd, resp)
}

func permRange(args []string) (string, string) {
	key := args[0]
	var rangeEnd string
	if len(key) == 0 {
		if rolePermPrefix && rolePermFromKey {
			cobrautl.ExitWithError(cobrautl.ExitBadArgs, fmt.Errorf("--from-key and --prefix flags are mutually exclusive"))
		}

		// Range permission is expressed as adt.BytesAffineInterval,
		// so the empty prefix which should be matched with every key must be like this ["\x00", <end>).
		key = "\x00"
		if rolePermPrefix || rolePermFromKey {
			// For the both cases of prefix and from-key, a permission with an empty key
			// should allow access to the entire key space.
			// 0x00 will be treated as open ended in server side.
			rangeEnd = "\x00"
		}
	} else {
		var err error
		rangeEnd, err = rangeEndFromPermFlags(args[0:])
		if err != nil {
			cobrautl.ExitWithError(cobrautl.ExitBadArgs, err)
		}
	}
	return key, rangeEnd
}

func rangeEndFromPermFlags(args []string) (string, error) {
	if len(args) == 1 {
		if rolePermPrefix {
			if rolePermFromKey {
				return "", fmt.Errorf("--from-key and --prefix flags are mutually exclusive")
			}
			return clientv3.GetPrefixRangeEnd(args[0]), nil
		}
		if rolePermFromKey {
			return "\x00", nil
		}
		// single key case
		return "", nil
	}
	if rolePermPrefix {
		return "", fmt.Errorf("unexpected endkey argument with --prefix flag")
	}
	if rolePermFromKey {
		return "", fmt.Errorf("unexpected endkey argument with --from-key flag")
	}
	return args[1], nil
}
