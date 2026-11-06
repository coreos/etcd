package e2e

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"go.etcd.io/etcd/pkg/v3/expect"
	"go.etcd.io/etcd/tests/v3/framework/e2e"
)

// TestCtlV3PutShortLivedClientCert verifies that when a client connects with a
// certificate expiring soon, clientCertExpirationSecs.Observe() is called and
// the observation lands in the expected histogram bucket.
func TestCtlV3PutShortLivedClientCert(t *testing.T) {
	e2e.BeforeTest(t)

	tmpDir := t.TempDir()
	caFile, serverCertFile, serverKeyFile, clientCertFile, clientKeyFile, err :=
		generateShortLivedCertForIPs(tmpDir, []net.IP{net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("failed to generate certs: %v", err)
	}

	cfg := e2e.NewConfigClientTLSCertAuth()
	cfg.MetricsURLScheme = "http"

	epc, err := e2e.InitEtcdProcessCluster(t, cfg)
	if err != nil {
		t.Fatalf("could not init etcd process cluster: %v", err)
	}

	// Override the framework-supplied fixture certs with our short-lived pair.
	for _, proc := range epc.Procs {
		args := proc.Config().Args
		for i, arg := range args {
			if i+1 >= len(args) {
				continue
			}
			switch arg {
			case "--cert-file":
				args[i+1] = serverCertFile
			case "--key-file":
				args[i+1] = serverKeyFile
			case "--trusted-ca-file":
				args[i+1] = caFile
			}
		}
	}

	epc, err = e2e.StartEtcdProcessCluster(t.Context(), t, epc, cfg)
	if err != nil {
		t.Fatalf("could not start etcd process cluster: %v", err)
	}
	defer epc.Close()

	cmdArgs := []string{
		e2e.BinPath.Etcdctl,
		"--endpoints", epc.EndpointsGRPC()[0],
		"--cacert", caFile,
		"--cert", clientCertFile,
		"--key", clientKeyFile,
		"put", "foo", "bar",
	}
	if err := e2e.SpawnWithExpect(cmdArgs, expect.ExpectedResponse{Value: "OK"}); err != nil {
		t.Fatalf("put with short-lived cert failed: %v", err)
	}

	// The client cert expires in ~10 minutes → secsUntilExpiry ≈ 600s.
	// Verify the observation landed in the (0, 1800] range:
	//   le="0"    bucket must be 0   → cert was not expired (value > 0)
	//   le="1800" bucket must be ≥ 1 → cert expires within 30 minutes
	// We don't assert an exact count because etcdctl may open more than one
	// gRPC connection per invocation, producing multiple observations.
	metricsURL := epc.Procs[0].Config().MetricsURL + "/metrics"
	resp, err := http.Get(metricsURL) //nolint:noctx
	if err != nil {
		t.Fatalf("failed to GET metrics: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read metrics body: %v", err)
	}
	metrics := string(body)

	zeroExpired := `etcd_server_client_cert_expiration_seconds_bucket{le="0"} 0`
	if !strings.Contains(metrics, zeroExpired) {
		t.Fatalf("expected %q (cert should not be expired), relevant lines:\n%s",
			zeroExpired, filterLines(metrics, "client_cert_expiration"))
	}

	nonZero1800 := regexp.MustCompile(`etcd_server_client_cert_expiration_seconds_bucket\{le="1800"\} [1-9]`)
	if !nonZero1800.MatchString(metrics) {
		t.Fatalf("expected le=\"1800\" bucket to have ≥1 observation, relevant lines:\n%s",
			filterLines(metrics, "client_cert_expiration"))
	}
}

// TestV3MetricsClientCertHistogramModified verifies that a regular put over
// mTLS produces at least one observation in the client-cert-expiry histogram.
func TestV3MetricsClientCertHistogramModified(t *testing.T) {
	testCtl(t, clientCertHistogramModified, withCfg(*e2e.NewConfigClientTLSCertAuth()))
}

func clientCertHistogramModified(cx ctlCtx) {
	if err := ctlV3Put(cx, "k", "v", ""); err != nil {
		cx.t.Fatal(err)
	}
	if err := e2e.CURLGet(cx.epc, e2e.CURLReq{
		Endpoint: "/metrics",
		Expected: expect.ExpectedResponse{Value: "etcd_server_client_cert_expiration_seconds_count"},
	}); err != nil {
		cx.t.Fatalf("expected etcd_server_client_cert_expiration_seconds_count metric: %v", err)
	}
}

func filterLines(s, substr string) string {
	var out strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, substr) {
			out.WriteString(line + "\n")
		}
	}
	return out.String()
}

// generateShortLivedCertForIPs creates a fresh CA, a long-lived server cert
// (with the given IP SANs and "localhost" DNS SAN), and a short-lived client
// cert (~10 minutes). Returns paths to the PEM files written under tmpDir.
func generateShortLivedCertForIPs(tmpDir string, ips []net.IP) (caFile, serverCertFile, serverKeyFile, clientCertFile, clientKeyFile string, err error) {
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", "", "", "", err
	}
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1001),
		Subject: pkix.Name{
			Organization: []string{"etcd"},
			CommonName:   "etcd-test-ca",
		},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	caBytes, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return "", "", "", "", "", err
	}
	caFile, _, err = saveCertToFile(tmpDir, caBytes, nil)
	if err != nil {
		return "", "", "", "", "", err
	}

	serverCertFile, serverKeyFile, err = issueChild(tmpDir, caTmpl, caKey, ips, []string{"localhost"}, time.Now().Add(time.Hour), "etcd-server")
	if err != nil {
		return "", "", "", "", "", err
	}
	// Short-lived so secsUntilExpiry lands in the (0, 1800] bucket.
	clientCertFile, clientKeyFile, err = issueChild(tmpDir, caTmpl, caKey, nil, nil, time.Now().Add(10*time.Minute), "etcd-client")
	if err != nil {
		return "", "", "", "", "", err
	}
	return caFile, serverCertFile, serverKeyFile, clientCertFile, clientKeyFile, nil
}

func issueChild(tmpDir string, ca *x509.Certificate, caKey *rsa.PrivateKey, ips []net.IP, dnsNames []string, notAfter time.Time, cn string) (string, string, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn, Organization: []string{"etcd"}},
		IPAddresses:  ips,
		DNSNames:     dnsNames,
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return "", "", err
	}
	return saveCertToFile(tmpDir, der, key)
}
