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

package transport

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

type mutableCertPoolProvider struct {
	mu       sync.RWMutex
	certPool *x509.CertPool
}

func newMutableCertPoolProvider(certPool *x509.CertPool) *mutableCertPoolProvider {
	return &mutableCertPoolProvider{certPool: certPool}
}

func (p *mutableCertPoolProvider) GetCertPool() *x509.CertPool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.certPool
}

func (p *mutableCertPoolProvider) SetCertPool(certPool *x509.CertPool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.certPool = certPool
}

func TestServerConfig_DynamicRootCAsPreservesHTTP2ALPN(t *testing.T) {
	serverTLSInfo, err := createSelfCert(t)
	require.NoError(t, err)

	serverTLSInfo.ClientCertAuth = true
	serverTLSInfo.Logger = zaptest.NewLogger(t)
	serverTLSInfo.SetDynamicTrustRoots(newMutableCertPoolProvider(x509.NewCertPool()))

	cfg, err := serverTLSInfo.ServerConfig()
	require.NoError(t, err)
	require.NotNil(t, cfg.GetConfigForClient)
	require.Contains(t, cfg.NextProtos, "h2")

	helloCfg, err := cfg.GetConfigForClient(&tls.ClientHelloInfo{})
	require.NoError(t, err)
	require.NotNil(t, helloCfg)
	require.Contains(t, helloCfg.NextProtos, "h2")
}

func TestServerConfig_DynamicRootCAsAcceptsNewClientCAOnNewHandshake(t *testing.T) {
	serverTLSInfo, err := createSelfCert(t)
	require.NoError(t, err)

	clientCA1, err := createSelfCertEx(t, "127.0.0.1", x509.ExtKeyUsageClientAuth)
	require.NoError(t, err)
	clientCA2, err := createSelfCertEx(t, "127.0.0.1", x509.ExtKeyUsageClientAuth)
	require.NoError(t, err)

	provider := newMutableCertPoolProvider(mustCertPoolFromFile(t, clientCA1.CertFile))

	serverTLSInfo.ClientCertAuth = true
	serverTLSInfo.Logger = zaptest.NewLogger(t)
	serverTLSInfo.SetDynamicTrustRoots(provider)

	ln := mustNewTLSListener(t, serverTLSInfo)
	defer ln.Close()

	rootCAs := mustCertPoolFromFile(t, serverTLSInfo.CertFile)

	clientConn1 := mustDialTLS(t, ln.Addr().String(), &tls.Config{
		Certificates: []tls.Certificate{mustLoadKeyPair(t, clientCA1.CertFile, clientCA1.KeyFile)},
		RootCAs:      rootCAs,
	})
	serverConn1 := mustAcceptConn(t, ln)
	clientConn1.Close()
	serverConn1.Close()

	provider.SetCertPool(mustCertPoolFromFile(t, clientCA2.CertFile))

	clientConn2 := mustDialTLS(t, ln.Addr().String(), &tls.Config{
		Certificates: []tls.Certificate{mustLoadKeyPair(t, clientCA2.CertFile, clientCA2.KeyFile)},
		RootCAs:      rootCAs,
	})
	serverConn2 := mustAcceptConn(t, ln)
	clientConn2.Close()
	serverConn2.Close()
}

func TestClientConfig_DynamicRootCAsAcceptsNewServerCAOnNewHandshake(t *testing.T) {
	serverCA1, err := createSelfCertEx(t, "127.0.0.1")
	require.NoError(t, err)
	serverCA2, err := createSelfCertEx(t, "127.0.0.1")
	require.NoError(t, err)

	stableCertFile, stableKeyFile := mustWriteStableTLSFiles(t, serverCA1.CertFile, serverCA1.KeyFile)
	serverInfo := &TLSInfo{
		CertFile: stableCertFile,
		KeyFile:  stableKeyFile,
		Logger:   zaptest.NewLogger(t),
	}

	ln := mustNewTLSListener(t, serverInfo)
	defer ln.Close()

	provider := newMutableCertPoolProvider(mustCertPoolFromFile(t, serverCA1.CertFile))
	clientInfo := TLSInfo{ServerName: "127.0.0.1"}
	clientInfo.SetDynamicTrustRoots(provider)

	clientCfg, err := clientInfo.ClientConfig()
	require.NoError(t, err)

	clientConn1 := mustDialTLS(t, ln.Addr().String(), clientCfg)
	serverConn1 := mustAcceptConn(t, ln)
	clientConn1.Close()
	serverConn1.Close()

	// Replacing the files under the same paths mirrors how operators rotate
	// server identities without changing listener configuration.
	mustReplaceTLSFiles(t, serverCA2.CertFile, serverCA2.KeyFile, stableCertFile, stableKeyFile)
	provider.SetCertPool(mustCertPoolFromFile(t, serverCA2.CertFile))

	clientConn2 := mustDialTLS(t, ln.Addr().String(), clientCfg)
	serverConn2 := mustAcceptConn(t, ln)
	require.Equal(t, mustLoadCertificate(t, serverCA2.CertFile).Raw, clientConn2.ConnectionState().PeerCertificates[0].Raw)
	clientConn2.Close()
	serverConn2.Close()

	mustReplaceTLSFiles(t, serverCA1.CertFile, serverCA1.KeyFile, stableCertFile, stableKeyFile)
	_, err = tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", ln.Addr().String(), clientCfg)
	require.Error(t, err)
}

