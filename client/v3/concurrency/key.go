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

package concurrency

import (
	"context"
	"errors"

	"go.etcd.io/etcd/api/v3/mvccpb"
	v3 "go.etcd.io/etcd/client/v3"
)

var (
	errLostWatcher    = errors.New("lost watcher waiting for delete")
	errWaitKeyDeleted = errors.New("wait key deleted")
)

func waitDelete(ctx context.Context, client *v3.Client, key string, rev int64, waitCh v3.WatchChan) error {
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	wch := client.Watch(cctx, key, v3.WithRev(rev))
	for {
		select {
		case wr, ok := <-wch:
			if !ok {
				if err := ctx.Err(); err != nil {
					return err
				}
				return errLostWatcher
			}
			if err := wr.Err(); err != nil {
				return err
			}
			for _, ev := range wr.Events {
				if ev.Type == mvccpb.Event_DELETE {
					return nil
				}
			}
		case wr, ok := <-waitCh:
			if !ok {
				if err := ctx.Err(); err != nil {
					return err
				}
				return errLostWatcher
			}
			if err := wr.Err(); err != nil {
				return err
			}
			for _, ev := range wr.Events {
				if ev.Type == mvccpb.Event_DELETE {
					return errWaitKeyDeleted
				}
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// waitDeletes efficiently waits until all keys matching the prefix and no greater
// than the create revision are deleted. It also stops if waitKey is deleted.
func waitDeletes(ctx context.Context, client *v3.Client, pfx, waitKey string, maxCreateRev int64) error {
	waitResp, err := client.Get(ctx, waitKey)
	if err != nil {
		return err
	}
	if len(waitResp.Kvs) == 0 {
		return errWaitKeyDeleted
	}

	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	waitCh := client.Watch(cctx, waitKey, v3.WithRev(waitResp.Header.Revision))

	getOpts := append(v3.WithLastCreate(), v3.WithMaxCreateRev(maxCreateRev))
	for {
		resp, err := client.Get(cctx, pfx, getOpts...)
		if err != nil {
			return err
		}
		if len(resp.Kvs) == 0 {
			return nil
		}
		lastKey := string(resp.Kvs[0].Key)
		if err = waitDelete(cctx, client, lastKey, resp.Header.Revision, waitCh); err != nil {
			return err
		}
	}
}
