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

package etcdmain

import (
	errorspkg "errors"
	"fmt"
	"os"
	"runtime"
	"strings"

	"go.uber.org/zap"
	"google.golang.org/grpc"

	"go.etcd.io/etcd/client/pkg/v3/fileutil"
	"go.etcd.io/etcd/client/pkg/v3/logutil"
	"go.etcd.io/etcd/client/pkg/v3/types"
	"go.etcd.io/etcd/pkg/v3/osutil"
	"go.etcd.io/etcd/server/v3/embed"
	"go.etcd.io/etcd/server/v3/etcdserver/errors"
)

type dirType string

var (
	dirMember = dirType("member")
	dirProxy  = dirType("proxy")
	dirEmpty  = dirType("empty")
)

func startEtcdOrProxyV2(args []string) {
	err := startEtcdOrProxyV2Error(args)
	switch {
	case err == nil:
		osutil.Exit(exitCodeOK)
	case errorspkg.Is(err, ErrArgumentError):
		fmt.Println(err)
		osutil.Exit(exitCodeBadArgs)
	case errorspkg.Is(err, ErrGeneralError):
		fmt.Println(err)
		osutil.Exit(exitCodeGeneral)
	default:
		fmt.Println(err)
		osutil.Exit(exitCodeGeneral)
	}
}

func startEtcdOrProxyV2Error(args []string) error {
	grpc.EnableTracing = false

	cfg := newConfig()
	defaultInitialCluster := cfg.ec.InitialCluster

	shouldExit, err := cfg.parse(args[1:])
	if shouldExit {
		return nil
	}
	lg := cfg.ec.GetLogger()
	// If we failed to parse the whole configuration, print the error using
	// preferably the resolved logger from the config,
	// but if does not exists, create a new temporary logger.
	if lg == nil {
		var zapError error
		// use this logger
		lg, zapError = logutil.CreateDefaultZapLogger(zap.InfoLevel)
		if zapError != nil {
			fmt.Printf("error creating zap logger %v", zapError)
			return fmt.Errorf("%w: error creating zap logger: %w", ErrGeneralError, zapError)
		}
	}
	lg.Info("Running: ", zap.Strings("args", args))
	if err != nil {
		lg.Warn("failed to verify flags", zap.Error(err))
		if errorspkg.Is(err, embed.ErrUnsetAdvertiseClientURLsFlag) {
			lg.Warn("advertise client URLs are not set", zap.Error(err))
		}
		if errorspkg.Is(err, ErrArgumentError) {
			return err
		}
		return fmt.Errorf("%w: failed to verify flags: %w", ErrArgumentError, err)
	}

	cfg.ec.SetupGlobalLoggers()

	defer func() {
		logger := cfg.ec.GetLogger()
		if logger != nil {
			logger.Sync()
		}
	}()

	defaultHost, dhErr := (&cfg.ec).UpdateDefaultClusterFromName(defaultInitialCluster)
	if defaultHost != "" {
		lg.Info(
			"detected default host for advertise",
			zap.String("host", defaultHost),
		)
	}
	if dhErr != nil {
		lg.Info("failed to detect default host", zap.Error(dhErr))
	}

	if cfg.ec.Dir == "" {
		cfg.ec.Dir = fmt.Sprintf("%v.etcd", cfg.ec.Name)
		lg.Warn(
			"'data-dir' was empty; using default",
			zap.String("data-dir", cfg.ec.Dir),
		)
	}

	var stopped <-chan struct{}
	var errc <-chan error

	which := identifyDataDirOrDie(cfg.ec.GetLogger(), cfg.ec.Dir)
	if which != dirEmpty {
		lg.Info(
			"server has already been initialized",
			zap.String("data-dir", cfg.ec.Dir),
			zap.String("dir-type", string(which)),
		)
		switch which {
		case dirMember:
			stopped, errc, err = startEtcd(&cfg.ec)
		case dirProxy:
			lg.Panic("v2 http proxy has already been deprecated in 3.6", zap.String("dir-type", string(which)))
		default:
			lg.Panic(
				"unknown directory type",
				zap.String("dir-type", string(which)),
			)
		}
	} else {
		lg.Info(
			"Initialize and start etcd server",
			zap.String("data-dir", cfg.ec.Dir),
			zap.String("dir-type", string(which)),
		)
		stopped, errc, err = startEtcd(&cfg.ec)
	}

	if err != nil {
		var derr *errors.DiscoveryError
		if errorspkg.As(err, &derr) {
			lg.Warn(
				"failed to bootstrap; discovery token was already used",
				zap.String("discovery-token", cfg.ec.DiscoveryCfg.Token),
				zap.Strings("discovery-endpoints", cfg.ec.DiscoveryCfg.Endpoints),
				zap.Error(err),
			)
			lg.Warn("do not reuse discovery token; generate a new one to bootstrap a cluster")

			return fmt.Errorf("%w: discovery failed: %w", ErrGeneralError, err)
		}

		if strings.Contains(err.Error(), "include") && strings.Contains(err.Error(), "--initial-cluster") {
			lg.Warn("failed to start", zap.Error(err))
			if cfg.ec.InitialCluster == cfg.ec.InitialClusterFromName(cfg.ec.Name) {
				lg.Warn("forgot to set --initial-cluster?")
			}
			if types.URLs(cfg.ec.AdvertisePeerUrls).String() == embed.DefaultInitialAdvertisePeerURLs {
				lg.Warn("forgot to set --initial-advertise-peer-urls?")
			}
			if cfg.ec.InitialCluster == cfg.ec.InitialClusterFromName(cfg.ec.Name) && len(cfg.ec.DiscoveryCfg.Endpoints) == 0 {
				lg.Warn("V3 discovery settings (i.e., --discovery-token, --discovery-endpoints) are not set")
			}
			return fmt.Errorf("%w: failed to start: %w", ErrGeneralError, err)
		}
		lg.Error("discovery failed", zap.Error(err))
		return fmt.Errorf("%w: discovery failed: %w", ErrGeneralError, err)
	}

	osutil.HandleInterrupts(lg)

	// At this point, the initialization of etcd is done.
	// The listeners are listening on the TCP ports and ready
	// for accepting connections. The etcd instance should be
	// joined with the cluster and ready to serve incoming
	// connections.
	notifySystemd(lg)

	select {
	case lerr := <-errc:
		// exit on listener errors
		lg.Error("listener failed", zap.Error(lerr))
		return fmt.Errorf("%w: listener failed: %w", ErrGeneralError, lerr)
	case <-stopped:
	}

	return nil
}

