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
	"errors"
)

// TLSConfigProvider supplies TLS configurations for listeners and transports.
// Each method must return a non-nil configuration or an error. Methods may be
// called multiple times and concurrently while listeners and transports are built;
// they are not called for every handshake. Use tls.Config callbacks such as
// GetConfigForClient, GetCertificate, GetClientCertificate and VerifyConnection
// for dynamic certificates and trust. The provider owns TLS policy, including
// protocol versions, peer verification and application protocol negotiation.
//
// Returned configurations are cloned before use. Providers must not mutate them
// or their referenced data after returning; callback state must be concurrency safe.
// Updating callback state affects new handshakes, not existing connections.
type TLSConfigProvider interface {
	ServerConfig() (*tls.Config, error)
	ClientConfig() (*tls.Config, error)
}

var _ TLSConfigProvider = TLSInfo{}

func cloneTLSConfig(cfg *tls.Config, err error) (*tls.Config, error) {
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, errors.New("TLSConfigProvider returned a nil TLS configuration")
	}
	return cfg.Clone(), nil
}
