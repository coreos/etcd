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
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	"go.etcd.io/etcd/client/pkg/v3/logutil"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/pkg/v3/cobrautl"
)

var (
	epClusterEndpoints bool
	epHashKVRev        int64
)

// NewEndpointCommand returns the cobra command for "endpoint".
func NewEndpointCommand() *cobra.Command {
	ec := &cobra.Command{
		Use:     "endpoint <subcommand>",
		Short:   "Endpoint related commands. Use `etcdctl endpoint --help` to see subcommands",
		Long:    "Endpoint related commands",
		GroupID: groupClusterMaintenanceID,
	}

	ec.PersistentFlags().BoolVar(&epClusterEndpoints, "cluster", false, "use all endpoints from the cluster member list")
	ec.AddCommand(newEpHealthCommand())
	ec.AddCommand(newEpStatusCommand())
	ec.AddCommand(newEpHashKVCommand())

	return ec
}

func newEpHealthCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "health",
		Short: "Checks the healthiness of endpoints specified in `--endpoints` flag",
		Run:   epHealthCommandFunc,
	}

	return cmd
}

func newEpStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Prints out the status of endpoints specified in `--endpoints` flag",
		Long: `When --write-out is set to simple, this command prints out comma-separated status lists for each endpoint.
The items in the lists are endpoint, ID, version, db size, is leader, is learner, raft term, raft index, raft applied index, errors.
`,
		Run: epStatusCommandFunc,
	}
}

func newEpHashKVCommand() *cobra.Command {
	hc := &cobra.Command{
		Use:   "hashkv",
		Short: "Prints the KV history hash for each endpoint in --endpoints",
		Run:   epHashKVCommandFunc,
	}
	hc.PersistentFlags().Int64Var(&epHashKVRev, "rev", 0, "maximum revision to hash (default: latest revision)")
	return hc
}

type epHealth struct {
	Ep     string `json:"endpoint"`
	Health bool   `json:"health"`
	Took   string `json:"took"`
	Error  string `json:"error,omitempty"`
}

// epHealthCommandFunc executes the "endpoint-health" command.
func epHealthCommandFunc(cmd *cobra.Command, args []string) {
	lg, err := logutil.CreateDefaultZapLogger(zap.InfoLevel)
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitError, err)
	}

	cfgSpec := clientConfigFromCmd(cmd)

	var cfgs []*clientv3.Config
	for _, ep := range endpointsFromCluster(cmd) {
		cloneCfgSpec := cfgSpec.Clone()
		cloneCfgSpec.Endpoints = []string{ep}
		cfg, err := clientv3.NewClientConfig(cloneCfgSpec, lg)
		if err != nil {
			cobrautl.ExitWithError(cobrautl.ExitBadArgs, err)
		}
		cfgs = append(cfgs, cfg)
	}

	var wg sync.WaitGroup
	hch := make(chan epHealth, len(cfgs))
	for _, cfg := range cfgs {
		wg.Add(1)
		go func(cfg *clientv3.Config) {
			defer wg.Done()
			ep := cfg.Endpoints[0]
			cfg.Logger = lg.Named("client")
			cli, err := clientv3.New(*cfg)
			if err != nil {
				hch <- epHealth{Ep: ep, Health: false, Error: err.Error()}
				return
			}
			st := time.Now()
			// get a random key. As long as we can get the response without an error, the
			// endpoint is health.
			ctx, cancel := commandCtx(cmd)
			_, err = cli.Get(ctx, "health")
			eh := epHealth{Ep: ep, Health: false, Took: time.Since(st).String()}
			// permission denied is OK since proposal goes through consensus to get it
			if err == nil || errors.Is(err, rpctypes.ErrPermissionDenied) {
				eh.Health = true
			} else {
				eh.Error = err.Error()
			}

			if eh.Health {
				resp, err := cli.AlarmList(ctx)
				if err == nil && len(resp.Alarms) > 0 {
					eh.Health = false
					eh.Error = "Active Alarm(s): "
					for _, v := range resp.Alarms {
						switch v.Alarm {
						case etcdserverpb.AlarmType_NOSPACE:
							eh.Error = eh.Error + "NOSPACE "
						case etcdserverpb.AlarmType_CORRUPT:
							eh.Error = eh.Error + "CORRUPT "
						default:
							eh.Error = eh.Error + "UNKNOWN "
						}
					}
				} else if err != nil {
					eh.Health = false
					eh.Error = "Unable to fetch the alarm list"
				}
			}
			cancel()
			hch <- eh
		}(cfg)
	}

	wg.Wait()
	close(hch)

	errs := false
	var healthList []epHealth
	for h := range hch {
		healthList = append(healthList, h)
		if h.Error != "" {
			errs = true
		}
	}
	display.EndpointHealth(healthList)
	if errs {
		cobrautl.ExitWithError(cobrautl.ExitError, fmt.Errorf("unhealthy cluster"))
	}
}

