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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/pkg/v3/cobrautl"
)

var (
	errBadArgsNum              = errors.New("bad number of arguments")
	errBadArgsNumConflictEnv   = errors.New("bad number of arguments (found conflicting environment key)")
	errBadArgsNumSeparator     = errors.New("bad number of arguments (found separator --, but no commands)")
	errBadArgsInteractiveWatch = errors.New("args[0] must be 'watch' for interactive calls")
)

var (
	watchRev         int64
	watchPrefix      bool
	watchInteractive bool
	watchPrevKey     bool
	progressNotify   bool
)

// NewWatchCommand returns the cobra command for "watch".
func NewWatchCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "watch [options] [key or prefix] [range_end] [--] [exec-command arg1 arg2 ...]",
		Short:   "Watches events stream on keys or prefixes",
		Run:     watchCommandFunc,
		GroupID: groupKVID,
	}

	cmd.Flags().BoolVarP(&watchInteractive, "interactive", "i", false, "Interactive mode")
	cmd.Flags().BoolVar(&watchPrefix, "prefix", false, "Watch on a prefix if prefix is set")
	cmd.Flags().Int64Var(&watchRev, "rev", 0, "Revision to start watching")
	cmd.Flags().BoolVar(&watchPrevKey, "prev-kv", false, "get the previous key-value pair before the event happens")
	cmd.Flags().BoolVar(&progressNotify, "progress-notify", false, "get periodic watch progress notification from server")

	return cmd
}

// watchCommandFunc executes the "watch" command.
func watchCommandFunc(cmd *cobra.Command, args []string) {
	envKey, envRange := os.Getenv("ETCDCTL_WATCH_KEY"), os.Getenv("ETCDCTL_WATCH_RANGE_END")
	if envKey == "" && envRange != "" {
		cobrautl.ExitWithError(cobrautl.ExitBadArgs, fmt.Errorf("ETCDCTL_WATCH_KEY is empty but got ETCDCTL_WATCH_RANGE_END=%q", envRange))
	}

	if watchInteractive {
		watchInteractiveFunc(cmd, os.Args, envKey, envRange)
		return
	}

	watchArgs, execArgs, err := parseWatchArgs(os.Args, args, envKey, envRange, false)
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitBadArgs, err)
	}

	c := mustBlockClientFromCmd(cmd)
	wc, err := getWatchChan(c, watchArgs)
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitBadArgs, err)
	}

	printWatchCh(c, wc, execArgs)
	if err = c.Close(); err != nil {
		cobrautl.ExitWithError(cobrautl.ExitBadConnection, err)
	}
	cobrautl.ExitWithError(cobrautl.ExitInterrupted, fmt.Errorf("watch is canceled by the server"))
}

func watchInteractiveFunc(cmd *cobra.Command, osArgs []string, envKey, envRange string) {
	c := mustClientFromCmd(cmd)

	reader := bufio.NewReader(os.Stdin)

	for {
		l, err := reader.ReadString('\n')
		if err != nil {
			cobrautl.ExitWithError(cobrautl.ExitInvalidInput, fmt.Errorf("error reading watch request line: %w", err))
		}
		l = strings.TrimSuffix(l, "\n")

		args := Argify(l)
		if len(args) < 1 {
			fmt.Fprintf(os.Stderr, "Invalid command: %s (watch and progress supported)\n", l)
			continue
		}
		switch args[0] {
		case "watch":
			if len(args) < 2 && envKey == "" {
				fmt.Fprintf(os.Stderr, "Invalid command %s (command type or key is not provided)\n", l)
				continue
			}
			watchArgs, execArgs, perr := parseWatchArgs(osArgs, args, envKey, envRange, true)
			if perr != nil {
				cobrautl.ExitWithError(cobrautl.ExitBadArgs, perr)
			}

			ch, err := getWatchChan(c, watchArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Invalid command %s (%v)\n", l, err)
				continue
			}
			go printWatchCh(c, ch, execArgs)
		case "progress":
			err := c.RequestProgress(clientv3.WithRequireLeader(context.Background()))
			if err != nil {
				cobrautl.ExitWithError(cobrautl.ExitError, err)
			}
		default:
			fmt.Fprintf(os.Stderr, "Invalid command %s (only support watch)\n", l)
			continue
		}
	}
}

