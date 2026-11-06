Previous change logs can be found at [CHANGELOG-3.7](https://github.com/etcd-io/etcd/blob/main/CHANGELOG/CHANGELOG-3.7.md).

---

## v3.8.0 (TBC)

### etcd server

- [Cleanup everything related to v2 snapshot](https://github.com/etcd-io/etcd/issues/20187), see notable changes below,
  - [Stop generating v2 snapshot files](https://github.com/etcd-io/etcd/pull/22263)
  - [Remove the periodic job of purging v2 snapshot files](https://github.com/etcd-io/etcd/pull/22271)
  - [Remove flag `--max-snapshots` and `--v2-deprecation`](https://github.com/etcd-io/etcd/pull/22306)
  - [Cleanup the legacy v2 snapshot files on bootstrap](https://github.com/etcd-io/etcd/pull/22336)
  - [Cleanup the legacy v2 snapshot source code and cleanup orphaned defragmentation files on bootstrap](https://github.com/etcd-io/etcd/pull/22341)
- [Add `LeaderId` (`leader_id`) to `ResponseHeader`, including headers for `DefragmentResponse`, `SnapshotResponse`, and `MoveLeaderResponse`](https://github.com/etcd-io/etcd/pull/22327).
- [Add `NonBlockingDefrag` feature gate to enable non-blocking backend defragmentation](https://github.com/etcd-io/etcd/pull/22425)

### Package `clientv3`

- [Emit a single resolver update in EtcdManualResolver.Build](https://github.com/etcd-io/etcd/pull/22133) to avoid spurious "operation was canceled" dial warnings.
- [Add a timeout for concurrent session creation.](https://github.com/etcd-io/etcd/pull/16385)

### Dependencies

- Move the systemd journal log writer (`logutil.NewJournalWriter`) from `client/pkg/v3` into `server/embed`, so the client packages no longer depend on `github.com/coreos/go-systemd/v22`.
- Compile binaries using [Go 1.27](https://go.dev/doc/devel/release#go1.27.minor).

### Deprecations

- Removed [NewJournalWriter in client/pkg/v3/logutil](https://github.com/etcd-io/etcd/pull/22502), so that `client/pkg/v3` no longer depends on `github.com/coreos/go-systemd/v22`. The function was deprecated in [v3.7.3](https://github.com/etcd-io/etcd/pull/22530).

### etcdutl

- [Add bbolt subcommand](https://github.com/etcd-io/etcd/pull/20162) to etcdutl

### Metrics, Monitoring

See [List of metrics](https://etcd.io/docs/latest/metrics/) for all metrics per release.

- Expose the full set of Go `runtime/metrics` on `/metrics` when `--metrics extensive` is set.
- Add [`etcd_disk_backend_defrag_blocking_duration_seconds`](https://github.com/etcd-io/etcd/pull/22425)
