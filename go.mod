module go.etcd.io/etcd/v3

go 1.15

replace (
	go.etcd.io/etcd/api/v3 => ./api
	go.etcd.io/etcd/client/v2 => ./client/v2
	go.etcd.io/etcd/client/v3 => ./client/v3
	go.etcd.io/etcd/etcdctl/v3 => ./etcdctl
	go.etcd.io/etcd/pkg/v3 => ./pkg
	go.etcd.io/etcd/raft/v3 => ./raft
	go.etcd.io/etcd/server/v3 => ./server
	go.etcd.io/etcd/tests/v3 => ./tests
)

require (
	github.com/bgentry/speakeasy v0.1.0
	github.com/dustin/go-humanize v1.0.0
	github.com/mattn/go-runewidth v0.0.9 // indirect
	github.com/orcaman/concurrent-map v0.0.0-20210106121528-16402b402231 // indirect
	github.com/spf13/cobra v1.1.1
	go.etcd.io/bbolt v1.3.5
	go.etcd.io/etcd/api/v3 v3.5.0-pre
	go.etcd.io/etcd/client/v2 v2.305.0-pre
	go.etcd.io/etcd/client/v3 v3.5.0-pre
	go.etcd.io/etcd/pkg/v3 v3.5.0-pre
	go.etcd.io/etcd/raft/v3 v3.5.0-pre
	go.etcd.io/etcd/server/v3 v3.5.0-pre
	go.etcd.io/etcd/tests/v3 v3.5.0-pre
	go.uber.org/zap v1.16.0
	golang.org/x/time v0.0.0-20200630173020-3af7569d3a1e
	google.golang.org/grpc v1.29.1
	gopkg.in/cheggaaa/pb.v1 v1.0.28
)
