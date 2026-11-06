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

package recipes_test

import (
	"fmt"
	"math/rand"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	recipe "go.etcd.io/etcd/client/v3/experimental/recipes"
	"go.etcd.io/etcd/tests/v3/framework/integration"
)

const (
	manyQueueClients    = 3
	queueItemsPerClient = 2
)

// TestQueueOneReaderOneWriter confirms the queue is FIFO
func TestQueueOneReaderOneWriter(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	done := make(chan struct{})
	defer func() {
		<-done
	}()
	go func() {
		defer func() {
			done <- struct{}{}
		}()
		etcdc := clus.RandClient()
		q := recipe.NewQueue(etcdc, "testq")
		for i := 0; i < 5; i++ {
			if err := q.Enqueue(fmt.Sprintf("%d", i)); err != nil {
				t.Errorf("error enqueuing (%v)", err)
			}
		}
	}()

	etcdc := clus.RandClient()
	q := recipe.NewQueue(etcdc, "testq")
	for i := 0; i < 5; i++ {
		s, err := q.Dequeue()
		require.NoErrorf(t, err, "error dequeueing (%v)", err)
		require.Equalf(t, s, fmt.Sprintf("%d", i), "expected dequeue value %v, got %v", s, i)
	}
}

func TestQueueManyReaderOneWriter(t *testing.T) {
	testQueueNReaderMWriter(t, manyQueueClients, 1)
}

func TestQueueOneReaderManyWriter(t *testing.T) {
	testQueueNReaderMWriter(t, 1, manyQueueClients)
}

func TestQueueManyReaderManyWriter(t *testing.T) {
	testQueueNReaderMWriter(t, manyQueueClients, manyQueueClients)
}

// BenchmarkQueue benchmarks Queues using many/many readers/writers
func BenchmarkQueue(b *testing.B) {
	integration.BeforeTest(b)

	// XXX switch tests to use TB interface
	clus := integration.NewCluster(nil, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(nil)
	for i := 0; i < b.N; i++ {
		testQueueNReaderMWriter(nil, manyQueueClients, manyQueueClients)
	}
}

// TestPrQueueOneReaderOneWriter tests whether priority queues respect priorities.
func TestPrQueueOneReaderOneWriter(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	// write out five items with random priority
	etcdc := clus.RandClient()
	q := recipe.NewPriorityQueue(etcdc, "testprq")
	for i := 0; i < 5; i++ {
		// [0, 2] priority for priority collision to test seq keys
		pr := uint16(rand.Intn(3))
		require.NoErrorf(t, q.Enqueue(fmt.Sprintf("%d", pr), pr), "error enqueuing")
	}

	// read back items; confirm priority order is respected
	lastPr := -1
	for i := 0; i < 5; i++ {
		s, err := q.Dequeue()
		require.NoErrorf(t, err, "error dequeueing (%v)", err)
		curPr := 0
		_, err = fmt.Sscanf(s, "%d", &curPr)
		require.NoErrorf(t, err, `error parsing item "%s" (%v)`, s, err)
		require.LessOrEqualf(t, lastPr, curPr, "expected priority %v > %v", curPr, lastPr)
	}
}

func TestPrQueueManyReaderManyWriter(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)
	rqs := newPriorityQueues(clus, manyQueueClients)
	wqs := newPriorityQueues(clus, manyQueueClients)
	testReadersWriters(t, rqs, wqs)
}

// BenchmarkPrQueueOneReaderOneWriter benchmarks Queues using n/n readers/writers
func BenchmarkPrQueueOneReaderOneWriter(b *testing.B) {
	integration.BeforeTest(b)

	// XXX switch tests to use TB interface
	clus := integration.NewCluster(nil, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(nil)
	rqs := newPriorityQueues(clus, 1)
	wqs := newPriorityQueues(clus, 1)
	for i := 0; i < b.N; i++ {
		testReadersWriters(nil, rqs, wqs)
	}
}

func testQueueNReaderMWriter(t *testing.T, n int, m int) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 3})
	defer clus.Terminate(t)
	testReadersWriters(t, newQueues(clus, n), newQueues(clus, m))
}

func newQueues(clus *integration.Cluster, n int) (qs []testQueue) {
	for i := 0; i < n; i++ {
		etcdc := clus.RandClient()
		qs = append(qs, recipe.NewQueue(etcdc, "q"))
	}
	return qs
}

