// Copyright 2026 The etcd Authors
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

package mvcc

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"go.uber.org/zap"

	"go.etcd.io/etcd/pkg/v3/traceutil"
	"go.etcd.io/etcd/server/v3/lease"
	"go.etcd.io/etcd/server/v3/storage/backend"
)

//go:embed testdata/exemplar_pod.pb
var exemplarPodData []byte

func BenchmarkStorageWrite(b *testing.B) {
	value := exemplarPodData
	if len(value) == 0 {
		b.Skip("missing exemplar_pod.pb")
	}
	numKeys := 150_000
	tmpDir := b.TempDir()
	fsType := getFSType(tmpDir)
	b.Run("FS="+fsType, func(b *testing.B) {
		b.Run("Keys="+strconv.Itoa(numKeys)+"/ValueSize="+strconv.Itoa(len(value)), func(b *testing.B) {
			for _, driver := range DefaultStorageDrivers {
				b.Run("Backend="+driver.Name, func(b *testing.B) {
					for _, compaction := range []bool{false, true} {
						b.Run("Compaction="+strconv.FormatBool(compaction), func(b *testing.B) {
							for _, defrag := range []bool{false, true} {
								if defrag && driver.SkipDefragInBenchmarks {
									continue
								}
								b.Run("Defrag="+strconv.FormatBool(defrag), func(b *testing.B) {
									bs, err := driver.Setup(tmpDir)
									if err != nil {
										b.Fatal(err)
									}
									defer bs.Close()
									writeKeys(bs.store, numKeys, value)
									bs.store.Commit()
									benchmarkStorageWrite(b, bs, compaction, defrag, value, numKeys)
								})
							}
						})
					}
				})
			}
		})
	})
}

func dirSize(dir string) int64 {
	var size int64
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return size
}

func benchmarkStorageWrite(b *testing.B, bs *storage, compaction, defrag bool, value []byte, numKeys int) {
	var wg sync.WaitGroup
	done := make(chan struct{})
	stats := &bgStats{}

	if defrag {
		wg.Add(1)
		go func() {
			defer wg.Done()
			backgroundDefrag(bs, stats, done)
		}()
	}

	if compaction {
		wg.Add(1)
		go func() {
			defer wg.Done()
			backgroundCompact(bs, stats, done)
		}()
	}

	i := 0
	for b.Loop() {
		for range numKeys {
			tw := bs.store.Write(traceutil.TODO())
			tw.Put(makeKey(i%numKeys), value, lease.NoLease)
			tw.End()
			i++
		}
	}
	close(done)
	wg.Wait()
	if compaction {
		err := runCompact(bs, stats)
		if err != nil {
			b.Fatal(err)
		}
	}
	if defrag {
		err := runDefrag(bs, stats)
		if err != nil {
			b.Fatal(err)
		}
	}
	bs.store.Commit()
	b.ReportMetric(float64(i)/b.Elapsed().Seconds(), "write_qps")
	diskSizeBytes := dirSize(bs.dir)
	b.ReportMetric(float64(diskSizeBytes/1024/1024), "disk_size_mb")
	keyValueSizeBytes := len(makeKey(numKeys)) + len(value)
	stateSizeBytes := numKeys * keyValueSizeBytes
	historySize := int(bs.store.Rev())*keyValueSizeBytes - stateSizeBytes
	logicalSize := stateSizeBytes + historySize
	if compaction {
		logicalSize = stateSizeBytes
	}
	b.ReportMetric(float64(diskSizeBytes)/float64(logicalSize), "disk_overhead")
	compactionCount := stats.compactionCounter.Load()
	if compactionCount > 0 {
		avgCompactionDuration := time.Duration(stats.compactionDurationNanoseconds.Load() / compactionCount)
		b.ReportMetric(float64(compactionCount), "compaction_count")
		b.ReportMetric(avgCompactionDuration.Seconds(), "avg_compaction_duration_s")
	}
	defragCount := stats.defragCounter.Load()
	if defragCount > 0 {
		avgDefragDuration := time.Duration(stats.defragDurationNanoseconds.Load() / defragCount)
		b.ReportMetric(float64(defragCount), "defrag_count")
		b.ReportMetric(avgDefragDuration.Seconds(), "avg_defrag_duration_s")
	}
}