// startEtcd runs StartEtcd in addition to hooks needed for standalone etcd.
func startEtcd(cfg *embed.Config) (<-chan struct{}, <-chan error, error) {
	e, err := embed.StartEtcd(cfg)
	if err != nil {
		return nil, nil, err
	}
	osutil.RegisterInterruptHandler(e.Close)
	select {
	case <-e.Server.ReadyNotify(): // wait for e.Server to join the cluster
	case <-e.Server.StopNotify(): // publish aborted from 'ErrStopped'
	}
	return e.Server.StopNotify(), e.Err(), nil
}

// identifyDataDirOrDie returns the type of the data dir.
// Dies if the datadir is invalid.
func identifyDataDirOrDie(lg *zap.Logger, dir string) dirType {
	names, err := fileutil.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return dirEmpty
		}
		lg.Fatal("failed to list data directory", zap.String("dir", dir), zap.Error(err))
	}

	var m, p bool
	for _, name := range names {
		switch dirType(name) {
		case dirMember:
			m = true
		case dirProxy:
			p = true
		default:
			lg.Warn(
				"found invalid file under data directory",
				zap.String("filename", name),
				zap.String("data-dir", dir),
			)
		}
	}

	if m && p {
		lg.Fatal("invalid datadir; both member and proxy directories exist")
	}
	if m {
		return dirMember
	}
	if p {
		return dirProxy
	}
	return dirEmpty
}

func checkSupportArch() {
	lg, err := logutil.CreateDefaultZapLogger(zap.InfoLevel)
	if err != nil {
		panic(err)
	}
	// To add a new platform, check https://github.com/etcd-io/website/blob/main/content/en/docs/${VERSION}/op-guide/supported-platform.md.
	// The ${VERSION} is the etcd version, e.g. v3.5, v3.6 etc.
	switch runtime.GOARCH {
	case "amd64", "arm64", "ppc64le", "s390x":
		return
	}
	// unsupported arch only configured via environment variable
	// so unset here to not parse through flag
	defer os.Unsetenv("ETCD_UNSUPPORTED_ARCH")
	if env, ok := os.LookupEnv("ETCD_UNSUPPORTED_ARCH"); ok && env == runtime.GOARCH {
		lg.Info("running etcd on unsupported architecture since ETCD_UNSUPPORTED_ARCH is set", zap.String("arch", env))
		return
	}

	lg.Error("Refusing to run etcd on unsupported architecture since ETCD_UNSUPPORTED_ARCH is not set", zap.String("arch", runtime.GOARCH))
	os.Exit(1)
}
