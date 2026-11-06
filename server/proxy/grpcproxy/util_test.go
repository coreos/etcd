// Copyright 2025 The etcd Authors
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

package grpcproxy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/metadata"

	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
)

func TestWithClientAuthToken(t *testing.T) {
	tests := []struct {
		name        string
		initContext func() context.Context
		want        string
	}{
		{
			name: "with valid token",
			initContext: func() context.Context {
				md := metadata.Pairs(rpctypes.TokenFieldNameGRPC, "test-token-123")
				return metadata.NewIncomingContext(t.Context(), md)
			},
			want: "test-token-123",
		},
		{
			name: "without token",
			initContext: func() context.Context {
				return t.Context()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			ctxWithToken := tt.initContext()

			got := withClientAuthToken(ctx, ctxWithToken)

			if tt.want == "" {
				assert.Equal(t, ctx, got)
				return
			}

			md, ok := metadata.FromOutgoingContext(got)
			assert.True(t, ok)
			tokens := md[rpctypes.TokenFieldNameGRPC]
			assert.Len(t, tokens, 1)
			assert.Equal(t, tt.want, tokens[0])
		})
	}
}