func BenchmarkStorageRead(b *testing.B) {
	value := exemplarPodData
	if len(value) == 0 {
		b.Skip("missing exemplar_pod.pb")
	}
	numKeys := 150_000
	tmpDir := b.TempDir()
	fsType := getFSType(tmpDir)
	b.Run("FS="+fsType, func(b *testing.B) {
		b.Run("Keys="+strconv.Itoa(numKeys)+"/ValueSize="+strconv.Itoa(len(value)), func(b *testing.B) {
			for _, driver := range DefaultStorageDrivers {
				b.Run("Backend="+driver.Name, func(b *testing.B) {
					bs, err := driver.Setup(tmpDir)
					if err != nil {
						b.Fatal(err)
					}
					defer bs.Close()
					writeKeys(bs.store, numKeys, value)
					bs.store.Commit()
					for _, mode := range []struct {
						name string
						mode ReadTxMode
					}{
						{name: "Concurrent", mode: ConcurrentReadTxMode},
						{name: "SharedBuf", mode: SharedBufReadTxMode},
					} {
						b.Run("Mode="+mode.name, func(b *testing.B) {
							for _, target := range []string{"PointGet", "RangeLimit100", "RangeAll", "CountOnly"} {
								b.Run("Target="+target, func(b *testing.B) {
									for _, revType := range []string{"Latest", "Historical"} {
										b.Run("Rev="+revType, func(b *testing.B) {
											benchmarkStorageRead(b, bs, mode.mode, target, revType, numKeys)
										})
									}
								})
							}
						})
					}
				})
			}
		})
	})
}

func benchmarkStorageRead(b *testing.B, bs *storage, mode ReadTxMode, target, revType string, numKeys int) {
	curRev := bs.store.Rev()
	historicalRev := curRev / 2
	if historicalRev <= 1 {
		historicalRev = 2
	}

	var ro RangeOptions
	if revType == "Historical" {
		ro.Rev = historicalRev
	}

	switch target {
	case "RangeLimit100":
		ro.Limit = 100
	case "CountOnly":
		ro.CountOnly = true
	}

	startKey := makeKey(100)
	var endKey []byte
	if target != "PointGet" {
		endKey = makeKey(600) // Span 500 keys
	}

	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			readTx := bs.store.Read(mode, traceutil.TODO())
			if target == "PointGet" {
				k := makeKey(i % numKeys)
				_, err := readTx.Range(context.Background(), k, nil, ro)
				if err != nil {
					readTx.End()
					b.Fatal(err)
				}
			} else {
				_, err := readTx.Range(context.Background(), startKey, endKey, ro)
				if err != nil {
					readTx.End()
					b.Fatal(err)
				}
			}
			readTx.End()
			i++
		}
	})

	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "read_qps")
}

func BenchmarkStorageWatch(b *testing.B) {
	value := exemplarPodData
	if len(value) == 0 {
		b.Skip("missing exemplar_pod.pb")
	}
	numKeys := 150_000
	tmpDir := b.TempDir()
	fsType := getFSType(tmpDir)
	b.Run("FS="+fsType, func(b *testing.B) {
		b.Run("Keys="+strconv.Itoa(numKeys)+"/ValueSize="+strconv.Itoa(len(value)), func(b *testing.B) {
			for _, driver := range DefaultStorageDrivers {
				b.Run("Backend="+driver.Name, func(b *testing.B) {
					bs, err := driver.Setup(tmpDir)
					if err != nil {
						b.Fatal(err)
					}
					defer bs.Close()
					writeKeys(bs.store, numKeys, value)
					bs.store.Commit()
					for _, behind := range []int64{10, 100, 1000, 10_000, 100_000} {
						if behind >= int64(numKeys) {
							continue
						}
						b.Run("Resync="+strconv.FormatInt(behind, 10), func(b *testing.B) {
							benchmarkStorageWatch(b, bs, behind)
						})
					}
				})
			}
		})
	})
}

