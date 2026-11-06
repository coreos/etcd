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

package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

// testCA holds a CA certificate and a leaf certificate signed by it for testing.
type testCA struct {
	caPEM   []byte           // PEM-encoded CA certificate
	caCert  *x509.Certificate // Parsed CA certificate
	caKey   *ecdsa.PrivateKey // CA private key
	leafPEM []byte           // PEM-encoded leaf certificate signed by this CA
	leaf    *x509.Certificate // Parsed leaf certificate
}

// generateTestCA creates a self-signed CA and a leaf certificate signed by it.
// The leaf cert can be used to verify the CA is actually in the pool.
func generateTestCA(t *testing.T) *testCA {
	t.Helper()

	// Generate CA key and cert
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	caSerial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	require.NoError(t, err)

	caTemplate := x509.Certificate{
		SerialNumber: caSerial,
		Subject: pkix.Name{
			Organization: []string{"Test CA"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	caDER, err := x509.CreateCertificate(rand.Reader, &caTemplate, &caTemplate, &caKey.PublicKey, caKey)
	require.NoError(t, err)

	caCert, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})

	// Generate leaf cert signed by the CA
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	leafSerial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	require.NoError(t, err)

	leafTemplate := x509.Certificate{
		SerialNumber: leafSerial,
		Subject: pkix.Name{
			CommonName: "test-leaf",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}

	leafDER, err := x509.CreateCertificate(rand.Reader, &leafTemplate, caCert, &leafKey.PublicKey, caKey)
	require.NoError(t, err)

	leaf, err := x509.ParseCertificate(leafDER)
	require.NoError(t, err)

	leafPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})

	return &testCA{
		caPEM:   caPEM,
		caCert:  caCert,
		caKey:   caKey,
		leafPEM: leafPEM,
		leaf:    leaf,
	}
}

// verifyLeaf checks if the leaf certificate validates against the given pool.
func (tc *testCA) verifyLeaf(pool *x509.CertPool) error {
	_, err := tc.leaf.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	return err
}

func TestNewCAReloader(t *testing.T) {
	logger := zaptest.NewLogger(t)
	ca := generateTestCA(t)

	// Create temp dir and CA file
	tmpDir := t.TempDir()
	caFile := filepath.Join(tmpDir, "ca.crt")
	err := os.WriteFile(caFile, ca.caPEM, 0600)
	require.NoError(t, err)

	// Create CAReloader
	reloader, err := NewCAReloader([]string{caFile}, logger)
	require.NoError(t, err)
	require.NotNil(t, reloader)

	// Verify pool can validate certs signed by the CA
	pool := reloader.GetCertPool()
	require.NotNil(t, pool)
	err = ca.verifyLeaf(pool)
	require.NoError(t, err, "pool should validate leaf cert signed by loaded CA")
}

func TestNewCAReloader_InvalidFile(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Try to create CAReloader with non-existent file
	_, err := NewCAReloader([]string{"/nonexistent/ca.crt"}, logger)
	require.Error(t, err)
}

func TestNewCAReloader_InvalidCert(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Create temp dir and invalid CA file
	tmpDir := t.TempDir()
	caFile := filepath.Join(tmpDir, "ca.crt")
	err := os.WriteFile(caFile, []byte("invalid cert data"), 0600)
	require.NoError(t, err)

	// Create CAReloader - should fail because cert is invalid
	_, err = NewCAReloader([]string{caFile}, logger)
	require.Error(t, err)
}

func TestCAReloader_Reload(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Generate two different CAs with leaf certs
	ca1 := generateTestCA(t)
	ca2 := generateTestCA(t)

	// Create temp dir and CA file
	tmpDir := t.TempDir()
	caFile := filepath.Join(tmpDir, "ca.crt")
	err := os.WriteFile(caFile, ca1.caPEM, 0600)
	require.NoError(t, err)

	// Create CAReloader with short interval
	reloader, err := NewCAReloader([]string{caFile}, logger)
	require.NoError(t, err)
	reloader.WithInterval(50 * time.Millisecond)
	reloader.Start()
	t.Cleanup(func() { reloader.Stop() })

	// Verify initial pool validates CA1's leaf cert
	pool := reloader.GetCertPool()
	require.NoError(t, ca1.verifyLeaf(pool), "initial pool should validate CA1 leaf")
	require.Error(t, ca2.verifyLeaf(pool), "initial pool should NOT validate CA2 leaf")

	// Update CA file to CA2
	err = os.WriteFile(caFile, ca2.caPEM, 0600)
	require.NoError(t, err)

	// Wait for reload
	time.Sleep(100 * time.Millisecond)

	// Verify updated pool validates CA2's leaf cert but NOT CA1's
	pool = reloader.GetCertPool()
	require.NoError(t, ca2.verifyLeaf(pool), "reloaded pool should validate CA2 leaf")
	require.Error(t, ca1.verifyLeaf(pool), "reloaded pool should NOT validate CA1 leaf")
}

