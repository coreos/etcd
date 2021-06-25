package api

import "go.etcd.io/etcd/server/v3/etcdserver/api/v2store"

type Response struct {
	Term    uint64
	Index   uint64
	Event   *v2store.Event
	Watcher v2store.Watcher
	Err     error
}