func benchmarkStorageWatch(b *testing.B, bs *storage, behind int64) {
	lastRev := bs.store.Rev()
	startRev := lastRev - behind + 1
	if startRev < 1 {
		startRev = 1
	}

	startK := []byte("/registry/pods/")
	endK := []byte("/registry/pods/\xff")

	b.ReportAllocs()
	b.ResetTimer()

	totalEvents := 0
	runs := 0
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			ws := bs.store.NewWatchStream()
			wID, err := ws.Watch(context.Background(), WatchID(runs+1), startK, endK, startRev)
			if err != nil {
				ws.Close()
				b.Fatal(err)
			}

			received := 0
			eventsChan := ws.Chan()
		consumeLoop:
			for {
				select {
				case resp, ok := <-eventsChan:
					if !ok {
						b.Fatal("Broken watch")
					}
					received += len(resp.Events)
					if resp.Revision >= lastRev {
						break consumeLoop
					}
				case <-time.After(5 * time.Second):
					b.Fatalf("watch catchup timed out: received %d of %d events", received, lastRev-startRev)
				}
			}
			if err = ws.Cancel(wID); err != nil {
				b.Fatal(err)
			}
			ws.Close()
			totalEvents += received
			runs++
		}
	})

	b.ReportMetric(float64(totalEvents)/b.Elapsed().Seconds(), "resync_events_per_s")
	if runs > 0 {
		b.ReportMetric(b.Elapsed().Seconds()/float64(runs), "avg_resync_duration_s")
	}
}

func BenchmarkStorageSize(b *testing.B) {
	value := exemplarPodData
	if len(value) == 0 {
		b.Skip("missing exemplar_pod.pb")
	}
	numKeys := 150_000
	tmpDir := b.TempDir()
	fsType := getFSType(tmpDir)
	b.Run("FS="+fsType, func(b *testing.B) {
		b.Run("Keys="+strconv.Itoa(numKeys)+"/ValueSize="+strconv.Itoa(len(value)), func(b *testing.B) {
			for _, driver := range DefaultStorageDrivers {
				b.Run("Backend="+driver.Name, func(b *testing.B) {
					bs, err := driver.Setup(b.TempDir())
					if err != nil {
						b.Fatal(err)
					}
					defer bs.Close()
					writeCount := 10
					for range writeCount {
						writeKeys(bs.store, numKeys, value)
					}
					bs.store.Commit()
					stateSize := float64(numKeys * (len(makeKey(numKeys)) + len(value)))
					historicalSize := stateSize * float64(writeCount-1)
					for targetPercentage := 0; targetPercentage <= 100; targetPercentage += 10 {
						b.Run(fmt.Sprintf("HistoryCompacted=%d%%", targetPercentage), func(b *testing.B) {
							benchmarkStorageSize(b, bs, targetPercentage, stateSize+historicalSize*float64(100-targetPercentage)/100)
						})
					}
				})
			}
		})
	})
}

func benchmarkStorageSize(b *testing.B, bs *storage, targetPercentage int, logicalSize float64) {
	maxRev := bs.store.Rev()
	b.Run("Compact", func(b *testing.B) {
		if b.N > 1 {
			b.Fatalf("this benchmark is designed for exactly 1 iteration; run with -benchtime=1x")
		}
		if targetPercentage != 0 {
			targetRev := int64(float64(maxRev) * (float64(targetPercentage) / 100.0))
			donec, err := bs.store.Compact(traceutil.TODO(), targetRev)
			if err != nil && !errors.Is(err, ErrCompacted) {
				b.Fatal(err)
			}
			<-donec
			bs.store.Commit()
		}
		diskSize := dirSize(bs.dir)
		b.ReportMetric(float64(diskSize)/(1024*1024), "disk_size_mb")
		b.ReportMetric(float64(diskSize)/logicalSize, "disk_overhead")
	})
	b.Run("Defrag", func(b *testing.B) {
		if b.N > 1 {
			b.Fatalf("this benchmark is designed for exactly 1 iteration; run with -benchtime=1x")
		}
		err := bs.Defrag()
		if err != nil {
			b.Fatal(err)
		}
		bs.store.Commit()
		diskSize := dirSize(bs.dir)
		b.ReportMetric(float64(diskSize)/(1024*1024), "disk_size_mb")
		b.ReportMetric(float64(diskSize)/logicalSize, "disk_overhead")
	})
}

