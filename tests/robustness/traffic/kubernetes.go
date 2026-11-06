// Copyright 2023 The etcd Authors
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

package traffic

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"

	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/kubernetes"
	"go.etcd.io/etcd/pkg/v3/stringutil"
	"go.etcd.io/etcd/tests/v3/robustness/client"
	"go.etcd.io/etcd/tests/v3/robustness/identity"
	"go.etcd.io/etcd/tests/v3/robustness/random"
)

var (
	Kubernetes Traffic = kubernetesTraffic{
		averageKeyCount: 10,
		resource:        "pods",
		namespace:       "default",
		// Please keep the sum of weights equal 100.
		readChoices: []random.ChoiceWeight[KubernetesRequestType]{
			{Choice: KubernetesGet, Weight: 5},
			{Choice: KubernetesGetStale, Weight: 2},
			{Choice: KubernetesGetRev, Weight: 8},
			{Choice: KubernetesListStale, Weight: 5},
			{Choice: KubernetesCount, Weight: 3},
			{Choice: KubernetesKeys, Weight: 2},
			{Choice: KubernetesListAndWatch, Weight: 75},
		},
		// Please keep the sum of weights equal 100.
		writeChoices: []random.ChoiceWeight[KubernetesRequestType]{
			{Choice: KubernetesUpdate, Weight: 90},
			{Choice: KubernetesDelete, Weight: 5},
			{Choice: KubernetesCreate, Weight: 5},
		},
	}

	KubernetesCreateDelete Traffic = kubernetesTraffic{
		averageKeyCount: 10,
		resource:        "pods",
		namespace:       "default",
		// Please keep the sum of weights equal 100.
		readChoices: []random.ChoiceWeight[KubernetesRequestType]{
			{Choice: KubernetesGet, Weight: 5},
			{Choice: KubernetesGetStale, Weight: 2},
			{Choice: KubernetesGetRev, Weight: 8},
			{Choice: KubernetesListStale, Weight: 5},
			{Choice: KubernetesCount, Weight: 3},
			{Choice: KubernetesKeys, Weight: 2},
			{Choice: KubernetesListAndWatch, Weight: 75},
		},
		// Please keep the sum of weights equal 100.
		writeChoices: []random.ChoiceWeight[KubernetesRequestType]{
			{Choice: KubernetesDelete, Weight: 40},
			{Choice: KubernetesCreate, Weight: 60},
		},
	}
)

type kubernetesTraffic struct {
	averageKeyCount int
	resource        string
	namespace       string
	readChoices     []random.ChoiceWeight[KubernetesRequestType]
	writeChoices    []random.ChoiceWeight[KubernetesRequestType]
}

func (t kubernetesTraffic) ExpectUniqueRevision() bool {
	return true
}

func (t kubernetesTraffic) RunKeyValueLoop(ctx context.Context, c *client.RecordingClient, p RunTrafficLoopParam) {
	kc := kubernetes.Client{Client: &clientv3.Client{KV: c}}
	s := p.Storage
	keyPrefix := "/registry/" + t.resource + "/"
	g := errgroup.Group{}

	g.Go(func() error {
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-p.Finish:
				return nil
			default:
			}
			err := t.Read(ctx, c, s, p.QPSLimiter, keyPrefix)
			if err != nil {
				continue
			}
		}
	})
	g.Go(func() error {
		lastWriteFailed := false
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-p.Finish:
				return nil
			default:
			}
			// Avoid multiple failed writes in a row
			if lastWriteFailed {
				_, err := t.List(ctx, kc, s, p.QPSLimiter, keyPrefix, t.averageKeyCount, 0)
				if err != nil {
					continue
				}
			}
			err := t.Write(ctx, kc, p.IDs, s, p.QPSLimiter, p.NonUniqueRequestConcurrencyLimiter)
			lastWriteFailed = err != nil
			if err != nil {
				continue
			}
		}
	})
	g.Wait()
}

