// Copyright 2025 The etcd Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"
)

var (
	// Returned when an option combination isn’t yet handled by the cache (e.g. WithPrevKV for Watch(), WithCountOnly for Get()).
	ErrUnsupportedRequest = errors.New("cache: unsupported request parameters")
	// Returned when the requested key or key‑range is invalid (empty or reversed) or lies outside c.prefix.
	ErrKeyRangeInvalid = errors.New("cache: invalid or out‑of‑range key range")
	// Returned when the cache timed out waiting for the requested revision
	ErrCacheTimeout = errors.New("cache: timed out waiting for revision")
)

// Cache buffers a single etcd Watch for a given key‐prefix and fan‑outs local watchers.
//
// Note: gRPC proxy is not supported. Cache relies on RequestProgress RPCs,
// which the gRPC proxy does not forward.
type Cache struct {
	prefix            string // prefix is the key-prefix this shard is responsible for ("" = root).
	cfg               Config // immutable runtime configuration
	watcher           clientv3.Watcher
	kv                clientv3.KV
	demux             *demux // demux fans incoming events out to active watchers and manages resync.
	store             *store // last‑observed snapshot
	ready             *ready
	stop              context.CancelFunc
	waitGroup         sync.WaitGroup
	internalCtx       context.Context
	progressRequestor progressRequestor
	clock             Clock
}

// New builds a cache shard that watches only the requested prefix.
// For the root cache pass "".
//
// Note: gRPC proxy is not supported. Cache relies on RequestProgress RPCs,
// which the gRPC proxy does not forward.
func New(client *clientv3.Client, prefix string, opts ...Option) (*Cache, error) {
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(&cfg)
	}
	return newCache(client, prefix, cfg, realClock{})
}

func newCache(client *clientv3.Client, prefix string, cfg Config, clock Clock) (*Cache, error) {
	if cfg.HistoryWindowSize <= 0 {
		return nil, fmt.Errorf("invalid HistoryWindowSize %d (must be > 0)", cfg.HistoryWindowSize)
	}
	if cfg.BTreeDegree < 2 {
		return nil, fmt.Errorf("invalid BTreeDegree %d (must be >= 2)", cfg.BTreeDegree)
	}
	if cfg.InitialBackoff <= 0 {
		return nil, fmt.Errorf("invalid InitialBackoff %v (must be > 0)", cfg.InitialBackoff)
	}
	if cfg.MaxBackoff < cfg.InitialBackoff {
		return nil, fmt.Errorf("invalid MaxBackoff %v (must be >= InitialBackoff %v)", cfg.MaxBackoff, cfg.InitialBackoff)
	}
	if cfg.GetTimeout <= 0 {
		return nil, fmt.Errorf("invalid GetTimeout %v (must be > 0)", cfg.GetTimeout)
	}

	internalCtx, cancel := context.WithCancel(context.Background())

	cache := &Cache{
		prefix:            prefix,
		cfg:               cfg,
		watcher:           client.Watcher,
		kv:                client.KV,
		store:             newStore(cfg.BTreeDegree, cfg.HistoryWindowSize),
		ready:             newReady(),
		stop:              cancel,
		internalCtx:       internalCtx,
		progressRequestor: newConditionalProgressRequestor(client.Watcher, clock, cfg.ProgressRequestInterval),
		clock:             clock,
	}

	cache.demux = NewDemux(internalCtx, &cache.waitGroup, cfg.HistoryWindowSize, cfg.ResyncInterval)

	cache.waitGroup.Add(2)
	go func() {
		defer cache.waitGroup.Done()
		cache.getWatchLoop()
	}()
	go func() {
		defer cache.waitGroup.Done()
		cache.progressRequestor.run(internalCtx)
	}()

	return cache, nil
}

func (c *Cache) RequestProgress(_ context.Context) error {
	c.demux.BroadcastProgress()
	return nil
}

