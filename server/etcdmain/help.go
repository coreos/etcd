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
	"strings"

	"golang.org/x/crypto/bcrypt"

	"go.etcd.io/etcd/server/v3/embed"
	"go.etcd.io/etcd/server/v3/etcdserver/api/rafthttp"
	"go.etcd.io/etcd/server/v3/features"
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
    Duration-typed fields (for example --heartbeat-interval, --election-timeout,
    --watch-progress-notify-interval) must be specified as integer nanoseconds
    in the JSON/YAML file (e.g. 600000000000 for 10m); human-readable strings
    such as "10m" are only accepted on the command line.

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
  --snapshot-count '10000'
    Number of committed transactions to trigger a snapshot.
  --heartbeat-interval '100'
    Time (in milliseconds) of a heartbeat interval.
  --election-timeout '1000'
    Time (in milliseconds) for an election to timeout. See tuning documentation for details.
  --initial-election-tick-advance 'true'
    Whether to fast-forward initial election ticks on boot for faster election.
  --listen-peer-urls 'http://localhost:2380'
    List of URLs to listen on for peer traffic.
  --listen-client-urls 'http://localhost:2379'
    List of URLs to listen on for client grpc traffic and http as long as --listen-client-http-urls is not specified.
  --listen-client-http-urls ''
    List of URLs to listen on for http only client traffic. Enabling this flag removes http services from --listen-client-urls.
  --max-wals '` + strconv.Itoa(embed.DefaultMaxWALs) + `'
    Maximum number of wal files to retain (0 is unlimited).
  --memory-mlock
    Enable to enforce etcd pages (in particular bbolt) to stay in RAM.
  --quota-backend-bytes '0'
    Sets the maximum size (in bytes) that the etcd backend database may consume. Exceeding this triggers an alarm and puts etcd in read-only mode. Set to 0 to use the default 2GiB limit.
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
  --max-concurrent-streams 'math.MaxUint32'
    Maximum concurrent streams that each client can open at a time.
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
  --enable-grpc-gateway
    Enable GRPC gateway.
  --raft-read-timeout '` + rafthttp.DefaultConnReadTimeout.String() + `'
    Read timeout set on each rafthttp connection
  --raft-write-timeout '` + rafthttp.DefaultConnWriteTimeout.String() + `'
    Write timeout set on each rafthttp connection
  --feature-gates ''
    A set of key=value pairs that describe server level feature gates for alpha/experimental features. Options are:` + "\n    " + strings.Join(features.NewDefaultServerFeatureGate("", nil).KnownFeatures(), "\n    ") + `