func (t kubernetesTraffic) RunWatchLoop(ctx context.Context, c *client.RecordingClient, p RunWatchLoopParam) {
	runWatchLoop(ctx, c, p, watchLoopConfig{
		key:           "/registry/" + t.resource + "/",
		requireLeader: true,
	})
}

func (t kubernetesTraffic) Read(ctx context.Context, c *client.RecordingClient, s *storage, limiter *rate.Limiter, keyPrefix string) error {
	kc := kubernetes.Client{Client: &clientv3.Client{KV: c}}
	op := random.PickRandom(t.readChoices)
	switch op {
	case KubernetesGet:
		key, unusedRev := s.PickRandom()
		if unusedRev == 0 {
			return errors.New("storage empty")
		}
		return t.Get(ctx, kc, s, limiter, key, 0)
	case KubernetesGetStale:
		key, rev := s.KeyWithUnrelatedRev()
		return t.Get(ctx, kc, s, limiter, key, rev)
	case KubernetesGetRev:
		return t.Get(ctx, kc, s, limiter, "/registry/"+t.resource, 0)
	case KubernetesListStale:
		_, rev := s.PickRandom()
		_, err := t.List(ctx, kc, s, limiter, keyPrefix, t.averageKeyCount, rev)
		return err
	case KubernetesCount:
		return t.Count(ctx, kc, limiter, keyPrefix)
	case KubernetesKeys:
		return t.Keys(ctx, kc, limiter, keyPrefix)
	case KubernetesListAndWatch:
		rev, err := t.List(ctx, kc, s, limiter, keyPrefix, t.averageKeyCount, 0)
		if err != nil {
			return err
		}
		t.Watch(ctx, c, s, limiter, keyPrefix, rev+1)
		return nil
	default:
		panic(fmt.Sprintf("invalid choice: %q", op))
	}
}

func (t kubernetesTraffic) Get(ctx context.Context, kc kubernetes.Interface, s *storage, limiter *rate.Limiter, key string, rev int64) error {
	_, err := kc.Get(ctx, key, kubernetes.GetOptions{Revision: rev})
	limiter.Wait(ctx)
	return err
}

func (t kubernetesTraffic) Count(ctx context.Context, kc kubernetes.Interface, limiter *rate.Limiter, keyPrefix string) error {
	_, err := kc.Count(ctx, keyPrefix, kubernetes.CountOptions{})
	limiter.Wait(ctx)
	return err
}

func (t kubernetesTraffic) Keys(ctx context.Context, kc kubernetes.Interface, limiter *rate.Limiter, keyPrefix string) error {
	_, err := kc.Keys(ctx, keyPrefix, kubernetes.KeysOptions{})
	limiter.Wait(ctx)
	return err
}

func (t kubernetesTraffic) List(ctx context.Context, kc kubernetes.Interface, s *storage, limiter *rate.Limiter, keyPrefix string, limit int, revision int64) (rev int64, err error) {
	hasMore := true
	var kvs []*mvccpb.KeyValue
	var cont string

	for hasMore {
		readCtx, cancel := context.WithTimeout(ctx, RequestTimeout)
		resp, err := kc.List(readCtx, keyPrefix, kubernetes.ListOptions{Continue: cont, Revision: revision, Limit: int64(limit)})
		cancel()
		if err != nil {
			return 0, err
		}
		limiter.Wait(ctx)

		kvs = append(kvs, resp.Kvs...)
		if revision == 0 {
			revision = resp.Revision
		}
		hasMore = resp.Count > int64(len(resp.Kvs))
		if hasMore {
			cont = string(kvs[len(kvs)-1].Key) + "\x00"
		}
	}
	s.Reset(revision, kvs)
	return revision, nil
}

