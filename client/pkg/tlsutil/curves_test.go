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
	"crypto/tls"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetCurveID_not_existing(t *testing.T) {
	_, ok := GetCurveID("not_existing")
	require.Falsef(t, ok, "Expected not ok")
}

func TestGetCurveID_success(t *testing.T) {
	cases := map[string]tls.CurveID{
		"X25519MLKEM768": tls.X25519MLKEM768,
		"X25519":         tls.X25519,
		"CurveP256":      tls.CurveP256,
		"CurveP384":      tls.CurveP384,
		"CurveP521":      tls.CurveP521,
	}
	for name, expectedID := range cases {
		id, ok := GetCurveID(name)
		require.Truef(t, ok, "Expected %v curve to exist", name)
		require.Equalf(t, expectedID, id, "For %v expected=%v found=%v", name, expectedID, id)
	}
}