Clustering:
  --initial-advertise-peer-urls 'http://localhost:2380'
    List of this member's peer URLs to advertise to the rest of the cluster.
  --initial-cluster 'default=http://localhost:2380'
    Initial cluster configuration for bootstrapping.
  --initial-cluster-state 'new'
    Initial cluster state ('new' when bootstrapping a new cluster or 'existing' when adding new members to an existing cluster).
    After successful initialization (bootstrapping or adding), flag is ignored on restarts
  --initial-cluster-token 'etcd-cluster'
    Initial cluster token for the etcd cluster during bootstrap.
    Specifying this can protect you from unintended cross-cluster interaction when running multiple clusters.
  --advertise-client-urls 'http://localhost:2379'
    List of this member's client URLs to advertise to the public.
    The client URLs advertised should be accessible to machines that talk to etcd cluster. etcd client libraries parse these URLs to connect to the cluster.
  --discovery ''
    Discovery URL used to bootstrap the cluster for v2 discovery. Will be deprecated in v3.7, and be decommissioned in v3.8.
  --discovery-token ''
    V3 discovery: discovery token for the etcd cluster to be bootstrapped.
  --discovery-endpoints ''
    V3 discovery: List of gRPC endpoints of the discovery service.
  --discovery-dial-timeout '2s'
    V3 discovery: dial timeout for client connections.
  --discovery-request-timeout '5s'
    V3 discovery: timeout for discovery requests (excluding dial timeout).
  --discovery-keepalive-time '2s'
    V3 discovery: keepalive time for client connections.
  --discovery-keepalive-timeout '6s'
    V3 discovery: keepalive timeout for client connections.
  --discovery-insecure-transport 'true'
    V3 discovery: disable transport security for client connections.
  --discovery-insecure-skip-tls-verify 'false'
    V3 discovery: skip server certificate verification (CAUTION: this option should be enabled only for testing purposes).
  --discovery-cert ''
    V3 discovery: identify secure client using this TLS certificate file.
  --discovery-key ''
    V3 discovery: identify secure client using this TLS key file.
  --discovery-cacert ''
    V3 discovery: verify certificates of TLS-enabled secure servers using this CA bundle.
  --discovery-user ''
    V3 discovery: username[:password] for authentication (prompt if password is not supplied).
  --discovery-password ''
    V3 discovery: password for authentication (if this option is used, --user option shouldn't include password).
  --discovery-fallback 'exit'
    Expected behavior ('exit') when discovery services fails. Note that v2 proxy is removed.
  --discovery-proxy ''
    HTTP proxy to use for traffic to discovery service. Will be deprecated in v3.7, and be decommissioned in v3.8.
  --discovery-srv ''
    DNS srv domain used to bootstrap the cluster.
  --discovery-srv-name ''
    Suffix to the dns srv name queried when bootstrapping.
  --strict-reconfig-check '` + strconv.FormatBool(embed.DefaultStrictReconfigCheck) + `'
    Reject reconfiguration requests that would cause quorum loss.
  --pre-vote 'true'
    Enable the raft Pre-Vote algorithm to prevent disruption when a node that has been partitioned away rejoins the cluster.
  --auto-compaction-retention '0'
    Auto compaction retention length. 0 means disable auto compaction.
  --auto-compaction-mode 'periodic'
    Interpret 'auto-compaction-retention' one of: periodic|revision. 'periodic' for duration based retention, defaulting to hours if no time unit is provided (e.g. '5m'). 'revision' for revision number based retention.

Security:
  --cert-file ''
    Path to the client server TLS cert file.
  --key-file ''
    Path to the client server TLS key file.
  --client-cert-auth 'false'
    Enable client cert authentication.
  --client-cert-file ''
    Path to an explicit peer client TLS cert file otherwise cert file will be used when client auth is required.
  --client-key-file ''
    Path to an explicit peer client TLS key file otherwise key file will be used when client auth is required.
  --client-crl-file ''
    Path to the client certificate revocation list file.
  --client-cert-allowed-hostname ''
    Comma-separated list of SAN hostnames for client cert authentication.
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
  --peer-client-cert-file ''
    Path to an explicit peer client TLS cert file otherwise peer cert file will be used when client auth is required.
  --peer-client-key-file ''
    Path to an explicit peer client TLS key file otherwise peer key file will be used when client auth is required.
  --peer-trusted-ca-file ''
    Path to the peer server TLS trusted CA file.
  --peer-cert-allowed-cn ''
    Comma-separated list of allowed CNs for inter-peer TLS authentication.
  --peer-cert-allowed-hostname ''
    Comma-separated list of allowed SAN hostnames for inter-peer TLS authentication.
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
  --tls-min-version 'TLS1.2'
    Minimum TLS version supported by etcd. Possible values: TLS1.2, TLS1.3.
  --tls-max-version ''
    Maximum TLS version supported by etcd. Possible values: TLS1.2, TLS1.3 (empty will be auto-populated by Go).

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
    Set level of detail for exported metrics, specify 'extensive' to include server side grpc histogram metrics and the full Go runtime metrics set.
  --listen-metrics-urls ''
    List of URLs to listen on for the /metrics and /health endpoints. For https, the client URL TLS info is used.

Logging:
  --logger 'zap'
    Currently only supports 'zap' for structured logging.
  --log-outputs 'default'
    Specify 'stdout' or 'stderr' to skip journald logging even when running under systemd, or list of comma separated output targets.
  --log-level 'info'
    Configures log level. Only supports debug, info, warn, error, panic, or fatal.
  --log-format 'json'
    Configures log format. Only supports json, console.
  --enable-log-rotation 'false'
    Enable log rotation of a single log-outputs file target.
  --log-rotation-config-json '{"maxsize": 100, "maxage": 0, "maxbackups": 0, "localtime": false, "compress": false}'
    Configures log rotation if enabled with a JSON logger config. MaxSize(MB), MaxAge(days,0=no limit), MaxBackups(0=no limit), LocalTime(use computers local time), Compress(gzip)".
  --warning-unary-request-duration '300ms'
    Set time duration after which a warning is logged if a unary request takes more than this duration.

Distributed tracing:
  --enable-distributed-tracing 'false'
    Enable distributed tracing.
  --distributed-tracing-address 'localhost:4317'
    Distributed tracing collector address.
  --distributed-tracing-service-name 'etcd'
    Distributed tracing service name, must be same across all etcd instances.
  --distributed-tracing-instance-id ''
    Distributed tracing instance ID, must be unique per each etcd instance.
  --distributed-tracing-sampling-rate '0'
    Number of samples to collect per million spans for distributed tracing.

Features:
  --corrupt-check-time '0s'
    Duration of time between cluster corruption check passes.
  --compact-hash-check-time '1m'
    Duration of time between leader checks followers compaction hashes.
  --compaction-batch-limit 1000
    CompactionBatchLimit sets the maximum revisions deleted in each compaction batch.
  --peer-skip-client-san-verification 'false'
    Skip verification of SAN field in client certificate for peer connections.
  --watch-progress-notify-interval '10m'
    Duration of periodical watch progress notification.
  --warning-apply-duration '100ms'
    Warning is generated if requests take more than this duration.
  --bootstrap-defrag-threshold-megabytes
    Enable the defrag during etcd server bootstrap on condition that it will free at least the provided threshold of disk space. Needs to be set to non-zero value to take effect.
  --max-learners '1'
    Set the max number of learner members allowed in the cluster membership.
  --compaction-sleep-interval
    Sets the sleep interval between each compaction batch.
  --downgrade-check-time
    Duration of time between two downgrade status checks.
  --snapshot-catchup-entries
    Number of entries for a slow follower to catch up after compacting the raft storage entries.
  --experimental-health-check-interval '0s'
    Period of the server-side health self-check loop. 0 means use the package default (500ms). Only honored when the ServerHealthCheck feature gate is enabled.
  --experimental-health-check-timeout '0s'
    Per-check wall-clock timeout. 0 means derive it from the request timeout (5s + 2 election timeouts, 7s with the default election timeout), the same budget a linearizable read gets. Only honored when the ServerHealthCheck feature gate is enabled.
  --experimental-health-check-apply-lag '0'
    Maximum committed-vs-applied index gap before apply_lag check fails. 0 means use the package default (10000). Values below 10000 make the check report failures while the member can still accept some request classes; apply_lag only flips the etcd.health/apply_lag observation entry and does not drive endpoint eviction. Only honored when the ServerHealthCheck feature gate is enabled.
  --experimental-health-check-failure-threshold '0'
    Consecutive failed ticks before the gRPC health state flips to NOT_SERVING. 0 means use the package default (3). Must be strictly greater than the success threshold (asymmetric debounce). Only honored when the ServerHealthCheck feature gate is enabled.
  --experimental-health-check-success-threshold '0'
    Consecutive successful ticks before the gRPC health state returns to SERVING. 0 means use the package default (2). Must be strictly less than the failure threshold (asymmetric debounce). Only honored when the ServerHealthCheck feature gate is enabled.

Unsafe feature:
  --force-new-cluster 'false'
    Force to create a new one-member cluster.
  --unsafe-no-fsync 'false'
    Disables fsync, unsafe, will cause data loss.

CAUTIOUS with unsafe flag! It may break the guarantees given by the consensus protocol!
`
)

// Add back "TO BE DEPRECATED" section if needed
