// Copyright 2015 The etcd Authors
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
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

func createSelfCert(t *testing.T) (*TLSInfo, error) {
	t.Helper()
	return createSelfCertEx(t, "127.0.0.1")
}

func createSelfCertEx(t *testing.T, host string, additionalUsages ...x509.ExtKeyUsage) (*TLSInfo, error) {
	t.Helper()
	d := t.TempDir()
	info, err := SelfCert(zaptest.NewLogger(t), d, []string{host + ":0"}, 1, additionalUsages...)
	if err != nil {
		return nil, err
	}
	return &info, nil
}

func fakeCertificateParserFunc(err error) func(certPEMBlock, keyPEMBlock []byte) (tls.Certificate, error) {
	return func(certPEMBlock, keyPEMBlock []byte) (tls.Certificate, error) {
		return tls.Certificate{}, err
	}
}

// TestNewListenerTLSInfo tests that NewListener with valid TLSInfo returns
// a TLS listener that accepts TLS connections.
func TestNewListenerTLSInfo(t *testing.T) {
	tlsInfo, err := createSelfCert(t)
	require.NoErrorf(t, err, "unable to create cert")
	testNewListenerTLSInfoAccept(t, *tlsInfo)
}

func TestNewListenerWithOpts(t *testing.T) {
	tlsInfo, err := createSelfCert(t)
	require.NoErrorf(t, err, "unable to create cert")

	tests := map[string]struct {
		opts        []ListenerOption
		scheme      string
		expectedErr bool
	}{
		"https scheme no TLSInfo": {
			opts:        []ListenerOption{},
			expectedErr: true,
			scheme:      "https",
		},
		"https scheme no TLSInfo with skip check": {
			opts:        []ListenerOption{WithSkipTLSInfoCheck(true)},
			expectedErr: false,
			scheme:      "https",
		},
		"https scheme empty TLSInfo with skip check": {
			opts: []ListenerOption{
				WithSkipTLSInfoCheck(true),
				WithTLSInfo(&TLSInfo{}),
			},
			expectedErr: false,
			scheme:      "https",
		},
		"https scheme empty TLSInfo no skip check": {
			opts: []ListenerOption{
				WithTLSInfo(&TLSInfo{}),
			},
			expectedErr: true,
			scheme:      "https",
		},
		"https scheme with TLSInfo and skip check": {
			opts: []ListenerOption{
				WithSkipTLSInfoCheck(true),
				WithTLSInfo(tlsInfo),
			},
			expectedErr: false,
			scheme:      "https",
		},
	}
	for testName, test := range tests {
		t.Run(testName, func(t *testing.T) {
			ln, err := NewListenerWithOpts("127.0.0.1:0", test.scheme, test.opts...)
			if ln != nil {
				defer ln.Close()
			}
			require.Falsef(t, test.expectedErr && err == nil, "expected error")
			if !test.expectedErr {
				require.NoErrorf(t, err, "unexpected error: %v", err)
			}
		})
	}
}

