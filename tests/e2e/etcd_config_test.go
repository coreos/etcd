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

package e2e

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	"go.etcd.io/etcd/pkg/v3/expect"
	"go.etcd.io/etcd/server/v3/embed"
	"go.etcd.io/etcd/tests/v3/framework/config"
	"go.etcd.io/etcd/tests/v3/framework/e2e"
)

const exampleConfigFile = "../../etcd.conf.yml.sample"

func TestEtcdExampleConfig(t *testing.T) {
	e2e.SkipInShortMode(t)

	proc, err := e2e.SpawnCmd([]string{e2e.BinPath.Etcd, "--config-file", exampleConfigFile}, nil)
	require.NoError(t, err)
	require.NoError(t, e2e.WaitReadyExpectProc(t.Context(), proc, e2e.EtcdServerReadyLines))
	require.NoError(t, proc.Stop())
}

func TestEtcdMultiPeer(t *testing.T) {
	e2e.SkipInShortMode(t)

	peers, tmpdirs := make([]string, 3), make([]string, 3)
	for i := range peers {
		peers[i] = fmt.Sprintf("e%d=http://127.0.0.1:%d", i, e2e.EtcdProcessBasePort+i)
		tmpdirs[i] = t.TempDir()
	}
	ic := strings.Join(peers, ",")

	procs := make([]*expect.ExpectProcess, len(peers))
	defer func() {
		for i := range procs {
			if procs[i] != nil {
				procs[i].Stop()
				procs[i].Close()
			}
		}
	}()
	for i := range procs {
		args := []string{
			e2e.BinPath.Etcd,
			"--name", fmt.Sprintf("e%d", i),
			"--listen-client-urls", "http://0.0.0.0:0",
			"--data-dir", tmpdirs[i],
			"--advertise-client-urls", "http://0.0.0.0:0",
			"--listen-peer-urls", fmt.Sprintf("http://127.0.0.1:%d,http://127.0.0.1:%d", e2e.EtcdProcessBasePort+i, e2e.EtcdProcessBasePort+len(peers)+i),
			"--initial-advertise-peer-urls", fmt.Sprintf("http://127.0.0.1:%d", e2e.EtcdProcessBasePort+i),
			"--initial-cluster", ic,
		}
		p, err := e2e.SpawnCmd(args, nil)
		require.NoError(t, err)
		procs[i] = p
	}

	for _, p := range procs {
		err := e2e.WaitReadyExpectProc(t.Context(), p, e2e.EtcdServerReadyLines)
		require.NoError(t, err)
	}
}

// TestEtcdUnixPeers checks that etcd will boot with unix socket peers.
func TestEtcdUnixPeers(t *testing.T) {
	e2e.SkipInShortMode(t)

	d := t.TempDir()
	proc, err := e2e.SpawnCmd(
		[]string{
			e2e.BinPath.Etcd,
			"--data-dir", d,
			"--name", "e1",
			"--listen-peer-urls", "unix://etcd.unix:1",
			"--initial-advertise-peer-urls", "unix://etcd.unix:1",
			"--initial-cluster", "e1=unix://etcd.unix:1",
		}, nil,
	)
	defer os.Remove("etcd.unix:1")
	require.NoError(t, err)
	require.NoError(t, e2e.WaitReadyExpectProc(t.Context(), proc, e2e.EtcdServerReadyLines))
	require.NoError(t, proc.Stop())
}

// TestEtcdListenMetricsURLsWithMissingClientTLSInfo checks that the HTTPs listen metrics URL
// but without the client TLS info will fail its verification.
func TestEtcdListenMetricsURLsWithMissingClientTLSInfo(t *testing.T) {
	e2e.SkipInShortMode(t)

	tempDir := t.TempDir()
	defer os.RemoveAll(tempDir)

	caFile, certFiles, keyFiles, err := generateCertsForIPs(tempDir, []net.IP{net.ParseIP("127.0.0.1")})
	require.NoError(t, err)

	// non HTTP but metrics URL is HTTPS, invalid when the client TLS info is not provided
	clientURL := fmt.Sprintf("http://localhost:%d", e2e.EtcdProcessBasePort)
	peerURL := fmt.Sprintf("https://localhost:%d", e2e.EtcdProcessBasePort+1)
	listenMetricsURL := fmt.Sprintf("https://localhost:%d", e2e.EtcdProcessBasePort+2)

	commonArgs := []string{
		e2e.BinPath.Etcd,
		"--name", "e0",
		"--data-dir", tempDir,

		"--listen-client-urls", clientURL,
		"--advertise-client-urls", clientURL,

		"--initial-advertise-peer-urls", peerURL,
		"--listen-peer-urls", peerURL,

		"--initial-cluster", "e0=" + peerURL,

		"--listen-metrics-urls", listenMetricsURL,

		"--peer-cert-file", certFiles[0],
		"--peer-key-file", keyFiles[0],
		"--peer-trusted-ca-file", caFile,
		"--peer-client-cert-auth",
	}

	proc, err := e2e.SpawnCmd(commonArgs, nil)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, proc.Stop())
		_ = proc.Close()
	}()

	require.NoError(t, e2e.WaitReadyExpectProc(t.Context(), proc, []string{embed.ErrMissingClientTLSInfoForMetricsURL.Error()}))
}

