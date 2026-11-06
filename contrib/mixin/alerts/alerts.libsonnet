{
  prometheusAlerts+:: {
    groups+: [
      {
        name: 'etcd',
        rules: [
          {
            alert: 'etcdMembersDown',
            expr: |||
              max without (endpoint) (
                sum without (%(etcd_instance_labels)s) (up{%(etcd_selector)s} == bool 0)
              or
                count without (To) (
                  sum without (%(etcd_instance_labels)s) (rate(etcd_network_peer_sent_failures_total{%(etcd_selector)s}[%(network_failure_range)ss])) > 0.01
                )
              )
              > 0
            ||| % { etcd_instance_labels: $._config.etcd_instance_labels, etcd_selector: $._config.etcd_selector, network_failure_range: $._config.scrape_interval_seconds * 4 },
            'for': '20m',
            labels: {
              severity: 'warning',
            },
            annotations: {
              description: 'etcd cluster "{{ $labels.%s }}": members are down ({{ $value }}).' % $._config.clusterLabel,
              summary: 'etcd cluster members are down.',
            },
          },
          {
            alert: 'etcdInsufficientMembers',
            expr: |||
              sum(up{%(etcd_selector)s} == bool 1) without (%(etcd_instance_labels)s) < ((count(up{%(etcd_selector)s}) without (%(etcd_instance_labels)s) + 1) / 2)
            ||| % $._config,
            'for': '3m',
            labels: {
              severity: 'critical',
            },
            annotations: {
              description: 'etcd cluster "{{ $labels.%s }}": insufficient members ({{ $value }}).' % $._config.clusterLabel,
              summary: 'etcd cluster has insufficient number of members.',
            },
          },
          {
            alert: 'etcdNoLeader',
            expr: |||
              etcd_server_has_leader{%(etcd_selector)s} == 0
            ||| % $._config,
            'for': '1m',
            labels: {
              severity: 'critical',
            },
            annotations: {
              description: 'etcd cluster "{{ $labels.%s }}": member {{ $labels.instance }} has no leader.' % $._config.clusterLabel,
              summary: 'etcd cluster has no leader.',
            },
          },
          {
            alert: 'etcdHighNumberOfLeaderChanges',
            expr: |||
              max without (%(etcd_instance_labels)s) (increase(etcd_server_leader_changes_seen_total{%(etcd_selector)s}[15m]) or 0*absent(etcd_server_leader_changes_seen_total{%(etcd_selector)s})) >= 4
            ||| % $._config,
            'for': '5m',
            labels: {
              severity: 'warning',
            },
            annotations: {
              description: 'etcd cluster "{{ $labels.%s }}": {{ $value }} leader changes within the last 15 minutes. Frequent elections may be a sign of insufficient resources, high network latency, or disruptions by other components and should be investigated.' % $._config.clusterLabel,
              summary: 'etcd cluster has high number of leader changes.',
            },
          },
          {
            alert: 'etcdHighNumberOfFailedGRPCRequests',
            expr: |||
              100 * sum(rate(grpc_server_handled_total{%(etcd_selector)s, grpc_code=~"Unknown|FailedPrecondition|ResourceExhausted|Internal|Unavailable|DataLoss|DeadlineExceeded"}[5m])) without (grpc_type, grpc_code)
                /
              sum(rate(grpc_server_handled_total{%(etcd_selector)s}[5m])) without (grpc_type, grpc_code)
                > 1
            ||| % $._config,
            'for': '10m',
            labels: {
              severity: 'warning',
            },
            annotations: {
              description: 'etcd cluster "{{ $labels.%s }}": {{ $value }}%% of requests for {{ $labels.grpc_method }} failed on etcd instance {{ $labels.instance }}.' % $._config.clusterLabel,
              summary: 'etcd cluster has high number of failed grpc requests.',
            },
          },
          {
            alert: 'etcdHighNumberOfFailedGRPCRequests',
            expr: |||
              100 * sum(rate(grpc_server_handled_total{%(etcd_selector)s, grpc_code=~"Unknown|FailedPrecondition|ResourceExhausted|Internal|Unavailable|DataLoss|DeadlineExceeded"}[5m])) without (grpc_type, grpc_code)
                /
              sum(rate(grpc_server_handled_total{%(etcd_selector)s}[5m])) without (grpc_type, grpc_code)
                > 5
            ||| % $._config,
            'for': '5m',
            labels: {
              severity: 'critical',
            },
            annotations: {
              description: 'etcd cluster "{{ $labels.%s }}": {{ $value }}%% of requests for {{ $labels.grpc_method }} failed on etcd instance {{ $labels.instance }}.' % $._config.clusterLabel,
              summary: 'etcd cluster has high number of failed grpc requests.',
            },
          },
          {
            alert: 'etcdGRPCRequestsSlow',
            expr: |||
              histogram_quantile(0.99, sum(rate(grpc_server_handling_seconds_bucket{%(etcd_selector)s, grpc_method!="Defragment", grpc_type="unary"}[5m])) without(grpc_type))
              > 0.15
            ||| % $._config,
            'for': '10m',
            labels: {
              severity: 'critical',
            },
            annotations: {
              description: 'etcd cluster "{{ $labels.%s }}": 99th percentile of gRPC requests is {{ $value }}s on etcd instance {{ $labels.instance }} for {{ $labels.grpc_method }} method.' % $._config.clusterLabel,
              summary: 'etcd grpc requests are slow',
            },
          },
          {
            alert: 'etcdMemberCommunicationSlow',
            expr: |||
              histogram_quantile(0.99, rate(etcd_network_peer_round_trip_time_seconds_bucket{%(etcd_selector)s}[5m]))
              > 0.15
            ||| % $._config,
            'for': '10m',
            labels: {
              severity: 'warning',
            },
            annotations: {
              description: 'etcd cluster "{{ $labels.%s }}": member communication with {{ $labels.To }} is taking {{ $value }}s on etcd instance {{ $labels.instance }}.' % $._config.clusterLabel,
              summary: 'etcd cluster member communication is slow.',
            },
          },
          {
            alert: 'etcdHighNumberOfFailedProposals',
            expr: |||
              rate(etcd_server_proposals_failed_total{%(etcd_selector)s}[15m]) > 5
            ||| % $._config,
            'for': '15m',
            labels: {
              severity: 'warning',
            },
            annotations: {
              description: 'etcd cluster "{{ $labels.%s }}": {{ $value }} proposal failures within the last 15 minutes on etcd instance {{ $labels.instance }}.' % $._config.clusterLabel,
              summary: 'etcd cluster has high number of proposal failures.',
            },
          },
          {
            alert: 'etcdHighFsyncDurations',
            expr: |||
              histogram_quantile(0.99, rate(etcd_disk_wal_fsync_duration_seconds_bucket{%(etcd_selector)s}[5m]))
              > 0.5
            ||| % $._config,
            'for': '10m',
            labels: {
              severity: 'warning',
            },
            annotations: {
              description: 'etcd cluster "{{ $labels.%s }}": 99th percentile fsync durations are {{ $value }}s on etcd instance {{ $labels.instance }}.' % $._config.clusterLabel,
              summary: 'etcd cluster 99th percentile fsync durations are too high.',
            },
          },
          {
            alert: 'etcdHighFsyncDurations',
            expr: |||
              histogram_quantile(0.99, rate(etcd_disk_wal_fsync_duration_seconds_bucket{%(etcd_selector)s}[5m]))
              > 1
            ||| % $._config,
            'for': '10m',
            labels: {
              severity: 'critical',
            },
            annotations: {
              description: 'etcd cluster "{{ $labels.%s }}": 99th percentile fsync durations are {{ $value }}s on etcd instance {{ $labels.instance }}.' % $._config.clusterLabel,
              summary: 'etcd cluster 99th percentile fsync durations are too high.',
            },
          },
          {
            alert: 'etcdHighCommitDurations',
            expr: |||
              histogram_quantile(0.99, rate(etcd_disk_backend_commit_duration_seconds_bucket{%(etcd_selector)s}[5m]))
              > 0.25
            ||| % $._config,
            'for': '10m',
            labels: {
              severity: 'warning',
            },
            annotations: {
              description: 'etcd cluster "{{ $labels.%s }}": 99th percentile commit durations {{ $value }}s on etcd instance {{ $labels.instance }}.' % $._config.clusterLabel,
              summary: 'etcd cluster 99th percentile commit durations are too high.',
            },
          },
          {
            alert: 'etcdDatabaseQuotaLowSpace',
            expr: |||
              (last_over_time(etcd_mvcc_db_total_size_in_bytes{%(etcd_selector)s}[5m]) / last_over_time(etcd_server_quota_backend_bytes{%(etcd_selector)s}[5m]))*100 > 95
            ||| % $._config,
            'for': '10m',
            labels: {
              severity: 'critical',
            },
            annotations: {
              description: 'etcd cluster "{{ $labels.%s }}": database size exceeds the defined quota on etcd instance {{ $labels.instance }}, please defrag or increase the quota as the writes to etcd will be disabled when it is full.' % $._config.clusterLabel,
              summary: 'etcd cluster database is running full.',
            },
          },
          {
            alert: 'etcdExcessiveDatabaseGrowth',
            expr: |||
              predict_linear(etcd_mvcc_db_total_size_in_bytes{%(etcd_selector)s}[4h], 4*60*60) > etcd_server_quota_backend_bytes{%(etcd_selector)s}
            ||| % $._config,
            'for': '10m',
            labels: {
              severity: 'warning',
            },
            annotations: {
              description: 'etcd cluster "{{ $labels.%s }}": Predicting running out of disk space in the next four hours, based on write observations within the past four hours on etcd instance {{ $labels.instance }}, please check as it might be disruptive.' % $._config.clusterLabel,
              summary: 'etcd cluster database growing very fast.',
            },
          },
          {
            alert: 'etcdDatabaseHighFragmentationRatio',
            expr: |||
              (last_over_time(etcd_mvcc_db_total_size_in_use_in_bytes{%(etcd_selector)s}[5m]) / last_over_time(etcd_mvcc_db_total_size_in_bytes{%(etcd_selector)s}[5m])) < 0.5 and etcd_mvcc_db_total_size_in_use_in_bytes{%(etcd_selector)s} > 104857600
            ||| % $._config,
            'for': '10m',
            labels: {
              severity: 'warning',
            },
            annotations: {
              description: 'etcd cluster "{{ $labels.%s }}": database size in use on instance {{ $labels.instance }} is {{ $value | humanizePercentage }} of the actual allocated disk space, please run defragmentation (e.g. etcdctl defrag) to retrieve the unused fragmented disk space.' % $._config.clusterLabel,
              summary: 'etcd database size in use is less than 50% of the actual allocated storage.',
              runbook_url: 'https://etcd.io/docs/v3.5/op-guide/maintenance/#defragmentation',
            },
          },
        ],
      },
    ],
  },
}