func newPriorityQueues(clus *integration.Cluster, n int) (qs []testQueue) {
	for i := 0; i < n; i++ {
		etcdc := clus.RandClient()
		q := &flatPriorityQueue{recipe.NewPriorityQueue(etcdc, "prq")}
		qs = append(qs, q)
	}
	return qs
}

func testReadersWriters(t *testing.T, rqs []testQueue, wqs []testQueue) {
	rerrc := make(chan error)
	werrc := make(chan error)
	manyWriters(wqs, queueItemsPerClient, werrc)
	manyReaders(rqs, len(wqs)*queueItemsPerClient, rerrc)
	for range wqs {
		if err := <-werrc; err != nil {
			t.Errorf("error writing (%v)", err)
		}
	}
	for range rqs {
		if err := <-rerrc; err != nil {
			t.Errorf("error reading (%v)", err)
		}
	}
}

func manyReaders(qs []testQueue, totalReads int, errc chan<- error) {
	var rxReads int32
	for _, q := range qs {
		go func(q testQueue) {
			for {
				total := atomic.AddInt32(&rxReads, 1)
				if int(total) > totalReads {
					break
				}
				if _, err := q.Dequeue(); err != nil {
					errc <- err
					return
				}
			}
			errc <- nil
		}(q)
	}
}

func manyWriters(qs []testQueue, writesEach int, errc chan<- error) {
	for _, q := range qs {
		go func(q testQueue) {
			for j := 0; j < writesEach; j++ {
				if err := q.Enqueue("foo"); err != nil {
					errc <- err
					return
				}
			}
			errc <- nil
		}(q)
	}
}

type testQueue interface {
	Enqueue(val string) error
	Dequeue() (string, error)
}

type flatPriorityQueue struct{ *recipe.PriorityQueue }

func (q *flatPriorityQueue) Enqueue(val string) error {
	// randomized to stress dequeuing logic; order isn't important
	return q.PriorityQueue.Enqueue(val, uint16(rand.Intn(2)))
}

func (q *flatPriorityQueue) Dequeue() (string, error) {
	return q.PriorityQueue.Dequeue()
}

type flatUint64PriorityQueue struct{ *recipe.PriorityQueue }

func (q *flatUint64PriorityQueue) Enqueue(val string) error {
	// randomized to stress dequeuing logic; order isn't important
	return q.PriorityQueue.EnqueueUint64(val, uint64(rand.Intn(2)))
}

func (q *flatUint64PriorityQueue) Dequeue() (string, error) {
	return q.PriorityQueue.Dequeue()
}

func newUint64PriorityQueues(clus *integration2.Cluster, n int) (qs []testQueue) {
	for i := 0; i < n; i++ {
		etcdc := clus.RandClient()
		q := &flatUint64PriorityQueue{recipe.NewPriorityQueue(etcdc, "prq")}
		qs = append(qs, q)
	}
	return qs
}

// TestUint64PrQueueOneReaderOneWriter tests whether uint64 priority queues respect priorities.
func TestUint64PrQueueOneReaderOneWriter(t *testing.T) {
	integration2.BeforeTest(t)

	clus := integration2.NewCluster(t, &integration2.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	// write out five items with random priority
	etcdc := clus.RandClient()
	q := recipe.NewPriorityQueue(etcdc, "testprq")
	for i := 0; i < 5; i++ {
		// [0, 2] priority for priority collision to test seq keys
		pr := uint64(rand.Intn(3))
		if err := q.EnqueueUint64(fmt.Sprintf("%d", pr), pr); err != nil {
			t.Fatalf("error enqueuing (%v)", err)
		}
	}

	// read back items; confirm priority order is respected
	lastPr := -1
	for i := 0; i < 5; i++ {
		s, err := q.Dequeue()
		if err != nil {
			t.Fatalf("error dequeueing (%v)", err)
		}
		curPr := 0
		if _, err := fmt.Sscanf(s, "%d", &curPr); err != nil {
			t.Fatalf(`error parsing item "%s" (%v)`, s, err)
		}
		if lastPr > curPr {
			t.Fatalf("expected priority %v > %v", curPr, lastPr)
		}
	}
}

func TestUint64PrQueueManyReaderManyWriter(t *testing.T) {
	integration2.BeforeTest(t)

	clus := integration2.NewCluster(t, &integration2.ClusterConfig{Size: 3})
	defer clus.Terminate(t)
	rqs := newUint64PriorityQueues(clus, manyQueueClients)
	wqs := newUint64PriorityQueues(clus, manyQueueClients)
	testReadersWriters(t, rqs, wqs)
}
