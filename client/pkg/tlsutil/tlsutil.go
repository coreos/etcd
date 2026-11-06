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

package tlsutil

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// Note: sync is still needed for sync.WaitGroup and sync.Once

var errNoCertsLoaded = errors.New("no certificates were loaded from CA files")

// NewCertPool creates x509 certPool with provided CA files.
func NewCertPool(CAFiles []string) (*x509.CertPool, error) {
	certPool := x509.NewCertPool()

	for _, CAFile := range CAFiles {
		pemByte, err := os.ReadFile(CAFile)
		if err != nil {
			return nil, err
		}

		for {
			var block *pem.Block
			block, pemByte = pem.Decode(pemByte)
			if block == nil {
				break
			}
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				return nil, err
			}

			certPool.AddCert(cert)
		}
	}

	return certPool, nil
}

// NewCert generates TLS cert by using the given cert,key and parse function.
func NewCert(certfile, keyfile string, parseFunc func([]byte, []byte) (tls.Certificate, error)) (*tls.Certificate, error) {
	cert, err := os.ReadFile(certfile)
	if err != nil {
		return nil, err
	}

	key, err := os.ReadFile(keyfile)
	if err != nil {
		return nil, err
	}

	if parseFunc == nil {
		parseFunc = tls.X509KeyPair
	}

	tlsCert, err := parseFunc(cert, key)
	if err != nil {
		return nil, err
	}
	return &tlsCert, nil
}

// DefaultCAReloadInterval is the default interval for checking CA file changes.
const DefaultCAReloadInterval = 10 * time.Second

// caState holds the current CA pool and file hashes for atomic swapping.
type caState struct {
	pool   *x509.CertPool
	hashes [][32]byte
}

// CAReloader manages dynamic reloading of CA certificate pools.
// It periodically checks for file changes and updates the cached pool.
// Reads are lock-free using atomic.Pointer (RCU pattern).
type CAReloader struct {
	caFiles  []string
	state    atomic.Pointer[caState]
	interval time.Duration
	logger   *zap.Logger

	wg       sync.WaitGroup
	stopc    chan struct{}
	stopOnce sync.Once
}

// NewCAReloader creates a new CAReloader for the given CA files.
// It performs an initial load of the CA files and returns an error if loading fails.
// Call Start() to begin the background polling goroutine.
func NewCAReloader(caFiles []string, logger *zap.Logger) (*CAReloader, error) {
	if logger == nil {
		logger = zap.NewNop()
	}

	r := &CAReloader{
		caFiles:  caFiles,
		interval: DefaultCAReloadInterval,
		logger:   logger,
		stopc:    make(chan struct{}),
	}

	// Initialize with empty state
	r.state.Store(&caState{})

	// Perform initial load - must succeed
	if _, err := r.reload(); err != nil {
		return nil, err
	}

	return r, nil
}

// WithInterval sets the polling interval for checking CA file changes.
func (r *CAReloader) WithInterval(d time.Duration) *CAReloader {
	r.interval = d
	return r
}

// Start begins the background goroutine that periodically checks for CA file changes.
func (r *CAReloader) Start() {
	r.wg.Add(1)
	go r.run()
}

// Stop stops the background polling goroutine and waits for it to finish.
// It is safe to call Stop multiple times.
// It is also safe to call Stop without having called Start.
func (r *CAReloader) Stop() {
	r.stopOnce.Do(func() {
		close(r.stopc)
	})
	r.wg.Wait()
}

// GetCertPool returns the currently cached CA certificate pool.
// This method is lock-free and safe for concurrent use.
func (r *CAReloader) GetCertPool() *x509.CertPool {
	return r.state.Load().pool
}

// run is the background goroutine that periodically checks for CA file changes.
func (r *CAReloader) run() {
	defer r.wg.Done()

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	r.logger.Info("starting CA certificate poll",
		zap.Duration("interval", r.interval),
		zap.Strings("ca-files", r.caFiles),
	)

	for {
		select {
		case <-ticker.C:
			if _, err := r.reload(); err != nil {
				r.logger.Warn("failed to reload CA certificates, using cached pool",
					zap.Strings("ca-files", r.caFiles),
					zap.Error(err),
				)
			}
		case <-r.stopc:
			r.logger.Info("stopping CA certificate poll")
			return
		}
	}
}

// reload reads the CA files from disk and updates the cached pool if changed.
// Returns (changed bool, err error) where changed indicates if the pool was updated.
func (r *CAReloader) reload() (bool, error) {
	start := time.Now()

	// Compute hashes of all CA file contents
	newHashes := make([][32]byte, len(r.caFiles))
	for i, caFile := range r.caFiles {
		data, err := os.ReadFile(caFile)
		if err != nil {
			caReloadFailureTotal.Inc()
			return false, err
		}
		newHashes[i] = sha256.Sum256(data)
	}

	// Check if any file has changed (lock-free read)
	current := r.state.Load()
	changed := len(current.hashes) != len(newHashes)
	if !changed {
		for i := range newHashes {
			if current.hashes[i] != newHashes[i] {
				changed = true
				break
			}
		}
	}

	if !changed {
		return false, nil
	}

	// Parse the new CA pool
	pool, err := NewCertPool(r.caFiles)
	if err != nil {
		caReloadFailureTotal.Inc()
		return false, err
	}

	// Ensure we actually parsed some certificates.
	// pem.Decode returns nil for invalid PEM data, so NewCertPool
	// may return an empty pool without error. We treat an empty pool
	// as an error to enable graceful fallback to the previous pool.
	if len(r.caFiles) > 0 && pool.Equal(x509.NewCertPool()) {
		caReloadFailureTotal.Inc()
		return false, errNoCertsLoaded
	}

	// Atomically swap to new state (RCU pattern)
	r.state.Store(&caState{
		pool:   pool,
		hashes: newHashes,
	})

	// Record metrics for successful reload
	caReloadSuccessTotal.Inc()
	caReloadDurationSeconds.Observe(time.Since(start).Seconds())

	r.logger.Info("reloaded CA certificates",
		zap.Strings("ca-files", r.caFiles),
	)

	return true, nil
}