type epStatus struct {
	Ep   string                   `json:"Endpoint"`
	Resp *clientv3.StatusResponse `json:"Status"`
}

func epStatusCommandFunc(cmd *cobra.Command, args []string) {
	cfg := clientConfigFromCmd(cmd)

	var statusList []epStatus
	var err error
	for _, ep := range endpointsFromCluster(cmd) {
		cfg.Endpoints = []string{ep}
		c := mustClient(cfg)
		ctx, cancel := commandCtx(cmd)
		resp, serr := c.Status(ctx, ep)
		cancel()
		c.Close()
		if serr != nil {
			err = serr
			fmt.Fprintf(os.Stderr, "Failed to get the status of endpoint %s (%v)\n", ep, serr)
			continue
		}
		statusList = append(statusList, epStatus{Ep: ep, Resp: resp})
	}

	display.EndpointStatus(statusList)

	if err != nil {
		os.Exit(cobrautl.ExitError)
	}
}

type epHashKV struct {
	Ep   string                   `json:"Endpoint"`
	Resp *clientv3.HashKVResponse `json:"HashKV"`
}

func epHashKVCommandFunc(cmd *cobra.Command, args []string) {
	cfg := clientConfigFromCmd(cmd)

	var hashList []epHashKV
	var err error
	for _, ep := range endpointsFromCluster(cmd) {
		cfg.Endpoints = []string{ep}
		c := mustClient(cfg)
		ctx, cancel := commandCtx(cmd)
		resp, serr := c.HashKV(ctx, ep, epHashKVRev)
		cancel()
		c.Close()
		if serr != nil {
			err = serr
			fmt.Fprintf(os.Stderr, "Failed to get the hash of endpoint %s (%v)\n", ep, serr)
			continue
		}
		hashList = append(hashList, epHashKV{Ep: ep, Resp: resp})
	}

	display.EndpointHashKV(hashList)

	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitError, err)
	}
}

func endpointsFromCluster(cmd *cobra.Command) []string {
	if !epClusterEndpoints {
		endpoints, err := cmd.Flags().GetStringSlice(FlagEndpoints)
		if err != nil {
			cobrautl.ExitWithError(cobrautl.ExitError, err)
		}
		return endpoints
	}

	sec := secureCfgFromCmd(cmd)
	au := authCfgFromCmd(cmd)
	dt := dialTimeoutFromCmd(cmd)
	ka := keepAliveTimeFromCmd(cmd)
	kat := keepAliveTimeoutFromCmd(cmd)
	eps, err := endpointsFromCmd(cmd)
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitError, err)
	}
	// exclude auth for not asking needless password (MemberList() doesn't need authentication)
	lg, _ := logutil.CreateDefaultZapLogger(zap.InfoLevel)

	cfg, err := clientv3.NewClientConfig(&clientv3.ConfigSpec{
		Endpoints:        eps,
		DialTimeout:      dt,
		KeepAliveTime:    ka,
		KeepAliveTimeout: kat,
		Secure:           sec,
		Auth:             au,
	}, lg)
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitError, err)
	}
	c, err := clientv3.New(*cfg)
	if err != nil {
		cobrautl.ExitWithError(cobrautl.ExitError, err)
	}

	ctx, cancel := commandCtx(cmd)
	defer func() {
		c.Close()
		cancel()
	}()
	membs, err := c.MemberList(ctx)
	if err != nil {
		err = fmt.Errorf("failed to fetch endpoints from etcd cluster member list: %w", err)
		cobrautl.ExitWithError(cobrautl.ExitError, err)
	}

	var ret []string
	for _, m := range membs.Members {
		ret = append(ret, m.ClientURLs...)
	}
	return ret
}
