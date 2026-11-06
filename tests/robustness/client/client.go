// Copyright 2022 The etcd Authors
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

package client

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/tests/v3/robustness/identity"
	"go.etcd.io/etcd/tests/v3/robustness/model"
	"go.etcd.io/etcd/tests/v3/robustness/report"
)

// RecordingClient provides a semi-etcd client (different interface than
// clientv3.Client) that records all the requests and responses made. Doesn't
// allow for concurrent requests to conform to model.AppendableHistory requirements.
type RecordingClient struct {
	ID     int
	client *clientv3.Client
	// using baseTime time-measuring operation to get monotonic clock reading
	// see https://github.com/golang/go/blob/master/src/time/time.go#L17
	baseTime time.Time

	watchMux        sync.Mutex
	watchOperations []model.WatchOperation
	wg              sync.WaitGroup
	// mux ensures order of request appending.
	kvMux        sync.Mutex
	kvOperations *model.AppendableHistory
}

var _ clientv3.KV = (*RecordingClient)(nil)

type TimedWatchEvent struct {
	model.WatchEvent
	Time time.Duration
}

func NewRecordingClient(endpoints []string, ids identity.Provider, baseTime time.Time) (*RecordingClient, error) {
	cc, err := clientv3.New(clientv3.Config{
		Endpoints:            endpoints,
		Logger:               zap.NewNop(),
		DialKeepAliveTime:    10 * time.Second,
		DialKeepAliveTimeout: 100 * time.Millisecond,
	})
	if err != nil {
		return nil, err
	}
	return &RecordingClient{
		ID:           ids.NewClientID(),
		client:       cc,
		kvOperations: model.NewAppendableHistory(ids),
		baseTime:     baseTime,
	}, nil
}

func (c *RecordingClient) Close() error {
	err := c.client.Close()
	c.wg.Wait()
	return err
}

func (c *RecordingClient) Report() report.ClientReport {
	return report.ClientReport{
		ClientID: c.ID,
		KeyValue: c.kvOperations.History.Operations(),
		Watch:    c.watchOperations,
	}
}

func (c *RecordingClient) Do(ctx context.Context, op clientv3.Op) (clientv3.OpResponse, error) {
	panic("not implemented")
}

func (c *RecordingClient) Get(ctx context.Context, key string, opts ...clientv3.OpOption) (*clientv3.GetResponse, error) {
	op := clientv3.OpGet(key, opts...)
	return c.Range(ctx, key, string(op.RangeBytes()), op.Rev(), op.Limit(), op.IsKeysOnly())
}

func (c *RecordingClient) Range(ctx context.Context, start, end string, revision, limit int64, keyOnly bool) (*clientv3.GetResponse, error) {
	ops := []clientv3.OpOption{}
	if end != "" {
		ops = append(ops, clientv3.WithRange(end))
	}
	if revision != 0 {
		ops = append(ops, clientv3.WithRev(revision))
	}
	if limit != 0 {
		ops = append(ops, clientv3.WithLimit(limit))
	}
	if keyOnly {
		ops = append(ops, clientv3.WithKeysOnly())
	}
	c.kvMux.Lock()
	defer c.kvMux.Unlock()
	callTime := time.Since(c.baseTime)
	resp, err := c.client.Get(ctx, start, ops...)
	returnTime := time.Since(c.baseTime)
	c.kvOperations.AppendRange(start, end, revision, limit, keyOnly, callTime, returnTime, resp, err)
	return resp, err
}

func (c *RecordingClient) RangeStream(ctx context.Context, start, end string, revision, limit int64, keyOnly bool) (*clientv3.GetResponse, error) {
	ops := []clientv3.OpOption{}
	if end != "" {
		ops = append(ops, clientv3.WithRange(end))
	}
	if revision != 0 {
		ops = append(ops, clientv3.WithRev(revision))
	}
	if limit != 0 {
		ops = append(ops, clientv3.WithLimit(limit))
	}
	if keyOnly {
		ops = append(ops, clientv3.WithKeysOnly())
	}
	c.kvMux.Lock()
	defer c.kvMux.Unlock()
	callTime := time.Since(c.baseTime)
	stream, err := c.client.GetStream(ctx, start, ops...)
	var resp *clientv3.GetResponse
	if err == nil {
		resp, err = clientv3.GetStreamToGetResponse(stream)
	}
	returnTime := time.Since(c.baseTime)
	c.kvOperations.AppendRange(start, end, revision, limit, keyOnly, callTime, returnTime, resp, err)
	return resp, err
}

