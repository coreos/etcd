// Copyright 2017 The etcd Authors
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

package auth

import (
	"fmt"
	"maps"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

const (
	jwtRSAPubKey  = "../../tests/fixtures/server.crt"
	jwtRSAPrivKey = "../../tests/fixtures/server.key.insecure"

	jwtECPubKey  = "../../tests/fixtures/server-ecdsa.crt"
	jwtECPrivKey = "../../tests/fixtures/server-ecdsa.key.insecure"

	jwtEdPubKey  = "../../tests/fixtures/ed25519-public-key.pem"
	jwtEdPrivKey = "../../tests/fixtures/ed25519-private-key.pem"
)

func TestJWTInfo(t *testing.T) {
	optsMap := map[string]map[string]string{
		"RSA-priv": {
			"priv-key":    jwtRSAPrivKey,
			"sign-method": "RS256",
			"ttl":         "1h",
		},
		"RSA": {
			"pub-key":     jwtRSAPubKey,
			"priv-key":    jwtRSAPrivKey,
			"sign-method": "RS256",
		},
		"RSAPSS-priv": {
			"priv-key":    jwtRSAPrivKey,
			"sign-method": "PS256",
		},
		"RSAPSS": {
			"pub-key":     jwtRSAPubKey,
			"priv-key":    jwtRSAPrivKey,
			"sign-method": "PS256",
		},
		"ECDSA-priv": {
			"priv-key":    jwtECPrivKey,
			"sign-method": "ES256",
		},
		"ECDSA": {
			"pub-key":     jwtECPubKey,
			"priv-key":    jwtECPrivKey,
			"sign-method": "ES256",
		},
		"Ed25519-priv": {
			"priv-key":    jwtEdPrivKey,
			"sign-method": "EdDSA",
		},
		"Ed25519": {
			"pub-key":     jwtEdPubKey,
			"priv-key":    jwtEdPrivKey,
			"sign-method": "EdDSA",
		},
		"HMAC": {
			"priv-key":    jwtECPrivKey, // any file, raw bytes used as shared secret
			"sign-method": "HS256",
		},
	}

	for k, opts := range optsMap {
		t.Run(k, func(tt *testing.T) {
			testJWTInfo(tt, opts)
		})
	}
}

func testJWTInfo(t *testing.T, opts map[string]string) {
	lg := zap.NewNop()
	jwt, err := newTokenProviderJWT(lg, opts)
	if err != nil {
		t.Fatal(err)
	}

	ctx := t.Context()

	token, aerr := jwt.assign(ctx, "abc", 123)
	if aerr != nil {
		t.Fatalf("%#v", aerr)
	}
	ai, ok := jwt.info(ctx, token, 123)
	require.Truef(t, ok, "failed to authenticate with token %s", token)
	require.Equalf(t, uint64(123), ai.Revision, "expected revision 123, got %d", ai.Revision)
	ai, ok = jwt.info(ctx, "aaa", 120)
	if ok || ai != nil {
		t.Fatalf("expected aaa to fail to authenticate, got %+v", ai)
	}

	// test verify-only provider
	if opts["pub-key"] != "" && opts["priv-key"] != "" {
		t.Run("verify-only", func(t *testing.T) {
			newOpts := make(map[string]string, len(opts))
			maps.Copy(newOpts, opts)
			delete(newOpts, "priv-key")
			verify, err := newTokenProviderJWT(lg, newOpts)
			if err != nil {
				t.Fatal(err)
			}

			// make sure that the token with revision claim 123 can be validated using revision 1234
			ai, ok := verify.info(ctx, token, 1234)
			require.Truef(t, ok, "failed to authenticate with token %s", token)
			require.Equalf(t, uint64(1234), ai.Revision, "expected revision 1234, got %d", ai.Revision)
			ai, ok = verify.info(ctx, "aaa", 120)
			if ok || ai != nil {
				t.Fatalf("expected aaa to fail to authenticate, got %+v", ai)
			}

			_, aerr := verify.assign(ctx, "abc", 123)
			require.ErrorIsf(t, aerr, ErrVerifyOnly, "unexpected error when attempting to sign with public key: %v", aerr)
		})
	}
}

func TestJWTTokenWithMissingFields(t *testing.T) {
	testCases := []struct {
		name        string
		username    string // An empty string means not present
		revision    uint64 // 0 means not present
		expectValid bool
	}{
		{
			name:        "valid token",
			username:    "hello",
			revision:    100,
			expectValid: true,
		},
		{
			name:        "no username",
			username:    "",
			revision:    100,
			expectValid: false,
		},
		{
			name:        "no revision",
			username:    "hello",
			revision:    0,
			expectValid: false,
		},
	}

	for _, tc := range testCases {
		tc := tc
		optsMap := map[string]string{
			"priv-key":    jwtRSAPrivKey,
			"sign-method": "RS256",
			"ttl":         "1h",
		}

		t.Run(tc.name, func(t *testing.T) {
			// prepare claims
			claims := jwt.MapClaims{
				"exp": time.Now().Add(time.Hour).Unix(),
			}
			if tc.username != "" {
				claims["username"] = tc.username
			}
			if tc.revision != 0 {
				claims["revision"] = tc.revision
			}

			// generate a JWT token with the given claims
			var opts jwtOptions
			err := opts.ParseWithDefaults(optsMap)
			require.NoError(t, err)
			key, err := opts.Key()
			require.NoError(t, err)

			tk := jwt.NewWithClaims(opts.SignMethod, claims)
			token, err := tk.SignedString(key)
			require.NoError(t, err)

			// verify the token
			jwtProvider, err := newTokenProviderJWT(zap.NewNop(), optsMap)
			require.NoError(t, err)
			ai, ok := jwtProvider.info(t.Context(), token, 123)

			require.Equal(t, tc.expectValid, ok)
			if ok {
				require.Equal(t, tc.username, ai.Username)
				require.Equal(t, tc.revision, ai.Revision)
			}
		})
	}
}

func TestJWTBad(t *testing.T) {
	badCases := map[string]map[string]string{
		"no options": {},
		"invalid method": {
			"sign-method": "invalid",
		},
		"rsa no key": {
			"sign-method": "RS256",
		},
		"invalid ttl": {
			"sign-method": "RS256",
			"ttl":         "forever",
		},
		"rsa invalid public key": {
			"sign-method": "RS256",
			"pub-key":     jwtRSAPrivKey,
			"priv-key":    jwtRSAPrivKey,
		},
		"rsa invalid private key": {
			"sign-method": "RS256",
			"pub-key":     jwtRSAPubKey,
			"priv-key":    jwtRSAPubKey,
		},
		"hmac no key": {
			"sign-method": "HS256",
		},
		"hmac pub key": {
			"sign-method": "HS256",
			"pub-key":     jwtRSAPubKey,
		},
		"missing public key file": {
			"sign-method": "HS256",
			"pub-key":     "missing-file",
		},
		"missing private key file": {
			"sign-method": "HS256",
			"priv-key":    "missing-file",
		},
		"ecdsa no key": {
			"sign-method": "ES256",
		},
		"ecdsa invalid public key": {
			"sign-method": "ES256",
			"pub-key":     jwtECPrivKey,
			"priv-key":    jwtECPrivKey,
		},
		"ecdsa invalid private key": {
			"sign-method": "ES256",
			"pub-key":     jwtECPubKey,
			"priv-key":    jwtECPubKey,
		},
	}

	lg := zap.NewNop()

	for k, v := range badCases {
		t.Run(k, func(t *testing.T) {
			_, err := newTokenProviderJWT(lg, v)
			if err == nil {
				t.Errorf("expected error for options %v", v)
			}
		})
	}
}

// testJWTOpts is useful for passing to NewTokenProvider which requires a string.
func testJWTOpts() string {
	return fmt.Sprintf("%s,pub-key=%s,priv-key=%s,sign-method=RS256", tokenTypeJWT, jwtRSAPubKey, jwtRSAPrivKey)
}
