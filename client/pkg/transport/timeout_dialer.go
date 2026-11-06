// Copyright 2015 The etcd Authors
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
	"context"
	"net"
	"time"
)

// withTimeouts wraps dial so that the connections it returns apply the read
// and write timeouts. Wrapping the configured dial function, rather than
// building another dialer, keeps every dialer option of NewTransport.
func withTimeouts(dial func(ctx context.Context, network, address string) (net.Conn, error), rdtimeoutd, wtimeoutd time.Duration) func(ctx context.Context, network, address string) (net.Conn, error) {
	if rdtimeoutd == 0 && wtimeoutd == 0 {
		return dial
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &timeoutConn{
			readTimeout:  rdtimeoutd,
			writeTimeout: wtimeoutd,
			Conn:         conn,
		}, nil
	}
}