func (c *RecordingClient) Put(ctx context.Context, key, value string, _ ...clientv3.OpOption) (*clientv3.PutResponse, error) {
	c.kvMux.Lock()
	defer c.kvMux.Unlock()
	callTime := time.Since(c.baseTime)
	resp, err := c.client.Put(ctx, key, value)
	returnTime := time.Since(c.baseTime)
	c.kvOperations.AppendPut(key, value, callTime, returnTime, resp, err)
	return resp, err
}

func (c *RecordingClient) Delete(ctx context.Context, key string, _ ...clientv3.OpOption) (*clientv3.DeleteResponse, error) {
	c.kvMux.Lock()
	defer c.kvMux.Unlock()
	callTime := time.Since(c.baseTime)
	resp, err := c.client.Delete(ctx, key)
	returnTime := time.Since(c.baseTime)
	c.kvOperations.AppendDelete(key, callTime, returnTime, resp, err)
	return resp, err
}

type wrappedTxn struct {
	txn        clientv3.Txn
	conditions []clientv3.Cmp
	onSuccess  []clientv3.Op
	onFailure  []clientv3.Op
	c          *RecordingClient
}

var _ clientv3.Txn = (*wrappedTxn)(nil)

func (w *wrappedTxn) If(cs ...clientv3.Cmp) clientv3.Txn {
	w.conditions = append(w.conditions, cs...)
	w.txn = w.txn.If(cs...)
	return w
}

func (w *wrappedTxn) Then(ops ...clientv3.Op) clientv3.Txn {
	w.onSuccess = append(w.onSuccess, ops...)
	w.txn = w.txn.Then(ops...)
	return w
}

func (w *wrappedTxn) Else(ops ...clientv3.Op) clientv3.Txn {
	w.onFailure = append(w.onFailure, ops...)
	w.txn = w.txn.Else(ops...)
	return w
}

func (w *wrappedTxn) Commit() (*clientv3.TxnResponse, error) {
	w.c.kvMux.Lock()
	defer w.c.kvMux.Unlock()
	callTime := time.Since(w.c.baseTime)
	resp, err := w.txn.Commit()
	returnTime := time.Since(w.c.baseTime)
	w.c.kvOperations.AppendTxn(w.conditions, w.onSuccess, w.onFailure, callTime, returnTime, resp, err)
	return resp, err
}

func (c *RecordingClient) Txn(ctx context.Context) clientv3.Txn {
	return &wrappedTxn{txn: c.client.Txn(ctx), c: c}
}

func (c *RecordingClient) LeaseGrant(ctx context.Context, ttl int64) (*clientv3.LeaseGrantResponse, error) {
	c.kvMux.Lock()
	defer c.kvMux.Unlock()
	callTime := time.Since(c.baseTime)
	resp, err := c.client.Lease.Grant(ctx, ttl)
	returnTime := time.Since(c.baseTime)
	c.kvOperations.AppendLeaseGrant(callTime, returnTime, resp, err)
	return resp, err
}

func (c *RecordingClient) LeaseRevoke(ctx context.Context, leaseID int64) (*clientv3.LeaseRevokeResponse, error) {
	c.kvMux.Lock()
	defer c.kvMux.Unlock()
	callTime := time.Since(c.baseTime)
	resp, err := c.client.Lease.Revoke(ctx, clientv3.LeaseID(leaseID))
	returnTime := time.Since(c.baseTime)
	c.kvOperations.AppendLeaseRevoke(leaseID, callTime, returnTime, resp, err)
	return resp, err
}

func (c *RecordingClient) PutWithLease(ctx context.Context, key string, value string, leaseID int64) (*clientv3.PutResponse, error) {
	opts := clientv3.WithLease(clientv3.LeaseID(leaseID))
	c.kvMux.Lock()
	defer c.kvMux.Unlock()
	callTime := time.Since(c.baseTime)
	resp, err := c.client.Put(ctx, key, value, opts)
	returnTime := time.Since(c.baseTime)
	c.kvOperations.AppendPutWithLease(key, value, leaseID, callTime, returnTime, resp, err)
	return resp, err
}