func TestNewListenerWithSocketOpts(t *testing.T) {
	tlsInfo, err := createSelfCert(t)
	require.NoErrorf(t, err, "unable to create cert")

	tests := map[string]struct {
		opts        []ListenerOption
		scheme      string
		expectedErr bool
	}{
		"nil socketopts": {
			opts:        []ListenerOption{WithSocketOpts(nil)},
			expectedErr: true,
			scheme:      "http",
		},
		"empty socketopts": {
			opts:        []ListenerOption{WithSocketOpts(&SocketOpts{})},
			expectedErr: true,
			scheme:      "http",
		},

		"reuse address": {
			opts:        []ListenerOption{WithSocketOpts(&SocketOpts{ReuseAddress: true})},
			scheme:      "http",
			expectedErr: true,
		},
		"reuse address with TLS": {
			opts: []ListenerOption{
				WithSocketOpts(&SocketOpts{ReuseAddress: true}),
				WithTLSInfo(tlsInfo),
			},
			scheme:      "https",
			expectedErr: true,
		},
		"reuse address and port": {
			opts:        []ListenerOption{WithSocketOpts(&SocketOpts{ReuseAddress: true, ReusePort: true})},
			scheme:      "http",
			expectedErr: false,
		},
		"reuse address and port with TLS": {
			opts: []ListenerOption{
				WithSocketOpts(&SocketOpts{ReuseAddress: true, ReusePort: true}),
				WithTLSInfo(tlsInfo),
			},
			scheme:      "https",
			expectedErr: false,
		},
		"reuse port with TLS and timeout": {
			opts: []ListenerOption{
				WithSocketOpts(&SocketOpts{ReusePort: true}),
				WithTLSInfo(tlsInfo),
				WithTimeout(5*time.Second, 5*time.Second),
			},
			scheme:      "https",
			expectedErr: false,
		},
		"reuse port with https scheme and no TLSInfo skip check": {
			opts: []ListenerOption{
				WithSocketOpts(&SocketOpts{ReusePort: true}),
				WithSkipTLSInfoCheck(true),
			},
			scheme:      "https",
			expectedErr: false,
		},
		"reuse port": {
			opts:        []ListenerOption{WithSocketOpts(&SocketOpts{ReusePort: true})},
			scheme:      "http",
			expectedErr: false,
		},
	}
	for testName, test := range tests {
		t.Run(testName, func(t *testing.T) {
			ln, err := NewListenerWithOpts("127.0.0.1:0", test.scheme, test.opts...)
			require.NoErrorf(t, err, "unexpected NewListenerWithSocketOpts error")
			defer ln.Close()
			ln2, err := NewListenerWithOpts(ln.Addr().String(), test.scheme, test.opts...)
			if ln2 != nil {
				ln2.Close()
			}
			if test.expectedErr {
				require.Errorf(t, err, "expected error")
			}
			if !test.expectedErr {
				require.NoErrorf(t, err, "unexpected error: %v", err)
			}

			if test.scheme == "http" {
				lnOpts := newListenOpts(test.opts...)
				if !lnOpts.IsSocketOpts() && !lnOpts.IsTimeout() {
					_, ok := ln.(*keepaliveListener)
					require.Truef(t, ok, "ln: unexpected listener type: %T, wanted *keepaliveListener", ln)
				}
			}
		})
	}
}

func testNewListenerTLSInfoAccept(t *testing.T, tlsInfo TLSInfo) {
	t.Helper()
	ln, err := NewListener("127.0.0.1:0", "https", &tlsInfo)
	require.NoErrorf(t, err, "unexpected NewListener error")
	defer ln.Close()

	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	cli := &http.Client{Transport: tr}
	go cli.Get("https://" + ln.Addr().String())

	conn, err := ln.Accept()
	require.NoErrorf(t, err, "unexpected Accept error")
	defer conn.Close()
	if _, ok := conn.(*tls.Conn); !ok {
		t.Error("failed to accept *tls.Conn")
	}
}

// TestNewListenerTLSInfoSkipClientSANVerify tests that if client IP address mismatches
// with specified address in its certificate the connection is still accepted
// if the flag SkipClientSANVerify is set (i.e. checkSAN() is disabled for the client side)
func TestNewListenerTLSInfoSkipClientSANVerify(t *testing.T) {
	tests := []struct {
		skipClientSANVerify bool
		goodClientHost      bool
		acceptExpected      bool
	}{
		{false, true, true},
		{false, false, false},
		{true, true, true},
		{true, false, true},
	}
	for _, test := range tests {
		testNewListenerTLSInfoClientCheck(t, test.skipClientSANVerify, test.goodClientHost, test.acceptExpected)
	}
}

