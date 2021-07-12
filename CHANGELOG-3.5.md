

Previous change logs can be found at [CHANGELOG-3.4](https://github.com/etcd-io/etcd/blob/main/CHANGELOG-3.4.md).


The minimum recommended etcd versions to run in **production** are 3.2.28+, 3.3.18+, and 3.4.2+.


<hr>


## v3.5.0 (2021-06)

See [code changes](https://github.com/etcd-io/etcd/compare/v3.4.0...v3.5.0) and [v3.5 upgrade guide](https://etcd.io/docs/latest/upgrades/upgrade_3_5/) for any breaking changes.

- [v3.5.0](https://github.com/etcd-io/etcd/releases/tag/v3.5.0) (2021 TBD), see [code changes](https://github.com/etcd-io/etcd/compare/v3.5.0-rc.1...v3.5.0).
- [v3.5.0-rc.1](https://github.com/etcd-io/etcd/releases/tag/v3.5.0-rc.1) (2021-06-10), see [code changes](https://github.com/etcd-io/etcd/compare/v3.5.0-rc.0...v3.5.0-rc.1).
- [v3.5.0-rc.0](https://github.com/etcd-io/etcd/releases/tag/v3.5.0-rc.0) (2021-06-04), see [code changes](https://github.com/etcd-io/etcd/compare/v3.5.0-beta.4...v3.5.0-rc.0).
- [v3.5.0-beta.4](https://github.com/etcd-io/etcd/releases/tag/v3.5.0-beta.4) (2021-05-26), see [code changes](https://github.com/etcd-io/etcd/compare/v3.5.0-beta.3...v3.5.0-beta.4).
- [v3.5.0-beta.3](https://github.com/etcd-io/etcd/releases/tag/v3.5.0-beta.3) (2021-05-18), see [code changes](https://github.com/etcd-io/etcd/compare/v3.5.0-beta.2...v3.5.0-beta.3).
- [v3.5.0-beta.2](https://github.com/etcd-io/etcd/releases/tag/v3.5.0-beta.2) (2021-05-18), see [code changes](https://github.com/etcd-io/etcd/compare/v3.5.0-beta.1...v3.5.0-beta.2).
- [v3.5.0-beta.1](https://github.com/etcd-io/etcd/releases/tag/v3.5.0-beta.1) (2021-05-18), see [code changes](https://github.com/etcd-io/etcd/compare/v3.4.0...v3.5.0-beta.1).

**Again, before running upgrades from any previous release, please make sure to read change logs below and [v3.5 upgrade guide](https://etcd.io/docs/latest/upgrades/upgrade_3_5/).**

### Breaking Changes

- `go.etcd.io/etcd` Go packages have moved to `go.etcd.io/etcd/{api,pkg,raft,client,etcdctl,server,raft,tests}/v3` to follow the [Go modules](https://github.com/golang/go/wiki/Modules) conventions
- `go.etcd.io/clientv3/snapshot` SnapshotManager class have moved to `go.etcd.io/clientv3/etcdctl`.
   The method `snapshot.Save` to download a snapshot from the remote server was preserved in 'go.etcd.io/clientv3/snapshot`.
- `go.etcd.io/client' package got migrated to 'go.etcd.io/client/v2'.
- Changed behavior of clientv3 API [MemberList](https://github.com/etcd-io/etcd/pull/11639).
  - Previously, it is directly served with server's local data, which could be stale.
  - Now, it is served with linearizable guarantee. If the server is disconnected from quorum, `MemberList` call will fail.
- [gRPC gateway](https://github.com/grpc-ecosystem/grpc-gateway) only supports [`/v3`](TODO) endpoint.
  - Deprecated [`/v3beta`](https://github.com/etcd-io/etcd/pull/9298).
  - `curl -L http://localhost:2379/v3beta/kv/put -X POST -d '{"key": "Zm9v", "value": "YmFy"}'` doesn't work in v3.5. Use `curl -L http://localhost:2379/v3/kv/put -X POST -d '{"key": "Zm9v", "value": "YmFy"}'` instead.
- **`etcd --experimental-enable-v2v3` flag remains experimental and to be deprecated.**
  - v2 storage emulation feature will be deprecated in the next release.
  - etcd 3.5 is the last version that supports V2 API. Flags `--enable-v2` and `--experimental-enable-v2v3` [are now deprecated](https://github.com/etcd-io/etcd/pull/12940) and will be removed in etcd v3.6 release.
- **`etcd --experimental-backend-bbolt-freelist-type` flag has been deprecated.** Use **`etcd --backend-bbolt-freelist-type`** instead. The default type is hashmap and it is stable now.
- **`etcd --debug` flag has been deprecated.** Use **`etcd --log-level=debug`** instead.
- Remove [`embed.Config.Debug`](https://github.com/etcd-io/etcd/pull/10947).
- **`etcd --log-output` flag has been deprecated.** Use **`etcd --log-outputs`** instead.
- **`etcd --logger=zap --log-outputs=stderr`** is now the default.
- **`etcd --logger=capnslog` flag value has been deprecated.**
- **`etcd --logger=zap --log-outputs=default` flag value is not supported.**.
  - Use `etcd --logger=zap --log-outputs=stderr`.
  - Or, use `etcd --logger=zap --log-outputs=systemd/journal` to send logs to the local systemd journal.
  - Previously, if etcd parent process ID (PPID) is 1 (e.g. run with systemd), `etcd --logger=capnslog --log-outputs=default` redirects server logs to local systemd journal. And if write to journald fails, it writes to `os.Stderr` as a fallback.
  - However, even with PPID 1, it can fail to dial systemd journal (e.g. run embedded etcd with Docker container). Then, [every single log write will fail](https://github.com/etcd-io/etcd/pull/9729) and fall back to `os.Stderr`, which is inefficient.
  - To avoid this problem, systemd journal logging must be configured manually.
- **`etcd --log-outputs=stderr`** is now the default.
- **`etcd --log-package-levels` flag for `capnslog` has been deprecated.** Now, **`etcd --logger=zap --log-outputs=stderr`** is the default.
- **`[CLIENT-URL]/config/local/log` endpoint has been deprecated, as is `etcd --log-package-levels` flag.**
  - `curl http://127.0.0.1:2379/config/local/log -XPUT -d '{"Level":"DEBUG"}'` won't work.
  - Please use `etcd --logger=zap --log-outputs=stderr` instead.
- Deprecated `etcd_debugging_mvcc_db_total_size_in_bytes` Prometheus metric. Use `etcd_mvcc_db_total_size_in_bytes` instead.
- Deprecated `etcd_debugging_mvcc_put_total` Prometheus metric. Use `etcd_mvcc_put_total` instead.
- Deprecated `etcd_debugging_mvcc_delete_total` Prometheus metric. Use `etcd_mvcc_delete_total` instead.
- Deprecated `etcd_debugging_mvcc_txn_total` Prometheus metric. Use `etcd_mvcc_txn_total` instead.
- Deprecated `etcd_debugging_mvcc_range_total` Prometheus metric. Use `etcd_mvcc_range_total` instead.
- Main branch `/version` outputs `3.5.0-pre`, instead of `3.4.0+git`.
- Changed `proxy` package function signature to [support structured logger](https://github.com/etcd-io/etcd/pull/11614).
  - Previously, `NewClusterProxy(c *clientv3.Client, advaddr string, prefix string) (pb.ClusterServer, <-chan struct{})`, now `NewClusterProxy(lg *zap.Logger, c *clientv3.Client, advaddr string, prefix string) (pb.ClusterServer, <-chan struct{})`.
  - Previously, `Register(c *clientv3.Client, prefix string, addr string, ttl int)`, now `Register(lg *zap.Logger, c *clientv3.Client, prefix string, addr string, ttl int) <-chan struct{}`.
  - Previously, `NewHandler(t *http.Transport, urlsFunc GetProxyURLs, failureWait time.Duration, refreshInterval time.Duration) http.Handler`, now `NewHandler(lg *zap.Logger, t *http.Transport, urlsFunc GetProxyURLs, failureWait time.Duration, refreshInterval time.Duration) http.Handler`.
- Changed `pkg/flags` function signature to [support structured logger](https://github.com/etcd-io/etcd/pull/11616).
  - Previously, `SetFlagsFromEnv(prefix string, fs *flag.FlagSet) error`, now `SetFlagsFromEnv(lg *zap.Logger, prefix string, fs *flag.FlagSet) error`.
  - Previously, `SetPflagsFromEnv(prefix string, fs *pflag.FlagSet) error`, now `SetPflagsFromEnv(lg *zap.Logger, prefix string, fs *pflag.FlagSet) error`.
- ClientV3 supports [grpc resolver API](https://github.com/etcd-io/etcd/blob/main/client/v3/naming/resolver/resolver.go).
  - Endpoints can be managed using [endpoints.Manager](https://github.com/etcd-io/etcd/blob/main/client/v3/naming/endpoints/endpoints.go)
  - Previously supported [GRPCResolver was decomissioned](https://github.com/etcd-io/etcd/pull/12675). Use [resolver](https://github.com/etcd-io/etcd/blob/main/client/v3/naming/resolver/resolver.go) instead.
- Turned on [--pre-vote by default](https://github.com/etcd-io/etcd/pull/12770). Should prevent disrupting RAFT leader by an individual member.
- [ETCD_CLIENT_DEBUG env](https://github.com/etcd-io/etcd/pull/12786): Now supports log levels (debug, info, warn, error, dpanic, panic, fatal). Only when set, overrides application-wide grpc logging settings.
- [Embed Etcd.Close()](https://github.com/etcd-io/etcd/pull/12828) needs to called exactly once and closes Etcd.Err() stream.
- [Embed Etcd does not override global/grpc logger](https://github.com/etcd-io/etcd/pull/12861) be default any longer. If desired, please call `embed.Config::SetupGlobalLoggers()` explicitly. 
- [Embed Etcd custom logger should be configured using simpler builder `NewZapLoggerBuilder`](https://github.com/etcd-io/etcd/pull/12973).
- Client errors of `context cancelled` or `context deadline exceeded` are exposed as `codes.Canceled` and `codes.DeadlineExceeded`, instead of `codes.Unknown`.


### Storage format changes
- [WAL log's snapshots persists raftpb.ConfState](https://github.com/etcd-io/etcd/pull/12735)
- [Backend persists raftpb.ConfState](https://github.com/etcd-io/etcd/pull/12962) in the `meta` bucket `confState` key.
- [Backend persists applied term](https://github.com/etcd-io/etcd/pull/) in the `meta` bucket.
- Backend persists `downgrade` in the `cluster` bucket

### Security

- Add [`TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256` and `TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256` to `etcd --cipher-suites`](https://github.com/etcd-io/etcd/pull/11864).
- Changed [the format of WAL entries related to auth for not keeping password as a plain text](https://github.com/etcd-io/etcd/pull/11943).
- Add third party [Security Audit Report](https://github.com/etcd-io/etcd/pull/12201).
- A [log warning](https://github.com/etcd-io/etcd/pull/12242) is added when etcd uses any existing directory that has a permission different than 700 on Linux and 777 on Windows.
- Add optional [`ClientCertFile` and `ClientKeyFile`](https://github.com/etcd-io/etcd/pull/12705) options for peer and client tls configuration when split certificates are used.

### Metrics, Monitoring

See [List of metrics](https://etcd.io/docs/latest/metrics/) for all metrics per release.

Note that any `etcd_debugging_*` metrics are experimental and subject to change.

- Deprecated `etcd_debugging_mvcc_db_total_size_in_bytes` Prometheus metric. Use `etcd_mvcc_db_total_size_in_bytes` instead.
- Deprecated `etcd_debugging_mvcc_put_total` Prometheus metric. Use `etcd_mvcc_put_total` instead.
- Deprecated `etcd_debugging_mvcc_delete_total` Prometheus metric. Use `etcd_mvcc_delete_total` instead.
- Deprecated `etcd_debugging_mvcc_txn_total` Prometheus metric. Use `etcd_mvcc_txn_total` instead.
- Deprecated `etcd_debugging_mvcc_range_total` Prometheus metric. Use `etcd_mvcc_range_total` instead.
- Add [`etcd_debugging_mvcc_current_revision`](https://github.com/etcd-io/etcd/pull/11126) Prometheus metric.
- Add [`etcd_debugging_mvcc_compact_revision`](https://github.com/etcd-io/etcd/pull/11126) Prometheus metric.
- Change [`etcd_cluster_version`](https://github.com/etcd-io/etcd/pull/11254) Prometheus metrics to include only major and minor version.
- Add [`etcd_debugging_mvcc_total_put_size_in_bytes`](https://github.com/etcd-io/etcd/pull/11374) Prometheus metric.
- Add [`etcd_server_client_requests_total` with `"type"` and `"client_api_version"` labels](https://github.com/etcd-io/etcd/pull/11687).
- Add [`etcd_wal_write_bytes_total`](https://github.com/etcd-io/etcd/pull/11738).
- Add [`etcd_debugging_auth_revision`](https://github.com/etcd-io/etcd/commit/f14d2a087f7b0fd6f7980b95b5e0b945109c95f3).
- Add [`os_fd_used` and `os_fd_limit` to monitor current OS file descriptors](https://github.com/etcd-io/etcd/pull/12214).

### etcd server

  - Add [don't attempt to grant nil permission to a role](https://github.com/etcd-io/etcd/pull/13086).
  - Add [don't activate alarms w/missing AlarmType](https://github.com/etcd-io/etcd/pull/13084).
  - Add [`TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256` and `TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256` to `etcd --cipher-suites`](https://github.com/etcd-io/etcd/pull/11864).
  - Automatically [create parent directory if it does not exist](https://github.com/etcd-io/etcd/pull/9626) (fix [issue#9609](https://github.com/etcd-io/etcd/issues/9609)).
  - v4.0 will configure `etcd --enable-v2=true --enable-v2v3=/aaa` to enable v2 API server that is backed by **v3 storage**.
- [`etcd --backend-bbolt-freelist-type`] flag is now stable.
  - `etcd --experimental-backend-bbolt-freelist-type` has been deprecated.
- Support [downgrade API](https://github.com/etcd-io/etcd/pull/11715).
- Deprecate v2 apply on cluster version. [Use v3 request to set cluster version and recover cluster version from v3 backend](https://github.com/etcd-io/etcd/pull/11427).
- [Use v2 api to update cluster version to support mixed version cluster during upgrade](https://github.com/etcd-io/etcd/pull/12988).
- [Fix corruption bug in defrag](https://github.com/etcd-io/etcd/pull/11613).
- Fix [quorum protection logic when promoting a learner](https://github.com/etcd-io/etcd/pull/11640).
- Improve [peer corruption checker](https://github.com/etcd-io/etcd/pull/11621) to work when peer mTLS is enabled.
- Log [`[CLIENT-PORT]/health` check in server side](https://github.com/etcd-io/etcd/pull/11704).
- Log [successful etcd server-side health check in debug level](https://github.com/etcd-io/etcd/pull/12677).
- Improve [compaction performance when latest index is greater than 1-million](https://github.com/etcd-io/etcd/pull/11734).
- [Refactor consistentindex](https://github.com/etcd-io/etcd/pull/11699).
- [Add log when etcdserver failed to apply command](https://github.com/etcd-io/etcd/pull/11670).
- Improve [count-only range performance](https://github.com/etcd-io/etcd/pull/11771).
- Remove [redundant storage restore operation to shorten the startup time](https://github.com/etcd-io/etcd/pull/11779).
  - With 40 million key test data,it can shorten the startup time from 5 min to 2.5 min.
- [Fix deadlock bug in mvcc](https://github.com/etcd-io/etcd/pull/11817).
- Fix [inconsistency between WAL and server snapshot](https://github.com/etcd-io/etcd/pull/11888).
  - Previously, server restore fails if it had crashed after persisting raft hard state but before saving snapshot.
  - See https://github.com/etcd-io/etcd/issues/10219 for more.
  - Add [missing CRC checksum check in WAL validate method otherwise causes panic](https://github.com/etcd-io/etcd/pull/11924).
  - See https://github.com/etcd-io/etcd/issues/11918.
- Improve logging around snapshot send and receive.
- [Push down RangeOptions.limit argv into index tree to reduce memory overhead](https://github.com/etcd-io/etcd/pull/11990).
- Add [reason field for /health response](https://github.com/etcd-io/etcd/pull/11983).
- Add [exclude alarms from health check conditionally](https://github.com/etcd-io/etcd/pull/12880).
- Add [`etcd --unsafe-no-fsync`](https://github.com/etcd-io/etcd/pull/11946) flag.
  - Setting the flag disables all uses of fsync, which is unsafe and will cause data loss. This flag makes it possible to run an etcd node for testing and development without placing lots of load on the file system.
- Add [`etcd --auth-token-ttl`](https://github.com/etcd-io/etcd/pull/11980) flag to customize `simpleTokenTTL` settings.
- Improve [`runtime.FDUsage` call pattern to reduce objects malloc of Memory Usage and CPU Usage](https://github.com/etcd-io/etcd/pull/11986).
- Improve [mvcc.watchResponse channel Memory Usage](https://github.com/etcd-io/etcd/pull/11987).
- Log [expensive request info in UnaryInterceptor](https://github.com/etcd-io/etcd/pull/12086).
- [Fix invalid Go type in etcdserverpb](https://github.com/etcd-io/etcd/pull/12000).
- [Improve healthcheck by using v3 range request and its corresponding timeout](https://github.com/etcd-io/etcd/pull/12195).
- Add [`etcd --experimental-watch-progress-notify-interval`](https://github.com/etcd-io/etcd/pull/12216) flag to make watch progress notify interval configurable.
- Fix [server panic in slow writes warnings](https://github.com/etcd-io/etcd/issues/12197).
  - Fixed via [PR#12238](https://github.com/etcd-io/etcd/pull/12238).
- [Fix server panic](https://github.com/etcd-io/etcd/pull/12288) when force-new-cluster flag is enabled in a cluster which had learner node.
- Add [`etcd --self-signed-cert-validity`](https://github.com/etcd-io/etcd/pull/12429) flag to support setting certificate expiration time.
  - Notice, certificates generated by etcd are valid for 1 year by default when specifying the auto-tls or peer-auto-tls option.
- Add [`etcd --experimental-warning-apply-duration`](https://github.com/etcd-io/etcd/pull/12448) flag which allows apply duration threshold to be configurable.
- Add [`etcd --experimental-memory-mlock`](https://github.com/etcd-io/etcd/pull/TODO) flag which prevents etcd memory pages to be swapped out.
- Add [`etcd --socket-reuse-port`](https://github.com/etcd-io/etcd/pull/12702) flag
  - Setting this flag enables `SO_REUSEPORT` which allows rebind of a port already in use. User should take caution when using this flag to ensure flock is properly enforced.
- Add [`etcd --socket-reuse-address`](https://github.com/etcd-io/etcd/pull/12702) flag
  - Setting this flag enables `SO_REUSEADDR` which allows binding to an address in `TIME_WAIT` state, improving etcd restart time.
- Reduce [around 30% memory allocation by logging range response size without marshal](https://github.com/etcd-io/etcd/pull/12871).
- `ETCD_VERIFY="all"` environment triggers [additional verification of consistency](https://github.com/etcd-io/etcd/pull/) of etcd data-dir files.
- Add [`etcd --enable-log-rotation`](https://github.com/etcd-io/etcd/pull/12774) boolean flag which enables log rotation if true.
- Add [`etcd --log-rotation-config-json`](https://github.com/etcd-io/etcd/pull/12774) flag which allows passthrough of JSON config to configure log rotation for a file output target.
- Add experimental distributed tracing boolean flag [`--experimental-enable-distributed-tracing`](https://github.com/etcd-io/etcd/pull/12919) which enables tracing.
- Add [`etcd --experimental-distributed-tracing-address`](https://github.com/etcd-io/etcd/pull/12919) string flag which allows configuring the OpenTelemetry collector address.
- Add [`etcd --experimental-distributed-tracing-service-name`](https://github.com/etcd-io/etcd/pull/12919) string flag which allows changing the default "etcd" service name.
- Add [`etcd --experimental-distributed-tracing-instance-id`](https://github.com/etcd-io/etcd/pull/12919) string flag which configures an instance ID, which must be unique per etcd instance.
- Fix [old leader still revokes lease after it steped to follower if cpu or disk io latency is high](https://github.com/etcd-io/etcd/pull/12531).

### Package `runtime`

- Optimize [`runtime.FDUsage` by removing unnecessary sorting](https://github.com/etcd-io/etcd/pull/12214).

### Package `embed`

- Remove [`embed.Config.Debug`](https://github.com/etcd-io/etcd/pull/10947).
  - Use `embed.Config.LogLevel` instead.
- Add [`embed.Config.ZapLoggerBuilder`](https://github.com/etcd-io/etcd/pull/11147) to allow creating a custom zap logger.
- Replace [global `*zap.Logger` with etcd server logger object](https://github.com/etcd-io/etcd/pull/12212).
- Add [`embed.Config.EnableLogRotation`](https://github.com/etcd-io/etcd/pull/12774) which enables log rotation if true.
- Add [`embed.Config.LogRotationConfigJSON`](https://github.com/etcd-io/etcd/pull/12774) to allow passthrough of JSON config to configure log rotation for a file output target.
- Add [`embed.Config.ExperimentalEnableDistributedTracing`](https://github.com/etcd-io/etcd/pull/12919) which enables experimental distributed tracing if true.
- Add [`embed.Config.ExperimentalDistributedTracingAddress`](https://github.com/etcd-io/etcd/pull/12919) which allows overriding default collector address.
- Add [`embed.Config.ExperimentalDistributedTracingServiceName`](https://github.com/etcd-io/etcd/pull/12919) which allows overriding default "etcd" service name.
- Add [`embed.Config.ExperimentalDistributedTracingServiceInstanceID`](https://github.com/etcd-io/etcd/pull/12919) which allows configuring an instance ID, which must be uniquer per etcd instance.

### Package `clientv3`

- Remove [excessive watch cancel logging messages](https://github.com/etcd-io/etcd/pull/12187).
  - See [kubernetes/kubernetes#93450](https://github.com/kubernetes/kubernetes/issues/93450).
- Add [`TryLock`](https://github.com/etcd-io/etcd/pull/11104) method to `clientv3/concurrency/Mutex`. A non-blocking method on `Mutex` which does not wait to get lock on the Mutex, returns immediately if Mutex is locked by another session.
- Fix [client balancer failover against multiple endpoints](https://github.com/etcd-io/etcd/pull/11184).
  - Fix [`"kube-apiserver: failover on multi-member etcd cluster fails certificate check on DNS mismatch"`](https://github.com/kubernetes/kubernetes/issues/83028).
- Fix [IPv6 endpoint parsing in client](https://github.com/etcd-io/etcd/pull/11211).
  - Fix ["1.16: etcd client does not parse IPv6 addresses correctly when members are joining" (kubernetes#83550)](https://github.com/kubernetes/kubernetes/issues/83550).
- Fix [errors caused by grpc changing balancer/resolver API](https://github.com/etcd-io/etcd/pull/11564). This change is compatible with grpc >= [v1.26.0](https://github.com/grpc/grpc-go/releases/tag/v1.26.0), but is not compatible with < v1.26.0 version.
- Use [ServerName as the authority](https://github.com/etcd-io/etcd/pull/11574) after bumping to grpc v1.26.0. Remove workaround in [#11184](https://github.com/etcd-io/etcd/pull/11184).
- Fix [`"hasleader"` metadata embedding](https://github.com/etcd-io/etcd/pull/11687).
  - Previously, `clientv3.WithRequireLeader(ctx)` was overwriting existing context keys.
- Fix [watch leak caused by lazy cancellation](https://github.com/etcd-io/etcd/pull/11850). When clients cancel their watches, a cancel request will now be immediately sent to the server instead of waiting for the next watch event.
- Make sure [save snapshot downloads checksum for integrity checks](https://github.com/etcd-io/etcd/pull/11896).
- Fix [auth token invalid after watch reconnects](https://github.com/etcd-io/etcd/pull/12264). Get AuthToken automatically when clientConn is ready.
- Improve [clientv3:get AuthToken gracefully without extra connection](https://github.com/etcd-io/etcd/pull/12165).
- Changed [clientv3 dialing code](https://github.com/etcd-io/etcd/pull/12671) to use grpc resolver API instead of custom balancer.
  - Endpoints self identify now as `etcd-endpoints://{id}/#initially={list of endpoints}` e.g. `etcd-endpoints://0xc0009d8540/#initially=[localhost:2079]`
- Make sure [save snapshot downloads checksum for integrity checks](https://github.com/etcd-io/etcd/pull/11896).

### Package `lease`

- Fix [memory leak in follower nodes](https://github.com/etcd-io/etcd/pull/11731).
  - https://github.com/etcd-io/etcd/issues/11495
  - https://github.com/etcd-io/etcd/issues/11730
- Make sure [grant/revoke won't be applied repeatedly after restarting etcd](https://github.com/etcd-io/etcd/pull/11935).

### Package `wal`

- Add [`etcd_wal_write_bytes_total`](https://github.com/etcd-io/etcd/pull/11738).
- Handle [out-of-range slice bound in `ReadAll` and entry limit in `decodeRecord`](https://github.com/etcd-io/etcd/pull/11793).

### etcdctl v3

- Fix `etcdctl member add` command to prevent potential timeout. ([PR#11194](https://github.com/etcd-io/etcd/pull/11194) and [PR#11638](https://github.com/etcd-io/etcd/pull/11638))
- Add [`etcdctl watch --progress-notify`](https://github.com/etcd-io/etcd/pull/11462) flag.
- Add [`etcdctl auth status`](https://github.com/etcd-io/etcd/pull/11536) command to check if authentication is enabled
- Add [`etcdctl get --count-only`](https://github.com/etcd-io/etcd/pull/11743) flag for output type `fields`.
- Add [`etcdctl member list -w=json --hex`](https://github.com/etcd-io/etcd/pull/11812) flag to print memberListResponse in hex format json.
- Changed [`etcdctl lock <lockname> exec-command`](https://github.com/etcd-io/etcd/pull/12829) to return exit code of exec-command.
- [New tool: `etcdutl`](https://github.com/etcd-io/etcd/pull/12971) incorporated functionality of: `etcdctl snapshot status|restore`, `etcdctl backup`, `etcdctl defrag --data-dir ...`.
- [ETCDCTL_API=2 `etcdctl migrate`](https://github.com/etcd-io/etcd/pull/12971) has been decomissioned. Use etcd <=v3.4 to restore v2 storage.

### gRPC gateway

- [gRPC gateway](https://github.com/grpc-ecosystem/grpc-gateway) only supports [`/v3`](TODO) endpoint.
  - Deprecated [`/v3beta`](https://github.com/etcd-io/etcd/pull/9298).
  - `curl -L http://localhost:2379/v3beta/kv/put -X POST -d '{"key": "Zm9v", "value": "YmFy"}'` does work in v3.5. Use `curl -L http://localhost:2379/v3/kv/put -X POST -d '{"key": "Zm9v", "value": "YmFy"}'` instead.
- Set [`enable-grpc-gateway`](https://github.com/etcd-io/etcd/pull/12297) flag to true when using a config file to keep the defaults the same as the command line configuration.

### gRPC Proxy

- Fix [`panic on error`](https://github.com/etcd-io/etcd/pull/11694) for metrics handler.
- Add [gRPC keepalive related flags](https://github.com/etcd-io/etcd/pull/11711) `grpc-keepalive-min-time`, `grpc-keepalive-interval` and `grpc-keepalive-timeout`.
- [Fix grpc watch proxy hangs when failed to cancel a watcher](https://github.com/etcd-io/etcd/pull/12030) .
- Add [metrics handler for grpcproxy self](https://github.com/etcd-io/etcd/pull/12107).
- Add [health handler for grpcproxy self](https://github.com/etcd-io/etcd/pull/12114).

### Auth

- Fix [NoPassword check when adding user through GRPC gateway](https://github.com/etcd-io/etcd/pull/11418) ([issue#11414](https://github.com/etcd-io/etcd/issues/11414))
- Fix bug where [some auth related messages are logged at wrong level](https://github.com/etcd-io/etcd/pull/11586)
- [Fix a data corruption bug by saving consistent index](https://github.com/etcd-io/etcd/pull/11652).
- [Improve checkPassword performance](https://github.com/etcd-io/etcd/pull/11735).
- [Add authRevision field in AuthStatus](https://github.com/etcd-io/etcd/pull/11659).

### API

- Add [`/v3/auth/status`](https://github.com/etcd-io/etcd/pull/11536) endpoint to check if authentication is enabled
- [Add `Linearizable` field to `etcdserverpb.MemberListRequest`](https://github.com/etcd-io/etcd/pull/11639).
- [Learner support Snapshot RPC](https://github.com/etcd-io/etcd/pull/12890/).

### Package `netutil`

- Remove [`netutil.DropPort/RecoverPort/SetLatency/RemoveLatency`](https://github.com/etcd-io/etcd/pull/12491).
  - These are not used anymore. They were only used for older versions of functional testing.
  - Removed to adhere to best security practices, minimize arbitrary shell invocation.

### `tools/etcd-dump-metrics`

- Implement [input validation to prevent arbitrary shell invocation](https://github.com/etcd-io/etcd/pull/12491).

### Dependency

- Upgrade [`google.golang.org/grpc`](https://github.com/grpc/grpc-go/releases) from [**`v1.23.0`**](https://github.com/grpc/grpc-go/releases/tag/v1.23.0) to [**`v1.37.0`**](https://github.com/grpc/grpc-go/releases/tag/v1.37.0).
- Upgrade [`go.uber.org/zap`](https://github.com/uber-go/zap/releases) from [**`v1.14.1`**](https://github.com/uber-go/zap/releases/tag/v1.14.1) to [**`v1.16.0`**](https://github.com/uber-go/zap/releases/tag/v1.16.0).

### Platforms

- etcd now [officially supports `arm64`](https://github.com/etcd-io/etcd/pull/12929).
  - See https://github.com/etcd-io/etcd/pull/12928 for adding automated tests with `arm64` EC2 instances (Graviton 2).
  - See https://github.com/etcd-io/website/pull/273 for new platform support tier policies.

### Release

- Add s390x build support ([PR#11548](https://github.com/etcd-io/etcd/pull/11548) and [PR#11358](https://github.com/etcd-io/etcd/pull/11358))

### Go

- Require [*Go 1.16+*](https://github.com/etcd-io/etcd/pull/11110).
- Compile with [*Go 1.16+*](https://golang.org/doc/devel/release.html#go1.16)
- etcd uses [go modules](https://github.com/etcd-io/etcd/pull/12279) (instead of vendor dir) to track dependencies.

### Project Governance

- The etcd team has added, a well defined and openly discussed, project [governance](https://github.com/etcd-io/etcd/pull/11175).


<hr>