func (c *RecordingClient) Defragment(ctx context.Context) (*clientv3.DefragmentResponse, error) {
	c.kvMux.Lock()
	defer c.kvMux.Unlock()
	callTime := time.Since(c.baseTime)
	resp, err := c.client.Defragment(ctx, c.client.Endpoints()[0])
	returnTime := time.Since(c.baseTime)
	c.kvOperations.AppendDefragment(callTime, returnTime, resp, err)
	return resp, err
}

func (c *RecordingClient) GetStream(ctx context.Context, key string, opts ...clientv3.OpOption) (clientv3.GetStreamChan, error) {
	panic("not implemented")
}

func (c *RecordingClient) Compact(ctx context.Context, rev int64, _ ...clientv3.CompactOption) (*clientv3.CompactResponse, error) {
	c.kvMux.Lock()
	defer c.kvMux.Unlock()
	callTime := time.Since(c.baseTime)
	resp, err := c.client.Compact(ctx, rev)
	returnTime := time.Since(c.baseTime)
	c.kvOperations.AppendCompact(rev, callTime, returnTime, resp, err)
	return resp, err
}

func (c *RecordingClient) MemberList(ctx context.Context, opts ...clientv3.OpOption) (*clientv3.MemberListResponse, error) {
	c.kvMux.Lock()
	defer c.kvMux.Unlock()
	resp, err := c.client.MemberList(ctx, opts...)
	return resp, err
}

func (c *RecordingClient) MemberAdd(ctx context.Context, peerAddrs []string) (*clientv3.MemberAddResponse, error) {
	c.kvMux.Lock()
	defer c.kvMux.Unlock()
	resp, err := c.client.MemberAdd(ctx, peerAddrs)
	return resp, err
}

func (c *RecordingClient) MemberAddAsLearner(ctx context.Context, peerAddrs []string) (*clientv3.MemberAddResponse, error) {
	c.kvMux.Lock()
	defer c.kvMux.Unlock()
	resp, err := c.client.MemberAddAsLearner(ctx, peerAddrs)
	return resp, err
}

func (c *RecordingClient) MemberRemove(ctx context.Context, id uint64) (*clientv3.MemberRemoveResponse, error) {
	c.kvMux.Lock()
	defer c.kvMux.Unlock()
	resp, err := c.client.MemberRemove(ctx, id)
	return resp, err
}

func (c *RecordingClient) MemberUpdate(ctx context.Context, id uint64, peerAddrs []string) (*clientv3.MemberUpdateResponse, error) {
	c.kvMux.Lock()
	defer c.kvMux.Unlock()
	resp, err := c.client.MemberUpdate(ctx, id, peerAddrs)
	return resp, err
}

func (c *RecordingClient) MemberPromote(ctx context.Context, id uint64) (*clientv3.MemberPromoteResponse, error) {
	c.kvMux.Lock()
	defer c.kvMux.Unlock()
	resp, err := c.client.MemberPromote(ctx, id)
	return resp, err
}

func (c *RecordingClient) Status(ctx context.Context, endpoint string) (*clientv3.StatusResponse, error) {
	c.kvMux.Lock()
	defer c.kvMux.Unlock()
	resp, err := c.client.Status(ctx, endpoint)
	return resp, err
}

func (c *RecordingClient) Endpoints() []string {
	return c.client.Endpoints()
}

func (c *RecordingClient) SetEndpoints(endpoints ...string) {
	c.client.SetEndpoints(endpoints...)
}

func (c *RecordingClient) Watch(ctx context.Context, key string, rev int64, withPrefix bool, withProgressNotify bool, withPrevKV bool) clientv3.WatchChan {
	request := model.WatchRequest{
		Key:                key,
		Revision:           rev,
		WithPrefix:         withPrefix,
		WithProgressNotify: withProgressNotify,
		WithPrevKV:         withPrevKV,
	}
	return c.watch(ctx, request)
}