// TestEtcdPeerCNAuth checks that the inter peer auth based on CN of cert is working correctly.
func TestEtcdPeerCNAuth(t *testing.T) {
	e2e.SkipInShortMode(t)

	peers, tmpdirs := make([]string, 3), make([]string, 3)
	for i := range peers {
		peers[i] = fmt.Sprintf("e%d=https://127.0.0.1:%d", i, e2e.EtcdProcessBasePort+i)
		tmpdirs[i] = t.TempDir()
	}
	ic := strings.Join(peers, ",")

	procs := make([]*expect.ExpectProcess, len(peers))
	defer func() {
		for i := range procs {
			if procs[i] != nil {
				procs[i].Stop()
				procs[i].Close()
			}
		}
	}()

	// node 0 and 1 have a cert with the correct CN, node 2 doesn't
	for i := range procs {
		commonArgs := []string{
			e2e.BinPath.Etcd,
			"--name", fmt.Sprintf("e%d", i),
			"--listen-client-urls", "http://0.0.0.0:0",
			"--data-dir", tmpdirs[i],
			"--advertise-client-urls", "http://0.0.0.0:0",
			"--listen-peer-urls", fmt.Sprintf("https://127.0.0.1:%d,https://127.0.0.1:%d", e2e.EtcdProcessBasePort+i, e2e.EtcdProcessBasePort+len(peers)+i),
			"--initial-advertise-peer-urls", fmt.Sprintf("https://127.0.0.1:%d", e2e.EtcdProcessBasePort+i),
			"--initial-cluster", ic,
		}

		var args []string
		if i <= 1 {
			args = []string{
				"--peer-cert-file", e2e.CertPath,
				"--peer-key-file", e2e.PrivateKeyPath,
				"--peer-client-cert-file", e2e.CertPath,
				"--peer-client-key-file", e2e.PrivateKeyPath,
				"--peer-trusted-ca-file", e2e.CaPath,
				"--peer-client-cert-auth",
				"--peer-cert-allowed-cn", "example.com",
			}
		} else {
			args = []string{
				"--peer-cert-file", e2e.CertPath2,
				"--peer-key-file", e2e.PrivateKeyPath2,
				"--peer-client-cert-file", e2e.CertPath2,
				"--peer-client-key-file", e2e.PrivateKeyPath2,
				"--peer-trusted-ca-file", e2e.CaPath,
				"--peer-client-cert-auth",
				"--peer-cert-allowed-cn", "example2.com",
			}
		}

		commonArgs = append(commonArgs, args...)

		p, err := e2e.SpawnCmd(commonArgs, nil)
		require.NoError(t, err)
		procs[i] = p
	}

	for i, p := range procs {
		var expect []string
		if i <= 1 {
			expect = e2e.EtcdServerReadyLines
		} else {
			expect = []string{"remote error: tls: bad certificate"}
		}
		err := e2e.WaitReadyExpectProc(t.Context(), p, expect)
		require.NoError(t, err)
	}
}

