// Copyright 2016 The etcd Authors
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

package recipe

import (
	"context"
	"fmt"

	"go.etcd.io/etcd/api/v3/mvccpb"
	v3 "go.etcd.io/etcd/client/v3"
)

// PriorityQueue implements a multi-reader, multi-writer distributed queue.
type PriorityQueue struct {
	client *v3.Client
	ctx    context.Context
	key    string
}

// NewPriorityQueue creates an etcd priority queue.
func NewPriorityQueue(client *v3.Client, key string) *PriorityQueue {
	return &PriorityQueue{client, context.TODO(), key + "/"}
}

// Enqueue puts a value into a queue with a given priority.
func (q *PriorityQueue) Enqueue(val string, pr uint16) error {
	prefix := fmt.Sprintf("%s%05d", q.key, pr)
	_, err := newSequentialKV(q.client, prefix, val)
	return err
}

// EnqueueUint64 puts a value into a queue with a given uint64 priority.
func (q *PriorityQueue) EnqueueUint64(val string, pr uint64) error {
	prefix := fmt.Sprintf("%s%05d", q.key, pr)
	_, err := newSequentialKV(q.client, prefix, val)
	return err
}

// Dequeue returns Enqueue()'d items in FIFO order. If the
// queue is empty, Dequeue blocks until items are available.
func (q *PriorityQueue) Dequeue() (string, error) {
	// TODO: fewer round trips by fetching more than one key
	resp, err := q.client.Get(q.ctx, q.key, v3.WithFirstKey()...)
	if err != nil {
		return "", err
	}

	kv, err := claimFirstKey(q.client, resp.Kvs)
	if err != nil {
		return "", err
	} else if kv != nil {
		return string(kv.Value), nil
	} else if resp.More {
		// missed some items, retry to read in more
		return q.Dequeue()
	}

	// nothing to dequeue; wait on items
	ev, err := WaitPrefixEvents(
		q.client,
		q.key,
		resp.Header.Revision,
		[]mvccpb.Event_EventType{mvccpb.Event_PUT})
	if err != nil {
		return "", err
	}

	ok, err := deleteRevKey(q.client, string(ev.Kv.Key), ev.Kv.ModRevision)
	if err != nil {
		return "", err
	} else if !ok {
		return q.Dequeue()
	}
	return string(ev.Kv.Value), err
}