func TestDynamicRootCAs_DoesNotBreakExistingConnection(t *testing.T) {
	serverCA1, err := createSelfCertEx(t, "127.0.0.1")
	require.NoError(t, err)
	serverCA2, err := createSelfCertEx(t, "127.0.0.1")
	require.NoError(t, err)

	stableCertFile, stableKeyFile := mustWriteStableTLSFiles(t, serverCA1.CertFile, serverCA1.KeyFile)
	serverInfo := &TLSInfo{
		CertFile: stableCertFile,
		KeyFile:  stableKeyFile,
		Logger:   zaptest.NewLogger(t),
	}

	ln := mustNewTLSListener(t, serverInfo)
	defer ln.Close()

	provider := newMutableCertPoolProvider(mustCertPoolFromFile(t, serverCA1.CertFile))
	clientInfo := TLSInfo{ServerName: "127.0.0.1"}
	clientInfo.SetDynamicTrustRoots(provider)

	clientCfg, err := clientInfo.ClientConfig()
	require.NoError(t, err)

	clientConn1 := mustDialTLS(t, ln.Addr().String(), clientCfg)
	defer clientConn1.Close()
	serverConn1 := mustAcceptConn(t, ln)
	defer serverConn1.Close()

	requirePayloadTransfer(t, clientConn1, serverConn1, "ping-ca1")
	requirePayloadTransfer(t, serverConn1, clientConn1, "pong-ca1")

	mustReplaceTLSFiles(t, serverCA2.CertFile, serverCA2.KeyFile, stableCertFile, stableKeyFile)
	provider.SetCertPool(mustCertPoolFromFile(t, serverCA2.CertFile))

	// Existing TLS sessions should stay alive because only future handshakes
	// consult the rotated trust roots.
	requirePayloadTransfer(t, clientConn1, serverConn1, "ping-ca2")
	requirePayloadTransfer(t, serverConn1, clientConn1, "pong-ca2")

	clientConn2 := mustDialTLS(t, ln.Addr().String(), clientCfg)
	serverConn2 := mustAcceptConn(t, ln)
	require.Equal(t, mustLoadCertificate(t, serverCA2.CertFile).Raw, clientConn2.ConnectionState().PeerCertificates[0].Raw)
	clientConn2.Close()
	serverConn2.Close()
}

func TestDynamicRootCAs_NilPreservesExistingBehavior(t *testing.T) {
	t.Run("client-config", func(t *testing.T) {
		serverCA1, err := createSelfCertEx(t, "127.0.0.1")
		require.NoError(t, err)
		serverCA2, err := createSelfCertEx(t, "127.0.0.1")
		require.NoError(t, err)

		stableCertFile, stableKeyFile := mustWriteStableTLSFiles(t, serverCA1.CertFile, serverCA1.KeyFile)
		serverInfo := &TLSInfo{
			CertFile: stableCertFile,
			KeyFile:  stableKeyFile,
			Logger:   zaptest.NewLogger(t),
		}

		ln := mustNewTLSListener(t, serverInfo)
		defer ln.Close()

		clientInfo := TLSInfo{
			ServerName:    "127.0.0.1",
			TrustedCAFile: serverCA1.CertFile,
		}
		clientCfg, err := clientInfo.ClientConfig()
		require.NoError(t, err)

		clientConn := mustDialTLS(t, ln.Addr().String(), clientCfg)
		serverConn := mustAcceptConn(t, ln)
		clientConn.Close()
		serverConn.Close()

		mustReplaceTLSFiles(t, serverCA2.CertFile, serverCA2.KeyFile, stableCertFile, stableKeyFile)

		_, err = tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", ln.Addr().String(), clientCfg)
		require.Error(t, err)
	})
}

func TestDynamicRootCAs_StripsPortFromFallbackServerName(t *testing.T) {
	serverCA, err := createSelfCertEx(t, "127.0.0.1")
	require.NoError(t, err)

	serverInfo := &TLSInfo{
		CertFile: serverCA.CertFile,
		KeyFile:  serverCA.KeyFile,
		Logger:   zaptest.NewLogger(t),
	}

	ln := mustNewTLSListener(t, serverInfo)
	defer ln.Close()

	provider := newMutableCertPoolProvider(mustCertPoolFromFile(t, serverCA.CertFile))
	clientInfo := TLSInfo{ServerName: "127.0.0.1"}
	clientInfo.SetDynamicTrustRoots(provider)

	clientCfg, err := clientInfo.ClientConfig()
	require.NoError(t, err)

	clientConn := mustDialTLS(t, ln.Addr().String(), clientCfg)
	defer clientConn.Close()
	serverConn := mustAcceptConn(t, ln)
	defer serverConn.Close()

	cs := clientConn.ConnectionState()
	cs.ServerName = ""
	require.NoError(t, clientInfo.verifyDynamicServerCertificate(cs, "127.0.0.1:2379"))
}

