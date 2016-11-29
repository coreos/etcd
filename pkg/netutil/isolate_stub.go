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

// +build !linux

package netutil

func DropPort(port int) error { return nil }

func RecoverPort(port int) error { return nil }

func SetPacketCorruption(cp int) error { return nil }

func SetPacketReordering(ms int, rp int, cp int) error { return nil }

func SetPackLoss(p int) error { return nil }

func SetPartitioning(ms int, rv int) error { return nil }

func SetLatency(ms, rv int) error { return nil }

func RemoveLatency() error { return nil }

func ResetDefaultInterface() error { return nil }
