// Copyright 2026 The etcd Authors
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

//go:build !windows && !plan9

package fileutil

import (
	"errors"
	"os"
	"syscall"
)

func Dup(f *os.File) (*os.File, error) {
	rc, err := f.SyscallConn()
	if err != nil {
		return nil, err
	}
	var duperr error
	var newfd int
	if err := rc.Control(func(oldfd uintptr) {
		newfd, err = syscall.Dup(int(oldfd))
	}); err != nil {
		return nil, err
	}
	if duperr != nil {
		return nil, err
	}
	if err := syscall.SetNonblock(newfd, true); err != nil {
		return nil, err
	}
	f = os.NewFile(uintptr(newfd), f.Name())
	if f == nil {
		return nil, errors.New("failed to reopen file")
	}
	return f, nil
}