// TestEtcdPeerMultiCNAuth checks that the inter peer auth based on CN of cert is working correctly
// when there are multiple allowed values for the CN.
func TestEtcdPeerMultiCNAuth(t *testing.T) {
	e2e.SkipInShortMode(t)

	peers, tmpdirs := make([]string, 3), make([]string, 3)
	for i := range peers {
		peers[i] = fmt.Sprintf("e%d=https://127.0.0.1:%d", i, e2e.EtcdProcessBasePort+i)
		tmpdirs[i] = t.TempDir()
	}
	ic := strings.Join(peers, ",")
	procs := make([]*expect.ExpectProcess, len(peers))
	defer func() {
		for i := range procs {
			if procs[i] != nil {
				procs[i].Stop()
				procs[i].Close()
			}
		}
	}()

	// all nodes have unique certs with different CNs
	// node 0 and 1 have a cert with one of the correct CNs, node 2 doesn't
	for i := range procs {
		commonArgs := []string{
			e2e.BinPath.Etcd,
			"--name", fmt.Sprintf("e%d", i),
			"--listen-client-urls", "http://0.0.0.0:0",
			"--data-dir", tmpdirs[i],
			"--advertise-client-urls", "http://0.0.0.0:0",
			"--listen-peer-urls", fmt.Sprintf("https://127.0.0.1:%d,https://127.0.0.1:%d", e2e.EtcdProcessBasePort+i, e2e.EtcdProcessBasePort+len(peers)+i),
			"--initial-advertise-peer-urls", fmt.Sprintf("https://127.0.0.1:%d", e2e.EtcdProcessBasePort+i),
			"--initial-cluster", ic,
		}

		var args []string
		switch i {
		case 0:
			args = []string{
				"--peer-cert-file", e2e.CertPath, // server.crt has CN "example.com".
				"--peer-key-file", e2e.PrivateKeyPath,
				"--peer-client-cert-file", e2e.CertPath,
				"--peer-client-key-file", e2e.PrivateKeyPath,
				"--peer-trusted-ca-file", e2e.CaPath,
				"--peer-client-cert-auth",
				"--peer-cert-allowed-cn", "example.com,example2.com",
			}
		case 1:
			args = []string{
				"--peer-cert-file", e2e.CertPath2, // server2.crt has CN "example2.com".
				"--peer-key-file", e2e.PrivateKeyPath2,
				"--peer-client-cert-file", e2e.CertPath2,
				"--peer-client-key-file", e2e.PrivateKeyPath2,
				"--peer-trusted-ca-file", e2e.CaPath,
				"--peer-client-cert-auth",
				"--peer-cert-allowed-cn", "example.com,example2.com",
			}
		default:
			args = []string{
				"--peer-cert-file", e2e.CertPath3, // server3.crt has CN "ca".
				"--peer-key-file", e2e.PrivateKeyPath3,
				"--peer-client-cert-file", e2e.CertPath3,
				"--peer-client-key-file", e2e.PrivateKeyPath3,
				"--peer-trusted-ca-file", e2e.CaPath,
				"--peer-client-cert-auth",
				"--peer-cert-allowed-cn", "example.com,example2.com",
			}
		}

		commonArgs = append(commonArgs, args...)
		p, err := e2e.SpawnCmd(commonArgs, nil)
		require.NoError(t, err)
		procs[i] = p
	}

	for i, p := range procs {
		var expect []string
		if i <= 1 {
			expect = e2e.EtcdServerReadyLines
		} else {
			expect = []string{"remote error: tls: bad certificate"}
		}
		err := e2e.WaitReadyExpectProc(t.Context(), p, expect)
		require.NoError(t, err)
	}
}

