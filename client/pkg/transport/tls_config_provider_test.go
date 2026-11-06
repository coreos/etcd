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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type testTLSConfigProvider struct {
	server, client *tls.Config
	err            error
}

func (p testTLSConfigProvider) ServerConfig() (*tls.Config, error) { return p.server, p.err }
func (p testTLSConfigProvider) ClientConfig() (*tls.Config, error) { return p.client, p.err }

func TestTLSConfigProvider(t *testing.T) {
	sentinel := errors.New("provider failed")
	for _, tc := range []struct {
		name     string
		provider testTLSConfigProvider
		wantErr  error
	}{
		{name: "config", provider: testTLSConfigProvider{server: &tls.Config{MinVersion: tls.VersionTLS13}, client: &tls.Config{ServerName: "peer"}}},
		{name: "error", provider: testTLSConfigProvider{err: sentinel}, wantErr: sentinel},
		{name: "nil", provider: testTLSConfigProvider{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := TLSInfo{ConfigProvider: tc.provider, CertFile: "missing", KeyFile: "missing", TrustedCAFile: "missing"}
			require.False(t, info.Empty())
			for _, method := range []struct {
				call     func() (*tls.Config, error)
				original *tls.Config
			}{{info.ServerConfig, tc.provider.server}, {info.ClientConfig, tc.provider.client}} {
				cfg, err := method.call()
				if tc.wantErr != nil {
					require.ErrorIs(t, err, tc.wantErr)
				} else if method.original == nil {
					require.ErrorContains(t, err, "nil TLS configuration")
				} else {
					require.NoError(t, err)
					require.NotSame(t, method.original, cfg)
					require.Equal(t, method.original.MinVersion, cfg.MinVersion)
					require.Equal(t, method.original.ServerName, cfg.ServerName)
				}
			}
		})
	}
	require.True(t, (TLSInfo{}).Empty())
	require.False(t, (TLSInfo{ConfigProvider: testTLSConfigProvider{}}).Empty())
}

func TestTLSConfigProviderListenerErrorClosesSocket(t *testing.T) {
	for _, p := range []testTLSConfigProvider{{}, {err: errors.New("provider failed")}} {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		_, err = newTLSListener(ln, &TLSInfo{ConfigProvider: p}, checkSAN)
		require.Error(t, err)
		_, err = ln.Accept()
		require.ErrorIs(t, err, net.ErrClosed)
	}
}

// Exercise callbacks through real mutual TLS handshakes. The certificates have
// URI identities only, so TLSInfo's IP/DNS SAN check must not override the provider.
func TestTLSConfigProviderDynamic(t *testing.T) {
	certificate := func(serial int64) (*tls.Certificate, *x509.CertPool) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
		identity, err := url.Parse("urn:etcd:peer")
		require.NoError(t, err)
		template := &x509.Certificate{
			SerialNumber: big.NewInt(serial), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
			IsCA: true, BasicConstraintsValid: true,
			KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
			ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
			URIs:        []*url.URL{identity},
		}
		der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
		require.NoError(t, err)
		leaf, err := x509.ParseCertificate(der)
		require.NoError(t, err)
		roots := x509.NewCertPool()
		roots.AddCert(leaf)
		return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, roots
	}
	first, firstRoots := certificate(1)
	second, secondRoots := certificate(2)
	var current atomic.Pointer[tls.Certificate]
	var serverTrust, clientTrust atomic.Pointer[x509.CertPool]
	current.Store(first)
	serverTrust.Store(firstRoots)
	clientTrust.Store(firstRoots)
	verify := func(roots *atomic.Pointer[x509.CertPool], usage x509.ExtKeyUsage) func(tls.ConnectionState) error {
		return func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("missing peer certificate")
			}
			cert := cs.PeerCertificates[0]
			if len(cert.URIs) != 1 || cert.URIs[0].String() != "urn:etcd:peer" {
				return errors.New("unexpected identity")
			}
			_, err := cert.Verify(x509.VerifyOptions{Roots: roots.Load(), KeyUsages: []x509.ExtKeyUsage{usage}})
			return err
		}
	}
	serverConfig := &tls.Config{
		MinVersion:       tls.VersionTLS12,
		ClientAuth:       tls.RequireAnyClientCert,
		GetCertificate:   func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return current.Load(), nil },
		VerifyConnection: verify(&serverTrust, x509.ExtKeyUsageClientAuth),
	}
	provider := testTLSConfigProvider{
		server: &tls.Config{GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) { return serverConfig, nil }},
		client: &tls.Config{
			MinVersion: tls.VersionTLS12,
			// Verification is performed against the current trust pool and URI below.
			InsecureSkipVerify:   true,
			GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return current.Load(), nil },
			VerifyConnection:     verify(&clientTrust, x509.ExtKeyUsageServerAuth),
		},
	}
	info := TLSInfo{ConfigProvider: provider, CRLFile: "unused", TrustedCAFile: "unused"}
	ln, err := NewListenerWithOpts("127.0.0.1:0", "https", WithTLSInfo(&info))
	require.NoError(t, err)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }), ReadHeaderTimeout: time.Second}
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close(); <-done }()
	tr, err := NewTransport(info, time.Second)
	require.NoError(t, err)
	defer tr.CloseIdleConnections()
	tr.DisableKeepAlives = true
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	request := func(ok bool, serial int64) {
		resp, err := client.Get("https://" + ln.Addr().String())
		if !ok {
			require.Error(t, err)
			return
		}
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusNoContent, resp.StatusCode)
		require.Equal(t, serial, resp.TLS.PeerCertificates[0].SerialNumber.Int64())
	}
	request(true, 1)
	// Rotate certificates and trust without rebuilding the listener or transport.
	current.Store(second)
	serverTrust.Store(secondRoots)
	clientTrust.Store(secondRoots)
	request(true, 2)
	clientTrust.Store(firstRoots)
	request(false, 0)
	clientTrust.Store(secondRoots)
	serverTrust.Store(firstRoots)
	request(false, 0)
	serverTrust.Store(secondRoots)
	request(true, 2)
}