func getWatchChan(c *clientv3.Client, args []string) (clientv3.WatchChan, error) {
	if len(args) < 1 {
		return nil, errBadArgsNum
	}

	key := args[0]
	opts := []clientv3.OpOption{clientv3.WithRev(watchRev)}
	if len(args) == 2 {
		if watchPrefix {
			return nil, fmt.Errorf("`range_end` and `--prefix` are mutually exclusive")
		}
		opts = append(opts, clientv3.WithRange(args[1]))
	}
	if watchPrefix {
		opts = append(opts, clientv3.WithPrefix())
	}
	if watchPrevKey {
		opts = append(opts, clientv3.WithPrevKV())
	}
	if progressNotify {
		opts = append(opts, clientv3.WithProgressNotify())
	}
	return c.Watch(clientv3.WithRequireLeader(context.Background()), key, opts...), nil
}

func printWatchCh(c *clientv3.Client, ch clientv3.WatchChan, execArgs []string) {
	for resp := range ch {
		if resp.Canceled {
			fmt.Fprintf(os.Stderr, "watch was canceled (%v)\n", resp.Err())
		}
		if resp.IsProgressNotify() {
			fmt.Fprintf(os.Stdout, "progress notify: %d\n", resp.Header.GetRevision())
		}
		display.Watch(&resp)

		if len(execArgs) > 0 {
			for _, event := range resp.Events {
				cmd := exec.CommandContext(c.Ctx(), execArgs[0], execArgs[1:]...)
				cmd.Env = os.Environ()
				cmd.Env = append(cmd.Env, fmt.Sprintf("ETCD_WATCH_REVISION=%d", resp.Header.GetRevision()))
				cmd.Env = append(cmd.Env, fmt.Sprintf("ETCD_WATCH_EVENT_TYPE=%q", event.GetType()))
				cmd.Env = append(cmd.Env, fmt.Sprintf("ETCD_WATCH_KEY=%q", event.GetKv().GetKey()))
				cmd.Env = append(cmd.Env, fmt.Sprintf("ETCD_WATCH_VALUE=%q", event.GetKv().GetValue()))
				cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
				if err := cmd.Run(); err != nil {
					fmt.Fprintf(os.Stderr, "command %q error (%v)\n", execArgs, err)
					os.Exit(1)
				}
			}
		}
	}
}