// TestEtcdPeerNameAuth checks that the inter peer auth based on cert name validation is working correctly.
func TestEtcdPeerNameAuth(t *testing.T) {
	e2e.SkipInShortMode(t)

	peers, tmpdirs := make([]string, 3), make([]string, 3)
	for i := range peers {
		peers[i] = fmt.Sprintf("e%d=https://127.0.0.1:%d", i, e2e.EtcdProcessBasePort+i)
		tmpdirs[i] = t.TempDir()
	}
	ic := strings.Join(peers, ",")

	procs := make([]*expect.ExpectProcess, len(peers))
	defer func() {
		for i := range procs {
			if procs[i] != nil {
				procs[i].Stop()
				procs[i].Close()
			}
			os.RemoveAll(tmpdirs[i])
		}
	}()

	// node 0 and 1 have a cert with the correct certificate name, node 2 doesn't
	for i := range procs {
		commonArgs := []string{
			e2e.BinPath.Etcd,
			"--name", fmt.Sprintf("e%d", i),
			"--listen-client-urls", "http://0.0.0.0:0",
			"--data-dir", tmpdirs[i],
			"--advertise-client-urls", "http://0.0.0.0:0",
			"--listen-peer-urls", fmt.Sprintf("https://127.0.0.1:%d,https://127.0.0.1:%d", e2e.EtcdProcessBasePort+i, e2e.EtcdProcessBasePort+len(peers)+i),
			"--initial-advertise-peer-urls", fmt.Sprintf("https://127.0.0.1:%d", e2e.EtcdProcessBasePort+i),
			"--initial-cluster", ic,
		}

		var args []string
		if i <= 1 {
			args = []string{
				"--peer-cert-file", e2e.CertPath,
				"--peer-key-file", e2e.PrivateKeyPath,
				"--peer-trusted-ca-file", e2e.CaPath,
				"--peer-client-cert-auth",
				"--peer-cert-allowed-hostname", "localhost",
			}
		} else {
			args = []string{
				"--peer-cert-file", e2e.CertPath2,
				"--peer-key-file", e2e.PrivateKeyPath2,
				"--peer-trusted-ca-file", e2e.CaPath,
				"--peer-client-cert-auth",
				"--peer-cert-allowed-hostname", "example2.com",
			}
		}

		commonArgs = append(commonArgs, args...)

		p, err := e2e.SpawnCmd(commonArgs, nil)
		require.NoError(t, err)
		procs[i] = p
	}

	for i, p := range procs {
		var expect []string
		if i <= 1 {
			expect = e2e.EtcdServerReadyLines
		} else {
			expect = []string{"client certificate authentication failed"}
		}
		err := e2e.WaitReadyExpectProc(t.Context(), p, expect)
		require.NoError(t, err)
	}
}

// TestEtcdPeerLocalAddr checks that the inter peer auth works with when
// the member LocalAddr is set.
func TestEtcdPeerLocalAddr(t *testing.T) {
	e2e.SkipInShortMode(t)

	nodeIP, err := getLocalIP()
	t.Log("Using node IP", nodeIP)
	require.NoError(t, err)

	peers, tmpdirs := make([]string, 3), make([]string, 3)

	for i := range peers {
		peerIP := nodeIP
		if i == 0 {
			peerIP = "127.0.0.1"
		}
		peers[i] = fmt.Sprintf("e%d=https://%s:%d", i, peerIP, e2e.EtcdProcessBasePort+i)
		tmpdirs[i] = t.TempDir()
	}
	procs := make([]*expect.ExpectProcess, len(peers))
	defer func() {
		for i := range procs {
			if procs[i] != nil {
				procs[i].Stop()
				procs[i].Close()
			}
			os.RemoveAll(tmpdirs[i])
		}
	}()

	tempDir := t.TempDir()
	caFile, certFiles, keyFiles, err := generateCertsForIPs(tempDir, []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP(nodeIP)})
	require.NoError(t, err)

	defer func() {
		os.RemoveAll(tempDir)
	}()

	// node 0 (127.0.0.1) does not set `--feature-gates=SetMemberLocalAddr=true`,
	// while nodes 1 and nodes 2 do.
	//
	// node 0's peer certificate is signed for 127.0.0.1, but it uses the host
	// IP (by default) to communicate with peers, so they don't match.
	// Accordingly, other peers will reject connections from node 0.
	//
	// Both node 1 and node 2's peer certificates are signed for the host IP,
	// and they also communicate with peers using the host IP (explicitly set
	// with --initial-advertise-peer-urls and
	// --feature-gates=SetMemberLocalAddr=true), so node 0 has no issue connecting
	// to them.
	//
	// Refer to https://github.com/etcd-io/etcd/issues/17068.
	for i := range procs {
		peerIP := nodeIP
		if i == 0 {
			peerIP = "127.0.0.1"
		}
		ic := strings.Join(peers, ",")
		commonArgs := []string{
			e2e.BinPath.Etcd,
			"--name", fmt.Sprintf("e%d", i),
			"--listen-client-urls", "http://0.0.0.0:0",
			"--data-dir", tmpdirs[i],
			"--advertise-client-urls", "http://0.0.0.0:0",
			"--initial-advertise-peer-urls", fmt.Sprintf("https://%s:%d", peerIP, e2e.EtcdProcessBasePort+i),
			"--listen-peer-urls", fmt.Sprintf("https://%s:%d,https://%s:%d", peerIP, e2e.EtcdProcessBasePort+i, peerIP, e2e.EtcdProcessBasePort+len(peers)+i),
			"--initial-cluster", ic,
		}

		var args []string
		if i == 0 {
			args = []string{
				"--peer-cert-file", certFiles[0],
				"--peer-key-file", keyFiles[0],
				"--peer-trusted-ca-file", caFile,
				"--peer-client-cert-auth",
			}
		} else {
			args = []string{
				"--peer-cert-file", certFiles[1],
				"--peer-key-file", keyFiles[1],
				"--peer-trusted-ca-file", caFile,
				"--peer-client-cert-auth",
				"--feature-gates=SetMemberLocalAddr=true",
			}
		}

		commonArgs = append(commonArgs, args...)

		p, err := e2e.SpawnCmd(commonArgs, nil)
		require.NoError(t, err)
		procs[i] = p
	}

	for i, p := range procs {
		var expect []string
		if i == 0 {
			expect = e2e.EtcdServerReadyLines
		} else {
			expect = []string{"x509: certificate is valid for 127.0.0.1, not "}
		}
		err := e2e.WaitReadyExpectProc(t.Context(), p, expect)
		require.NoError(t, err)
	}
}

