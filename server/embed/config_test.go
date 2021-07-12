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

package embed

import (
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.etcd.io/etcd/client/pkg/v3/srv"
	"go.etcd.io/etcd/client/pkg/v3/transport"
	"go.etcd.io/etcd/client/pkg/v3/types"

	"sigs.k8s.io/yaml"
)

func notFoundErr(service, domain string) error {
	name := fmt.Sprintf("_%s._tcp.%s", service, domain)
	return &net.DNSError{Err: "no such host", Name: name, Server: "10.0.0.53:53", IsTimeout: false, IsTemporary: false, IsNotFound: true}
}

func TestConfigFileOtherFields(t *testing.T) {
	ctls := securityConfig{TrustedCAFile: "cca", CertFile: "ccert", KeyFile: "ckey"}
	ptls := securityConfig{TrustedCAFile: "pca", CertFile: "pcert", KeyFile: "pkey"}
	yc := struct {
		ClientSecurityCfgFile securityConfig       `json:"client-transport-security"`
		PeerSecurityCfgFile   securityConfig       `json:"peer-transport-security"`
		ForceNewCluster       bool                 `json:"force-new-cluster"`
		Logger                string               `json:"logger"`
		LogOutputs            []string             `json:"log-outputs"`
		Debug                 bool                 `json:"debug"`
		SocketOpts            transport.SocketOpts `json:"socket-options"`
	}{
		ctls,
		ptls,
		true,
		"zap",
		[]string{"/dev/null"},
		false,
		transport.SocketOpts{
			ReusePort: true,
		},
	}

	b, err := yaml.Marshal(&yc)
	if err != nil {
		t.Fatal(err)
	}

	tmpfile := mustCreateCfgFile(t, b)
	defer os.Remove(tmpfile.Name())

	cfg, err := ConfigFromFile(tmpfile.Name())
	if err != nil {
		t.Fatal(err)
	}

	if !ctls.equals(&cfg.ClientTLSInfo) {
		t.Errorf("ClientTLS = %v, want %v", cfg.ClientTLSInfo, ctls)
	}
	if !ptls.equals(&cfg.PeerTLSInfo) {
		t.Errorf("PeerTLS = %v, want %v", cfg.PeerTLSInfo, ptls)
	}

	assert.Equal(t, true, cfg.ForceNewCluster, "ForceNewCluster does not match")

	assert.Equal(t, true, cfg.SocketOpts.ReusePort, "ReusePort does not match")

	assert.Equal(t, false, cfg.SocketOpts.ReuseAddress, "ReuseAddress does not match")
}

// TestUpdateDefaultClusterFromName ensures that etcd can start with 'etcd --name=abc'.
func TestUpdateDefaultClusterFromName(t *testing.T) {
	cfg := NewConfig()
	defaultInitialCluster := cfg.InitialCluster
	oldscheme := cfg.APUrls[0].Scheme
	origpeer := cfg.APUrls[0].String()
	origadvc := cfg.ACUrls[0].String()

	cfg.Name = "abc"
	lpport := cfg.LPUrls[0].Port()

	// in case of 'etcd --name=abc'
	exp := fmt.Sprintf("%s=%s://localhost:%s", cfg.Name, oldscheme, lpport)
	_, _ = cfg.UpdateDefaultClusterFromName(defaultInitialCluster)
	if exp != cfg.InitialCluster {
		t.Fatalf("initial-cluster expected %q, got %q", exp, cfg.InitialCluster)
	}
	// advertise peer URL should not be affected
	if origpeer != cfg.APUrls[0].String() {
		t.Fatalf("advertise peer url expected %q, got %q", origadvc, cfg.APUrls[0].String())
	}
	// advertise client URL should not be affected
	if origadvc != cfg.ACUrls[0].String() {
		t.Fatalf("advertise client url expected %q, got %q", origadvc, cfg.ACUrls[0].String())
	}
}

