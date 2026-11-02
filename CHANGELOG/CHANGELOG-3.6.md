

Previous change logs can be found at [CHANGELOG-3.5](https://github.com/etcd-io/etcd/blob/main/CHANGELOG/CHANGELOG-3.5.md).

---

## v3.6.17 (TBC)

### etcd server

- Fix [`--force-new-cluster` permanently bricking a member whose backend consistent index is ahead of its WAL](https://github.com/etcd-io/etcd/pull/22686)

---

## v3.6.16 (2026-10-22)

### etcd server

- Fix [data race between `Hash` and `Restore` in the MVCC store](https://github.com/etcd-io/etcd/pull/22637)

### etcdctl

- Fix [etcdctl txn panic with `bad value` when comparing a lease](https://github.com/etcd-io/etcd/pull/22542)

### Dependencies

- Compile binaries using [go 1.26.10](https://github.com/etcd-io/etcd/pull/22631).
- Bump [go.opentelemetry.io/otel/sdk, go.opentelemetry.io/otel/exporters/otlp/otlptrace and go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc to v1.45.0 to address GO-2026-6505](https://github.com/etcd-io/etcd/pull/22554).
- Bump [golang.org/x/net to v0.61.0 to address GO-2026-6617, GO-2026-6612, GO-2026-6611 and GO-2026-6603](https://github.com/etcd-io/etcd/pull/22590).
- Bump [bump github.com/klauspost/compress from v1.17.9 to v1.18.7 to address CVE-2026-63209](https://github.com/etcd-io/etcd/pull/22647).

---

## v3.6.15 (2026-09-22)

### etcd server

- [Fix fsync snap directory when saving a received snapshot db](https://github.com/etcd-io/etcd/pull/22401)

### etcdctl

- [Fix duplicate `RaftTerm` field in the `endpoint status` output when using `--write-out=fields`](https://github.com/etcd-io/etcd/pull/22218)
- [fileutil: close locked file handle on os.Remove error in purgeFile](https://github.com/etcd-io/etcd/pull/22456)

### Dependencies

- Compile binaries using [go 1.26.8](https://github.com/etcd-io/etcd/pull/22414).
- Bump [golang.org/x/text to v0.39.0](https://github.com/etcd-io/etcd/pull/22365), to address CVE-2026-56852.
- Bump [github.com/gorilla/websocket to v1.5.3], to address GHSA-w67g-5rqw-f597.
- Bump [go.opentelemetry.io/otel to v1.44.0 to addrss CVE-2026-41178](https://github.com/etcd-io/etcd/pull/22199).
- Bump [google.golang.org/grpc to v1.83.2 to address CVE-2026-84303](https://github.com/etcd-io/etcd/pull/22468).

---

## v3.6.14 (2026-07-23)

### etcd server

- Fix [unbounded io.ReadAll on peer lease HTTP handler body](https://github.com/etcd-io/etcd/pull/22148)
- [Set a reasonable value for `snapshotLimitByte`](https://github.com/etcd-io/etcd/pull/22146)
- Fix [the `costTxnReq` ignores nested `RequestTxn` issue](https://github.com/etcd-io/etcd/pull/22140)
- [Set a ReadHeaderTimeout for client http.Server](https://github.com/etcd-io/etcd/pull/22144)
- Fix [the security issue where a user granted read permission on one key could receive watch responses for every key starting from that key](https://github.com/etcd-io/etcd/security/advisories/GHSA-xg4h-6gfc-h4m8)

### Package `clientv3`

- Fix [unsynchronized range over leaseCache.entries](https://github.com/etcd-io/etcd/pull/22150)

### package `client/pkg/v3`

- [Set a tlsHandshakeTimeout for tlsListener](https://github.com/etcd-io/etcd/pull/22142). Refer to [GHSA-6vch-q96h-7gc3](https://github.com/etcd-io/etcd/security/advisories/GHSA-6vch-q96h-7gc3) for more details.

### Dependencies

- Compile binaries using [go 1.25.12](https://github.com/etcd-io/etcd/pull/22060).
- [Bump golang.org/x/net from 0.54.0 to 0.55.0 to address CVE-2026-25681, CVE-2026-27136, CVE-2026-39821, CVE-2026-42502, CVE-2026-25680 and CVE-2026-42506](https://github.com/etcd-io/etcd/pull/22030)
- [Bump google.golang.org/grpc to 1.82.1 to address GHSA-hrxh-6v49-42gf](https://github.com/etcd-io/etcd/pull/22155)

---

## v3.6.13 (2026-07-01)

### etcd server

- Add [`write-only-skip-check` option for `--v2-deprecation` to bypass the v2 content check](https://github.com/etcd-io/etcd/pull/21850)
- Fix [websocket authentication with bearer-prefixed auth tokens](https://github.com/etcd-io/etcd/pull/21932).
- Fix [CRL enforcement bypass on gRPC listener when `--listen-client-http-urls` is configured](https://github.com/etcd-io/etcd/pull/22025), refer to [security/advisories/GHSA-3wh4-j44w-pg92](https://github.com/etcd-io/etcd/security/advisories/GHSA-3wh4-j44w-pg92) for more details.

### Dependencies

- Compile binaries using [go 1.25.11](https://github.com/etcd-io/etcd/pull/21892)
- Bump golang.org/x/crypto to [v0.52.0](https://github.com/etcd-io/etcd/pull/21904) to resolve CVE-2026-46598, CVE-2026-39835, CVE-2026-39828 and CVE-2026-46597.
- Bump `go.opentelemetry.io/otel` from `v1.40.0` to `v1.43.0` and `go.opentelemetry.io/otel/sdk` from `v1.40.0` to `v1.43.0` to address CVE-2026-29181 and CVE-2026-39883 ([#21918](https://github.com/etcd-io/etcd/pull/21918)).

---

## v3.6.12 (2026-06-01)

### etcd server

- [bugfix: MemberUpdate implicitly and unexpectedly promotes a learner](https://github.com/etcd-io/etcd/pull/21736)
- [server: allow non-admin maintenance status](https://github.com/etcd-io/etcd/pull/21811)

### etcdutl

- [etcdutl: validate data file path instead of panic](https://github.com/etcd-io/etcd/pull/21768)

### Dependencies

- Compile binaries using [go 1.25.10](https://github.com/etcd-io/etcd/pull/21727)

---

## v3.6.11 (2026-05-01)

### etcd server

- [Fixed an issue that prevented adding a new member when one member was down, even though quorum was still satisfied](https://github.com/etcd-io/etcd/pull/21667)
- Fix RBAC authorization bypass allowing read access via PrevKv or lease attachment in Put requests nested in etcd transactions (see [PR/21681](https://github.com/etcd-io/etcd/pull/21681) and [PR/21685](https://github.com/etcd-io/etcd/pull/21685))

### Dependencies

- [Bump golang.org/x/image to v0.39.0 to resolve GO-2026-4962](https://github.com/etcd-io/etcd/pull/21668)
- Compile binaries using [go 1.25.9](https://github.com/etcd-io/etcd/pull/21586)

---

## v3.6.10 (2026-04-01)

### etcd server

- [etcdserver: allow non-admin to fetch member list and alarms](https://github.com/etcd-io/etcd/pull/21549)

### etcdctl

- Fix [etcdctl endpoint command regression with option --cluster when auth is enabled](https://github.com/etcd-io/etcd/pull/21530)

---

## v3.6.9 (2026-03-20)

### etcd server

- [Ensure the metrics interceptor runs before other interceptors so that metrics remain up to date](https://github.com/etcd-io/etcd/pull/21329)
- Fix [Race between read index and leader change](https://github.com/etcd-io/etcd/pull/21378)
- Fix [Stale reads caused by process pausing](https://github.com/etcd-io/etcd/pull/21417)
- Revert [Reuse events between sync loops](https://github.com/etcd-io/etcd/pull/21443)
- Guard unauthenticated endpoints with auth checks to fix [Authorization bypasses in multiple APIs (CVE-2026-33413)](https://github.com/etcd-io/etcd/security/advisories/GHSA-q8m4-xhhv-38mg)
- Enforce auth checks for nested txn ops to fix [Nested etcd transactions bypass RBAC authorization checks (CVE-2026-33343)](https://github.com/etcd-io/etcd/security/advisories/GHSA-rfx7-8w68-q57q)

### Package `clientv3`

- [Print the endpoint the grpc request was actually sent to in unary interceptor](https://github.com/etcd-io/etcd/pull/21382)

### etcd grpc-proxy

- [server/etcdmain: fix startup deadlock in grpcproxy](https://github.com/etcd-io/etcd/pull/21354)

### etcdctl

- Fix [slice bounds trimming single-quoted args in Argify](https://github.com/etcd-io/etcd/pull/21402)

### Dependencies

- [Bump go.opentelemetry.io/otel/sdk to v1.40.0 to resolve https://pkg.go.dev/vuln/GO-2026-4394](https://github.com/etcd-io/etcd/pull/21340)
- Compile binaries using [go 1.25.8](https://github.com/etcd-io/etcd/pull/21463)
- [Bump golang.org/x/net to v0.51.0 to resolve GO-2026-4559](https://github.com/etcd-io/etcd/pull/21440)
- [Bump google.golang.org/grpc to 1.79.3 to resolve CVE-2026-33186](https://github.com/etcd-io/etcd/pull/21501)

---

## v3.6.8 (2026-02-13)

### etcd server

- [Postpone removal of the --max-snapshots flag from v3.7 to v3.8](https://github.com/etcd-io/etcd/pull/21161)
- [Revoke the deprecation of the `--snapshot-count` flag](https://github.com/etcd-io/etcd/pull/21163)

### Package `clientv3`

- [Remove the use of grpc-go's Metadata field](https://github.com/etcd-io/etcd/pull/21241)

### Dependencies

- Bump [golang.org/x/crypto to 0.45.0 to address CVE-2025-47914, and CVE-2025-58181](https://github.com/etcd-io/etcd/pull/21037).
- Compile binaries using [go 1.24.13](https://github.com/etcd-io/etcd/pull/21266). This addresses [CVE-2025-61726](https://github.com/advisories/GHSA-gm9r-q53w-2gh4), [CVE-2025-61731](https://github.com/advisories/GHSA-xvqr-69v8-f3gv), and [CVE-2025-61732](https://github.com/advisories/GHSA-8jvr-vh7g-f8gx).

---

## v3.6.7 (2025-12-17)

### etcd server

- [Print token fingerprint instead of the original tokens in log messages](https://github.com/etcd-io/etcd/pull/20941)

### Dependencies

- Compile binaries using [go 1.24.11](https://github.com/etcd-io/etcd/pull/20998).

---

## v3.6.6 (2025-11-11)

### etcd server

- [Reject watch request with -1 revision to prevent invalid resync behavior on uncompacted etcd](https://github.com/etcd-io/etcd/pull/20707)
- [Change the TLS handshake 'EOF' errors to DEBUG not to spam logs](https://github.com/etcd-io/etcd/pull/20749)
- Fix [endpoint status not retuning the correct storage quota](https://github.com/etcd-io/etcd/pull/20790)
- Fix [`--force-new-cluster can't clean up learners after creating snapshot`](https://github.com/etcd-io/etcd/pull/20896)
- Fix [duplicate metrics collector registration that caused warning messages](https://github.com/etcd-io/etcd/pull/20905)
- Fix [cannot promote member from follower when auth is enabled](https://github.com/etcd-io/etcd/pull/20874)

### Dependencies

- Compile binaries using [go 1.24.10](https://github.com/etcd-io/etcd/pull/20901).

---

## v3.6.5 (2025-09-19)

### etcd server

- [Remove the flag `--experimental-snapshot-catch-up-entries` from `etcd --help` output](https://github.com/etcd-io/etcd/pull/20422)
- Fix [etcd repeatedly log the error "cannot detect storage schema version: missing confstate information"](https://github.com/etcd-io/etcd/pull/20496)
- Fix [etcd may return success for leaseRenew request even when the lease is revoked](https://github.com/etcd-io/etcd/pull/20615)
- Fix [potential data corruption when applySnapshot and defragment happen concurrently](https://github.com/etcd-io/etcd/pull/20650)

### Dependencies

- Compile binaries using [go 1.24.7](https://github.com/etcd-io/etcd/pull/20664).
- [Bump bbolt to v1.4.3](https://github.com/etcd-io/etcd/pull/20513).

---

## v3.6.4 (2025-07-25)

### etcd server

- Fix [etcdserver bootstrap failure when replaying learner promotion operation due to not exist in v3store](https://github.com/etcd-io/etcd/pull/20387)

---

## v3.6.3 (2025-07-22)

### etcd server

- Fix [v2store check (IsMetaStoreOnly) returns wrong result even there is no any auth data](https://github.com/etcd-io/etcd/pull/20370)
- Improve [help message for --quota-backend-bytes](https://github.com/etcd-io/etcd/pull/20352)

---

## v3.6.2 (2025-07-09)

### etcd server

- Fix [Watch on future revision returns old events or notifications](https://github.com/etcd-io/etcd/pull/20286)

### Dependencies

- [Bump bbolt to v1.4.2](https://github.com/etcd-io/etcd/pull/20267)
- Compile binaries using [go 1.23.11](https://github.com/etcd-io/etcd/pull/20314).

---

## v3.6.1 (2025-06-06)

### etcd server

- [Replaced the deprecated/removed `UnaryServerInterceptor` and `StreamServerInterceptor` in otelgrpc with `NewServerHandler`](https://github.com/etcd-io/etcd/pull/20043)
- [Add protection on `PromoteMember` and `UpdateRaftAttributes` to prevent panicking](https://github.com/etcd-io/etcd/pull/20051)
- [Fix the issue that `--force-new-cluster` can't remove all other members in a corner case](https://github.com/etcd-io/etcd/pull/20071)
- Fix [mvcc: avoid double decrement of watcher gauge on close/cancel race](https://github.com/etcd-io/etcd/pull/20067)
- [Add validation to ensure there is no empty v3discovery endpoint](https://github.com/etcd-io/etcd/pull/20113)

### etcdctl

- Fix [command `etcdctl endpoint health` doesn't work when options are set via environment variables](https://github.com/etcd-io/etcd/pull/20121)

### Dependencies

- Compile binaries using [go 1.23.10](https://github.com/etcd-io/etcd/pull/20128).

---

## v3.6.0 (2025-05-15)

There isn't any production code change since v3.6.0-rc.5.

---

## v3.6.0-rc.5 (2025-05-08)

### etcd server

- Fix [the compaction pause duration metric is not emitted for every compaction batch](https://github.com/etcd-io/etcd/pull/19770)

### Package `clientv3`

- [Replace `resolver.State.Addresses` with `resolver.State.Endpoint.Addresses`](https://github.com/etcd-io/etcd/pull/19782).
- [Deprecated the Metadata field in the Endpoint struct from the client/v3/naming/endpoints package](https://github.com/etcd-io/etcd/pull/19842).

### Dependencies

- Compile binaries using [go 1.23.9](https://github.com/etcd-io/etcd/pull/19867).

---

## v3.6.0-rc.4 (2025-04-15)

### etcd server

- [Switch to validating v3 when v2 and v3 are synchronized](https://github.com/etcd-io/etcd/pull/19703).

### Dependencies

- Compile binaries using [go 1.23.8](https://github.com/etcd-io/etcd/pull/19724)

---

## v3.6.0-rc.3 (2025-03-27)

### etcd server

- [Auto sync members in v3store for the issues which have already been affected by #19557](https://github.com/etcd-io/etcd/pull/19636).
- [Move `client/internal/v2` into `server/internel/clientv2`](https://github.com/etcd-io/etcd/pull/19673).
- [Replace ExperimentalMaxLearners with a Feature Gate](https://github.com/etcd-io/etcd/pull/19560).

### etcd grpc-proxy

- Fix [grpcproxy can get stuck in and endless loop causing high CPU usage](https://github.com/etcd-io/etcd/pull/19562)

### Dependencies

- Bump [github.com/golang-jwt/jwt/v5 from 5.2.1 to 5.2.2 to address CVE-2025-30204](https://github.com/etcd-io/etcd/pull/19647).
- Bump [bump golang.org/x/net from v0.37.0 to v0.38.0 to address CVE-2025-22872](https://github.com/etcd-io/etcd/pull/19687).

---

## v3.6.0-rc.2 (2025-03-05)

### etcd server

- Add [Prometheus metric to query server feature gates](https://github.com/etcd-io/etcd/pull/19495).

### Dependencies

- Compile binaries using [go 1.23.7](https://github.com/etcd-io/etcd/pull/19527).
- Bump [golang.org/x/net to v0.36.0 to address CVE-2025-22870](https://github.com/etcd-io/etcd/pull/19531).
- Bump [github.com/grpc-ecosystem/grpc-gateway/v2 to v2.26.3 to fix the issue of etcdserver crashing on receiving REST watch stream requests](https://github.com/etcd-io/etcd/pull/19522).

---

## v3.6.0-rc.1 (2025-02-25)

### etcdctl v3

- Add [`DowngradeInfo` in result of endpoint status](https://github.com/etcd-io/etcd/pull/19471)

### etcd server

- Add [`DowngradeInfo` to endpoint status response](https://github.com/etcd-io/etcd/pull/19471)

### Dependencies

- Bump [golang.org/x/crypto to v0.35.0 to address CVE-2025-22869](https://github.com/etcd-io/etcd/pull/19480).

---

## v3.6.0-rc.0 (2025-02-13)

See [code changes](https://github.com/etcd-io/etcd/compare/v3.5.0...v3.6.0).

### Breaking Changes

- `etcd` will no longer start on data dir created by newer versions (for example etcd v3.6 will not run on v3.7+ data dir). To downgrade data dir please check out `etcdutl migrate` command.
- `etcd` doesn't support serving client requests on the peer listen endpoints (--listen-peer-urls). See [pull/13565](https://github.com/etcd-io/etcd/pull/13565).
- `etcdctl` will sleep(2s) in case of range delete without `--range` flag. See [pull/13747](https://github.com/etcd-io/etcd/pull/13747)
- Applications which depend on etcd v3.6 packages must be built with go version >= v1.18.

#### Flags Removed

- The following flags have been removed:

  - `--enable-v2`
  - `--experimental-enable-v2v3`
  - `--proxy`
  - `--proxy-failure-wait`
  - `--proxy-refresh-interval`
  - `--proxy-dial-timeout`
  - `--proxy-write-timeout`
  - `--proxy-read-timeout`

### Deprecations

- Deprecated [V2 discovery](https://etcd.io/docs/v3.5/dev-internal/discovery_protocol/).
- Deprecated [SetKeepAlive and SetKeepAlivePeriod in limitListenerConn](https://github.com/etcd-io/etcd/pull/14356).
- Removed [etcdctl defrag --data-dir](https://github.com/etcd-io/etcd/pull/13793).
- Removed [etcdctl snapshot status](https://github.com/etcd-io/etcd/pull/13809).
- Removed [etcdctl snapshot restore](https://github.com/etcd-io/etcd/pull/13809).
- Removed [NewZapCoreLoggerBuilder in server/embed](https://github.com/etcd-io/etcd/pull/19404)

### etcdctl v3

- Add command to generate [shell completion](https://github.com/etcd-io/etcd/pull/13133).
- When print endpoint status, [show db size in use](https://github.com/etcd-io/etcd/pull/13639)
- [Always print the raft_term in decimal](https://github.com/etcd-io/etcd/pull/13711) when displaying member list in json.
- [Add one more field `storageVersion`](https://github.com/etcd-io/etcd/pull/13773) into the response of command `etcdctl endpoint status`.
- Add [`--max-txn-ops`](https://github.com/etcd-io/etcd/pull/14340) flag to make-mirror command.
- Add [`--consistency`](https://github.com/etcd-io/etcd/pull/15261) flag to member list command.
- Display [field `hash_revision`](https://github.com/etcd-io/etcd/pull/14812) for `etcdctl endpoint hash` command.
- Add [`--max-request-bytes` and `--max-recv-bytes`](https://github.com/etcd-io/etcd/pull/18718) global flags.

### etcdutl v3

- Add command to generate [shell completion](https://github.com/etcd-io/etcd/pull/13142).
- Add `migrate` command for downgrading/upgrading etcd data dir files.
- Add [optional --bump-revision and --mark-compacted flag to etcdutl snapshot restore operation](https://github.com/etcd-io/etcd/pull/16029).
- Add [hashkv](https://github.com/etcd-io/etcd/pull/15965) command to print hash of keys and values up to given revision
- Removed [legacy etcdutl backup](https://github.com/etcd-io/etcd/pull/16662)
- [Count the number of keys from users perspective](https://github.com/etcd-io/etcd/pull/19344)

### Package `clientv3`

- [Support serializable `MemberList` operation](https://github.com/etcd-io/etcd/pull/15261).

### Package `server`

- Package `mvcc` was moved to `storage/mvcc`
- Package `mvcc/backend` was moved to `storage/backend`
- Package `mvcc/buckets` was moved to `storage/schema`
- Package `wal` was moved to `storage/wal`
- Package `datadir` was moved to `storage/datadir`

### Package `raft`
- [Decouple raft from etcd](https://github.com/etcd-io/etcd/issues/14713). Migrated raft to a separate [repository](https://github.com/etcd-io/raft), and renamed raft module to `go.etcd.io/raft/v3`.

### etcd server

- Add [`etcd --log-format`](https://github.com/etcd-io/etcd/pull/13339) flag to support log format.
- Add [`etcd --experimental-max-learners`](https://github.com/etcd-io/etcd/pull/13377) flag to allow configuration of learner max membership.
- Add [`etcd --experimental-enable-lease-checkpoint-persist`](https://github.com/etcd-io/etcd/pull/13508) flag to handle upgrade from v3.5.2 clusters with this feature enabled.
- Add [`etcdctl make-mirror --rev`](https://github.com/etcd-io/etcd/pull/13519) flag to support incremental mirror.
- Add [v3 discovery](https://github.com/etcd-io/etcd/pull/13635) to bootstrap a new etcd cluster.
- Add [field `storage`](https://github.com/etcd-io/etcd/pull/13772) into the response body of endpoint `/version`.
- Add [`etcd --max-concurrent-streams`](https://github.com/etcd-io/etcd/pull/14169) flag to configure the max concurrent streams each client can open at a time, and defaults to math.MaxUint32.
- Add [`etcd grpc-proxy --experimental-enable-grpc-logging`](https://github.com/etcd-io/etcd/pull/14266) flag to logging all grpc requests and responses.
- Add [`etcd --experimental-compact-hash-check-enabled --experimental-compact-hash-check-time`](https://github.com/etcd-io/etcd/issues/14039) flags to support enabling reliable corruption detection on compacted revisions.
- Add [Protection on maintenance request when auth is enabled](https://github.com/etcd-io/etcd/pull/14663).
- Graduated [`--experimental-warning-unary-request-duration` to `--warning-unary-request-duration`](https://github.com/etcd-io/etcd/pull/14414). Note the experimental flag is deprecated and will be decommissioned in v3.7.
- Add [field `hash_revision` into `HashKVResponse`](https://github.com/etcd-io/etcd/pull/14537).
- Add [`etcd --experimental-snapshot-catch-up-entries`](https://github.com/etcd-io/etcd/pull/15033) flag to configure number of entries for a slow follower to catch up after compacting the raft storage entries and defaults to 5k. 
- Decreased [`--snapshot-count` default value from 100,000 to 10,000](https://github.com/etcd-io/etcd/pull/15408)
- Add [`etcd --tls-min-version --tls-max-version`](https://github.com/etcd-io/etcd/pull/15156) to enable support for TLS 1.3.
- Add [quota to endpoint status response](https://github.com/etcd-io/etcd/pull/17877)
- Add [feature gate `SetMemberLocalAddr`](https://github.com/etcd-io/etcd/pull/19413) to [enable using the first specified and non-loopback local address from initial-advertise-peer-urls as the local address when communicating with a peer](https://github.com/etcd-io/etcd/pull/17661)
- Add [Support multiple values for allowed client and peer TLS identities](https://github.com/etcd-io/etcd/pull/18015)
- Add [`embed.Config.GRPCAdditionalServerOptions`](https://github.com/etcd-io/etcd/pull/14066) to support updating the default internal gRPC configuration for embedded use cases.

### etcd grpc-proxy

- Add [`etcd grpc-proxy start --endpoints-auto-sync-interval`](https://github.com/etcd-io/etcd/pull/14354) flag to enable and configure interval of auto sync of endpoints with server.
- Add [`etcd grpc-proxy start --listen-cipher-suites`](https://github.com/etcd-io/etcd/pull/14308) flag to support adding configurable cipher list.
- Add [`tls min/max version to grpc proxy`](https://github.com/etcd-io/etcd/pull/18816) to support setting TLS min and max version.

### tools/benchmark

- [Add etcd client autoSync flag](https://github.com/etcd-io/etcd/pull/13416)

### Metrics, Monitoring

See [List of metrics](https://etcd.io/docs/latest/metrics/) for all metrics per release.

- Add [`etcd_disk_defrag_inflight`](https://github.com/etcd-io/etcd/pull/13371).
- Add [`etcd_debugging_server_alarms`](https://github.com/etcd-io/etcd/pull/14276).
- Add [`etcd_server_range_duration_seconds`](https://github.com/etcd-io/etcd/pull/17983).

### Go
- Require [Go 1.23+](https://github.com/etcd-io/etcd/pull/16594).
- Compile with [Go 1.23+](https://go.dev/doc/devel/release#go1.21.minor). Please refer to [gc-guide](https://go.dev/doc/gc-guide) to configure `GOGC` and `GOMEMLIMIT` properly. 

### Other

- Use Distroless as base image to make the image less vulnerable and reduce image size.
- [Upgrade grpc-gateway from v1 to v2](https://github.com/etcd-io/etcd/pull/16595).
- [Switch from grpc-ecosystem/go-grpc-prometheus to grpc-ecosystem/go-grpc-middleware/providers/prometheus](https://github.com/etcd-io/etcd/pull/19195).

---