func TestGrpcproxyAndCommonName(t *testing.T) {
	e2e.SkipInShortMode(t)

	argsWithNonEmptyCN := []string{
		e2e.BinPath.Etcd,
		"grpc-proxy",
		"start",
		"--cert", e2e.CertPath2,
		"--key", e2e.PrivateKeyPath2,
		"--cacert", e2e.CaPath,
	}

	argsWithEmptyCN := []string{
		e2e.BinPath.Etcd,
		"grpc-proxy",
		"start",
		"--cert", e2e.CertPath3,
		"--key", e2e.PrivateKeyPath3,
		"--cacert", e2e.CaPath,
	}

	err := e2e.SpawnWithExpect(argsWithNonEmptyCN, expect.ExpectedResponse{Value: "cert has non empty Common Name"})
	require.ErrorContains(t, err, "cert has non empty Common Name")

	p, err := e2e.SpawnCmd(argsWithEmptyCN, nil)
	defer func() {
		if p != nil {
			p.Stop()
		}
	}()

	require.NoError(t, err)
}

func TestGrpcproxyAndListenCipherSuite(t *testing.T) {
	e2e.SkipInShortMode(t)

	cases := []struct {
		name string
		args []string
	}{
		{
			name: "ArgsWithCipherSuites",
			args: []string{
				e2e.BinPath.Etcd,
				"grpc-proxy",
				"start",
				"--listen-cipher-suites", "TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256",
			},
		},
		{
			name: "ArgsWithoutCipherSuites",
			args: []string{
				e2e.BinPath.Etcd,
				"grpc-proxy",
				"start",
				"--listen-cipher-suites", "",
			},
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			pw, err := e2e.SpawnCmd(test.args, nil)
			require.NoError(t, err)
			require.NoError(t, pw.Stop())
		})
	}
}

func TestBootstrapDefragFlag(t *testing.T) {
	e2e.SkipInShortMode(t)

	proc, err := e2e.SpawnCmd([]string{e2e.BinPath.Etcd, "--bootstrap-defrag-threshold-megabytes", "1000"}, nil)
	require.NoError(t, err)
	require.NoError(t, e2e.WaitReadyExpectProc(t.Context(), proc, []string{"Skipping defragmentation"}))
	require.NoError(t, proc.Stop())

	// wait for the process to exit, otherwise test will have leaked goroutine
	if err := proc.Close(); err != nil {
		t.Logf("etcd process closed with error %v", err)
	}
}

func TestSnapshotCatchupEntriesFlag(t *testing.T) {
	e2e.SkipInShortMode(t)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	proc, err := e2e.SpawnCmd([]string{e2e.BinPath.Etcd, "--snapshot-catchup-entries", "1000"}, nil)
	require.NoError(t, err)
	require.NoError(t, e2e.WaitReadyExpectProc(ctx, proc, []string{"\"snapshot-catchup-entries\":1000"}))
	require.NoError(t, e2e.WaitReadyExpectProc(ctx, proc, []string{"serving client traffic"}))
	require.NoError(t, proc.Stop())

	// wait for the process to exit, otherwise test will have leaked goroutine
	if err := proc.Close(); err != nil {
		t.Logf("etcd process closed with error %v", err)
	}
}