func testNewListenerTLSInfoClientCheck(t *testing.T, skipClientSANVerify, goodClientHost, acceptExpected bool) {
	t.Helper()
	tlsInfo, err := createSelfCert(t)
	require.NoErrorf(t, err, "unable to create cert")

	host := "127.0.0.222"
	if goodClientHost {
		host = "127.0.0.1"
	}
	clientTLSInfo, err := createSelfCertEx(t, host, x509.ExtKeyUsageClientAuth)
	require.NoErrorf(t, err, "unable to create cert")

	tlsInfo.SkipClientSANVerify = skipClientSANVerify
	tlsInfo.TrustedCAFile = clientTLSInfo.CertFile

	rootCAs := x509.NewCertPool()
	loaded, err := os.ReadFile(tlsInfo.CertFile)
	require.NoErrorf(t, err, "unexpected missing certfile")
	rootCAs.AppendCertsFromPEM(loaded)

	clientCert, err := tls.LoadX509KeyPair(clientTLSInfo.CertFile, clientTLSInfo.KeyFile)
	require.NoErrorf(t, err, "unable to create peer cert")

	tlsConfig := &tls.Config{}
	tlsConfig.InsecureSkipVerify = false
	tlsConfig.Certificates = []tls.Certificate{clientCert}
	tlsConfig.RootCAs = rootCAs

	ln, err := NewListener("127.0.0.1:0", "https", tlsInfo)
	require.NoErrorf(t, err, "unexpected NewListener error")
	defer ln.Close()

	tr := &http.Transport{TLSClientConfig: tlsConfig}
	cli := &http.Client{Transport: tr}
	chClientErr := make(chan error, 1)
	go func() {
		_, err := cli.Get("https://" + ln.Addr().String())
		chClientErr <- err
	}()

	chAcceptErr := make(chan error, 1)
	chAcceptConn := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			chAcceptErr <- err
		} else {
			chAcceptConn <- conn
		}
	}()

	select {
	case <-chClientErr:
		if acceptExpected {
			t.Errorf("accepted for good client address: skipClientSANVerify=%t, goodClientHost=%t", skipClientSANVerify, goodClientHost)
		}
	case acceptErr := <-chAcceptErr:
		t.Fatalf("unexpected Accept error: %v", acceptErr)
	case conn := <-chAcceptConn:
		defer conn.Close()
		if _, ok := conn.(*tls.Conn); !ok {
			t.Errorf("failed to accept *tls.Conn")
		}
		if !acceptExpected {
			t.Errorf("accepted for bad client address: skipClientSANVerify=%t, goodClientHost=%t", skipClientSANVerify, goodClientHost)
		}
	}
}

func TestNewListenerTLSEmptyInfo(t *testing.T) {
	_, err := NewListener("127.0.0.1:0", "https", nil)
	if err == nil {
		t.Errorf("err = nil, want not presented error")
	}
}

func TestNewTransportTLSInfo(t *testing.T) {
	tlsinfo, err := createSelfCert(t)
	require.NoErrorf(t, err, "unable to create cert")

	tests := []TLSInfo{
		{},
		{
			CertFile: tlsinfo.CertFile,
			KeyFile:  tlsinfo.KeyFile,
		},
		{
			CertFile:      tlsinfo.CertFile,
			KeyFile:       tlsinfo.KeyFile,
			TrustedCAFile: tlsinfo.TrustedCAFile,
		},
		{
			TrustedCAFile: tlsinfo.TrustedCAFile,
		},
	}

	for i, tt := range tests {
		tt.parseFunc = fakeCertificateParserFunc(nil)
		trans, err := NewTransport(tt, time.Second)
		require.NoErrorf(t, err, "Received unexpected error from NewTransport")
		require.NotNilf(t, trans.TLSClientConfig, "#%d: want non-nil TLSClientConfig", i)
	}
}

func TestTLSInfoNonexist(t *testing.T) {
	tlsInfo := TLSInfo{CertFile: "@badname", KeyFile: "@badname"}
	_, err := tlsInfo.ServerConfig()
	werr := &os.PathError{
		Op:   "open",
		Path: "@badname",
		Err:  errors.New("no such file or directory"),
	}
	if err.Error() != werr.Error() {
		t.Errorf("err = %v, want %v", err, werr)
	}
}