// "commandArgs" is the command arguments after "spf13/cobra" parses
// all "watch" command flags, strips out special characters (e.g. "--").
// "orArgs" is the raw arguments passed to "watch" command
// (e.g. ./bin/etcdctl watch foo --rev 1 bar).
// "--" characters are invalid arguments for "spf13/cobra" library,
// so no need to handle such cases.
func parseWatchArgs(osArgs, commandArgs []string, envKey, envRange string, interactive bool) (watchArgs []string, execArgs []string, err error) {
	rawArgs := make([]string, len(osArgs))
	copy(rawArgs, osArgs)
	watchArgs = make([]string, len(commandArgs))
	copy(watchArgs, commandArgs)

	// remove preceding commands (e.g. ./bin/etcdctl watch)
	// handle "./bin/etcdctl watch foo -- echo watch event"
	for idx := range rawArgs {
		if rawArgs[idx] == "watch" {
			rawArgs = rawArgs[idx+1:]
			break
		}
	}

	// remove preceding commands (e.g. "watch foo bar" in interactive mode)
	// handle "./bin/etcdctl watch foo -- echo watch event"
	if interactive {
		if watchArgs[0] != "watch" {
			// "watch" not found
			watchPrefix, watchRev, watchPrevKey = false, 0, false
			return nil, nil, errBadArgsInteractiveWatch
		}
		watchArgs = watchArgs[1:]
	}

	execIdx, execExist := 0, false
	if !interactive {
		for execIdx = range rawArgs {
			if rawArgs[execIdx] == "--" {
				execExist = true
				break
			}
		}
		if execExist && execIdx == len(rawArgs)-1 {
			// "watch foo bar --" should error
			return nil, nil, errBadArgsNumSeparator
		}
		// "watch" with no argument should error
		if !execExist && len(rawArgs) < 1 && envKey == "" {
			return nil, nil, errBadArgsNum
		}
		if execExist && envKey != "" {
			// "ETCDCTL_WATCH_KEY=foo watch foo -- echo 1" should error
			// (watchArgs==["foo","echo","1"])
			widx, ridx := len(watchArgs)-1, len(rawArgs)-1
			for ; widx >= 0; widx-- {
				if watchArgs[widx] == rawArgs[ridx] {
					ridx--
					continue
				}
				// watchArgs has extra:
				// ETCDCTL_WATCH_KEY=foo watch foo  --  echo 1
				// watchArgs:                       foo echo 1
				if ridx == execIdx {
					return nil, nil, errBadArgsNumConflictEnv
				}
			}
		}
		// check conflicting arguments
		// e.g. "watch --rev 1 -- echo Hello World" has no conflict
		if !execExist && len(watchArgs) > 0 && envKey != "" {
			// "ETCDCTL_WATCH_KEY=foo watch foo" should error
			// (watchArgs==["foo"])
			return nil, nil, errBadArgsNumConflictEnv
		}
	} else {
		for execIdx = range watchArgs {
			if watchArgs[execIdx] == "--" {
				execExist = true
				break
			}
		}
		if execExist && execIdx == len(watchArgs)-1 {
			// "watch foo bar --" should error
			watchPrefix, watchRev, watchPrevKey = false, 0, false
			return nil, nil, errBadArgsNumSeparator
		}

		flagset := NewWatchCommand().Flags()
		if perr := flagset.Parse(watchArgs); perr != nil {
			watchPrefix, watchRev, watchPrevKey = false, 0, false
			return nil, nil, perr
		}
		pArgs := flagset.Args()

		// "watch" with no argument should error
		if !execExist && envKey == "" && len(pArgs) < 1 {
			watchPrefix, watchRev, watchPrevKey = false, 0, false
			return nil, nil, errBadArgsNum
		}
		// check conflicting arguments
		// e.g. "watch --rev 1 -- echo Hello World" has no conflict
		if !execExist && len(pArgs) > 0 && envKey != "" {
			// "ETCDCTL_WATCH_KEY=foo watch foo" should error
			// (watchArgs==["foo"])
			watchPrefix, watchRev, watchPrevKey = false, 0, false
			return nil, nil, errBadArgsNumConflictEnv
		}
	}

	argsWithSep := rawArgs
	if interactive {
		// interactive mode directly passes "--" to the command args
		argsWithSep = watchArgs
	}

	idx, foundSep := 0, false
	for idx = range argsWithSep {
		if argsWithSep[idx] == "--" {
			foundSep = true
			break
		}
	}
	if foundSep {
		execArgs = argsWithSep[idx+1:]
	}

	if interactive {
		flagset := NewWatchCommand().Flags()
		if perr := flagset.Parse(argsWithSep); perr != nil {
			return nil, nil, perr
		}
		watchArgs = flagset.Args()

		watchPrefix, err = flagset.GetBool("prefix")
		if err != nil {
			return nil, nil, err
		}
		watchRev, err = flagset.GetInt64("rev")
		if err != nil {
			return nil, nil, err
		}
		watchPrevKey, err = flagset.GetBool("prev-kv")
		if err != nil {
			return nil, nil, err
		}
	}

	// "ETCDCTL_WATCH_KEY=foo watch -- echo hello"
	// should translate "watch foo -- echo hello"
	// (watchArgs=["echo","hello"] should be ["foo","echo","hello"])
	if envKey != "" {
		ranges := []string{envKey}
		if envRange != "" {
			ranges = append(ranges, envRange)
		}
		watchArgs = append(ranges, watchArgs...)
	}

	if !foundSep {
		return watchArgs, nil, nil
	}

	// "watch foo bar --rev 1 -- echo hello" or "watch foo --rev 1 bar -- echo hello",
	// then "watchArgs" is "foo bar echo hello"
	// so need ignore args after "argsWithSep[idx]", which is "--"
	endIdx := 0
	for endIdx = len(watchArgs) - 1; endIdx >= 0; endIdx-- {
		if watchArgs[endIdx] == argsWithSep[idx+1] {
			break
		}
	}
	watchArgs = watchArgs[:endIdx]

	return watchArgs, execArgs, nil
}
