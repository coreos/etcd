# etcdserver Exit Codes

This document lists the exit codes returned by etcdserver.

All current explicit exit points use **code 1** (generic failure). Code 0 is
returned implicitly when the process exits normally (clean shutdown path).

## Exit Code 0 — Success / Clean Shutdown

Returned implicitly when etcd exits cleanly. Not an explicit `os.Exit(0)` call.

Triggers:
- Process receives SIGTERM / SIGINT and shuts down gracefully.
- Server main goroutine returns nil.

## Exit Code 1 — Start-up / Runtime Errors

Every explicit `os.Exit(1)` call in the tree:

### `server/etcdmain/etcd.go`

| Line | Condition |
|------|----------|
| ~60  | `setupLoggingOrDie` / `setupCheckDataDirOrDie` / member-dir bootstrap failure during `startEtcdOrProxyMain` |
| ~69  | `startEtcdOrProxyMain` returned an unhandled error |

### `server/etcdmain/grpc_proxy.go`

The grpc-proxy path has roughly twenty `os.Exit(1)` call sites covering:
- Flag parsing failures (`parseProxyFlags`)
- Listener resolution / creation errors
- Proxy construction / start failures
- TLS / mTLS setup errors
- Client cert generation / loading failures
- Metrics handler registration failures
- HTTP server listen / serve failures
- Cluster boot, member init, snapshot bootstrap failures (V2 proxy)

### `server/etcdmain/main.go`

| Line | Condition |
|------|----------|
| ~34  | `setupLoggingOrDie` during `main()` when neither etcd nor proxy can initialise logging |

### Other exit paths

- `server/etcdserver/raft.go` — Raft node reportable halt (unrecoverable
  corruption or disk failure) triggers `log.Fatalf`, which maps to exit 1.
- `server/etcdserver/server.go` — `ReportUnreachable` / `reportError` panic
  paths also terminate with exit 1.

## Exit Code Summary

| Code | Meaning | Count |
|------|---------|-------|
| 0    | Success / clean shutdown | implicit |
| 1    | Generic start-up or runtime failure | 20+ explicit call sites |

## Notes

- There are **no** dedicated non-zero exit codes for specific failure modes
  (no `2 = config error`, `3 = data dir corrupt`, etc.). Everything that
  fails to start or hits a fatal runtime condition returns `1`.
- The Raft layer uses `log.Fatalf` which maps to exit code 1 as well.
- Exit codes in the proxy path (`grpc_proxy.go`) mirror the same convention.
- Issue #21032 tracks whether more granular exit codes should be introduced.