func TestTLSInfoEmpty(t *testing.T) {
	tests := []struct {
		info TLSInfo
		want bool
	}{
		{TLSInfo{}, true},
		{TLSInfo{TrustedCAFile: "baz"}, true},
		{TLSInfo{CertFile: "foo"}, false},
		{TLSInfo{KeyFile: "bar"}, false},
		{TLSInfo{CertFile: "foo", KeyFile: "bar"}, false},
		{TLSInfo{CertFile: "foo", TrustedCAFile: "baz"}, false},
		{TLSInfo{KeyFile: "bar", TrustedCAFile: "baz"}, false},
		{TLSInfo{CertFile: "foo", KeyFile: "bar", TrustedCAFile: "baz"}, false},
	}

	for i, tt := range tests {
		got := tt.info.Empty()
		if tt.want != got {
			t.Errorf("#%d: result of Empty() incorrect: want=%t got=%t", i, tt.want, got)
		}
	}
}

func TestTLSInfoMissingFields(t *testing.T) {
	tlsinfo, err := createSelfCert(t)
	require.NoErrorf(t, err, "unable to create cert")

	tests := []TLSInfo{
		{CertFile: tlsinfo.CertFile},
		{KeyFile: tlsinfo.KeyFile},
		{CertFile: tlsinfo.CertFile, TrustedCAFile: tlsinfo.TrustedCAFile},
		{KeyFile: tlsinfo.KeyFile, TrustedCAFile: tlsinfo.TrustedCAFile},
	}

	for i, info := range tests {
		if _, err = info.ServerConfig(); err == nil {
			t.Errorf("#%d: expected non-nil error from ServerConfig()", i)
		}

		if _, err = info.ClientConfig(); err == nil {
			t.Errorf("#%d: expected non-nil error from ClientConfig()", i)
		}
	}
}

func TestTLSInfoParseFuncError(t *testing.T) {
	tlsinfo, err := createSelfCert(t)
	require.NoErrorf(t, err, "unable to create cert")

	tests := []struct {
		info TLSInfo
	}{
		{
			info: *tlsinfo,
		},

		{
			info: TLSInfo{CertFile: "", KeyFile: "", TrustedCAFile: tlsinfo.CertFile, EmptyCN: true},
		},
	}

	for i, tt := range tests {
		tt.info.parseFunc = fakeCertificateParserFunc(errors.New("fake"))

		if _, err = tt.info.ServerConfig(); err == nil {
			t.Errorf("#%d: expected non-nil error from ServerConfig()", i)
		}

		if _, err = tt.info.ClientConfig(); err == nil {
			t.Errorf("#%d: expected non-nil error from ClientConfig()", i)
		}
	}
}

func TestTLSInfoConfigFuncs(t *testing.T) {
	ln := zaptest.NewLogger(t)
	tlsinfo, err := createSelfCert(t)
	require.NoErrorf(t, err, "unable to create cert")

	tests := []struct {
		info       TLSInfo
		clientAuth tls.ClientAuthType
		wantCAs    bool
	}{
		{
			info:       TLSInfo{CertFile: tlsinfo.CertFile, KeyFile: tlsinfo.KeyFile, Logger: ln},
			clientAuth: tls.NoClientCert,
			wantCAs:    false,
		},

		{
			info:       TLSInfo{CertFile: tlsinfo.CertFile, KeyFile: tlsinfo.KeyFile, TrustedCAFile: tlsinfo.CertFile, Logger: ln},
			clientAuth: tls.RequireAndVerifyClientCert,
			wantCAs:    true,
		},
	}

	for i, tt := range tests {
		tt.info.parseFunc = fakeCertificateParserFunc(nil)

		sCfg, err := tt.info.ServerConfig()
		if err != nil {
			t.Errorf("#%d: expected nil error from ServerConfig(), got non-nil: %v", i, err)
		}

		if tt.wantCAs != (sCfg.ClientCAs != nil) {
			t.Errorf("#%d: wantCAs=%t but ClientCAs=%v", i, tt.wantCAs, sCfg.ClientCAs)
		}

		cCfg, err := tt.info.ClientConfig()
		if err != nil {
			t.Errorf("#%d: expected nil error from ClientConfig(), got non-nil: %v", i, err)
		}

		if tt.wantCAs != (cCfg.RootCAs != nil) {
			t.Errorf("#%d: wantCAs=%t but RootCAs=%v", i, tt.wantCAs, sCfg.RootCAs)
		}
	}
}