// TestEtcdHealthyWithTinySnapshotCatchupEntries ensures multi-node etcd cluster remains healthy with 10 snapshot catch up entry
func TestEtcdHealthyWithTinySnapshotCatchupEntries(t *testing.T) {
	e2e.BeforeTest(t)
	epc, err := e2e.NewEtcdProcessCluster(t.Context(), t,
		e2e.WithClusterSize(3),
		e2e.WithSnapshotCount(10),
		e2e.WithSnapshotCatchUpEntries(10),
	)
	require.NoErrorf(t, err, "could not start etcd process cluster (%v)", err)
	t.Cleanup(func() {
		if errC := epc.Close(); errC != nil {
			t.Fatalf("error closing etcd processes (%v)", errC)
		}
	})

	// simulate 5 clients keep writing to etcd in parallel with no error
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	g, ctx := errgroup.WithContext(ctx)
	for i := 0; i < 5; i++ {
		clientID := i
		g.Go(func() error {
			cc := epc.Etcdctl()
			for j := 0; j < 50; j++ {
				if _, err := cc.Put(ctx, "foo", fmt.Sprintf("bar%d", clientID), config.PutOptions{}); err != nil {
					return err
				}
			}
			return nil
		})
	}
	require.NoError(t, g.Wait())
}

func TestEtcdTLSVersion(t *testing.T) {
	e2e.SkipInShortMode(t)

	d := t.TempDir()
	proc, err := e2e.SpawnCmd(
		[]string{
			e2e.BinPath.Etcd,
			"--data-dir", d,
			"--name", "e1",
			"--listen-client-urls", "https://0.0.0.0:0",
			"--advertise-client-urls", "https://0.0.0.0:0",
			"--listen-peer-urls", fmt.Sprintf("https://127.0.0.1:%d", e2e.EtcdProcessBasePort),
			"--initial-advertise-peer-urls", fmt.Sprintf("https://127.0.0.1:%d", e2e.EtcdProcessBasePort),
			"--initial-cluster", fmt.Sprintf("e1=https://127.0.0.1:%d", e2e.EtcdProcessBasePort),
			"--peer-cert-file", e2e.CertPath,
			"--peer-key-file", e2e.PrivateKeyPath,
			"--cert-file", e2e.CertPath2,
			"--key-file", e2e.PrivateKeyPath2,

			"--tls-min-version", "TLS1.2",
			"--tls-max-version", "TLS1.3",
		}, nil,
	)
	assert.NoError(t, err)
	assert.NoErrorf(t, e2e.WaitReadyExpectProc(t.Context(), proc, e2e.EtcdServerReadyLines), "did not receive expected output from etcd process")
	assert.NoError(t, proc.Stop())

	proc.Wait() // ensure the port has been released
	proc.Close()
}

func TestEtcdAdvertiseClientUnix(t *testing.T) {
	e2e.SkipInShortMode(t)

	// Create a temporary directory for the data directory
	dataDir := t.TempDir()
	socketDir := t.TempDir()
	unixSocket := fmt.Sprintf("unix://%s/etcd-client.sock", socketDir)

	// Start etcd with AdvertiseClientUrls set to a unix socket
	proc, err := e2e.SpawnCmd(
		[]string{
			e2e.BinPath.Etcd,
			"--data-dir", dataDir,
			"--name", "etcd1",
			"--listen-client-urls", unixSocket,
			"--advertise-client-urls", unixSocket,
		}, nil,
	)
	require.NoError(t, err)
	defer func() {
		_ = proc.Stop()
		_ = proc.Close()
	}()

	// Wait for the process to be ready
	require.NoError(t, e2e.WaitReadyExpectProc(t.Context(), proc, e2e.EtcdServerReadyLines))

	// Write a key/value pair using etcdctl with unix socket
	putArgs := []string{
		e2e.BinPath.Etcdctl,
		"--endpoints", unixSocket,
		"put", "foo", "bar",
	}
	putProc, err := e2e.SpawnCmd(putArgs, nil)
	require.NoError(t, err)
	require.NoError(t, e2e.WaitReadyExpectProc(t.Context(), putProc, []string{"OK"}))
	require.NoError(t, putProc.Stop())
	_ = putProc.Close()

	// Read the key back using etcdctl with unix socket
	getArgs := []string{
		e2e.BinPath.Etcdctl,
		"--endpoints", unixSocket,
		"get", "foo",
	}
	getProc, err := e2e.SpawnCmd(getArgs, nil)
	require.NoError(t, err)
	require.NoError(t, e2e.WaitReadyExpectProc(t.Context(), getProc, []string{"foo", "bar"}))
	require.NoError(t, getProc.Stop())
	_ = getProc.Close()
}
