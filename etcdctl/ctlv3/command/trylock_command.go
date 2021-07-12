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
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"

	"github.com/spf13/cobra"
)

var lockTimeout = 0

// NewTryLockCommand returns the cobra command for "lock".
func NewTryLockCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "trylock <lockname> [exec-command arg1 arg2 ...]",
		Short: "Try acquires a named lock, retry within the timeout period or return immediately when --timeout is 0",
		Run:   trylockCommandFunc,
	}
	c.Flags().IntVarP(&lockTTL, "ttl", "", lockTTL, "timeout for session")
	c.Flags().IntVarP(&lockTimeout, "timeout", "", lockTimeout, "retry within the timeout period")
	return c
}

func trylockCommandFunc(cmd *cobra.Command, args []string) {
	if len(args) == 0 {
		ExitWithError(ExitBadArgs, errors.New("trylock takes a lock name argument and an optional command to execute"))
	}
	c := mustClientFromCmd(cmd)
	if err := trylockUntilSignal(c, args[0], args[1:]); err != nil {
		ExitWithError(ExitError, err)
	}
}

func trylockUntilSignal(c *clientv3.Client, lockname string, cmdArgs []string) error {
	s, err := concurrency.NewSession(c, concurrency.WithTTL(lockTTL))
	if err != nil {
		return err
	}

	m := concurrency.NewMutex(s, lockname)
	ctx, cancel := context.WithCancel(context.TODO())

	// unlock in case of ordinary shutdown
	donec := make(chan struct{})
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigc
		cancel()
		close(donec)
	}()

	if lockTimeout == 0 {
		if err := m.TryLock(ctx); err != nil {
			return err
		}
	} else {
		if err := m.TryLockTimeout(ctx, time.Duration(lockTimeout)); err != nil {
			return err
		}
	}

	if len(cmdArgs) > 0 {
		cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
		cmd.Env = append(environLockResponse(m), os.Environ()...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		err := cmd.Run()
		unlockErr := m.Unlock(context.TODO())
		if err != nil {
			return err
		}
		return unlockErr
	}

	k, kerr := c.Get(ctx, m.Key())
	if kerr != nil {
		return kerr
	}
	if len(k.Kvs) == 0 {
		return errors.New("lock lost on init")
	}
	display.Get(*k)

	select {
	case <-donec:
		return m.Unlock(context.TODO())
	case <-s.Done():
	}

	return errors.New("session expired")
}