func (t kubernetesTraffic) Write(ctx context.Context, kc kubernetes.Interface, ids identity.Provider, s *storage, limiter *rate.Limiter, nonUniqueWriteLimiter ConcurrencyLimiter) (err error) {
	writeCtx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()
	count := s.Count()
	if count < t.averageKeyCount/2 {
		_, err = kc.OptimisticPut(writeCtx, t.generateKey(), []byte(fmt.Sprintf("%d", ids.NewRequestID())), 0, kubernetes.PutOptions{})
	} else {
		key, rev := s.PickRandom()
		if rev == 0 {
			return errors.New("storage empty")
		}
		if count > t.averageKeyCount*3/2 && nonUniqueWriteLimiter.Take() {
			_, err = kc.OptimisticDelete(writeCtx, key, rev, kubernetes.DeleteOptions{GetOnFailure: true})
			nonUniqueWriteLimiter.Return()
		} else {
			shouldReturn := false

			choices := t.writeChoices
			if shouldReturn = nonUniqueWriteLimiter.Take(); !shouldReturn {
				choices = filterOutNonUniqueKubernetesWrites(t.writeChoices)
			}
			op := random.PickRandom(choices)
			switch op {
			case KubernetesDelete:
				_, err = kc.OptimisticDelete(writeCtx, key, rev, kubernetes.DeleteOptions{GetOnFailure: true})
			case KubernetesUpdate:
				_, err = kc.OptimisticPut(writeCtx, key, []byte(fmt.Sprintf("%d", ids.NewRequestID())), rev, kubernetes.PutOptions{GetOnFailure: true})
			case KubernetesCreate:
				_, err = kc.OptimisticPut(writeCtx, t.generateKey(), []byte(fmt.Sprintf("%d", ids.NewRequestID())), 0, kubernetes.PutOptions{})
			default:
				panic(fmt.Sprintf("invalid choice: %q", op))
			}
			if shouldReturn {
				nonUniqueWriteLimiter.Return()
			}
		}
	}
	if err != nil {
		return err
	}
	limiter.Wait(ctx)
	return nil
}

func filterOutNonUniqueKubernetesWrites(choices []random.ChoiceWeight[KubernetesRequestType]) (resp []random.ChoiceWeight[KubernetesRequestType]) {
	for _, choice := range choices {
		if choice.Choice != KubernetesDelete {
			resp = append(resp, choice)
		}
	}
	return resp
}

func (t kubernetesTraffic) Watch(ctx context.Context, c *client.RecordingClient, s *storage, limiter *rate.Limiter, keyPrefix string, revision int64) {
	watchCtx, cancel := context.WithTimeout(ctx, WatchTimeout)
	defer cancel()

	// Kubernetes issues Watch requests by requiring a leader to exist
	// in the cluster:
	// https://github.com/kubernetes/kubernetes/blob/2016fab3085562b4132e6d3774b6ded5ba9939fd/staging/src/k8s.io/apiserver/pkg/storage/etcd3/store.go#L872
	watchCtx = clientv3.WithRequireLeader(watchCtx)
	for e := range c.Watch(watchCtx, keyPrefix, revision, true, true, true) {
		s.Update(e)
	}
	limiter.Wait(ctx)
}

func (t kubernetesTraffic) generateKey() string {
	return fmt.Sprintf("/registry/%s/%s/%s", t.resource, t.namespace, stringutil.RandString(5))
}

func (t kubernetesTraffic) RunCompactLoop(ctx context.Context, c *client.RecordingClient, param RunCompactLoopParam) {
	// Based on https://github.com/kubernetes/apiserver/blob/7dd4904f1896e11244ba3c5a59797697709de6b6/pkg/storage/etcd3/compact.go#L112-L127
	var compactTime int64
	var rev int64
	var err error
	for {
		select {
		case <-time.After(param.Period):
		case <-ctx.Done():
			return
		case <-param.Finish:
			return
		}

		compactTime, rev, err = compact(ctx, c, compactTime, rev)
		if err != nil {
			continue
		}
	}
}

// Based on https://github.com/kubernetes/apiserver/blob/7dd4904f1896e11244ba3c5a59797697709de6b6/pkg/storage/etcd3/compact.go#L30
const (
	compactRevKey = "compact_rev_key"
)

