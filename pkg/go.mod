module go.etcd.io/etcd/pkg/v3

go 1.27

toolchain go1.27.3

require (
	github.com/creack/pty v1.1.18
	github.com/dustin/go-humanize v1.1.0
	github.com/spf13/cobra v1.10.2
	github.com/spf13/pflag v1.0.10
	github.com/stretchr/testify v1.12.1
	go.etcd.io/etcd/client/pkg/v3 v3.8.0-alpha.0
	go.opentelemetry.io/otel/trace v1.47.0
	go.uber.org/zap v1.28.0
	golang.org/x/sys v0.49.0
	google.golang.org/grpc v1.85.0
	google.golang.org/protobuf v1.36.12
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	go.opentelemetry.io/otel v1.47.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20261019180522-ce908d29299b // indirect
)

replace go.etcd.io/etcd/client/pkg/v3 => ../client/pkg
