
Previous change logs can be found at [CHANGELOG-3.6](https://github.com/etcd-io/etcd/blob/main/CHANGELOG/CHANGELOG-3.6.md).

---

## v3.7.4 (TBC)

### etcd server

- Fix [`--force-new-cluster` permanently bricking a member whose backend consistent index is ahead of its WAL](https://github.com/etcd-io/etcd/pull/22685)

---

## v3.7.3 (2026-10-22)

### etcd server

- Fix [data race between `Hash` and `Restore` in the MVCC store](https://github.com/etcd-io/etcd/pull/22636)
- [Fix data race in keys-only `RangeStream`](https://github.com/etcd-io/etcd/pull/22601)
- Fix [performance regression caused by the leader sending `MsgApp` messages to followers in reverse order](https://github.com/etcd-io/etcd/pull/22607)

### Deprecations

- Deprecated [NewJournalWriter in client/pkg/v3/logutil](https://github.com/etcd-io/etcd/pull/22530). It will be removed in v3.8, so that `client/pkg/v3` no longer depends on `github.com/coreos/go-systemd/v22`.

### Dependencies

- Compile binaries using [go 1.26.10](https://github.com/etcd-io/etcd/pull/22630).
- Bump [go.opentelemetry.io/otel/sdk, go.opentelemetry.io/otel/exporters/otlp/otlptrace and go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc to v1.45.0 to address GO-2026-6505](https://github.com/etcd-io/etcd/pull/22552).
- Bump [golang.org/x/net to v0.61.0 to address GO-2026-6617, GO-2026-6612, GO-2026-6611 and GO-2026-6603](https://github.com/etcd-io/etcd/pull/22583).

---

## v3.7.2 (2026-09-22)

### etcd server

- [Update `MinimalEtcdVersion` to read latest snapshot entry from WAL](https://github.com/etcd-io/etcd/pull/22201)
- [Fix fsync snap directory when saving a received snapshot db](https://github.com/etcd-io/etcd/pull/22378)

### etcdctl

- [Fix duplicate `RaftTerm` field in the `endpoint status` output when using `--write-out=fields`](https://github.com/etcd-io/etcd/pull/22217)
- [fileutil: close locked file handle on os.Remove error in purgeFile](https://github.com/etcd-io/etcd/pull/22455)

### Dependencies

- Compile binaries using [go 1.26.8](https://github.com/etcd-io/etcd/pull/22413).
- Bump [go.opentelemetry.io/otel to v1.44.0 to addrss CVE-2026-41178](https://github.com/etcd-io/etcd/pull/22198).
- Bump [google.golang.org/grpc to v1.83.2 to address CVE-2026-84303](https://github.com/etcd-io/etcd/pull/22469).

---

## v3.7.1 (2026-07-23)

### etcd server

- Fix [unbounded io.ReadAll on peer lease HTTP handler body](https://github.com/etcd-io/etcd/pull/22147)
- [Set a reasonable value for `snapshotLimitByte`](https://github.com/etcd-io/etcd/pull/22145)
- Fix [the `costTxnReq` ignores nested `RequestTxn` issue](https://github.com/etcd-io/etcd/pull/22139)
- [Set a ReadHeaderTimeout for client http.Server](https://github.com/etcd-io/etcd/pull/22143)
- Fix [the security issue where a user granted read permission on one key could receive watch responses for every key starting from that key](https://github.com/etcd-io/etcd/security/advisories/GHSA-xg4h-6gfc-h4m8)

### Package `clientv3`

- Fix [unsynchronized range over leaseCache.entries](https://github.com/etcd-io/etcd/pull/22149)

### package `client/pkg/v3`

- [Set a tlsHandshakeTimeout for tlsListener](https://github.com/etcd-io/etcd/pull/22141). Refer to [GHSA-6vch-q96h-7gc3](https://github.com/etcd-io/etcd/security/advisories/GHSA-6vch-q96h-7gc3) for more details.

### Dependencies

- [Bump google.golang.org/grpc to 1.82.1 to address GHSA-hrxh-6v49-42gf](https://github.com/etcd-io/etcd/pull/22157)

---

## v3.7.0 (2026-07-08)

### etcd server

- Fix [CRL enforcement bypass on gRPC listener when `--listen-client-http-urls` is configured](https://github.com/etcd-io/etcd/pull/22024), refer to [security/advisories/GHSA-3wh4-j44w-pg92](https://github.com/etcd-io/etcd/security/advisories/GHSA-3wh4-j44w-pg92) for more details.
- Fix [websocket authentication with bearer-prefixed auth tokens](https://github.com/etcd-io/etcd/pull/21929).

### Package `clientv3`

- [Make the etcd client creation non-blocking](https://github.com/etcd-io/etcd/pull/21942): etcd no longer honors the deprecated `grpc.WithBlock` dial option. To preserve the previous blocking behavior when needed, follow the guidance in grpc-go's [anti-patterns documentation](https://github.com/grpc/grpc-go/blob/master/Documentation/anti-patterns.md#especially-bad-using-deprecated-dialoptions).

### Dependencies

- Compile binaries using [go 1.26.5](https://github.com/etcd-io/etcd/pull/22058).
- Bump golang.org/x/crypto to [v0.52.0](https://github.com/etcd-io/etcd/pull/21903) to resolve CVE-2026-46598, CVE-2026-39835, CVE-2026-39828 and CVE-2026-46597.
- Bump go.etcd.io/raft/v3 from 3.7.0-rc.1 to [v3.7.0](https://github.com/etcd-io/etcd/pull/22008).
- Bump go.etcd.io/bbolt from 1.5.0-rc.0 to [v1.5.0](https://github.com/etcd-io/etcd/pull/22008).

---

## v3.7.0-rc.0 (2026-06-01)

### Breaking Changes

- [Removed all deprecated experimental flags](https://github.com/etcd-io/etcd/pull/19959)
- [Removed v2discovery](https://github.com/etcd-io/etcd/pull/20109)
- [Removed client/v2](https://github.com/etcd-io/etcd/pull/20117)
- [Removed v2 request and apply_v2.go](https://github.com/etcd-io/etcd/pull/21263)
- We no longer [release individual architecture Docker image tags](https://github.com/etcd-io/etcd/pull/21840). Please use the multi-arch manifest.

### etcd server

- [Prioritize LeaseRevoke requests to ensure timely lease expiration during overload conditions](https://github.com/etcd-io/etcd/pull/20492)
- [Update go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc to v0.61.0 and replaced the deprecated `UnaryServerInterceptor` and `StreamServerInterceptor` with `NewServerHandler`](https://github.com/etcd-io/etcd/pull/20017)
- [Add Support for Unix Socket endpoints](https://github.com/etcd-io/etcd/pull/19760)
- [Improves performance of lease and user/role operations (up to 2x) by updating `(*readView) Rev()` to use `SharedBufReadTxMode`](https://github.com/etcd-io/etcd/pull/20411)
- [Allow client to retrieve AuthStatus without authentication](https://github.com/etcd-io/etcd/pull/20802)
- [Add FastLeaseKeepAlive feature to enable faster lease renewal by skipping the wait for the applied index](https://github.com/etcd-io/etcd/pull/20589)
- [Bootstrap etcd from v3store](https://github.com/etcd-io/etcd/issues/20187), see changes below,
  - [Stop loading v2 snapshot files](https://github.com/etcd-io/etcd/pull/21107)
  - [Initialize confState from v3 store on bootstrap](https://github.com/etcd-io/etcd/pull/21138)
  - [Remove flag `--max-snapshots` in 3.8 rather than 3.7](https://github.com/etcd-io/etcd/pull/21160)
  - [Keep the `--snapshot-count` flag](https://github.com/etcd-io/etcd/pull/21162)
- [Fix data inconsistency when a transaction includes a range request with a specified revision](https://github.com/etcd-io/etcd/pull/21432)
- [Improve performance for key-only range operations by retrieving data exclusively from in-memory indices when results are not sorted by value](https://github.com/etcd-io/etcd/pull/21791)

### Package `clientv3`

- Allow setting JWT directly by users, see <https://github.com/etcd-io/etcd/pull/16803> and <https://github.com/etcd-io/etcd/pull/20747>.
- [Function etcdClientDebugLevel is renamed to ClientLogLevel and made it public](https://github.com/etcd-io/etcd/pull/20006)

### Package `pkg`

- [Optimize find performance by splitting intervals with the same left endpoint by their right endpoints](https://github.com/etcd-io/etcd/pull/19768)
- [netutil: Refactor IPv6 address comparison logic](https://github.com/etcd-io/etcd/pull/20365)

### Dependencies

- Compile binaries with [Go 1.26](https://go.dev/doc/devel/release#go1.26.minor).
- [Migrate the deprecated go-grpc-middleware v1 logging and tags libraries to v2 interceptors](https://github.com/etcd-io/etcd/pull/20420)
- [Migrate from gogo/protobuf to standard google.golang.org/protobuf](https://github.com/etcd-io/etcd/issues/14533)

### Deprecations

- Deprecated [UsageFunc in pkg/cobrautl](https://github.com/etcd-io/etcd/pull/18356).

### etcdctl

- [Organize etcdctl commands](https://github.com/etcd-io/etcd/pull/20162) to make them more concise and easier to understand.
- [Hide the global flags](https://github.com/etcd-io/etcd/pull/20493) to make the output of `etcdctl --help` looks cleaner and is consistent with kubectl.

### etcdutl

- [Add a timeout flag to all etcdutl commands](https://github.com/etcd-io/etcd/pull/20708) when waiting to acquire a file lock on the database file.

### Metrics, Monitoring

See [List of metrics](https://etcd.io/docs/latest/metrics/) for all metrics per release.

- Add [`etcd_server_request_duration_seconds`](https://github.com/etcd-io/etcd/pull/21038).
- Add [the following metrics related to watch send loop](https://github.com/etcd-io/etcd/pull/21030),
  - etcd_debugging_server_watch_send_loop_watch_stream_duration_seconds
  - etcd_debugging_server_watch_send_loop_watch_stream_duration_per_event_seconds
  - etcd_debugging_server_watch_send_loop_control_stream_duration_seconds
  - etcd_debugging_server_watch_send_loop_progress_duration_seconds
