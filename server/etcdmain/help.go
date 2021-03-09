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
	"fmt"
	"strconv"

	"go.etcd.io/etcd/server/v3/embed"
	"golang.org/x/crypto/bcrypt"
)

var (
	usageline = `Usage:

  etcd [flags]
    Start an etcd server.

  etcd --version
    Show the version of etcd.

  etcd -h | --help
    Show the help information about etcd.

  etcd --config-file
    Path to the server configuration file. Note that if a configuration file is provided, other command line flags and environment variables will be ignored.

  etcd gateway
    Run the stateless pass-through etcd TCP connection forwarding proxy.

  etcd grpc-proxy
    Run the stateless etcd v3 gRPC L7 reverse proxy.
`
	flagsline = `
Member:
  --name 'default'
    Human-readable name for this member.
  --data-dir '${name}.etcd'
    Path to the data directory.
  --wal-dir ''
    Path to the dedicated wal directory.
  --snapshot-count '100000'
    Number of committed transactions to trigger a snapshot to disk.
  --heartbeat-interval '100'
    Time (in milliseconds) of a heartbeat interval.
  --election-timeout '1000'
    Time (in milliseconds) for an election to timeout. See tuning documentation for details.
  --initial-election-tick-advance 'true'
    Whether to fast-forward initial election ticks on boot for faster election.
  --listen-peer-urls 'http://localhost:2380'
    List of URLs to listen on for peer traffic.
  --listen-client-urls 'http://localhost:2379'
    List of URLs to listen on for client traffic.
  --max-snapshots '` + strconv.Itoa(embed.DefaultMaxSnapshots) + `'
    Maximum number of snapshot files to retain (0 is unlimited).
  --max-wals '` + strconv.Itoa(embed.DefaultMaxWALs) + `'
    Maximum number of wal files to retain (0 is unlimited).
  --quota-backend-bytes '0'
    Raise alarms when backend size exceeds the given quota (0 defaults to low space quota).
  --backend-bbolt-freelist-type 'map'
    BackendFreelistType specifies the type of freelist that boltdb backend uses(array and map are supported types).
  --backend-batch-interval ''
    BackendBatchInterval is the maximum time before commit the backend transaction.
  --backend-batch-limit '0'
    BackendBatchLimit is the maximum operations before commit the backend transaction.
  --max-txn-ops '128'
    Maximum number of operations permitted in a transaction.
  --max-request-bytes '1572864'
    Maximum client request size in bytes the server will accept.
  --grpc-keepalive-min-time '5s'
    Minimum duration interval that a client should wait before pinging server.
  --grpc-keepalive-interval '2h'
    Frequency duration of server-to-client ping to check if a connection is alive (0 to disable).
  --grpc-keepalive-timeout '20s'
    Additional duration of wait before closing a non-responsive connection (0 to disable).
  --socket-reuse-port 'false'
    Enable to set socket option SO_REUSEPORT on listeners allowing rebinding of a port already in use.
  --socket-reuse-address 'false'
	Enable to set socket option SO_REUSEADDR on listeners allowing binding to an address in TIME_WAIT state.

Clustering:
  --initial-advertise-peer-urls 'http://localhost:2380'
    List of this member's peer URLs to advertise to the rest of the cluster.
  --initial-cluster 'default=http://localhost:2380'
    Initial cluster configuration for bootstrapping.
  --initial-cluster-state 'new'
    Initial cluster state ('new' or 'existing').
  --initial-cluster-token 'etcd-cluster'
    Initial cluster token for the etcd cluster during bootstrap.
    Specifying this can protect you from unintended cross-cluster interaction when running multiple clusters.
  --advertise-client-urls 'http://localhost:2379'
    List of this member's client URLs to advertise to the public.
    The client URLs advertised should be accessible to machines that talk to etcd cluster. etcd client libraries parse these URLs to connect to the cluster.
  --discovery ''
    Discovery URL used to bootstrap the cluster.
  --discovery-fallback 'proxy'
    Expected behavior ('exit' or 'proxy') when discovery services fails.
    "proxy" supports v2 API only.
  --discovery-proxy ''
    HTTP proxy to use for traffic to discovery service.
  --discovery-srv ''
    DNS srv domain used to bootstrap the cluster.
  --discovery-srv-name ''
    Suffix to the dns srv name queried when bootstrapping.
  --strict-reconfig-check '` + strconv.FormatBool(embed.DefaultStrictReconfigCheck) + `'
    Reject reconfiguration requests that would cause quorum loss.
  --pre-vote 'false'
    Enable to run an additional Raft election phase.
  --auto-compaction-retention '0'
    Auto compaction retention length. 0 means disable auto compaction.
  --auto-compaction-mode 'periodic'
    Interpret 'auto-compaction-retention' one of: periodic|revision. 'periodic' for duration based retention, defaulting to hours if no time unit is provided (e.g. '5m'). 'revision' for revision number based retention.
  --enable-v2 '` + strconv.FormatBool(embed.DefaultEnableV2) + `'
    Accept etcd V2 client requests.

Security:
  --cert-file ''
    Path to the client server TLS cert file.
  --key-file ''
    Path to the client server TLS key file.
  --client-cert-auth 'false'
    Enable client cert authentication.
  --client-crl-file ''
    Path to the client certificate revocation list file.
  --client-cert-allowed-hostname ''
    Allowed TLS hostname for client cert authentication.
  --trusted-ca-file ''
    Path to the client server TLS trusted CA cert file.
  --auto-tls 'false'
    Client TLS using generated certificates.
  --peer-cert-file ''
    Path to the peer server TLS cert file.
  --peer-key-file ''
    Path to the peer server TLS key file.
  --peer-client-cert-auth 'false'
    Enable peer client cert authentication.
  --peer-trusted-ca-file ''
    Path to the peer server TLS trusted CA file.
  --peer-cert-allowed-cn ''
    Required CN for client certs connecting to the peer endpoint.
  --peer-cert-allowed-hostname ''
    Allowed TLS hostname for inter peer authentication.
  --peer-auto-tls 'false'
    Peer TLS using self-generated certificates if --peer-key-file and --peer-cert-file are not provided.
  --self-signed-cert-validity '1'
    The validity period of the client and peer certificates that are automatically generated by etcd when you specify ClientAutoTLS and PeerAutoTLS, the unit is year, and the default is 1.
  --peer-crl-file ''
    Path to the peer certificate revocation list file.
  --cipher-suites ''
    Comma-separated list of supported TLS cipher suites between client/server and peers (empty will be auto-populated by Go).
  --cors '*'
    Comma-separated whitelist of origins for CORS, or cross-origin resource sharing, (empty or * means allow all).
  --host-whitelist '*'
    Acceptable hostnames from HTTP client requests, if server is not secure (empty or * means allow all).

Auth:
  --auth-token 'simple'
    Specify a v3 authentication token type and its options ('simple' or 'jwt').
  --bcrypt-cost ` + fmt.Sprintf("%d", bcrypt.DefaultCost) + `
    Specify the cost / strength of the bcrypt algorithm for hashing auth passwords. Valid values are between ` + fmt.Sprintf("%d", bcrypt.MinCost) + ` and ` + fmt.Sprintf("%d", bcrypt.MaxCost) + `.
  --auth-token-ttl 300
    Time (in seconds) of the auth-token-ttl.

Profiling and Monitoring:
  --enable-pprof 'false'
    Enable runtime profiling data via HTTP server. Address is at client URL + "/debug/pprof/"
  --metrics 'basic'
    Set level of detail for exported metrics, specify 'extensive' to include server side grpc histogram metrics.
  --listen-metrics-urls ''
    List of URLs to listen on for the metrics and health endpoints.

Logging:
  --logger 'zap'
    Currently only supports 'zap' for structured logging.
  --log-outputs 'default'
    Specify 'stdout' or 'stderr' to skip journald logging even when running under systemd, or list of comma separated output targets.
  --log-level 'info'
    Configures log level. Only supports debug, info, warn, error, panic, or fatal.

v2 Proxy (to be deprecated in v4):
  --proxy 'off'
    Proxy mode setting ('off', 'readonly' or 'on').
  --proxy-failure-wait 5000
    Time (in milliseconds) an endpoint will be held in a failed state.
  --proxy-refresh-interval 30000
    Time (in milliseconds) of the endpoints refresh interval.
  --proxy-dial-timeout 1000
    Time (in milliseconds) for a dial to timeout.
  --proxy-write-timeout 5000
    Time (in milliseconds) for a write to timeout.
  --proxy-read-timeout 0
    Time (in milliseconds) for a read to timeout.

Experimental feature:
  --experimental-initial-corrupt-check 'false'
    Enable to check data corruption before serving any client/peer traffic.
  --experimental-corrupt-check-time '0s'
    Duration of time between cluster corruption check passes.
  --experimental-enable-v2v3 ''
    Serve v2 requests through the v3 backend under a given prefix.
  --experimental-enable-lease-checkpoint 'false'
    ExperimentalEnableLeaseCheckpoint enables primary lessor to persist lease remainingTTL to prevent indefinite auto-renewal of long lived leases.
  --experimental-compaction-batch-limit 1000
    ExperimentalCompactionBatchLimit sets the maximum revisions deleted in each compaction batch.
  --experimental-peer-skip-client-san-verification 'false'
    Skip verification of SAN field in client certificate for peer connections.
  --experimental-watch-progress-notify-interval '10m'
    Duration of periodical watch progress notification.
  --experimental-warning-apply-duration '100ms'
	Warning is generated if requests take more than this duration.

Unsafe feature:
  --force-new-cluster 'false'
    Force to create a new one-member cluster.
  --unsafe-no-fsync 'false'
    Disables fsync, unsafe, will cause data loss.

Metrics:
  --metrics-big-value 1048576 (equal 1MB)
    Big value size metrics.
  --metrics-hot-key 524288 (equal 0.5MB)
    Hot Key size metrics.

CAUTIOUS with unsafe flag! It may break the guarantees given by the consensus protocol!
`
)

// Add back "TO BE DEPRECATED" section if needed