func TestDynamicRootCAs_PreservesHostnameVerification(t *testing.T) {
	serverCA, err := createSelfCertEx(t, "127.0.0.1")
	require.NoError(t, err)

	serverInfo := &TLSInfo{
		CertFile: serverCA.CertFile,
		KeyFile:  serverCA.KeyFile,
		Logger:   zaptest.NewLogger(t),
	}

	ln := mustNewTLSListener(t, serverInfo)
	defer ln.Close()

	provider := newMutableCertPoolProvider(mustCertPoolFromFile(t, serverCA.CertFile))
	clientInfo := TLSInfo{ServerName: "127.0.0.2"}
	clientInfo.SetDynamicTrustRoots(provider)

	clientCfg, err := clientInfo.ClientConfig()
	require.NoError(t, err)

	_, err = tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", ln.Addr().String(), clientCfg)
	require.Error(t, err)
}

func TestDynamicRootCAs_NilProviderPoolFailsHandshake(t *testing.T) {
	serverCA, err := createSelfCertEx(t, "127.0.0.1")
	require.NoError(t, err)

	serverInfo := &TLSInfo{
		CertFile: serverCA.CertFile,
		KeyFile:  serverCA.KeyFile,
		Logger:   zaptest.NewLogger(t),
	}

	ln := mustNewTLSListener(t, serverInfo)
	defer ln.Close()

	clientInfo := TLSInfo{ServerName: "127.0.0.1"}
	clientInfo.SetDynamicTrustRoots(newMutableCertPoolProvider(nil))

	clientCfg, err := clientInfo.ClientConfig()
	require.NoError(t, err)

	_, err = tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", ln.Addr().String(), clientCfg)
	require.Error(t, err)
}

func mustNewTLSListener(t *testing.T, info *TLSInfo) net.Listener {
	t.Helper()

	ln, err := NewListener("127.0.0.1:0", "https", info)
	require.NoError(t, err)
	return ln
}

func mustDialTLS(t *testing.T, addr string, cfg *tls.Config) *tls.Conn {
	t.Helper()

	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", addr, cfg)
	require.NoError(t, err)
	return conn
}

func mustAcceptConn(t *testing.T, ln net.Listener) net.Conn {
	t.Helper()

	conn, err := ln.Accept()
	require.NoError(t, err)
	return conn
}

func requirePayloadTransfer(t *testing.T, writer net.Conn, reader net.Conn, payload string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	require.NoError(t, writer.SetDeadline(deadline))
	require.NoError(t, reader.SetDeadline(deadline))

	_, err := writer.Write([]byte(payload))
	require.NoError(t, err)

	buf := make([]byte, len(payload))
	_, err = io.ReadFull(reader, buf)
	require.NoError(t, err)
	require.Equal(t, payload, string(buf))
}

func mustWriteStableTLSFiles(t *testing.T, certFile string, keyFile string) (string, string) {
	t.Helper()

	dir := t.TempDir()
	stableCertFile := filepath.Join(dir, "cert.pem")
	stableKeyFile := filepath.Join(dir, "key.pem")
	mustReplaceTLSFiles(t, certFile, keyFile, stableCertFile, stableKeyFile)
	return stableCertFile, stableKeyFile
}

func mustReplaceTLSFiles(t *testing.T, srcCertFile string, srcKeyFile string, dstCertFile string, dstKeyFile string) {
	t.Helper()

	certBytes, err := os.ReadFile(srcCertFile)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(dstCertFile, certBytes, 0o600))

	keyBytes, err := os.ReadFile(srcKeyFile)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(dstKeyFile, keyBytes, 0o600))
}

func mustCertPoolFromFile(t *testing.T, certFile string) *x509.CertPool {
	t.Helper()

	pemBytes, err := os.ReadFile(certFile)
	require.NoError(t, err)

	certPool := x509.NewCertPool()
	require.True(t, certPool.AppendCertsFromPEM(pemBytes))
	return certPool
}

func mustLoadKeyPair(t *testing.T, certFile string, keyFile string) tls.Certificate {
	t.Helper()

	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	require.NoError(t, err)
	return cert
}

func mustLoadCertificate(t *testing.T, certFile string) *x509.Certificate {
	t.Helper()

	pemBytes, err := os.ReadFile(certFile)
	require.NoError(t, err)

	block, _ := pem.Decode(pemBytes)
	require.NotNil(t, block)

	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	return cert
}