// TestUpdateDefaultClusterFromNameOverwrite ensures that machine's default host is only used
// if advertise URLs are default values(localhost:2379,2380) AND if listen URL is 0.0.0.0.
func TestUpdateDefaultClusterFromNameOverwrite(t *testing.T) {
	if defaultHostname == "" {
		t.Skip("machine's default host not found")
	}

	cfg := NewConfig()
	defaultInitialCluster := cfg.InitialCluster
	oldscheme := cfg.APUrls[0].Scheme
	origadvc := cfg.ACUrls[0].String()

	cfg.Name = "abc"
	lpport := cfg.LPUrls[0].Port()
	cfg.LPUrls[0] = url.URL{Scheme: cfg.LPUrls[0].Scheme, Host: fmt.Sprintf("0.0.0.0:%s", lpport)}
	dhost, _ := cfg.UpdateDefaultClusterFromName(defaultInitialCluster)
	if dhost != defaultHostname {
		t.Fatalf("expected default host %q, got %q", defaultHostname, dhost)
	}
	aphost, apport := cfg.APUrls[0].Hostname(), cfg.APUrls[0].Port()
	if apport != lpport {
		t.Fatalf("advertise peer url got different port %s, expected %s", apport, lpport)
	}
	if aphost != defaultHostname {
		t.Fatalf("advertise peer url expected machine default host %q, got %q", defaultHostname, aphost)
	}
	expected := fmt.Sprintf("%s=%s://%s:%s", cfg.Name, oldscheme, defaultHostname, lpport)
	if expected != cfg.InitialCluster {
		t.Fatalf("initial-cluster expected %q, got %q", expected, cfg.InitialCluster)
	}

	// advertise client URL should not be affected
	if origadvc != cfg.ACUrls[0].String() {
		t.Fatalf("advertise-client-url expected %q, got %q", origadvc, cfg.ACUrls[0].String())
	}
}

func (s *securityConfig) equals(t *transport.TLSInfo) bool {
	return s.CertFile == t.CertFile &&
		s.CertAuth == t.ClientCertAuth &&
		s.TrustedCAFile == t.TrustedCAFile
}

func mustCreateCfgFile(t *testing.T, b []byte) *os.File {
	tmpfile, err := ioutil.TempFile("", "servercfg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tmpfile.Write(b); err != nil {
		t.Fatal(err)
	}
	if err = tmpfile.Close(); err != nil {
		t.Fatal(err)
	}
	return tmpfile
}

func TestAutoCompactionModeInvalid(t *testing.T) {
	cfg := NewConfig()
	cfg.Logger = "zap"
	cfg.LogOutputs = []string{"/dev/null"}
	cfg.AutoCompactionMode = "period"
	err := cfg.Validate()
	if err == nil {
		t.Errorf("expected non-nil error, got %v", err)
	}
}

func TestAutoCompactionModeParse(t *testing.T) {
	tests := []struct {
		mode      string
		retention string
		werr      bool
		wdur      time.Duration
	}{
		// revision
		{"revision", "1", false, 1},
		{"revision", "1h", false, time.Hour},
		{"revision", "a", true, 0},
		{"revision", "-1", true, 0},
		// periodic
		{"periodic", "1", false, time.Hour},
		{"periodic", "a", true, 0},
		{"revision", "-1", true, 0},
		// err mode
		{"errmode", "1", false, 0},
		{"errmode", "1h", false, time.Hour},
	}

	hasErr := func(err error) bool {
		return err != nil
	}

	for i, tt := range tests {
		dur, err := parseCompactionRetention(tt.mode, tt.retention)
		if hasErr(err) != tt.werr {
			t.Errorf("#%d: err = %v, want %v", i, err, tt.werr)
		}
		if dur != tt.wdur {
			t.Errorf("#%d: duration = %s, want %s", i, dur, tt.wdur)
		}
	}
}

func TestPeerURLsMapAndTokenFromSRV(t *testing.T) {
	defer func() { getCluster = srv.GetCluster }()

	tests := []struct {
		withSSL    []string
		withoutSSL []string
		apurls     []string
		wurls      string
		werr       bool
	}{
		{
			[]string{},
			[]string{},
			[]string{"http://localhost:2380"},
			"",
			true,
		},
		{
			[]string{"1.example.com=https://1.example.com:2380", "0=https://2.example.com:2380", "1=https://3.example.com:2380"},
			[]string{},
			[]string{"https://1.example.com:2380"},
			"0=https://2.example.com:2380,1.example.com=https://1.example.com:2380,1=https://3.example.com:2380",
			false,
		},
		{
			[]string{"1.example.com=https://1.example.com:2380"},
			[]string{"0=http://2.example.com:2380", "1=http://3.example.com:2380"},
			[]string{"https://1.example.com:2380"},
			"0=http://2.example.com:2380,1.example.com=https://1.example.com:2380,1=http://3.example.com:2380",
			false,
		},
		{
			[]string{},
			[]string{"1.example.com=http://1.example.com:2380", "0=http://2.example.com:2380", "1=http://3.example.com:2380"},
			[]string{"http://1.example.com:2380"},
			"0=http://2.example.com:2380,1.example.com=http://1.example.com:2380,1=http://3.example.com:2380",
			false,
		},
	}

	hasErr := func(err error) bool {
		return err != nil
	}

	for i, tt := range tests {
		getCluster = func(serviceScheme string, service string, name string, dns string, apurls types.URLs) ([]string, error) {
			var urls []string
			if serviceScheme == "https" && service == "etcd-server-ssl" {
				urls = tt.withSSL
			} else if serviceScheme == "http" && service == "etcd-server" {
				urls = tt.withoutSSL
			}
			if len(urls) > 0 {
				return urls, nil
			}
			return urls, notFoundErr(service, dns)
		}

		cfg := NewConfig()
		cfg.Name = "1.example.com"
		cfg.InitialCluster = ""
		cfg.InitialClusterToken = ""
		cfg.DNSCluster = "example.com"
		cfg.APUrls = types.MustNewURLs(tt.apurls)

		if err := cfg.Validate(); err != nil {
			t.Errorf("#%d: failed to validate test Config: %v", i, err)
			continue
		}

		urlsmap, _, err := cfg.PeerURLsMapAndToken("etcd")
		if urlsmap.String() != tt.wurls {
			t.Errorf("#%d: urlsmap = %s, want = %s", i, urlsmap.String(), tt.wurls)
		}
		if hasErr(err) != tt.werr {
			t.Errorf("#%d: err = %v, want = %v", i, err, tt.werr)
		}
	}
}

