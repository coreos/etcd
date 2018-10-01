{
  _config+:: {
    etcd_selector: 'job=~".*etcd.*"',
  },

  prometheusAlerts+:: {
    groups+: [
      {
        name: 'etcd',
        rules: [
          {
            alert: 'EtcdInsufficientMembers',
            expr: |||
              count(up{%(etcd_selector)s} == 0) by (job) > (count(up{%(etcd_selector)s}) by (job) / 2 - 1)
            ||| % $._config,
            'for': '3m',
            labels: {
              severity: 'critical',
            },
            annotations: {
              message: 'Etcd cluster "{{ $labels.job }}": insufficient members ({{ $value }}).',
            },
          },
          {
            alert: 'EtcdNoLeader',
            expr: |||
              etcd_server_has_leader{%(etcd_selector)s} == 0
            ||| % $._config,
            'for': '1m',
            labels: {
              severity: 'critical',
            },
            annotations: {
              message: 'Etcd cluster "{{ $labels.job }}": member {{ $labels.instance }} has no leader.',
            },
          },
          {
            alert: 'EtcdHighNumberOfLeaderChanges',
            expr: |||
              rate(etcd_server_leader_changes_seen_total{%(etcd_selector)s}[15m]) > 3
            ||| % $._config,
            'for': '15m',
            labels: {
              severity: 'warning',
            },
            annotations: {
              message: 'Etcd cluster "{{ $labels.job }}": instance {{ $labels.instance }} has seen {{ $value }} leader changes within the last hour.',
            },
          },
          {
            alert: 'EtcdHighNumberOfFailedGRPCRequests',
            expr: |||
              100 * sum(rate(grpc_server_handled_total{%(etcd_selector)s, grpc_code!="OK"}[5m])) BY (job, instance, grpc_service, grpc_method)
                /
              sum(rate(grpc_server_handled_total{%(etcd_selector)s}[5m])) BY (job, instance, grpc_service, grpc_method)
                > 1
            ||| % $._config,
            'for': '10m',
            labels: {
              severity: 'warning',
            },
            annotations: {
              message: 'Etcd cluster "{{ $labels.job }}": {{ $value }}% of requests for {{ $labels.grpc_method }} failed on etcd instance {{ $labels.instance }}.',
            },
          },
          {
            alert: 'EtcdHighNumberOfFailedGRPCRequests',
            expr: |||
              100 * sum(rate(grpc_server_handled_total{%(etcd_selector)s, grpc_code!="OK"}[5m])) BY (job, instance, grpc_service, grpc_method)
                /
              sum(rate(grpc_server_handled_total{%(etcd_selector)s}[5m])) BY (job, instance, grpc_service, grpc_method)
                > 5
            ||| % $._config,
            'for': '5m',
            labels: {
              severity: 'critical',
            },
            annotations: {
              message: 'Etcd cluster "{{ $labels.job }}": {{ $value }}% of requests for {{ $labels.grpc_method }} failed on etcd instance {{ $labels.instance }}.',
            },
          },
          {
            alert: 'EtcdGRPCRequestsSlow',
            expr: |||
              histogram_quantile(0.99, sum(rate(grpc_server_handling_seconds_bucket{%(etcd_selector)s, grpc_type="unary"}[5m])) by (job, instance, grpc_service, grpc_method, le))
              > 0.15
            ||| % $._config,
            'for': '10m',
            labels: {
              severity: 'critical',
            },
            annotations: {
              message: 'Etcd cluster "{{ $labels.job }}": gRPC requests to {{ $labels.grpc_method }} are taking {{ $value }}s on etcd instance {{ $labels.instance }}.',
            },
          },
          {
            alert: 'EtcdMemberCommunicationSlow',
            expr: |||
              histogram_quantile(0.99, rate(etcd_network_peer_round_trip_time_seconds_bucket{%(etcd_selector)s}[5m]))
              > 0.15
            ||| % $._config,
            'for': '10m',
            labels: {
              severity: 'warning',
            },
            annotations: {
              message: 'Etcd cluster "{{ $labels.job }}": member communication with {{ $labels.To }} is taking {{ $value }}s on etcd instance {{ $labels.instance }}.',
            },
          },
          {
            alert: 'EtcdHighNumberOfFailedProposals',
            expr: |||
              rate(etcd_server_proposals_failed_total{%(etcd_selector)s}[15m]) > 5
            ||| % $._config,
            'for': '15m',
            labels: {
              severity: 'warning',
            },
            annotations: {
              message: 'Etcd cluster "{{ $labels.job }}": {{ $value }} proposal failures within the last hour on etcd instance {{ $labels.instance }}.',
            },
          },
          {
            alert: 'EtcdHighFsyncDurations',
            expr: |||
              histogram_quantile(0.99, rate(etcd_disk_wal_fsync_duration_seconds_bucket{%(etcd_selector)s}[5m]))
              > 0.5
            ||| % $._config,
            'for': '10m',
            labels: {
              severity: 'warning',
            },
            annotations: {
              message: 'Etcd cluster "{{ $labels.job }}": 99th percentile fync durations are {{ $value }}s on etcd instance {{ $labels.instance }}.',
            },
          },
          {
            alert: 'EtcdHighCommitDurations',
            expr: |||
              histogram_quantile(0.99, rate(etcd_disk_backend_commit_duration_seconds_bucket{%(etcd_selector)s}[5m]))
              > 0.25
            ||| % $._config,
            'for': '10m',
            labels: {
              severity: 'warning',
            },
            annotations: {
              message: 'Etcd cluster "{{ $labels.job }}": 99th percentile commit durations {{ $value }}s on etcd instance {{ $labels.instance }}.',
            },
          },
          {
            record: 'instance:fd_utilization',
            expr: 'process_open_fds / process_max_fds',
          },
          {
            alert: 'FdExhaustionClose',
            expr: |||
              predict_linear(instance:fd_utilization{%(etcd_selector)s}[1h], 3600 * 4) > 1
            ||| % $._config,
            'for': '10m',
            labels: {
              severity: 'warning',
            },
            annotations: {
              message: '{{ $labels.job }} instance {{ $labels.instance }} will exhaust its file descriptors soon',
            },
          },
          {
            alert: 'FdExhaustionClose',
            expr: |||
              predict_linear(instance:fd_utilization{%(etcd_selector)s}[10m], 3600) > 1
            ||| % $._config,
            'for': '10m',
            labels: {
              severity: 'critical',
            },
            annotations: {
              description: '{{ $labels.job }} instance {{ $labels.instance }} will exhaust its file descriptors soon',
            },
          },
        ],
      },
    ],
  },

  grafanaDashboards+:: {
    'etcd.json': {
      id: 6,
      title: 'etcd',
      description: 'etcd sample Grafana dashboard with Prometheus',
      tags: [],
      style: 'dark',
      timezone: 'browser',
      editable: true,
      hideControls: false,
      sharedCrosshair: false,
      rows: [
        {
          collapse: false,
          editable: true,
          height: '250px',
          panels: [
            {
              cacheTimeout: null,
              colorBackground: false,
              colorValue: false,
              colors: [
                'rgba(245, 54, 54, 0.9)',
                'rgba(237, 129, 40, 0.89)',
                'rgba(50, 172, 45, 0.97)',
              ],
              datasource: '$datasource',
              editable: true,
              'error': false,
              format: 'none',
              gauge: {
                maxValue: 100,
                minValue: 0,
                show: false,
                thresholdLabels: false,
                thresholdMarkers: true,
              },
              id: 28,
              interval: null,
              isNew: true,
              links: [],
              mappingType: 1,
              mappingTypes: [
                {
                  name: 'value to text',
                  value: 1,
                },
                {
                  name: 'range to text',
                  value: 2,
                },
              ],
              maxDataPoints: 100,
              nullPointMode: 'connected',
              nullText: null,
              postfix: '',
              postfixFontSize: '50%',
              prefix: '',
              prefixFontSize: '50%',
              rangeMaps: [{
                from: 'null',
                text: 'N/A',
                to: 'null',
              }],
              span: 3,
              sparkline: {
                fillColor: 'rgba(31, 118, 189, 0.18)',
                full: false,
                lineColor: 'rgb(31, 120, 193)',
                show: false,
              },
              targets: [{
                expr: 'sum(etcd_server_has_leader{job="$cluster"})',
                intervalFactor: 2,
                legendFormat: '',
                metric: 'etcd_server_has_leader',
                refId: 'A',
                step: 20,
              }],
              thresholds: '',
              title: 'Up',
              type: 'singlestat',
              valueFontSize: '200%',
              valueMaps: [{
                op: '=',
                text: 'N/A',
                value: 'null',
              }],
              valueName: 'avg',
            },
            {
              aliasColors: {},
              bars: false,
              datasource: '$datasource',
              editable: true,
              'error': false,
              fill: 0,
              id: 23,
              isNew: true,
              legend: {
                avg: false,
                current: false,
                max: false,
                min: false,
                show: false,
                total: false,
                values: false,
              },
              lines: true,
              linewidth: 2,
              links: [],
              nullPointMode: 'connected',
              percentage: false,
              pointradius: 5,
              points: false,
              renderer: 'flot',
              seriesOverrides: [],
              span: 5,
              stack: false,
              steppedLine: false,
              targets: [
                {
                  expr: 'sum(rate(grpc_server_started_total{job="$cluster",grpc_type="unary"}[5m]))',
                  format: 'time_series',
                  intervalFactor: 2,
                  legendFormat: 'RPC Rate',
                  metric: 'grpc_server_started_total',
                  refId: 'A',
                  step: 2,
                },
                {
                  expr: 'sum(rate(grpc_server_handled_total{job="$cluster",grpc_type="unary",grpc_code!="OK"}[5m]))',
                  format: 'time_series',
                  intervalFactor: 2,
                  legendFormat: 'RPC Failed Rate',
                  metric: 'grpc_server_handled_total',
                  refId: 'B',
                  step: 2,
                },
              ],
              thresholds: [],
              timeFrom: null,
              timeShift: null,
              title: 'RPC Rate',
              tooltip: {
                msResolution: false,
                shared: true,
                sort: 0,
                value_type: 'individual',
              },
              type: 'graph',
              xaxis: {
                mode: 'time',
                name: null,
                show: true,
                values: [],
              },
              yaxes: [
                {
                  format: 'ops',
                  label: null,
                  logBase: 1,
                  max: null,
                  min: null,
                  show: true,
                },
                {
                  format: 'short',
                  label: null,
                  logBase: 1,
                  max: null,
                  min: null,
                  show: true,
                },
              ],
            },
            {
              aliasColors: {},
              bars: false,
              datasource: '$datasource',
              editable: true,
              'error': false,
              fill: 0,
              id: 41,
              isNew: true,
              legend: {
                avg: false,
                current: false,
                max: false,
                min: false,
                show: false,
                total: false,
                values: false,
              },
              lines: true,
              linewidth: 2,
              links: [],
              nullPointMode: 'connected',
              percentage: false,
              pointradius: 5,
              points: false,
              renderer: 'flot',
              seriesOverrides: [],
              span: 4,
              stack: true,
              steppedLine: false,
              targets: [
                {
                  expr: 'sum(grpc_server_started_total{job="$cluster",grpc_service="etcdserverpb.Watch",grpc_type="bidi_stream"}) - sum(grpc_server_handled_total{job="$cluster",grpc_service="etcdserverpb.Watch",grpc_type="bidi_stream"})',
                  intervalFactor: 2,
                  legendFormat: 'Watch Streams',
                  metric: 'grpc_server_handled_total',
                  refId: 'A',
                  step: 4,
                },
                {
                  expr: 'sum(grpc_server_started_total{job="$cluster",grpc_service="etcdserverpb.Lease",grpc_type="bidi_stream"}) - sum(grpc_server_handled_total{job="$cluster",grpc_service="etcdserverpb.Lease",grpc_type="bidi_stream"})',
                  intervalFactor: 2,
                  legendFormat: 'Lease Streams',
                  metric: 'grpc_server_handled_total',
                  refId: 'B',
                  step: 4,
                },
              ],
              thresholds: [],
              timeFrom: null,
              timeShift: null,
              title: 'Active Streams',
              tooltip: {
                msResolution: false,
                shared: true,
                sort: 0,
                value_type: 'individual',
              },
              type: 'graph',
              xaxis: {
                mode: 'time',
                name: null,
                show: true,
                values: [],
              },
              yaxes: [
                {
                  format: 'short',
                  label: '',
                  logBase: 1,
                  max: null,
                  min: null,
                  show: true,
                },
                {
                  format: 'short',
                  label: null,
                  logBase: 1,
                  max: null,
                  min: null,
                  show: true,
                },
              ],
            },
          ],
          showTitle: false,
          title: 'Row',
        },
        {
          collapse: false,
          editable: true,
          height: '250px',
          panels: [
            {
              aliasColors: {},
              bars: false,
              datasource: '$datasource',
              decimals: null,
              editable: true,
              'error': false,
              fill: 0,
              grid: {},
              id: 1,
              legend: {
                avg: false,
                current: false,
                max: false,
                min: false,
                show: false,
                total: false,
                values: false,
              },
              lines: true,
              linewidth: 2,
              links: [],
              nullPointMode: 'connected',
              percentage: false,
              pointradius: 5,
              points: false,
              renderer: 'flot',
              seriesOverrides: [],
              span: 4,
              stack: false,
              steppedLine: false,
              targets: [{
                expr: 'etcd_debugging_mvcc_db_total_size_in_bytes{job="$cluster"}',
                hide: false,
                interval: '',
                intervalFactor: 2,
                legendFormat: '{{instance}} DB Size',
                metric: '',
                refId: 'A',
                step: 4,
              }],
              thresholds: [],
              timeFrom: null,
              timeShift: null,
              title: 'DB Size',
              tooltip: {
                msResolution: false,
                shared: true,
                sort: 0,
                value_type: 'cumulative',
              },
              type: 'graph',
              xaxis: {
                mode: 'time',
                name: null,
                show: true,
                values: [],
              },
              yaxes: [
                {
                  format: 'bytes',
                  logBase: 1,
                  max: null,
                  min: null,
                  show: true,
                },
                {
                  format: 'short',
                  logBase: 1,
                  max: null,
                  min: null,
                  show: false,
                },
              ],
            },
            {
              aliasColors: {},
              bars: false,
              datasource: '$datasource',
              editable: true,
              'error': false,
              fill: 0,
              grid: {},
              id: 3,
              legend: {
                avg: false,
                current: false,
                max: false,
                min: false,
                show: false,
                total: false,
                values: false,
              },
              lines: true,
              linewidth: 2,
              links: [],
              nullPointMode: 'connected',
              percentage: false,
              pointradius: 1,
              points: false,
              renderer: 'flot',
              seriesOverrides: [],
              span: 4,
              stack: false,
              steppedLine: true,
              targets: [
                {
                  expr: 'histogram_quantile(0.99, sum(rate(etcd_disk_wal_fsync_duration_seconds_bucket{job="$cluster"}[5m])) by (instance, le))',
                  hide: false,
                  intervalFactor: 2,
                  legendFormat: '{{instance}} WAL fsync',
                  metric: 'etcd_disk_wal_fsync_duration_seconds_bucket',
                  refId: 'A',
                  step: 4,
                },
                {
                  expr: 'histogram_quantile(0.99, sum(rate(etcd_disk_backend_commit_duration_seconds_bucket{job="$cluster"}[5m])) by (instance, le))',
                  intervalFactor: 2,
                  legendFormat: '{{instance}} DB fsync',
                  metric: 'etcd_disk_backend_commit_duration_seconds_bucket',
                  refId: 'B',
                  step: 4,
                },
              ],
              thresholds: [],
              timeFrom: null,
              timeShift: null,
              title: 'Disk Sync Duration',
              tooltip: {
                msResolution: false,
                shared: true,
                sort: 0,
                value_type: 'cumulative',
              },
              type: 'graph',
              xaxis: {
                mode: 'time',
                name: null,
                show: true,
                values: [],
              },
              yaxes: [
                {
                  format: 's',
                  logBase: 1,
                  max: null,
                  min: null,
                  show: true,
                },
                {
                  format: 'short',
                  logBase: 1,
                  max: null,
                  min: null,
                  show: false,
                },
              ],
            },
            {
              aliasColors: {},
              bars: false,
              datasource: '$datasource',
              editable: true,
              'error': false,
              fill: 0,
              id: 29,
              isNew: true,
              legend: {
                avg: false,
                current: false,
                max: false,
                min: false,
                show: false,
                total: false,
                values: false,
              },
              lines: true,
              linewidth: 2,
              links: [],
              nullPointMode: 'connected',
              percentage: false,
              pointradius: 5,
              points: false,
              renderer: 'flot',
              seriesOverrides: [],
              span: 4,
              stack: false,
              steppedLine: false,
              targets: [{
                expr: 'process_resident_memory_bytes{job="$cluster"}',
                intervalFactor: 2,
                legendFormat: '{{instance}} Resident Memory',
                metric: 'process_resident_memory_bytes',
                refId: 'A',
                step: 4,
              }],
              thresholds: [],
              timeFrom: null,
              timeShift: null,
              title: 'Memory',
              tooltip: {
                msResolution: false,
                shared: true,
                sort: 0,
                value_type: 'individual',
              },
              type: 'graph',
              xaxis: {
                mode: 'time',
                name: null,
                show: true,
                values: [],
              },
              yaxes: [
                {
                  format: 'bytes',
                  label: null,
                  logBase: 1,
                  max: null,
                  min: null,
                  show: true,
                },
                {
                  format: 'short',
                  label: null,
                  logBase: 1,
                  max: null,
                  min: null,
                  show: true,
                },
              ],
            },
          ],
          title: 'New row',
        },
        {
          collapse: false,
          editable: true,
          height: '250px',
          panels: [
            {
              aliasColors: {},
              bars: false,
              datasource: '$datasource',
              editable: true,
              'error': false,
              fill: 5,
              id: 22,
              isNew: true,
              legend: {
                avg: false,
                current: false,
                max: false,
                min: false,
                show: false,
                total: false,
                values: false,
              },
              lines: true,
              linewidth: 2,
              links: [],
              nullPointMode: 'connected',
              percentage: false,
              pointradius: 5,
              points: false,
              renderer: 'flot',
              seriesOverrides: [],
              span: 3,
              stack: true,
              steppedLine: false,
              targets: [{
                expr: 'rate(etcd_network_client_grpc_received_bytes_total{job="$cluster"}[5m])',
                intervalFactor: 2,
                legendFormat: '{{instance}} Client Traffic In',
                metric: 'etcd_network_client_grpc_received_bytes_total',
                refId: 'A',
                step: 4,
              }],
              thresholds: [],
              timeFrom: null,
              timeShift: null,
              title: 'Client Traffic In',
              tooltip: {
                msResolution: false,
                shared: true,
                sort: 0,
                value_type: 'individual',
              },
              type: 'graph',
              xaxis: {
                mode: 'time',
                name: null,
                show: true,
                values: [],
              },
              yaxes: [
                {
                  format: 'Bps',
                  label: null,
                  logBase: 1,
                  max: null,
                  min: null,
                  show: true,
                },
                {
                  format: 'short',
                  label: null,
                  logBase: 1,
                  max: null,
                  min: null,
                  show: true,
                },
              ],
            },
            {
              aliasColors: {},
              bars: false,
              datasource: '$datasource',
              editable: true,
              'error': false,
              fill: 5,
              id: 21,
              isNew: true,
              legend: {
                avg: false,
                current: false,
                max: false,
                min: false,
                show: false,
                total: false,
                values: false,
              },
              lines: true,
              linewidth: 2,
              links: [],
              nullPointMode: 'connected',
              percentage: false,
              pointradius: 5,
              points: false,
              renderer: 'flot',
              seriesOverrides: [],
              span: 3,
              stack: true,
              steppedLine: false,
              targets: [{
                expr: 'rate(etcd_network_client_grpc_sent_bytes_total{job="$cluster"}[5m])',
                intervalFactor: 2,
                legendFormat: '{{instance}} Client Traffic Out',
                metric: 'etcd_network_client_grpc_sent_bytes_total',
                refId: 'A',
                step: 4,
              }],
              thresholds: [],
              timeFrom: null,
              timeShift: null,
              title: 'Client Traffic Out',
              tooltip: {
                msResolution: false,
                shared: true,
                sort: 0,
                value_type: 'individual',
              },
              type: 'graph',
              xaxis: {
                mode: 'time',
                name: null,
                show: true,
                values: [],
              },
              yaxes: [
                {
                  format: 'Bps',
                  label: null,
                  logBase: 1,
                  max: null,
                  min: null,
                  show: true,
                },
                {
                  format: 'short',
                  label: null,
                  logBase: 1,
                  max: null,
                  min: null,
                  show: true,
                },
              ],
            },
            {
              aliasColors: {},
              bars: false,
              datasource: '$datasource',
              editable: true,
              'error': false,
              fill: 0,
              id: 20,
              isNew: true,
              legend: {
                avg: false,
                current: false,
                max: false,
                min: false,
                show: false,
                total: false,
                values: false,
              },
              lines: true,
              linewidth: 2,
              links: [],
              nullPointMode: 'connected',
              percentage: false,
              pointradius: 5,
              points: false,
              renderer: 'flot',
              seriesOverrides: [],
              span: 3,
              stack: false,
              steppedLine: false,
              targets: [{
                expr: 'sum(rate(etcd_network_peer_received_bytes_total{job="$cluster"}[5m])) by (instance)',
                intervalFactor: 2,
                legendFormat: '{{instance}} Peer Traffic In',
                metric: 'etcd_network_peer_received_bytes_total',
                refId: 'A',
                step: 4,
              }],
              thresholds: [],
              timeFrom: null,
              timeShift: null,
              title: 'Peer Traffic In',
              tooltip: {
                msResolution: false,
                shared: true,
                sort: 0,
                value_type: 'individual',
              },
              type: 'graph',
              xaxis: {
                mode: 'time',
                name: null,
                show: true,
                values: [],
              },
              yaxes: [
                {
                  format: 'Bps',
                  label: null,
                  logBase: 1,
                  max: null,
                  min: null,
                  show: true,
                },
                {
                  format: 'short',
                  label: null,
                  logBase: 1,
                  max: null,
                  min: null,
                  show: true,
                },
              ],
            },
            {
              aliasColors: {},
              bars: false,
              datasource: '$datasource',
              decimals: null,
              editable: true,
              'error': false,
              fill: 0,
              grid: {},
              id: 16,
              legend: {
                avg: false,
                current: false,
                max: false,
                min: false,
                show: false,
                total: false,
                values: false,
              },
              lines: true,
              linewidth: 2,
              links: [],
              nullPointMode: 'connected',
              percentage: false,
              pointradius: 5,
              points: false,
              renderer: 'flot',
              seriesOverrides: [],
              span: 3,
              stack: false,
              steppedLine: false,
              targets: [{
                expr: 'sum(rate(etcd_network_peer_sent_bytes_total{job="$cluster"}[5m])) by (instance)',
                hide: false,
                interval: '',
                intervalFactor: 2,
                legendFormat: '{{instance}} Peer Traffic Out',
                metric: 'etcd_network_peer_sent_bytes_total',
                refId: 'A',
                step: 4,
              }],
              thresholds: [],
              timeFrom: null,
              timeShift: null,
              title: 'Peer Traffic Out',
              tooltip: {
                msResolution: false,
                shared: true,
                sort: 0,
                value_type: 'cumulative',
              },
              type: 'graph',
              xaxis: {
                mode: 'time',
                name: null,
                show: true,
                values: [],
              },
              yaxes: [
                {
                  format: 'Bps',
                  logBase: 1,
                  max: null,
                  min: null,
                  show: true,
                },
                {
                  format: 'short',
                  logBase: 1,
                  max: null,
                  min: null,
                  show: true,
                },
              ],
            },
          ],
          title: 'New row',
        },
        {
          collapse: false,
          editable: true,
          height: '250px',
          panels: [
            {
              aliasColors: {},
              bars: false,
              datasource: '$datasource',
              editable: true,
              'error': false,
              fill: 0,
              id: 40,
              isNew: true,
              legend: {
                avg: false,
                current: false,
                max: false,
                min: false,
                show: false,
                total: false,
                values: false,
              },
              lines: true,
              linewidth: 2,
              links: [],
              nullPointMode: 'connected',
              percentage: false,
              pointradius: 5,
              points: false,
              renderer: 'flot',
              seriesOverrides: [],
              span: 6,
              stack: false,
              steppedLine: false,
              targets: [
                {
                  expr: 'sum(rate(etcd_server_proposals_failed_total{job="$cluster"}[5m]))',
                  intervalFactor: 2,
                  legendFormat: 'Proposal Failure Rate',
                  metric: 'etcd_server_proposals_failed_total',
                  refId: 'A',
                  step: 2,
                },
                {
                  expr: 'sum(etcd_server_proposals_pending{job="$cluster"})',
                  intervalFactor: 2,
                  legendFormat: 'Proposal Pending Total',
                  metric: 'etcd_server_proposals_pending',
                  refId: 'B',
                  step: 2,
                },
                {
                  expr: 'sum(rate(etcd_server_proposals_committed_total{job="$cluster"}[5m]))',
                  intervalFactor: 2,
                  legendFormat: 'Proposal Commit Rate',
                  metric: 'etcd_server_proposals_committed_total',
                  refId: 'C',
                  step: 2,
                },
                {
                  expr: 'sum(rate(etcd_server_proposals_applied_total{job="$cluster"}[5m]))',
                  intervalFactor: 2,
                  legendFormat: 'Proposal Apply Rate',
                  refId: 'D',
                  step: 2,
                },
              ],
              thresholds: [],
              timeFrom: null,
              timeShift: null,
              title: 'Raft Proposals',
              tooltip: {
                msResolution: false,
                shared: true,
                sort: 0,
                value_type: 'individual',
              },
              type: 'graph',
              xaxis: {
                mode: 'time',
                name: null,
                show: true,
                values: [],
              },
              yaxes: [
                {
                  format: 'short',
                  label: '',
                  logBase: 1,
                  max: null,
                  min: null,
                  show: true,
                },
                {
                  format: 'short',
                  label: null,
                  logBase: 1,
                  max: null,
                  min: null,
                  show: true,
                },
              ],
            },
            {
              aliasColors: {},
              bars: false,
              datasource: '$datasource',
              decimals: 0,
              editable: true,
              'error': false,
              fill: 0,
              id: 19,
              isNew: true,
              legend: {
                alignAsTable: false,
                avg: false,
                current: false,
                max: false,
                min: false,
                rightSide: false,
                show: false,
                total: false,
                values: false,
              },
              lines: true,
              linewidth: 2,
              links: [],
              nullPointMode: 'connected',
              percentage: false,
              pointradius: 5,
              points: false,
              renderer: 'flot',
              seriesOverrides: [],
              span: 6,
              stack: false,
              steppedLine: false,
              targets: [{
                expr: 'changes(etcd_server_leader_changes_seen_total{job="$cluster"}[1d])',
                intervalFactor: 2,
                legendFormat: '{{instance}} Total Leader Elections Per Day',
                metric: 'etcd_server_leader_changes_seen_total',
                refId: 'A',
                step: 2,
              }],
              thresholds: [],
              timeFrom: null,
              timeShift: null,
              title: 'Total Leader Elections Per Day',
              tooltip: {
                msResolution: false,
                shared: true,
                sort: 0,
                value_type: 'individual',
              },
              type: 'graph',
              xaxis: {
                mode: 'time',
                name: null,
                show: true,
                values: [],
              },
              yaxes: [
                {
                  format: 'short',
                  label: null,
                  logBase: 1,
                  max: null,
                  min: null,
                  show: true,
                },
                {
                  format: 'short',
                  label: null,
                  logBase: 1,
                  max: null,
                  min: null,
                  show: true,
                },
              ],
            },
          ],
          title: 'New row',
        },
      ],
      time: {
        from: 'now-15m',
        to: 'now',
      },
      timepicker: {
        now: true,
        refresh_intervals: [
          '5s',
          '10s',
          '30s',
          '1m',
          '5m',
          '15m',
          '30m',
          '1h',
          '2h',
          '1d',
        ],
        time_options: [
          '5m',
          '15m',
          '1h',
          '6h',
          '12h',
          '24h',
          '2d',
          '7d',
          '30d',
        ],
      },
      templating: {
        list: [
          {
            current: {
              text: 'Prometheus',
              value: 'Prometheus',
            },
            hide: 0,
            label: null,
            name: 'datasource',
            options: [],
            query: 'prometheus',
            refresh: 1,
            regex: '',
            type: 'datasource',
          },
          {
            allValue: null,
            current: {
              text: 'prod',
              value: 'prod',
            },
            datasource: '$datasource',
            hide: 0,
            includeAll: false,
            label: 'cluster',
            multi: false,
            name: 'cluster',
            options: [],
            query: 'label_values(etcd_server_has_leader{job!="kube-controllers", job!="apiserver"}, job)',
            refresh: 1,
            regex: '',
            sort: 2,
            tagValuesQuery: '',
            tags: [],
            tagsQuery: '',
            type: 'query',
            useTags: false,
          },
        ],
      },
      annotations: {
        list: [],
      },
      refresh: false,
      schemaVersion: 13,
      version: 215,
      links: [],
      gnetId: null,
    },
  },
}
