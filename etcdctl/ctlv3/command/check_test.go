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

package command

import (
	"testing"

	"github.com/stretchr/testify/require"

	"go.etcd.io/etcd/pkg/v3/cobrautl"
)

func TestCheckPerfConfigFromCmd(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want checkPerfCfg
	}{
		{
			name: "uses selected preset when overrides are omitted",
			args: []string{"--load", "m"},
			want: checkPerfCfg{
				limit:    1000,
				clients:  200,
				duration: 60,
			},
		},
		{
			name: "overrides selected preset with custom values",
			args: []string{"--load", "m", "--clients", "7", "--limit", "42", "--duration", "5"},
			want: checkPerfCfg{
				limit:    42,
				clients:  7,
				duration: 5,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := NewCheckPerfCommand()
			require.NoError(t, cmd.Flags().Parse(tt.args))

			got, code, err := checkPerfConfigFromCmd(cmd)

			require.NoError(t, err)
			require.Equal(t, cobrautl.ExitSuccess, code)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestCheckPerfConfigFromCmdRejectsInvalidOverrides(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "clients must be positive",
			args:    []string{"--clients", "0"},
			wantErr: "clients must be greater than 0",
		},
		{
			name:    "limit must be positive",
			args:    []string{"--limit", "-1"},
			wantErr: "limit must be greater than 0",
		},
		{
			name:    "duration must be positive",
			args:    []string{"--duration", "0"},
			wantErr: "duration must be greater than 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := NewCheckPerfCommand()
			require.NoError(t, cmd.Flags().Parse(tt.args))

			_, code, err := checkPerfConfigFromCmd(cmd)

			require.ErrorContains(t, err, tt.wantErr)
			require.Equal(t, cobrautl.ExitInvalidInput, code)
		})
	}
}

func TestCheckPerfConfigFromCmdRejectsUnknownLoad(t *testing.T) {
	cmd := NewCheckPerfCommand()
	require.NoError(t, cmd.Flags().Parse([]string{"--load", "tiny"}))

	_, code, err := checkPerfConfigFromCmd(cmd)

	require.ErrorContains(t, err, "unknown load option tiny")
	require.Equal(t, cobrautl.ExitBadFeature, code)
}
