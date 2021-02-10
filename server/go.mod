module go.etcd.io/etcd/server/v3

go 1.15

require (
	github.com/coreos/go-semver v0.3.0
	github.com/coreos/go-systemd/v22 v22.1.0
	github.com/dustin/go-humanize v1.0.0
	github.com/form3tech-oss/jwt-go v3.2.2+incompatible
	github.com/gogo/protobuf v1.3.1
	github.com/golang/groupcache v0.0.0-20200121045136-8c9f03a8e57e
	github.com/golang/protobuf v1.3.5
	github.com/google/btree v1.0.0
	github.com/grpc-ecosystem/go-grpc-middleware v1.2.2
	github.com/grpc-ecosystem/go-grpc-prometheus v1.2.0
	github.com/grpc-ecosystem/grpc-gateway v1.14.6
	github.com/jonboulle/clockwork v0.2.2
	github.com/kr/pretty v0.2.1 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/orcaman/concurrent-map v0.0.0-20210106121528-16402b402231
	github.com/prometheus/client_golang v1.5.1
	github.com/prometheus/client_model v0.2.0
	github.com/prometheus/common v0.10.0 // indirect
	github.com/prometheus/procfs v0.2.0 // indirect
	github.com/sirupsen/logrus v1.7.0 // indirect
	github.com/soheilhy/cmux v0.1.4
	github.com/spf13/cobra v1.1.1
	github.com/tmc/grpc-websocket-proxy v0.0.0-20200427203606-3cfed13b9966
	github.com/xiang90/probing v0.0.0-20190116061207-43a291ad63a2
	go.etcd.io/bbolt v1.3.5
	go.etcd.io/etcd/api/v3 v3.5.0-pre
	go.etcd.io/etcd/client/v2 v2.305.0-pre
	go.etcd.io/etcd/client/v3 v3.5.0-pre
	go.etcd.io/etcd/pkg/v3 v3.5.0-pre
	go.etcd.io/etcd/raft/v3 v3.5.0-pre
	go.uber.org/multierr v1.5.0
	go.uber.org/zap v1.16.0
	golang.org/x/crypto v0.0.0-20201002170205-7f63de1d35b0
	golang.org/x/lint v0.0.0-20200302205851-738671d3881b // indirect
	golang.org/x/net v0.0.0-20201006153459-a7d1128ccaa0
	golang.org/x/time v0.0.0-20200630173020-3af7569d3a1e
	golang.org/x/tools v0.0.0-20201014170642-d1624618ad65 // indirect
	google.golang.org/genproto v0.0.0-20200513103714-09dca8ec2884
	google.golang.org/grpc v1.29.1
	sigs.k8s.io/yaml v1.2.0
)

replace (
	go.etcd.io/etcd/api/v3 => ../api
	go.etcd.io/etcd/client/v2 => ../client/v2
	go.etcd.io/etcd/client/v3 => ../client/v3
	go.etcd.io/etcd/pkg/v3 => ../pkg
	go.etcd.io/etcd/raft/v3 => ../raft
)

// Bad imports are sometimes causing attempts to pull that code.
// This makes the error more explicit.
replace (
	go.etcd.io/etcd => ./FORBIDDEN_DEPENDENCY
	go.etcd.io/tests/v3 => ./FORBIDDEN_DEPENDENCY
)