func TestNewListenerUnixSocket(t *testing.T) {
	l, err := NewListener("testsocket", "unix", nil)
	if err != nil {
		t.Errorf("error listening on unix socket (%v)", err)
	}
	l.Close()
}

// TestNewListenerTLSInfoSelfCert tests that a new certificate accepts connections.
func TestNewListenerTLSInfoSelfCert(t *testing.T) {
	tmpdir := t.TempDir()

	tlsinfo, err := SelfCert(zaptest.NewLogger(t), tmpdir, []string{"127.0.0.1"}, 1)
	require.NoError(t, err)
	require.Falsef(t, tlsinfo.Empty(), "tlsinfo should have certs (%+v)", tlsinfo)
	testNewListenerTLSInfoAccept(t, tlsinfo)

	assert.Panicsf(t, func() {
		SelfCert(nil, tmpdir, []string{"127.0.0.1"}, 1)
	}, "expected panic with nil log")
}

func TestIsClosedConnError(t *testing.T) {
	l, err := NewListener("testsocket", "unix", nil)
	if err != nil {
		t.Errorf("error listening on unix socket (%v)", err)
	}
	l.Close()
	_, err = l.Accept()
	require.Truef(t, IsClosedConnError(err), "expect true, got false (%v)", err)
}

func TestSocktOptsEmpty(t *testing.T) {
	tests := []struct {
		sopts SocketOpts
		want  bool
	}{
		{SocketOpts{}, true},
		{SocketOpts{ReuseAddress: true, ReusePort: false}, false},
		{SocketOpts{ReusePort: true}, false},
	}

	for i, tt := range tests {
		got := tt.sopts.Empty()
		if tt.want != got {
			t.Errorf("#%d: result of Empty() incorrect: want=%t got=%t", i, tt.want, got)
		}
	}
}