func getFSType(dir string) string {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return "unknown"
	}
	switch uint32(stat.Type) {
	case 0x01021994:
		return "tmpfs"
	case 0xEF53:
		return "ext4"
	case 0x58465342:
		return "xfs"
	case 0x9123683E:
		return "btrfs"
	case 0x794C7630:
		return "overlayfs"
	case 0x858458F6:
		return "ramfs"
	case 0x6969:
		return "nfs"
	default:
		return fmt.Sprintf("fs_0x%x", stat.Type)
	}
}

func writeKeys(store WatchableKV, numKeys int, val []byte) {
	for i := 0; i < numKeys; i++ {
		tw := store.Write(traceutil.TODO())
		tw.Put(makeKey(i), val, lease.NoLease)
		tw.End()
	}
}

type bgStats struct {
	compactionDurationNanoseconds atomic.Int64
	compactionCounter             atomic.Int64
	defragDurationNanoseconds     atomic.Int64
	defragCounter                 atomic.Int64
}

func backgroundCompact(bs *storage, stats *bgStats, done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		default:
		}
		err := runCompact(bs, stats)
		if err != nil {
			panic(err)
		}
	}
}

func backgroundDefrag(bs *storage, stats *bgStats, done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		default:
		}
		err := runDefrag(bs, stats)
		if err != nil {
			panic(err)
		}
	}
}

func runCompact(bs *storage, stats *bgStats) error {
	compactRev := bs.store.Rev()
	start := time.Now()
	donec, err := bs.store.Compact(traceutil.TODO(), compactRev)
	if err != nil {
		if errors.Is(err, ErrCompacted) {
			return nil
		}
		return err
	}
	<-donec
	stats.compactionDurationNanoseconds.Add(time.Since(start).Nanoseconds())
	stats.compactionCounter.Add(1)
	return nil
}

func runDefrag(bs *storage, stats *bgStats) error {
	start := time.Now()
	err := bs.Defrag()
	if err != nil {
		return err
	}
	stats.defragDurationNanoseconds.Add(time.Since(start).Nanoseconds())
	stats.defragCounter.Add(1)
	return nil
}

func makeKey(index int) []byte {
	return []byte(fmt.Sprintf("/registry/pods/namespace-%d/pod-deployment-%d", index%50, index))
}

type storage struct {
	name   string
	store  WatchableKV
	dir    string
	close  func()
	defrag func() error
}

func (s *storage) Defrag() error {
	return s.defrag()
}

func (s *storage) Close() {
	s.close()
}

type StorageDriver struct {
	Name                   string
	Setup                  func(dir string) (*storage, error)
	SkipDefragInBenchmarks bool
}

var DefaultStorageDrivers = []StorageDriver{
	{
		Name:                   "bbolt",
		SkipDefragInBenchmarks: true, // Defrag is blocking for bbolt
		Setup: func(dir string) (*storage, error) {
			dbPath := filepath.Join(dir, "bbolt.db")
			be := backend.NewDefaultBackend(zap.NewNop(), dbPath)
			st := New(zap.NewNop(), be, &lease.FakeLessor{}, StoreConfig{})
			return &storage{
				name:   "bbolt",
				store:  st,
				dir:    dir,
				defrag: func() error { return be.Defrag() },
				close: func() {
					_ = st.Close()
					_ = be.Close()
				},
			}, nil
		},
	},
}