// Watch registers a cache-backed watcher for a given key or prefix.
// It returns a WatchChan that streams WatchResponses containing events.
func (c *Cache) Watch(ctx context.Context, key string, opts ...clientv3.OpOption) clientv3.WatchChan {
	if err := c.WaitReady(ctx); err != nil {
		emptyWatchChan := make(chan clientv3.WatchResponse)
		close(emptyWatchChan)
		return emptyWatchChan
	}

	op := clientv3.OpWatch(key, opts...)
	startRev := op.Rev()

	pred, err := c.validateWatch(key, op)
	if err != nil {
		ch := make(chan clientv3.WatchResponse, 1)
		ch <- clientv3.WatchResponse{
			Header:       &pb.ResponseHeader{},
			Canceled:     true,
			CancelReason: err.Error(),
		}
		close(ch)
		return ch
	}

	w := newWatcher(c.cfg.PerWatcherBufferSize, pred)
	c.demux.Register(w, startRev)

	responseChan := make(chan clientv3.WatchResponse)
	c.waitGroup.Add(1)
	go func() {
		defer c.waitGroup.Done()
		defer close(responseChan)
		defer c.demux.Unregister(w)

		var progressTimer Timer
		var progressChan <-chan time.Time
		if op.IsProgressNotify() {
			progressTimer = c.clock.NewTimer(c.cfg.ProgressNotifyInterval)
			defer progressTimer.Stop()
			progressChan = progressTimer.Chan()
		}

		for {
			select {
			case <-ctx.Done():
				return
			case <-c.internalCtx.Done():
				return
			case <-progressChan:
				c.demux.BroadcastProgressTo(w)
				progressTimer.Reset(c.cfg.ProgressNotifyInterval)
			case resp, ok := <-w.respCh:
				if !ok {
					if w.cancelResp != nil {
						select {
						case <-ctx.Done():
						case <-c.internalCtx.Done():
						case responseChan <- *w.cancelResp:
						}
					}
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-c.internalCtx.Done():
					return
				case responseChan <- resp:
					if progressTimer != nil {
						progressTimer.Reset(c.cfg.ProgressNotifyInterval)
					}
				}
			}
		}
	}()
	return responseChan
}

func (c *Cache) Get(ctx context.Context, key string, opts ...clientv3.OpOption) (*clientv3.GetResponse, error) {
	if c.store.LatestRev() == 0 {
		if err := c.WaitReady(ctx); err != nil {
			return nil, err
		}
	}
	op := clientv3.OpGet(key, opts...)

	if _, err := c.validateGet(key, op); err != nil {
		return nil, err
	}

	startKey := []byte(key)
	endKey := op.RangeBytes()
	requestedRev := op.Rev()

	if !op.IsSerializable() {
		serverRev, err := c.serverRevision(ctx)
		if err != nil {
			return nil, err
		}
		if requestedRev > serverRev {
			return nil, rpctypes.ErrFutureRev
		}
		if err = c.waitTillRevision(ctx, serverRev); err != nil {
			return nil, err
		}
	}

	kvs, latestRev, err := c.store.Get(startKey, endKey, requestedRev)
	if err != nil {
		return nil, err
	}

	return &clientv3.GetResponse{
		Header: &pb.ResponseHeader{Revision: latestRev},
		Kvs:    kvs,
		Count:  int64(len(kvs)),
	}, nil
}

// Ready returns true if the snapshot has been loaded and the first watch has been confirmed.
func (c *Cache) Ready() bool {
	return c.ready.Ready()
}

// WaitReady blocks until the cache is ready or the ctx is cancelled.
func (c *Cache) WaitReady(ctx context.Context) error {
	return c.ready.WaitReady(ctx)
}

