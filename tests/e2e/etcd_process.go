// Copyright 2017 The etcd Authors
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

package e2e

import (
	"fmt"
	"net/url"
	"os"

	"go.etcd.io/etcd/client/pkg/v3/fileutil"
	"go.etcd.io/etcd/pkg/v3/expect"
	"go.uber.org/zap"
)

var (
	etcdServerReadyLines = []string{"ready to serve client requests"}
	binPath              string
	ctlBinPath           string
	utlBinPath           string
)

// etcdProcess is a process that serves etcd requests.
type etcdProcess interface {
	EndpointsV2() []string
	EndpointsV3() []string
	EndpointsMetrics() []string

	Start() error
	Restart() error
	Stop() error
	Close() error
	WithStopSignal(sig os.Signal) os.Signal
	Config() *etcdServerProcessConfig

	Logs() logsExpect
}

type logsExpect interface {
	Expect(string) (string, error)
}

type etcdServerProcess struct {
	cfg   *etcdServerProcessConfig
	proc  *expect.ExpectProcess
	donec chan struct{} // closed when Interact() terminates
}

type etcdServerProcessConfig struct {
	lg       *zap.Logger
	execPath string
	args     []string
	tlsArgs  []string

	dataDirPath string
	keepDataDir bool

	name string

	purl url.URL

	acurl string
	murl  string

	initialToken   string
	initialCluster string
}

func newEtcdServerProcess(cfg *etcdServerProcessConfig) (*etcdServerProcess, error) {
	if !fileutil.Exist(cfg.execPath) {
		return nil, fmt.Errorf("could not find etcd binary: %s", cfg.execPath)
	}
	if !cfg.keepDataDir {
		if err := os.RemoveAll(cfg.dataDirPath); err != nil {
			return nil, err
		}
	}
	return &etcdServerProcess{cfg: cfg, donec: make(chan struct{})}, nil
}

func (ep *etcdServerProcess) EndpointsV2() []string      { return []string{ep.cfg.acurl} }
func (ep *etcdServerProcess) EndpointsV3() []string      { return ep.EndpointsV2() }
func (ep *etcdServerProcess) EndpointsMetrics() []string { return []string{ep.cfg.murl} }

func (ep *etcdServerProcess) Start() error {
	if ep.proc != nil {
		panic("already started")
	}
	ep.cfg.lg.Info("starting server...", zap.String("name", ep.cfg.name))
	proc, err := spawnCmdWithLogger(ep.cfg.lg, append([]string{ep.cfg.execPath}, ep.cfg.args...))
	if err != nil {
		return err
	}
	ep.proc = proc
	err = ep.waitReady()
	if err == nil {
		ep.cfg.lg.Info("started server.", zap.String("name", ep.cfg.name))
	}
	return err
}

func (ep *etcdServerProcess) Restart() error {
	ep.cfg.lg.Info("restaring server...", zap.String("name", ep.cfg.name))
	if err := ep.Stop(); err != nil {
		return err
	}
	ep.donec = make(chan struct{})
	err := ep.Start()
	if err == nil {
		ep.cfg.lg.Info("restared server", zap.String("name", ep.cfg.name))
	}
	return err
}

func (ep *etcdServerProcess) Stop() (err error) {
	ep.cfg.lg.Info("stoping server...", zap.String("name", ep.cfg.name))
	if ep == nil || ep.proc == nil {
		return nil
	}
	err = ep.proc.Stop()
	if err != nil {
		return err
	}
	ep.proc = nil
	<-ep.donec
	ep.donec = make(chan struct{})
	if ep.cfg.purl.Scheme == "unix" || ep.cfg.purl.Scheme == "unixs" {
		err = os.Remove(ep.cfg.purl.Host + ep.cfg.purl.Path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	ep.cfg.lg.Info("stopped server.", zap.String("name", ep.cfg.name))
	return nil
}

func (ep *etcdServerProcess) Close() error {
	ep.cfg.lg.Info("closing server...", zap.String("name", ep.cfg.name))
	if err := ep.Stop(); err != nil {
		return err
	}
	if !ep.cfg.keepDataDir {
		ep.cfg.lg.Info("removing directory", zap.String("data-dir", ep.cfg.dataDirPath))
		return os.RemoveAll(ep.cfg.dataDirPath)
	}
	return nil
}

func (ep *etcdServerProcess) WithStopSignal(sig os.Signal) os.Signal {
	ret := ep.proc.StopSignal
	ep.proc.StopSignal = sig
	return ret
}

func (ep *etcdServerProcess) waitReady() error {
	defer close(ep.donec)
	return waitReadyExpectProc(ep.proc, etcdServerReadyLines)
}

func (ep *etcdServerProcess) Config() *etcdServerProcessConfig { return ep.cfg }

func (ep *etcdServerProcess) Logs() logsExpect {
	if ep.proc == nil {
		ep.cfg.lg.Panic("Please grap logs before process is stopped")
	}
	return ep.proc
}