func compact(ctx context.Context, client *client.RecordingClient, t, rev int64) (int64, int64, error) {
	// Based on https://github.com/kubernetes/apiserver/blob/7dd4904f1896e11244ba3c5a59797697709de6b6/pkg/storage/etcd3/compact.go#L133-L162
	resp, err := client.Txn(ctx).
		If(clientv3.Compare(clientv3.Version(compactRevKey), "=", t)).
		Then(clientv3.OpPut(compactRevKey, strconv.FormatInt(rev, 10))).
		Else(clientv3.OpGet(compactRevKey)).
		Commit()
	if err != nil {
		return t, rev, err
	}

	curRev := resp.Header.Revision

	if !resp.Succeeded {
		curTime := resp.Responses[0].GetResponseRange().Kvs[0].Version
		return curTime, curRev, nil
	}
	curTime := t + 1

	if rev == 0 {
		return curTime, curRev, nil
	}
	if _, err = client.Compact(ctx, rev); err != nil {
		return curTime, curRev, err
	}
	return curTime, curRev, nil
}

type KubernetesRequestType string

const (
	KubernetesDelete       KubernetesRequestType = "delete"
	KubernetesUpdate       KubernetesRequestType = "update"
	KubernetesCreate       KubernetesRequestType = "create"
	KubernetesGet          KubernetesRequestType = "get"
	KubernetesGetStale     KubernetesRequestType = "get_stale"
	KubernetesGetRev       KubernetesRequestType = "get_rev"
	KubernetesListStale    KubernetesRequestType = "list_stale"
	KubernetesListAndWatch KubernetesRequestType = "list_watch"
	KubernetesCount        KubernetesRequestType = "count"
	KubernetesKeys         KubernetesRequestType = "keys"
)

type storage struct {
	mux         sync.RWMutex
	keyRevision map[string]int64
	revision    int64
}

// NewKubernetesStorage creates a new storage instance for tracking Kubernetes resource revisions.
// This storage is used by Kubernetes traffic to maintain consistency in read and write operations.
func NewKubernetesStorage() *storage {
	return &storage{
		keyRevision: map[string]int64{},
	}
}

func (s *storage) Update(resp clientv3.WatchResponse) {
	s.mux.Lock()
	defer s.mux.Unlock()
	for _, e := range resp.Events {
		if e.Kv.ModRevision < s.revision {
			continue
		}
		s.revision = e.Kv.ModRevision
		switch e.Type {
		case mvccpb.Event_PUT:
			s.keyRevision[string(e.Kv.Key)] = e.Kv.ModRevision
		case mvccpb.Event_DELETE:
			delete(s.keyRevision, string(e.Kv.Key))
		}
	}
}

func (s *storage) Reset(revision int64, kvs []*mvccpb.KeyValue) {
	s.mux.Lock()
	defer s.mux.Unlock()
	if revision <= s.revision {
		return
	}
	s.keyRevision = make(map[string]int64, len(kvs))
	for _, kv := range kvs {
		s.keyRevision[string(kv.Key)] = kv.ModRevision
	}
	s.revision = revision
}

func (s *storage) Count() int {
	s.mux.RLock()
	defer s.mux.RUnlock()
	return len(s.keyRevision)
}

func (s *storage) PickRandom() (key string, rev int64) {
	s.mux.RLock()
	defer s.mux.RUnlock()
	return s.pickRandomLocked()
}

func (s *storage) pickRandomLocked() (key string, rev int64) {
	l := len(s.keyRevision)
	if l == 0 {
		return "", 0
	}
	n := rand.Intn(l)
	i := 0
	for k, v := range s.keyRevision {
		if i == n {
			return k, v
		}
		i++
	}
	return "", 0
}

func (s *storage) KeyWithUnrelatedRev() (key string, rev int64) {
	s.mux.RLock()
	defer s.mux.RUnlock()
	l := len(s.keyRevision)
	if l == 0 {
		return "", 0
	}
	key, _ = s.pickRandomLocked()
	_, rev = s.pickRandomLocked()
	return key, rev
}
