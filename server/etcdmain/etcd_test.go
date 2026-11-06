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

package etcdmain

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStartEtcdOrProxyV2Error(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		expectErr error
	}{
		{
			name:      "help flag returns nil error",
			args:      []string{"etcd", "--help"},
			expectErr: nil,
		},
		{
			name:      "version flag returns nil error",
			args:      []string{"etcd", "--version"},
			expectErr: nil,
		},
		{
			name:      "bad flag returns ErrArgumentError",
			args:      []string{"etcd", "--non-existent-flag"},
			expectErr: ErrArgumentError,
		},
		{
			name:      "extra non-flag args return ErrArgumentError",
			args:      []string{"etcd", "unexpected-arg"},
			expectErr: ErrArgumentError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := startEtcdOrProxyV2Error(tt.args)
			if tt.expectErr != nil {
				require.Error(t, err)
				require.True(t, errors.Is(err, tt.expectErr), "expected error wrapping %v, got %v", tt.expectErr, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