func (c *RecordingClient) watch(ctx context.Context, request model.WatchRequest) clientv3.WatchChan {
	ops := []clientv3.OpOption{}
	if request.WithPrefix {
		ops = append(ops, clientv3.WithPrefix())
	}
	if request.Revision != 0 {
		ops = append(ops, clientv3.WithRev(request.Revision))
	}
	if request.WithProgressNotify {
		ops = append(ops, clientv3.WithProgressNotify())
	}
	if request.WithPrevKV {
		ops = append(ops, clientv3.WithPrevKV())
	}
	respCh := make(chan clientv3.WatchResponse)

	responses := []model.WatchResponse{}
	c.watchMux.Lock()
	c.watchOperations = append(c.watchOperations, model.WatchOperation{
		Request:   request,
		Responses: responses,
	})
	index := len(c.watchOperations) - 1
	c.watchMux.Unlock()

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer close(respCh)
		for r := range c.client.Watch(ctx, request.Key, ops...) {
			responses = append(responses, ToWatchResponse(r, c.baseTime))
			c.watchMux.Lock()
			c.watchOperations[index].Responses = responses
			c.watchMux.Unlock()
			select {
			case respCh <- r:
			case <-ctx.Done():
				return
			case <-c.client.Ctx().Done():
				return
			}
		}
	}()
	return respCh
}

func (c *RecordingClient) RequestProgress(ctx context.Context) error {
	return c.client.RequestProgress(ctx)
}

func ToWatchResponse(r clientv3.WatchResponse, baseTime time.Time) model.WatchResponse {
	// using time.Since time-measuring operation to get monotonic clock reading
	// see https://github.com/golang/go/blob/master/src/time/time.go#L17
	resp := model.WatchResponse{Time: time.Since(baseTime)}
	for _, event := range r.Events {
		resp.Events = append(resp.Events, toWatchEvent(event))
	}
	resp.IsProgressNotify = r.IsProgressNotify()
	resp.Revision = r.Header.Revision
	err := r.Err()
	if err != nil {
		resp.Error = r.Err().Error()
	}
	return resp
}

func toWatchEvent(event *clientv3.Event) (watch model.WatchEvent) {
	watch.Revision = event.Kv.ModRevision
	watch.Key = string(event.Kv.Key)
	watch.Value = model.ToValueOrHash(string(event.Kv.Value))

	if event.PrevKv != nil {
		watch.PrevValue = &model.ValueRevision{
			Value:       model.ToValueOrHash(string(event.PrevKv.Value)),
			ModRevision: event.PrevKv.ModRevision,
			Version:     event.PrevKv.Version,
		}
	}
	watch.IsCreate = event.IsCreate()

	switch event.Type {
	case mvccpb.Event_PUT:
		watch.Type = model.PutOperation
	case mvccpb.Event_DELETE:
		watch.Type = model.DeleteOperation
	default:
		panic(fmt.Sprintf("Unexpected event type: %s", event.Type))
	}
	return watch
}

type ClientSet struct {
	idProvider identity.Provider
	baseTime   time.Time

	mux     sync.Mutex
	closed  bool
	clients []*RecordingClient
	reports []report.ClientReport
}

func NewSet(ids identity.Provider, baseTime time.Time) *ClientSet {
	return &ClientSet{
		idProvider: ids,
		baseTime:   baseTime,

		clients: []*RecordingClient{},
	}
}

func (cs *ClientSet) NewClient(endpoints []string) (*RecordingClient, error) {
	cs.mux.Lock()
	defer cs.mux.Unlock()
	if cs.closed {
		return nil, errors.New("the clientset is already closed")
	}
	cli, err := NewRecordingClient(endpoints, cs.idProvider, cs.baseTime)
	if err != nil {
		return nil, err
	}
	cs.clients = append(cs.clients, cli)
	return cli, nil
}

func (cs *ClientSet) Reports() []report.ClientReport {
	cs.mux.Lock()
	defer cs.mux.Unlock()
	if !cs.closed {
		cs.close()
	}
	if cs.reports == nil {
		reports := cs.generateReports()
		cs.reports = reports
	}
	return cs.reports
}

func (cs *ClientSet) Close() {
	cs.mux.Lock()
	defer cs.mux.Unlock()
	cs.close()
}

func (cs *ClientSet) close() {
	if cs.closed {
		return
	}
	for _, c := range cs.clients {
		c.Close()
	}
	cs.closed = true
}

func (cs *ClientSet) generateReports() []report.ClientReport {
	reports := make([]report.ClientReport, 0, len(cs.clients))
	for _, c := range cs.clients {
		reports = append(reports, c.Report())
	}
	return reports
}

func (cs *ClientSet) IdentityProvider() identity.Provider {
	return cs.idProvider
}

func (cs *ClientSet) BaseTime() time.Time {
	return cs.baseTime
}
