

Previous change logs can be found at [CHANGELOG-3.4](https://github.com/etcd-io/etcd/blob/main/CHANGELOG/CHANGELOG-3.4.md).

---

## v3.5.36 (TBC)

### etcd server

- Fix [`--force-new-cluster` permanently bricking a member whose backend consistent index is ahead of its WAL](https://github.com/etcd-io/etcd/pull/22688)

---

## v3.5.35 (2026-10-22)

### etcd server

- Fix [data race between `Hash` and `Restore` in the MVCC store](https://github.com/etcd-io/etcd/pull/22640)
- [Fix fsync snap directory when saving a received snapshot db](https://github.com/etcd-io/etcd/pull/22472)

### Dependencies

- Compile binaries using [go 1.26.10](https://github.com/etcd-io/etcd/pull/22632).
- Bump [go.opentelemetry.io/otel/sdk, go.opentelemetry.io/otel/exporters/otlp/otlptrace and go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc to v1.45.0 to address GO-2026-6505](https://github.com/etcd-io/etcd/pull/22558).
- Bump [golang.org/x/net to v0.61.0 to address GO-2026-6617, GO-2026-6612, GO-2026-6611 and GO-2026-6603](https://github.com/etcd-io/etcd/pull/22591).

---

## v3.5.34 (2026-09-22)

### etcdctl

- [Fix duplicate `RaftTerm` field in the `endpoint status` output when using `--write-out=fields`](https://github.com/etcd-io/etcd/pull/22219)
- [fileutil: close locked file handle on os.Remove error in purgeFile](https://github.com/etcd-io/etcd/pull/22457)

### Dependencies

- Compile binaries using [go 1.26.8](https://github.com/etcd-io/etcd/pull/22415).
- Bump [github.com/gorilla/websocket to v1.5.3](https://github.com/etcd-io/etcd/pull/22366), to address GHSA-w67g-5rqw-f597.
- Bump [go.opentelemetry.io/otel to v1.44.0 to addrss CVE-2026-41178](https://github.com/etcd-io/etcd/pull/22200).
- Bump [google.golang.org/grpc to v1.83.2 to address CVE-2026-84303](https://github.com/etcd-io/etcd/pull/22467).

---

## v3.5.33 (2026-07-23)

### etcd server

- Fix [unbounded io.ReadAll on peer lease HTTP handler body](https://github.com/etcd-io/etcd/pull/22161)
- Fix [the `costTxnReq` ignores nested `RequestTxn` issue](https://github.com/etcd-io/etcd/pull/22167)
- [Set a ReadHeaderTimeout for client http.Server](https://github.com/etcd-io/etcd/pull/22163)
- Fix [the security issue where a user granted read permission on one key could receive watch responses for every key starting from that key](https://github.com/etcd-io/etcd/security/advisories/GHSA-xg4h-6gfc-h4m8)

### Package `clientv3`

- Fix [unsynchronized range over leaseCache.entries](https://github.com/etcd-io/etcd/pull/22159)

### package `client/pkg/v3`

- [Set a tlsHandshakeTimeout for tlsListener](https://github.com/etcd-io/etcd/pull/22160). Refer to [GHSA-6vch-q96h-7gc3](https://github.com/etcd-io/etcd/security/advisories/GHSA-6vch-q96h-7gc3) for more details.

### Dependencies

- Compile binaries using [go 1.25.12](https://github.com/etcd-io/etcd/pull/22061).
- [Bump golang.org/x/net to v0.56.0 to address GO-2026-5942 and golang.org/x/text to v0.39.0 to address GO-2026-5970](https://github.com/etcd-io/etcd/pull/22138)
- [Bump google.golang.org/grpc to 1.82.1 to address GHSA-hrxh-6v49-42gf](https://github.com/etcd-io/etcd/pull/22153)

---

## v3.5.32 (2026-07-01)

### etcd server

- Add [`write-only-skip-check` option for `--v2-deprecation` to bypass the v2 content check](https://github.com/etcd-io/etcd/pull/21897)
- [server: allow non-admin maintenance status](https://github.com/etcd-io/etcd/pull/21815)
- Fix [websocket authentication with bearer-prefixed auth tokens](https://github.com/etcd-io/etcd/pull/21935).
- Fix [CRL enforcement bypass on gRPC listener when `--listen-client-http-urls` is configured](https://github.com/etcd-io/etcd/pull/22021), refer to [security/advisories/GHSA-3wh4-j44w-pg92](https://github.com/etcd-io/etcd/security/advisories/GHSA-3wh4-j44w-pg92) for more details.
- Avoid logging for JWT token for a case of failed parsing ([#21993](https://github.com/etcd-io/etcd/pull/21993)).

### etcdutl

- [enhance `etcdutl check v2store` to check both v2 snapshot and WAL records](https://github.com/etcd-io/etcd/pull/21889)

### Dependencies

- Compile binaries using [go 1.25.11](https://github.com/etcd-io/etcd/pull/21893)
- Bump `go.opentelemetry.io/otel` from `v1.40.0` to `v1.43.0` and `go.opentelemetry.io/otel/sdk` from `v1.40.0` to `v1.43.0` to address CVE-2026-29181 and CVE-2026-39883 ([#21919](https://github.com/etcd-io/etcd/pull/21919)).

---

## v3.5.31 (2026-06-01)

### etcd server

- [bugfix: MemberUpdate implicitly and unexpectedly promotes a learner](https://github.com/etcd-io/etcd/pull/21737)

### etcdutl

- [etcdutl: validate data file path instead of panic](https://github.com/etcd-io/etcd/pull/21769)

### Dependencies

- Compile binaries using [go 1.25.9](https://github.com/etcd-io/etcd/pull/21728)
- [Bump golang.org/x/crypto to v0.52.0 address GO-2026-5026](https://github.com/etcd-io/etcd/pull/21820)

---

## v3.5.30 (2026-05-01)

### etcd server

- [Fixed an issue that prevented adding a new member when one member was down, even though quorum was still satisfied](https://github.com/etcd-io/etcd/pull/21669)
- Fix RBAC authorization bypass allowing read access via PrevKv or lease attachment in Put requests nested in etcd transactions (see [PR/21682](https://github.com/etcd-io/etcd/pull/21682) and [PR/21687](https://github.com/etcd-io/etcd/pull/21687))

### Dependencies

- Compile binaries using [go 1.25.9](https://github.com/etcd-io/etcd/pull/21587)

---

## v3.5.29 (2026-04-01)

### etcd server

- [etcdserver: allow non-admin to fetch member list and alarms](https://github.com/etcd-io/etcd/pull/21550)

### etcdctl

- Fix [etcdctl endpoint command regression with option --cluster when auth is enabled](https://github.com/etcd-io/etcd/pull/21532)

---

## v3.5.28 (2026-03-20)

### etcd server

- [Ensure the metrics interceptor runs before other interceptors so that metrics remain up to date](https://github.com/etcd-io/etcd/pull/21336)
- Fix [Race between read index and leader change](https://github.com/etcd-io/etcd/pull/21387)
- Fix [Stale reads caused by process pausing](https://github.com/etcd-io/etcd/pull/21421)
- Fix [cannot promote member from follower when auth is enabled](https://github.com/etcd-io/etcd/pull/21494)
- Guard unauthenticated endpoints with auth checks to fix [Authorization bypasses in multiple APIs (CVE-2026-33413)](https://github.com/etcd-io/etcd/security/advisories/GHSA-q8m4-xhhv-38mg)
- Enforce auth checks for nested txn ops to fix [Nested etcd transactions bypass RBAC authorization checks (CVE-2026-33343)](https://github.com/etcd-io/etcd/security/advisories/GHSA-rfx7-8w68-q57q)

### Package `clientv3`

- [Print the endpoint the grpc request was actually sent to in unary interceptor](https://github.com/etcd-io/etcd/pull/21380)

### etcd grpc-proxy

- [server/etcdmain: fix startup deadlock in grpcproxy](https://github.com/etcd-io/etcd/pull/21356)

### etcdctl

- Fix [slice bounds trimming single-quoted args in Argify](https://github.com/etcd-io/etcd/pull/21403)

### Dependencies

- [Bump go.opentelemetry.io/otel/sdk to v1.40.0 to resolve https://pkg.go.dev/vuln/GO-2026-4394](https://github.com/etcd-io/etcd/pull/21338)
- Compile binaries using [go 1.25.8](https://github.com/etcd-io/etcd/pull/21462)
- [Bump golang.org/x/net to v0.51.0 to resolve GO-2026-4559](https://github.com/etcd-io/etcd/pull/21441)
- [Bump google.golang.org/grpc to 1.79.3 to resolve CVE-2026-33186](https://github.com/etcd-io/etcd/pull/21500)

---

## v3.5.27 (2026-02-13)

### Package `clientv3`

- [Remove the use of grpc-go's Metadata field](https://github.com/etcd-io/etcd/pull/21242)

### Dependencies

- Compile binaries using [go 1.24.13](https://github.com/etcd-io/etcd/pull/21266). This addresses [CVE-2025-61726](https://github.com/advisories/GHSA-gm9r-q53w-2gh4), [CVE-2025-61731](https://github.com/advisories/GHSA-xvqr-69v8-f3gv), and [CVE-2025-61732](https://github.com/advisories/GHSA-8jvr-vh7g-f8gx).

---

## v3.5.26 (2025-12-17)

### etcd server

- [Print token fingerprint instead of the original tokens in log messages](https://github.com/etcd-io/etcd/pull/20942)
- Fix [zombie members in v3store](https://github.com/etcd-io/etcd/pull/20995)

### etcdctl

- [Fix a typo of 'etcdctl snapshot restore' command](https://github.com/etcd-io/etcd/pull/20948).

### Dependencies

- Compile binaries using [go 1.24.11](https://github.com/etcd-io/etcd/pull/20999).
- Bump [golang.org/x/crypto to 0.45.0 to address CVE-2025-47914, and CVE-2025-58181](https://github.com/etcd-io/etcd/pull/21023).

---

## v3.5.25 (2025-11-11)

### etcd server

- Fix [`--force-new-cluster can't clean up learners after creating snapshot`](https://github.com/etcd-io/etcd/pull/20896)

### etcdutl

- Add [flag `--wal-dir` to `etcdutl check v2store` command to support dedicated WAL directory](https://github.com/etcd-io/etcd/pull/20886)

### Dependencies

- Compile binaries using [go 1.24.10](https://github.com/etcd-io/etcd/pull/20902).

---

## v3.5.24 (2025-10-22)

### etcd server

- [Reject watch request with -1 revision to prevent invalid resync behavior on uncompacted etcd](https://github.com/etcd-io/etcd/pull/20709)
- [Change the TLS handshake 'EOF' errors to DEBUG not to spam logs](https://github.com/etcd-io/etcd/pull/20751)
- Fix [Learner promotion not being persisted into v3store may be propagated across multiple upgrades](https://github.com/etcd-io/etcd/pull/20797)

### Dependencies

- Compile binaries using [go 1.24.9](https://github.com/etcd-io/etcd/pull/20806).

---

## v3.5.23 (2025-09-19)

### etcd server

- Fix [etcd may return success for leaseRenew request even when the lease is revoked](https://github.com/etcd-io/etcd/pull/20616)
- Fix [potential data corruption when applySnapshot and defragment happen concurrently](https://github.com/etcd-io/etcd/pull/20653)

### Dependencies

- Compile binaries using [go 1.24.7](https://github.com/etcd-io/etcd/pull/20665).
- [Bump bbolt to v1.3.12](https://github.com/etcd-io/etcd/pull/20514).

---

## v3.5.22 (2025-07-22)

### etcd server

- Fix [the compaction pause duration metric is not emitted for every compaction batch](https://github.com/etcd-io/etcd/pull/19771)
- Fix [mvcc: avoid double decrement of watcher gauge on close/cancel race](https://github.com/etcd-io/etcd/pull/20066)
- Fix [Watch on future revision returns old events or notifications](https://github.com/etcd-io/etcd/pull/20290)
- Fix [`--force-new-cluster` can't remove all other members in a corner case](https://github.com/etcd-io/etcd/pull/20339)
- Fix [v2store check (IsMetaStoreOnly) returns wrong result even there is no any auth data](https://github.com/etcd-io/etcd/pull/20357)
- Improve [help message for --quota-backend-bytes](https://github.com/etcd-io/etcd/pull/20380)

### Package `clientv3`

- [Replace `resolver.State.Addresses` with `resolver.State.Endpoint.Addresses`](https://github.com/etcd-io/etcd/pull/19783).
- [Deprecated the Metadata field in the Endpoint struct from the client/v3/naming/endpoints package](https://github.com/etcd-io/etcd/pull/19846).

### Dependencies

- Compile binaries using [go 1.23.11](https://github.com/etcd-io/etcd/pull/20321)

---

## v3.5.21 (2025-03-27)

### Dependencies

- Bump [github.com/golang-jwt/jwt/v4 from 4.5.1 to 4.5.2 to address CVE-2025-30204](https://github.com/etcd-io/etcd/pull/19646).
- Bump [bump golang.org/x/net from v0.36.0 to v0.38.0 to address CVE-2025-22870 and CVE-2025-22872](https://github.com/etcd-io/etcd/pull/19686).

---

## v3.5.20 (2025-03-21)

### etcd server

- Fix [the learner promotion changes not being persisted into v3store (bbolt)](https://github.com/etcd-io/etcd/pull/19563)
- Update [the RLock in Demoted method for read-only access to expiry](https://github.com/etcd-io/etcd/pull/19445)

### etcdctl

- Fix [command `etcdctl member promote` doesn't support json output](https://github.com/etcd-io/etcd/pull/19602)

### etcd grpc-proxy

- Fix [grpcproxy can get stuck in and endless loop causing high CPU usage](https://github.com/etcd-io/etcd/pull/19562)

---

## v3.5.19 (2025-03-05)

### etcd server
- Backport [add learner status check to readyz endpoint](https://github.com/etcd-io/etcd/pull/19280).
- Fix [performance regression due to uncertain compaction sleep interval](https://github.com/etcd-io/etcd/pull/19405).

### `tools/benchmark`
- Backport [add mixed read-write performance evaluation scripts](https://github.com/etcd-io/etcd/pull/19275).

### Dependencies
- Compile binaries using [go 1.23.7](https://github.com/etcd-io/etcd/pull/19528).
- Bump [golang.org/x/crypto to v0.35.0 to address CVE-2025-22869](https://github.com/etcd-io/etcd/pull/19478).
- Bump [golang.org/x/net to v0.36.0 to address CVE-2025-22870](https://github.com/etcd-io/etcd/pull/19530).

---

## v3.5.18 (2025-01-24)

### etcd server
- Avoid deadlock in etcd.Close when stopping during bootstrapping, see https://github.com/etcd-io/etcd/pull/19167 and https://github.com/etcd-io/etcd/pull/19258.
- [Print warning messages if any of the deprecated v2store related flags is set](https://github.com/etcd-io/etcd/pull/18999)
- Fix [missing delete event on watch opened on same revision as compaction request](https://github.com/etcd-io/etcd/pull/19249)

### Package `clientv3`
- Fix [runtime panic that occurs when KeepAlive is called with a Context implemented by an uncomparable type](https://github.com/etcd-io/etcd/pull/18937)

### etcdutl v3
- Add [command `etcdutl check v2store` to offline check whether v2store contains custom content](https://github.com/etcd-io/etcd/pull/19113)

### etcd grpc-proxy
- Add [`tls min/max version to grpc proxy`](https://github.com/etcd-io/etcd/pull/18829) to support setting TLS min and max version.

### Dependencies
- Bump [golang-jwt/jwt to 4.5.1 to address GO-2024-3250](https://github.com/etcd-io/etcd/pull/18899).
- Compile binaries using [go 1.22.11](https://github.com/etcd-io/etcd/pull/19211).
- Bump [golang.org/x/crypto to 0.32.0 to address CVE-2024-45337](https://github.com/etcd-io/etcd/pull/19154).
- Bump [golang.org/x/net to 0.34.0 to address CVE-2024-45338](https://github.com/etcd-io/etcd/pull/19158).

---

## v3.5.17 (2024-11-12)

### etcd server
- Fix [watchserver related goroutine leakage](https://github.com/etcd-io/etcd/pull/18784)
- Fix [risk of a partial write txn being applied](https://github.com/etcd-io/etcd/pull/18799)
- Fix [panicking occurred due to improper error handling during defragmentation](https://github.com/etcd-io/etcd/pull/18842)
- Fix [close temp file(s) in case an error happens during defragmentation](https://github.com/etcd-io/etcd/pull/18854)

### Dependencies
- Compile binaries using [go 1.22.9](https://github.com/etcd-io/etcd/pull/18849).

---

## v3.5.16 (2024-09-10)

### etcd server
- Fix [performance regression issue caused by the `ensureLeadership` in lease renew](https://github.com/etcd-io/etcd/pull/18439).
- [Keep the tombstone during compaction if it happens to be the compaction revision](https://github.com/etcd-io/etcd/pull/18474)
- Add [`etcd --experimental-compaction-sleep-interval`](https://github.com/etcd-io/etcd/pull/18514) flag to control the sleep interval between each compaction batch.

### Dependencies
- Compile binaries using [go 1.22.7](https://github.com/etcd-io/etcd/pull/18550).
- Upgrade [bbolt to v1.3.11](https://github.com/etcd-io/etcd/pull/18489).

---

## v3.5.15 (2024-07-19)

### etcd server
- Fix [add prometheus metric registration for metric `etcd_disk_wal_write_duration_seconds`](https://github.com/etcd-io/etcd/pull/18174).
- Add [Support multiple values for allowed client and peer TLS identities](https://github.com/etcd-io/etcd/pull/18160)
- Fix [noisy logs from simple auth token expiration by reducing log level to debug](https://github.com/etcd-io/etcd/pull/18245)
- [Differentiate the warning message for rejected client and peer connections](https://github.com/etcd-io/etcd/pull/18319)

### Package clientv3
- [Print gRPC metadata in guaranteed order using the official go fmt pkg](https://github.com/etcd-io/etcd/pull/18312).

### Dependencies
- Compile binaries using [go 1.21.12](https://github.com/etcd-io/etcd/pull/18271).
- [Fully address CVE-2023-45288 and fix govulncheck CI check](https://github.com/etcd-io/etcd/pull/18170)

## v3.5.14 (2024-05-29)

### etcd server
- Fix [LeaseTimeToLive returns error if leader changed](https://github.com/etcd-io/etcd/pull/17704).
- Add [metrics `etcd_disk_wal_write_duration_seconds`](https://github.com/etcd-io/etcd/pull/17616).
- Fix [ignore raft messages if member id mismatch](https://github.com/etcd-io/etcd/pull/17813).
- Update [the compaction log when bootstrap](https://github.com/etcd-io/etcd/pull/17830).
- Fix [Revision decreasing after panic during compaction](https://github.com/etcd-io/etcd/pull/17865)
- Add [`etcd --experimental-stop-grpc-service-on-defrag`](https://github.com/etcd-io/etcd/pull/17914) to enable client failover on defrag.
- Add [support for `AllowedCN` and `AllowedHostname` through config file](https://github.com/etcd-io/etcd/pull/18063)

### etcdutl v3
- Add [`--initial-memory-map-size` to `snapshot restore` to avoid memory allocation issues](https://github.com/etcd-io/etcd/pull/17977)

### Package `clientv3`
- Add [requests retry when receiving ErrGPRCNotSupportedForLearner and endpoints > 1](https://github.com/etcd-io/etcd/pull/17641).
- Fix [initialization for mu in client context](https://github.com/etcd-io/etcd/pull/17699).

### Dependencies
- Compile binaries using [go 1.21.10](https://github.com/etcd-io/etcd/pull/17980).
- Upgrade [bbolt to v1.3.10](https://github.com/etcd-io/etcd/pull/17943).

---

## v3.5.13 (2024-03-29)

### etcd server
- Fix leases wrongly revoked by the leader by [ignoring old leader's leases revoking request](https://github.com/etcd-io/etcd/pull/17425).
- Fix [no progress notification being sent for watch that doesn't get any events](https://github.com/etcd-io/etcd/pull/17566).
- Fix [watch event loss after compaction](https://github.com/etcd-io/etcd/pull/17612).

### Package `clientv3`
- Add [client backoff and retry config options](https://github.com/etcd-io/etcd/pull/17363).
- [Ignore SetKeepAlivePeriod errors on OpenBSD](https://github.com/etcd-io/etcd/pull/17387).
- [Support unix/unixs socket in client or peer URLs](https://github.com/etcd-io/etcd/pull/15940)

### gRPC Proxy
- Add [three flags (see below) for grpc-proxy](https://github.com/etcd-io/etcd/pull/17447)
  - `--dial-keepalive-time`
  - `--dial-keepalive-timeout`
  - `--permit-without-stream`

### Dependencies
- Upgrade [bbolt to v1.3.9](https://github.com/etcd-io/etcd/pull/17483).
- Compile binaries using [go 1.21.8](https://github.com/etcd-io/etcd/pull/17537).
- Upgrade [google.golang.org/protobuf to v1.33.0 to address CVE-2024-24786](https://github.com/etcd-io/etcd/pull/17553).
- Upgrade github.com/sirupsen/logrus to v1.9.3 to address [PRISMA-2023-0056](https://github.com/etcd-io/etcd/pull/17482).

### Others
- [Make CGO_ENABLED configurable](https://github.com/etcd-io/etcd/pull/17421).

---

## v3.5.12 (2024-01-31)

### etcd server
- Fix [not validating database consistent index, and panicking on nil backend](https://github.com/etcd-io/etcd/pull/17151)
- Document [`experimental-enable-lease-checkpoint-persist` flag in etcd help](https://github.com/etcd-io/etcd/pull/17190)
- Fix [needlessly flocking snapshot files when deleting](https://github.com/etcd-io/etcd/pull/17206)
- Add [digest for etcd base image](https://github.com/etcd-io/etcd/pull/17205)
- Fix [delete inconsistencies in read buffer](https://github.com/etcd-io/etcd/pull/17230)
- Add [mvcc: print backend database size and size in use in compaction logs](https://github.com/etcd-io/etcd/pull/17291)

### Dependencies
- Compile binaries using [go 1.20.13](https://github.com/etcd-io/etcd/pull/17275)
- Upgrade [golang.org/x/crypto to v0.17+ to address CVE-2023-48795](https://github.com/etcd-io/etcd/pull/17346)

## v3.5.11 (2023-12-07)

### etcd server
- Fix distributed tracing by ensuring `--experimental-distributed-tracing-sampling-rate` configuration option is available to [set tracing sample rate](https://github.com/etcd-io/etcd/pull/16951).
- Fix [url redirects while checking peer urls during new member addition](https://github.com/etcd-io/etcd/pull/16986)
- Add [livez/readyz HTTP endpoints](https://github.com/etcd-io/etcd/pull/17039)

### Dependencies
- Compile binaries using [go 1.20.12](https://github.com/etcd-io/etcd/pull/17077)
- Fix [CVE-2023-47108](https://github.com/advisories/GHSA-8pgv-569h-w5rw) by [bumping go.opentelemetry.io/otel to 1.20.0 and go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc to 0.46.0](https://github.com/etcd-io/etcd/pull/16946).

---

## v3.5.10 (2023-10-27)

### etcd server
- Fix [`--socket-reuse-port` and `--socket-reuse-address` not able to be set in configuration file](https://github.com/etcd-io/etcd/pull/16435).
- Fix [corruption check may get a `ErrCompacted` error when server has just been compacted](https://github.com/etcd-io/etcd/pull/16048)
- Improve [Lease put performance for the case that auth is disabled or the user is admin](https://github.com/etcd-io/etcd/pull/16019)
- Improve [Skip getting authInfo from incoming context when auth is disabled](https://github.com/etcd-io/etcd/pull/16241)
- Fix [Hash and HashKV have duplicated RESTful API](https://github.com/etcd-io/etcd/pull/16490)

### etcdutl v3
- Add [optional --bump-revision and --mark-compacted flag to etcdutl snapshot restore operation](https://github.com/etcd-io/etcd/pull/16165).

### etcdctl v3
- Add [optional --bump-revision and --mark-compacted flag to etcdctl snapshot restore operation](https://github.com/etcd-io/etcd/pull/16165).

### etcd grpc-proxy
- Fix [Memberlist results not updated when proxy node down](https://github.com/etcd-io/etcd/pull/15907).

### Package `clientv3`
- Fix [Multiple endpoints with same prefix got mixed up](https://github.com/etcd-io/etcd/pull/15939)
- Fix [Unexpected blocking when barrier waits on a nonexistent key](https://github.com/etcd-io/etcd/pull/16188)
- Fix [Reset auth token when failing to authenticate due to auth being disabled](https://github.com/etcd-io/etcd/pull/16241)
- Fix [panic in etcd validate secure endpoints](https://github.com/etcd-io/etcd/pull/16565)

### Dependencies
- Compile binaries using [go 1.20.10](https://github.com/etcd-io/etcd/pull/16745).
- Upgrade gRPC to 1.58.3 in https://github.com/etcd-io/etcd/pull/16625, https://github.com/etcd-io/etcd/pull/16781 and https://github.com/etcd-io/etcd/pull/16790. Note that gRPC server will reject requests with connection header (refer to https://github.com/grpc/grpc-go/pull/4803).
- Upgrade [bbolt to v1.3.8](https://github.com/etcd-io/etcd/pull/16833)

---

## v3.5.9 (2023-05-11)

### etcd server
- Fix [LeaseTimeToLive API may return keys to clients which have no read permission on the keys](https://github.com/etcd-io/etcd/pull/15815).

### Dependencies
- Compile binaries using [go 1.19.9](https://github.com/etcd-io/etcd/pull/15822).

---

## v3.5.8 (2023-04-13)

### etcd server
- Add [`etcd --tls-min-version --tls-max-version`](https://github.com/etcd-io/etcd/pull/15483) to enable support for TLS 1.3.
- Add [`etcd --listen-client-http-urls`](https://github.com/etcd-io/etcd/pull/15589) flag to support separating http server from grpc one, thus giving full immunity to [watch stream starvation under high read load](https://github.com/etcd-io/etcd/issues/15402).
- Change [http2 frame scheduler to random algorithm](https://github.com/etcd-io/etcd/pull/15452)
- Fix [Watch response traveling back in time when reconnecting member downloads snapshot from the leader](https://github.com/etcd-io/etcd/pull/15515)
- Fix [race when starting both secure & insecure gRPC servers on the same address](https://github.com/etcd-io/etcd/pull/15517)
- Fix [server/auth: disallow creating empty permission ranges](https://github.com/etcd-io/etcd/pull/15619)
- Fix [aligning zap log timestamp resolution to microseconds](https://github.com/etcd-io/etcd/pull/15240). Etcd now uses zap timestamp format: `2006-01-02T15:04:05.999999Z0700` (microsecond instead of milliseconds precision).
- Fix [wsproxy did not print log in JSON format](https://github.com/etcd-io/etcd/pull/15661).
- Fix [CVE-2021-28235](https://nvd.nist.gov/vuln/detail/CVE-2021-28235) by [clearing password after authenticating the user](https://github.com/etcd-io/etcd/pull/15653).
- Fix [etcdserver may panic when parsing a JWT token without username or revision](https://github.com/etcd-io/etcd/pull/15676).
- Fix [Requested watcher progress notifications are not synchronised with stream](https://github.com/etcd-io/etcd/pull/15695).

### Package `netutil`
- Fix [consistently format IPv6 addresses for comparison](https://github.com/etcd-io/etcd/pull/15187).

### Package `clientv3`
- Fix [etcd might send duplicated events to watch clients](https://github.com/etcd-io/etcd/pull/15274).

### Dependencies
- Recommend [Go 1.19+](https://github.com/etcd-io/etcd/pull/15337).
- Compile binaries using [go to 1.19.8](https://github.com/etcd-io/etcd/pull/15651)
- Upgrade [golang.org/x/net to v0.7.0](https://github.com/etcd-io/etcd/pull/15337)
- Upgrade [bbolt to v1.3.7](https://github.com/etcd-io/etcd/pull/15222).

### Docker image
- [Remove nsswitch.conf from docker image](https://github.com/etcd-io/etcd/pull/15161)
- Fix [etcd docker images all tagged with amd64 architecture](https://github.com/etcd-io/etcd/pull/15612)

---

## v3.5.7 (2023-01-20)

### etcd server
- Fix [Remove memberID from data corrupt alarm](https://github.com/etcd-io/etcd/pull/14852).
- Fix [Allow non mutating requests pass through quotaKVServer when NOSPACE](https://github.com/etcd-io/etcd/pull/14884).
- Fix [nil pointer panic for readonly txn due to nil response](https://github.com/etcd-io/etcd/pull/14899).
- Fix [The last record which was partially synced to disk isn't automatically repaired](https://github.com/etcd-io/etcd/pull/15069).
- Fix [etcdserver might promote a non-started learner](https://github.com/etcd-io/etcd/pull/15096).

### Package `clientv3`
- Reverted the fix to [auth invalid token and old revision errors in watch](https://github.com/etcd-io/etcd/pull/14995).

### Dependencies
- Recommend [Go 1.17+](https://github.com/etcd-io/etcd/pull/15019).
- Compile binaries using [Go 1.17.13](https://github.com/etcd-io/etcd/pull/15019)
- Bumped [some dependencies](https://github.com/etcd-io/etcd/pull/15018) to address some HIGH Vulnerabilities.

### Docker image
- Use [distroless base image](https://github.com/etcd-io/etcd/pull/15016) to address critical Vulnerabilities.
- Updated [base image from base-debian11 to static-debian11 and removed dependency on busybox](https://github.com/etcd-io/etcd/pull/15037).

---

## v3.5.6 (2022-11-21)

### etcd server
- Fix [auth invalid token and old revision errors in watch](https://github.com/etcd-io/etcd/pull/14547)
- Fix [avoid closing a watch with ID 0 incorrectly](https://github.com/etcd-io/etcd/pull/14563)
- Fix [auth: fix data consistency issue caused by recovery from snapshot](https://github.com/etcd-io/etcd/pull/14648)
- Fix [revision might be inconsistency between members when etcd crashes during processing defragmentation operation](https://github.com/etcd-io/etcd/pull/14733)
- Fix [timestamp in inconsistent format](https://github.com/etcd-io/etcd/pull/14799)
- Fix [Failed resolving host due to lost DNS record](https://github.com/etcd-io/etcd/pull/14573)

### Package `clientv3`
- Fix [Add backoff before retry when watch stream returns unavailable](https://github.com/etcd-io/etcd/pull/14582).
- Fix [stack overflow error in double barrier](https://github.com/etcd-io/etcd/pull/14658)
- Fix [Refreshing token on CommonName based authentication causes segmentation violation in client](https://github.com/etcd-io/etcd/pull/14790).

### etcd grpc-proxy
- Add [`etcd grpc-proxy start --listen-cipher-suites`](https://github.com/etcd-io/etcd/pull/14500) flag to support adding configurable cipher list.

---

## v3.5.5 (2022-09-15)

### Deprecations
- Deprecated [SetKeepAlive and SetKeepAlivePeriod in limitListenerConn](https://github.com/etcd-io/etcd/pull/14366).

### Package `clientv3`
- Fix [do not overwrite authTokenBundle on dial](https://github.com/etcd-io/etcd/pull/14132).
- Fix [IsOptsWithPrefix returns false even if WithPrefix() is included](https://github.com/etcd-io/etcd/pull/14187).

### etcd server
- [Build official darwin/arm64 artifacts](https://github.com/etcd-io/etcd/pull/14436).
- Add [`etcd --max-concurrent-streams`](https://github.com/etcd-io/etcd/pull/14219) flag to configure the max concurrent streams each client can open at a time, and defaults to math.MaxUint32.
- Add [`etcd --experimental-compact-hash-check-enabled --experimental-compact-hash-check-time`](https://github.com/etcd-io/etcd/issues/14039) flags to support enabling reliable corruption detection on compacted revisions.
- Fix [unexpected error during txn](https://github.com/etcd-io/etcd/issues/14110).
- Fix [lease leak issue due to tokenProvider isn't enabled when restoring auth store from a snapshot](https://github.com/etcd-io/etcd/pull/13205).
- Fix [the race condition between goroutine and channel on the same leases to be revoked](https://github.com/etcd-io/etcd/pull/14087).
- Fix [lessor may continue to schedule checkpoint after stepping down leader role](https://github.com/etcd-io/etcd/pull/14087).
- Fix [Restrict the max size of each WAL entry to the remaining size of the WAL file](https://github.com/etcd-io/etcd/pull/14127).
- Fix [Protect rangePermCache with a RW lock correctly](https://github.com/etcd-io/etcd/pull/14227)
- Fix [memberID equals zero in corruption alarm](https://github.com/etcd-io/etcd/pull/14272)
- Fix [Durability API guarantee broken in single node cluster](https://github.com/etcd-io/etcd/pull/14424)
- Fix [etcd fails to start after performing alarm list operation and then power off/on](https://github.com/etcd-io/etcd/pull/14429)
- Fix [authentication data not loaded on member startup](https://github.com/etcd-io/etcd/pull/14409)

### etcdctl v3

- Fix [etcdctl move-leader may fail for multiple endpoints](https://github.com/etcd-io/etcd/pull/14434)


### Other
- [Bump golang.org/x/crypto to latest version](https://github.com/etcd-io/etcd/pull/13996) to address [CVE-2022-27191](https://github.com/advisories/GHSA-8c26-wmh5-6g9v).
- [Bump OpenTelemetry to 1.0.1 and gRPC to 1.41.0](https://github.com/etcd-io/etcd/pull/14312).

---

## v3.5.4 (2022-04-24)

### etcd server
- Fix [etcd panic on startup (auth enabled)](https://github.com/etcd-io/etcd/pull/13946)

### package `client/pkg/v3`

- [Revert the change of trimming the trailing dot from SRV.Target](https://github.com/etcd-io/etcd/pull/13950) returned by DNS lookup


---

## v3.5.3 (2022-04-13)

### etcd server
- Fix [Provide a better liveness probe for when etcd runs as a Kubernetes pod](https://github.com/etcd-io/etcd/pull/13706)
- Fix [inconsistent log format](https://github.com/etcd-io/etcd/pull/13864)
- Fix [Inconsistent revision and data occurs](https://github.com/etcd-io/etcd/pull/13908)
- Fix [Etcdserver is still in progress of processing LeaseGrantRequest when it receives a LeaseKeepAliveRequest on the same leaseID](https://github.com/etcd-io/etcd/pull/13932)
- Fix [consistent_index coming from snapshot is overwritten by the old local value](https://github.com/etcd-io/etcd/pull/13933)
- [Update container base image snapshot](https://github.com/etcd-io/etcd/pull/13862)
- Fix [Defrag unsets backend options](https://github.com/etcd-io/etcd/pull/13701).

### package `client/pkg/v3`

- [Trim the suffix dot from the target](https://github.com/etcd-io/etcd/pull/13714) in SRV records returned by DNS lookup

### etcdctl v3

- [Always print the raft_term in decimal](https://github.com/etcd-io/etcd/pull/13727) when displaying member list in json.

---

## [v3.5.2](https://github.com/etcd-io/etcd/releases/tag/v3.5.2) (2022-02-01)

See [code changes](https://github.com/etcd-io/etcd/compare/v3.5.1...v3.5.2) and [v3.5 upgrade guide](https://etcd.io/docs/latest/upgrades/upgrade_3_5/) for any breaking changes.

### etcd server
- Fix [exclude the same alarm type activated by multiple peers](https://github.com/etcd-io/etcd/pull/13476).
- Add [`etcd --experimental-enable-lease-checkpoint-persist`](https://github.com/etcd-io/etcd/pull/13508) flag to enable checkpoint persisting.
- Fix [Lease checkpoints don't prevent to reset ttl on leader change](https://github.com/etcd-io/etcd/pull/13508), requires enabling checkpoint persisting.
- Fix [assertion failed due to tx closed when recovering v3 backend from a snapshot db](https://github.com/etcd-io/etcd/pull/13501)
- Fix [segmentation violation(SIGSEGV) error due to premature unlocking of watchableStore](https://github.com/etcd-io/etcd/pull/13541)

---

## [v3.5.1](https://github.com/etcd-io/etcd/releases/tag/v3.5.1) (2021-10-15)

See [code changes](https://github.com/etcd-io/etcd/compare/v3.5.0...v3.5.1) and [v3.5 upgrade guide](https://etcd.io/docs/latest/upgrades/upgrade_3_5/) for any breaking changes.

### etcd server

- Fix [self-signed-cert-validity parameter cannot be specified in the config file](https://github.com/etcd-io/etcd/pull/13237).
- Fix [ensure that cluster members stored in v2store and backend are in sync](https://github.com/etcd-io/etcd/pull/13348)

### etcd client

- [Fix etcd client sends invalid :authority header](https://github.com/etcd-io/etcd/issues/13192)

### package clientv3

- Endpoints self identify now as `etcd-endpoints://{id}/{authority}` where authority is based on first endpoint passed, for example `etcd-endpoints://0xc0009d8540/localhost:2079`

### Other

- Updated [base image](https://github.com/etcd-io/etcd/pull/13386) from `debian:buster-v1.4.0` to `debian:bullseye-20210927` to fix the following critical CVEs:
  - [CVE-2021-3711](https://nvd.nist.gov/vuln/detail/CVE-2021-3711): miscalculation of a buffer size in openssl's SM2 decryption
  - [CVE-2021-35942](https://nvd.nist.gov/vuln/detail/CVE-2021-35942): integer overflow flaw in glibc
  - [CVE-2019-9893](https://nvd.nist.gov/vuln/detail/CVE-2019-9893): incorrect syscall argument generation in libseccomp
  - [CVE-2021-36159](https://nvd.nist.gov/vuln/detail/CVE-2021-36159): libfetch in apk-tools mishandles numeric strings in FTP and HTTP protocols to allow out of bound reads.

---

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
- Add [`etcd_disk_defrag_inflight`](https://github.com/etcd-io/etcd/pull/13395).

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
- `ETCD_VERIFY="all"` environment triggers [additional verification of consistency](https://github.com/etcd-io/etcd/pull/12901) of etcd data-dir files.
- Add [`etcd --enable-log-rotation`](https://github.com/etcd-io/etcd/pull/12774) boolean flag which enables log rotation if true.
- Add [`etcd --log-rotation-config-json`](https://github.com/etcd-io/etcd/pull/12774) flag which allows passthrough of JSON config to configure log rotation for a file output target.
- Add experimental distributed tracing boolean flag [`--experimental-enable-distributed-tracing`](https://github.com/etcd-io/etcd/pull/12919) which enables tracing.
- Add [`etcd --experimental-distributed-tracing-address`](https://github.com/etcd-io/etcd/pull/12919) string flag which allows configuring the OpenTelemetry collector address.
- Add [`etcd --experimental-distributed-tracing-service-name`](https://github.com/etcd-io/etcd/pull/12919) string flag which allows changing the default "etcd" service name.
- Add [`etcd --experimental-distributed-tracing-instance-id`](https://github.com/etcd-io/etcd/pull/12919) string flag which configures an instance ID, which must be unique per etcd instance.
- Add [`--experimental-bootstrap-defrag-threshold-megabytes`](https://github.com/etcd-io/etcd/pull/12941) which configures a threshold for the unused db size and etcdserver will automatically perform defragmentation on bootstrap when it exceeds this value. The functionality is disabled if the value is 0.

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
- [ETCDCTL_API=3 `etcdctl migrate`](https://github.com/etcd-io/etcd/pull/12971) has been decommissioned. Use etcd <=v3.4 to restore v2 storage.

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
- Fix [a bug of not refreshing expired tokens](https://github.com/etcd-io/etcd/pull/13308).
- 
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


---