func TestLogRotation(t *testing.T) {
	tests := []struct {
		name              string
		logOutputs        []string
		logRotationConfig string
		wantErr           bool
		wantErrMsg        error
	}{
		{
			name:              "mixed log output targets",
			logOutputs:        []string{"stderr", "/tmp/path"},
			logRotationConfig: `{"maxsize": 1}`,
		},
		{
			name:              "log output relative path",
			logOutputs:        []string{"stderr", "tmp/path"},
			logRotationConfig: `{"maxsize": 1}`,
		},
		{
			name:              "no file targets",
			logOutputs:        []string{"stderr"},
			logRotationConfig: `{"maxsize": 1}`,
			wantErr:           true,
			wantErrMsg:        ErrLogRotationInvalidLogOutput,
		},
		{
			name:              "multiple file targets",
			logOutputs:        []string{"/tmp/path1", "/tmp/path2"},
			logRotationConfig: DefaultLogRotationConfig,
			wantErr:           true,
			wantErrMsg:        ErrLogRotationInvalidLogOutput,
		},
		{
			name:              "default output",
			logRotationConfig: `{"maxsize": 1}`,
			wantErr:           true,
			wantErrMsg:        ErrLogRotationInvalidLogOutput,
		},
		{
			name:              "default log rotation config",
			logOutputs:        []string{"/tmp/path"},
			logRotationConfig: DefaultLogRotationConfig,
		},
		{
			name:              "invalid logger config",
			logOutputs:        []string{"/tmp/path"},
			logRotationConfig: `{"maxsize": true}`,
			wantErr:           true,
			wantErrMsg:        errors.New("invalid log rotation config: json: cannot unmarshal bool into Go struct field logRotationConfig.maxsize of type int"),
		},
		{
			name:              "improperly formatted logger config",
			logOutputs:        []string{"/tmp/path"},
			logRotationConfig: `{"maxsize": true`,
			wantErr:           true,
			wantErrMsg:        errors.New("improperly formatted log rotation config: unexpected end of JSON input"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := NewConfig()
			cfg.Logger = "zap"
			cfg.LogOutputs = tt.logOutputs
			cfg.EnableLogRotation = true
			cfg.LogRotationConfigJSON = tt.logRotationConfig
			err := cfg.Validate()
			if err != nil && !tt.wantErr {
				t.Errorf("test %q, unexpected error %v", tt.name, err)
			}
			if err != nil && tt.wantErr && tt.wantErrMsg.Error() != err.Error() {
				t.Errorf("test %q, expected error: %+v, got: %+v", tt.name, tt.wantErrMsg, err)
			}
			if err == nil && tt.wantErr {
				t.Errorf("test %q, expected error, got nil", tt.name)
			}
			if err == nil {
				cfg.GetLogger().Info("test log")
			}
		})
	}
}

func TestLogFormat(t *testing.T) {
	tests := []struct {
		name       string
		LogFormat  string
		wantErr    bool
		wantErrMsg error
	}{
		{
			name: "default",
		},
		{
			name:      "json",
			LogFormat: "json",
		},
		{
			name:      "console",
			LogFormat: "console",
		},
		{
			name:       "unsupported",
			LogFormat:  "xml",
			wantErr:    true,
			wantErrMsg: errors.New("--log-format=xml is not supported"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := NewConfig()
			if tt.LogFormat != "" {
				cfg.LogFormat = tt.LogFormat
			}
			err := cfg.Validate()
			if err != nil && !tt.wantErr {
				t.Errorf("test %q, unexpected error %v", tt.name, err)
			}
			if err != nil && tt.wantErr && tt.wantErrMsg.Error() != err.Error() {
				t.Errorf("test %q, expected error: %+v, got: %+v", tt.name, tt.wantErrMsg, err)
			}
			if err == nil && tt.wantErr {
				t.Errorf("test %q, expected error, got nil", tt.name)
			}
		})
	}
}
