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

package grpcproxy

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
)

func getAuthTokenFromClient(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ts, ok := md[rpctypes.TokenFieldNameGRPC]
		if ok {
			return ts[0]
		}
	}
	return ""
}

func withClientAuthToken(ctx, ctxWithToken context.Context) context.Context {
	token := getAuthTokenFromClient(ctxWithToken)
	if token != "" {
		md := metadata.Pairs(rpctypes.TokenFieldNameGRPC, token)
		return metadata.NewOutgoingContext(ctx, md)
	}
	return ctx
}

type proxyTokenCredential struct {
	token string
}

func (cred *proxyTokenCredential) RequireTransportSecurity() bool {
	return false
}

func (cred *proxyTokenCredential) GetRequestMetadata(ctx context.Context, s ...string) (map[string]string, error) {
	return map[string]string{
		rpctypes.TokenFieldNameGRPC: cred.token,
	}, nil
}

func AuthUnaryClientInterceptor(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	token := getAuthTokenFromClient(ctx)
	if token != "" {
		tokenCred := &proxyTokenCredential{token}
		opts = append(opts, grpc.PerRPCCredentials(tokenCred))
	}
	return invoker(ctx, method, req, reply, cc, opts...)
}

func AuthStreamClientInterceptor(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	token := getAuthTokenFromClient(ctx)
	if token != "" {
		tokenCred := &proxyTokenCredential{token}
		opts = append(opts, grpc.PerRPCCredentials(tokenCred))
	}
	return streamer(ctx, desc, cc, method, opts...)
}