func TestCAReloader_GracefulFallback(t *testing.T) {
	logger := zaptest.NewLogger(t)
	ca := generateTestCA(t)

	// Create temp dir and CA file
	tmpDir := t.TempDir()
	caFile := filepath.Join(tmpDir, "ca.crt")
	err := os.WriteFile(caFile, ca.caPEM, 0600)
	require.NoError(t, err)

	// Create CAReloader with short interval
	reloader, err := NewCAReloader([]string{caFile}, logger)
	require.NoError(t, err)
	reloader.WithInterval(50 * time.Millisecond)
	reloader.Start()
	t.Cleanup(func() { reloader.Stop() })

	// Verify initial pool works
	pool := reloader.GetCertPool()
	require.NoError(t, ca.verifyLeaf(pool), "initial pool should validate leaf")

	// Write invalid data to CA file
	err = os.WriteFile(caFile, []byte("invalid cert data"), 0600)
	require.NoError(t, err)

	// Wait for reload attempt
	time.Sleep(100 * time.Millisecond)

	// Pool should still work (graceful fallback to previous valid pool)
	pool = reloader.GetCertPool()
	require.NoError(t, ca.verifyLeaf(pool), "pool should still validate leaf after failed reload")
}

func TestCAReloader_Concurrency(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Generate two different CAs for alternating writes
	ca1 := generateTestCA(t)
	ca2 := generateTestCA(t)

	// Create temp dir and CA file
	tmpDir := t.TempDir()
	caFile := filepath.Join(tmpDir, "ca.crt")
	err := os.WriteFile(caFile, ca1.caPEM, 0600)
	require.NoError(t, err)

	// Create CAReloader with short interval
	reloader, err := NewCAReloader([]string{caFile}, logger)
	require.NoError(t, err)
	reloader.WithInterval(10 * time.Millisecond)
	reloader.Start()
	t.Cleanup(func() { reloader.Stop() })

	// Run concurrent reads and verify pool always validates at least one CA
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				pool := reloader.GetCertPool()
				assert.NotNil(t, pool)
				// Pool should validate either CA1 or CA2 (depending on reload state)
				ca1Valid := ca1.verifyLeaf(pool) == nil
				ca2Valid := ca2.verifyLeaf(pool) == nil
				assert.True(t, ca1Valid || ca2Valid, "pool should validate at least one CA")
			}
		}()
	}

	// Also update the file concurrently
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 10; j++ {
			caPEM := ca1.caPEM
			if j%2 == 0 {
				caPEM = ca2.caPEM
			}
			_ = os.WriteFile(caFile, caPEM, 0600)
			time.Sleep(5 * time.Millisecond)
		}
	}()

	wg.Wait()
}

func TestCAReloader_Stop(t *testing.T) {
	logger := zaptest.NewLogger(t)
	ca := generateTestCA(t)

	// Create temp dir and CA file
	tmpDir := t.TempDir()
	caFile := filepath.Join(tmpDir, "ca.crt")
	err := os.WriteFile(caFile, ca.caPEM, 0600)
	require.NoError(t, err)

	// Create CAReloader
	reloader, err := NewCAReloader([]string{caFile}, logger)
	require.NoError(t, err)
	reloader.WithInterval(10 * time.Millisecond)
	reloader.Start()

	// Stop should complete without hanging
	done := make(chan struct{})
	go func() {
		reloader.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(1 * time.Second):
		t.Fatal("Stop() did not complete in time")
	}
}

func TestCAReloader_StopWithoutStart(t *testing.T) {
	logger := zaptest.NewLogger(t)
	ca := generateTestCA(t)

	// Create temp dir and CA file
	tmpDir := t.TempDir()
	caFile := filepath.Join(tmpDir, "ca.crt")
	err := os.WriteFile(caFile, ca.caPEM, 0600)
	require.NoError(t, err)

	// Create CAReloader but don't call Start()
	reloader, err := NewCAReloader([]string{caFile}, logger)
	require.NoError(t, err)

	// Stop should complete without hanging even though Start was never called
	done := make(chan struct{})
	go func() {
		reloader.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(1 * time.Second):
		t.Fatal("Stop() without Start() should not deadlock")
	}
}

func TestCAReloader_NoChangeNoReload(t *testing.T) {
	logger := zaptest.NewLogger(t)
	ca := generateTestCA(t)

	// Create temp dir and CA file
	tmpDir := t.TempDir()
	caFile := filepath.Join(tmpDir, "ca.crt")
	err := os.WriteFile(caFile, ca.caPEM, 0600)
	require.NoError(t, err)

	// Create CAReloader with short interval
	reloader, err := NewCAReloader([]string{caFile}, logger)
	require.NoError(t, err)
	reloader.WithInterval(50 * time.Millisecond)
	reloader.Start()
	t.Cleanup(func() { reloader.Stop() })

	// Verify initial pool works
	pool := reloader.GetCertPool()
	require.NoError(t, ca.verifyLeaf(pool), "initial pool should validate leaf")

	// Wait for multiple reload cycles without changing file
	time.Sleep(150 * time.Millisecond)

	// Pool should still work and validate the same CA
	pool = reloader.GetCertPool()
	require.NoError(t, ca.verifyLeaf(pool), "pool should still validate leaf after no-op reload cycles")
}

func TestCAReloader_MultipleCAs(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Generate two CAs
	ca1 := generateTestCA(t)
	ca2 := generateTestCA(t)

	// Create temp dir and write both CAs to the same file
	tmpDir := t.TempDir()
	caFile := filepath.Join(tmpDir, "ca.crt")
	combinedPEM := append(ca1.caPEM, ca2.caPEM...)
	err := os.WriteFile(caFile, combinedPEM, 0600)
	require.NoError(t, err)

	// Create CAReloader
	reloader, err := NewCAReloader([]string{caFile}, logger)
	require.NoError(t, err)

	// Pool should validate leaf certs from BOTH CAs
	pool := reloader.GetCertPool()
	require.NoError(t, ca1.verifyLeaf(pool), "pool should validate CA1 leaf")
	require.NoError(t, ca2.verifyLeaf(pool), "pool should validate CA2 leaf")
}