func (c *Cache) WaitForRevision(ctx context.Context, rev int64) error {
	for {
		if c.store.LatestRev() >= rev {
			return nil
		}
		select {
		case <-time.After(10 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (c *Cache) serverRevision(ctx context.Context) (int64, error) {
	key := c.prefix
	if key == "" {
		key = "/"
	}
	resp, err := c.kv.Get(ctx, key, clientv3.WithLimit(1), clientv3.WithCountOnly())
	if err != nil {
		return 0, err
	}
	return resp.Header.Revision, nil
}

func (c *Cache) waitTillRevision(ctx context.Context, rev int64) error {
	if c.store.LatestRev() >= rev {
		return nil
	}

	c.progressRequestor.add()
	defer c.progressRequestor.remove()

	startTime := time.Now()

	timeoutCh := time.After(c.cfg.WaitTimeout)
	go func() {
		select {
		case <-timeoutCh:
			c.store.revCond.Broadcast()
		case <-ctx.Done():
			c.store.revCond.Broadcast()
		}
	}()

	c.store.mu.RLock()
	defer c.store.mu.RUnlock()

	for c.store.latest.rev < rev {
		if time.Since(startTime) >= c.cfg.WaitTimeout {
			return ErrCacheTimeout
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		c.store.revCond.Wait()
	}

	return nil
}

// Close cancels the private context and blocks until all goroutines return.
func (c *Cache) Close() {
	c.stop()
	c.waitGroup.Wait()
}

func (c *Cache) getWatchLoop() {
	ctx := c.internalCtx
	backoff := c.cfg.InitialBackoff
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		if err := c.getWatch(); err != nil { //nolint:staticcheck // SA4023: Related diagnostic for the defensive nil check in getWatchLoop.
			fmt.Printf("getWatch failed, will retry after %v: %v\n", backoff, err)
		}
		timer := c.clock.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.Chan():
		}
		backoff *= 2
		if backoff > c.cfg.MaxBackoff {
			backoff = c.cfg.MaxBackoff
		}
	}
}

func (c *Cache) getWatch() error { //nolint:staticcheck // SA4023: never returns nil, defensive nil check in getWatchLoop.
	getResp, err := c.get(c.internalCtx)
	if err != nil {
		return err
	}
	return c.watch(getResp.Header.Revision + 1)
}

func (c *Cache) get(ctx context.Context) (*clientv3.GetResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.GetTimeout)
	defer cancel()

	resp, err := c.kv.Get(ctx, c.prefix, clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	c.store.Restore(resp.Kvs, resp.Header.Revision)
	return resp, nil
}

func (c *Cache) watch(rev int64) error {
	readyOnce := sync.Once{}
	for {
		storeW := newWatcher(c.cfg.PerWatcherBufferSize, nil)
		c.demux.Register(storeW, rev)
		applyErr := make(chan error, 1)
		c.waitGroup.Add(1)
		go func() {
			defer c.waitGroup.Done()
			if err := c.applyStorage(storeW); err != nil {
				applyErr <- err
			}
			close(applyErr)
		}()

		err := c.watchEvents(rev, applyErr, &readyOnce)
		c.demux.Unregister(storeW)

		if err != nil {
			return err
		}
	}
}

func (c *Cache) applyStorage(storeW *watcher) error {
	for {
		select {
		case <-c.internalCtx.Done():
			return nil
		case resp, ok := <-storeW.respCh:
			if !ok {
				return nil
			}
			if err := c.store.Apply(resp); err != nil {
				return err
			}
		}
	}
}

func (c *Cache) watchEvents(rev int64, applyErr <-chan error, readyOnce *sync.Once) error {
	watchCh := c.watcher.Watch(
		c.internalCtx,
		c.prefix,
		clientv3.WithPrefix(),
		clientv3.WithRev(rev),
		clientv3.WithProgressNotify(),
		clientv3.WithCreatedNotify(),
	)
	for {
		select {
		case <-c.internalCtx.Done():
			return c.internalCtx.Err()
		case resp, ok := <-watchCh:
			if !ok {
				return nil
			}
			readyOnce.Do(func() {
				c.demux.Init(rev)
				c.ready.Set()
			})
			if err := resp.Err(); err != nil {
				c.ready.Reset()
				return err
			}
			err := c.demux.Broadcast(resp)
			if err != nil {
				c.ready.Reset()
				return err
			}
		case err := <-applyErr:
			c.ready.Reset()
			return err
		}
	}
}

func (c *Cache) validateWatch(key string, op clientv3.Op) (pred KeyPredicate, err error) {
	switch {
	case op.IsPrevKV():
		return nil, fmt.Errorf("%w: PrevKV not supported", ErrUnsupportedRequest)
	case op.IsFragment():
		return nil, fmt.Errorf("%w: Fragment not supported", ErrUnsupportedRequest)
	case op.IsCreatedNotify():
		return nil, fmt.Errorf("%w: CreatedNotify not supported", ErrUnsupportedRequest)
	case op.IsFilterPut():
		return nil, fmt.Errorf("%w: FilterPut not supported", ErrUnsupportedRequest)
	case op.IsFilterDelete():
		return nil, fmt.Errorf("%w: FilterDelete not supported", ErrUnsupportedRequest)
	}

	startKey := []byte(key)
	endKey := op.RangeBytes() // nil = single key, {0}=FromKey, else explicit range

	if err := c.validateRange(startKey, endKey); err != nil {
		return nil, err
	}
	return KeyPredForRange(startKey, endKey), nil
}

func (c *Cache) validateGet(key string, op clientv3.Op) (KeyPredicate, error) {
	switch {
	case op.IsCountOnly():
		return nil, fmt.Errorf("%w: CountOnly not supported", ErrUnsupportedRequest)
	case op.IsPrevKV():
		return nil, fmt.Errorf("%w: PrevKV not supported", ErrUnsupportedRequest)
	case op.IsSortSet():
		return nil, fmt.Errorf("%w: SortSet not supported", ErrUnsupportedRequest)
	case op.Limit() != 0:
		return nil, fmt.Errorf("%w: Limit(%d) not supported", ErrUnsupportedRequest, op.Limit())
	case op.MinModRev() != 0:
		return nil, fmt.Errorf("%w: MinModRev(%d) not supported", ErrUnsupportedRequest, op.MinModRev())
	case op.MaxModRev() != 0:
		return nil, fmt.Errorf("%w: MaxModRev(%d) not supported", ErrUnsupportedRequest, op.MaxModRev())
	case op.MinCreateRev() != 0:
		return nil, fmt.Errorf("%w: MinCreateRev(%d) not supported", ErrUnsupportedRequest, op.MinCreateRev())
	case op.MaxCreateRev() != 0:
		return nil, fmt.Errorf("%w: MaxCreateRev(%d) not supported", ErrUnsupportedRequest, op.MaxCreateRev())
	}

	startKey := []byte(key)
	endKey := op.RangeBytes()

	if err := c.validateRange(startKey, endKey); err != nil {
		return nil, err
	}

	return KeyPredForRange(startKey, endKey), nil
}

func (c *Cache) validateRange(startKey, endKey []byte) error {
	prefixStart := []byte(c.prefix)
	prefixEnd := []byte(clientv3.GetPrefixRangeEnd(c.prefix))

	isSingleKey := len(endKey) == 0
	isFromKey := len(endKey) == 1 && endKey[0] == 0

	switch {
	case isSingleKey:
		if c.prefix == "" {
			return nil
		}
		if bytes.Compare(startKey, prefixStart) < 0 || bytes.Compare(startKey, prefixEnd) >= 0 {
			return ErrKeyRangeInvalid
		}
		return nil

	case isFromKey:
		if c.prefix != "" {
			return ErrKeyRangeInvalid
		}
		return nil

	default:
		if bytes.Compare(endKey, startKey) <= 0 {
			return ErrKeyRangeInvalid
		}
		if c.prefix == "" {
			return nil
		}
		if bytes.Compare(startKey, prefixStart) < 0 || bytes.Compare(endKey, prefixEnd) > 0 {
			return ErrKeyRangeInvalid
		}
		return nil
	}
}

// WaitForNextResync blocks until the next resync loop iteration is complete.
func (c *Cache) WaitForNextResync(ctx context.Context) error {
	return c.demux.WaitForNextResync(ctx)
}