// TestNewListenerWithACRLFile tests when a revocation list is present.
func TestNewListenerWithACRLFile(t *testing.T) {
	clientTLSInfo, err := createSelfCertEx(t, "127.0.0.1", x509.ExtKeyUsageClientAuth)
	require.NoErrorf(t, err, "unable to create client cert")

	loadFileAsPEM := func(fileName string) []byte {
		loaded, readErr := os.ReadFile(fileName)
		require.NoErrorf(t, readErr, "unable to read file %q", fileName)
		block, _ := pem.Decode(loaded)
		return block.Bytes
	}

	clientCert, err := x509.ParseCertificate(loadFileAsPEM(clientTLSInfo.CertFile))
	require.NoErrorf(t, err, "unable to parse client cert")

	tests := map[string]struct {
		expectHandshakeError      bool
		revokedCertificateEntries []x509.RevocationListEntry
		revocationListContents    []byte
	}{
		"empty revocation list": {
			expectHandshakeError: false,
		},
		"client cert is revoked": {
			expectHandshakeError: true,
			revokedCertificateEntries: []x509.RevocationListEntry{
				{
					SerialNumber:   clientCert.SerialNumber,
					RevocationTime: time.Now(),
				},
			},
		},
		"invalid CRL file content": {
			expectHandshakeError:   true,
			revocationListContents: []byte("@invalidcontent"),
		},
	}

	for testName, test := range tests {
		t.Run(testName, func(t *testing.T) {
			tmpdir := t.TempDir()
			tlsInfo, err := createSelfCert(t)
			require.NoErrorf(t, err, "unable to create server cert")
			tlsInfo.TrustedCAFile = clientTLSInfo.CertFile
			tlsInfo.CRLFile = filepath.Join(tmpdir, "revoked.r0")

			cert, err := x509.ParseCertificate(loadFileAsPEM(tlsInfo.CertFile))
			require.NoErrorf(t, err, "unable to decode server cert")

			key, err := x509.ParseECPrivateKey(loadFileAsPEM(tlsInfo.KeyFile))
			require.NoErrorf(t, err, "unable to parse server key")

			revocationListContents := test.revocationListContents
			if len(revocationListContents) == 0 {
				tmpl := &x509.RevocationList{
					RevokedCertificateEntries: test.revokedCertificateEntries,
					ThisUpdate:                time.Now(),
					NextUpdate:                time.Now().Add(time.Hour),
					Number:                    big.NewInt(1),
				}
				revocationListContents, err = x509.CreateRevocationList(rand.Reader, tmpl, cert, key)
				require.NoErrorf(t, err, "unable to create revocation list")
			}

			err = os.WriteFile(tlsInfo.CRLFile, revocationListContents, 0o600)
			require.NoErrorf(t, err, "unable to write revocation list")

			chHandshakeFailure := make(chan error, 1)
			tlsInfo.HandshakeFailure = func(_ *tls.Conn, err error) {
				if err != nil {
					chHandshakeFailure <- err
				}
			}

			rootCAs := x509.NewCertPool()
			rootCAs.AddCert(cert)

			clientCert, err := tls.LoadX509KeyPair(clientTLSInfo.CertFile, clientTLSInfo.KeyFile)
			require.NoErrorf(t, err, "unable to create peer cert")

			ln, err := NewListener("127.0.0.1:0", "https", tlsInfo)
			require.NoErrorf(t, err, "unable to start listener")

			tlsConfig := &tls.Config{}
			tlsConfig.InsecureSkipVerify = false
			tlsConfig.Certificates = []tls.Certificate{clientCert}
			tlsConfig.RootCAs = rootCAs

			tr := &http.Transport{TLSClientConfig: tlsConfig}
			cli := &http.Client{Transport: tr, Timeout: 5 * time.Second}
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				if _, gerr := cli.Get("https://" + ln.Addr().String()); gerr != nil {
					t.Logf("http GET failed: %v", gerr)
				}
			}()

			chAcceptConn := make(chan net.Conn, 1)
			go func() {
				defer wg.Done()
				conn, err := ln.Accept()
				if err == nil {
					chAcceptConn <- conn
				}
			}()

			timer := time.NewTimer(5 * time.Second)
			defer func() {
				if !timer.Stop() {
					<-timer.C
				}
			}()

			select {
			case err := <-chHandshakeFailure:
				if !test.expectHandshakeError {
					t.Errorf("expecting no handshake error, got: %v", err)
				}
			case conn := <-chAcceptConn:
				if test.expectHandshakeError {
					t.Errorf("expecting handshake error, got nothing")
				}
				conn.Close()
			case <-timer.C:
				t.Error("timed out waiting for closed connection or handshake error")
			}

			ln.Close()
			wg.Wait()
		})
	}
}

func TestListenCurvePreferencesServerOnly(t *testing.T) {
	tlsinfo, err := createSelfCert(t)
	require.NoErrorf(t, err, "unable to create cert")

	curves := []tls.CurveID{tls.CurveP384}
	tlsinfo.ListenCurvePreferences = curves

	serverCfg, err := tlsinfo.ServerConfig()
	require.NoError(t, err)
	assert.Equalf(t, curves, serverCfg.CurvePreferences, "ServerConfig() should apply ListenCurvePreferences")

	clientCfg, err := tlsinfo.ClientConfig()
	require.NoError(t, err)
	assert.Emptyf(t, clientCfg.CurvePreferences, "ClientConfig() should not apply ListenCurvePreferences")
}
