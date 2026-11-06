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

package healthcheck

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// This test file deliberately does not import
// google.golang.org/grpc/health. That package's init() registers
// grpc-go's client-side health check function, so importing it
// anywhere in the test binary would hide a missing blank import in
// healthcheck.go — the one failure mode that breaks silently, since
// grpc-go just skips health checking and keeps every backend in
// rotation. The backends therefore run stubHealthServer below.

// The tests dial two in-process backends through a resolver that
// publishes its own ServiceConfig, which is exactly the situation
// clientv3 creates: EtcdManualResolver always publishes
// round_robin, and grpc-go ignores WithDefaultServiceConfig unless
// the resolver's ServiceConfig is nil or service config lookup is
// disabled. So every assertion here is about which ServiceConfig
// actually took effect, observed through where requests land.

const (
	identifyService = "healthchecktest.Identify"
	identifyMethod  = "/" + identifyService + "/Who"

	// resolverServiceConfig is a load balancing policy that is
	// observably different from the one GRPCClientHealthCheckOptions
	// asks for: pick_first pins every request to a single backend.
	resolverServiceConfig = `{"loadBalancingPolicy": "pick_first"}`

	dialTimeout = 5 * time.Second
)

var identifyDesc = grpc.ServiceDesc{
	ServiceName: identifyService,
	HandlerType: (*any)(nil),
	Methods: []grpc.MethodDesc{{
		MethodName: "Who",
		Handler: func(srv any, _ context.Context, _ func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
			return &wrapperspb.StringValue{Value: srv.(*backend).name}, nil
		},
	}},
	Streams:  nil,
	Metadata: nil,
}

// backend is one in-process gRPC server that answers identifyMethod
// with its own name and publishes a grpc.health.v1 server.
type backend struct {
	name string
}

// cluster is a set of backends behind one bufconn dialer plus the
// resolver that publishes them.
type cluster struct {
	backends map[string]*bufconn.Listener
	health   map[string]*stubHealthServer
	scheme   string
}

var schemeCounter struct {
	sync.Mutex
	n int
}

func newCluster(t *testing.T, names ...string) *cluster {
	t.Helper()
	c := &cluster{
		backends: make(map[string]*bufconn.Listener),
		health:   make(map[string]*stubHealthServer),
	}
	endpoints := make([]resolver.Endpoint, 0, len(names))
	for _, name := range names {
		lis := bufconn.Listen(1024 * 1024)
		hs := newStubHealthServer()
		hs.setServingStatus("", healthpb.HealthCheckResponse_SERVING)
		srv := grpc.NewServer()
		healthpb.RegisterHealthServer(srv, hs)
		srv.RegisterService(&identifyDesc, &backend{name: name})
		go func() { _ = srv.Serve(lis) }()
		t.Cleanup(srv.Stop)

		c.backends[name] = lis
		c.health[name] = hs
		endpoints = append(endpoints, resolver.Endpoint{
			Addresses: []resolver.Address{{Addr: name}},
		})
	}

	schemeCounter.Lock()
	schemeCounter.n++
	c.scheme = fmt.Sprintf("healthchecktest-%d", schemeCounter.n)
	schemeCounter.Unlock()
	resolver.Register(&testResolverBuilder{scheme: c.scheme, endpoints: endpoints})

	t.Cleanup(func() {
		for _, lis := range c.backends {
			_ = lis.Close()
		}
	})
	return c
}

// setServing flips a backend's grpc.health.v1 entry.
func (c *cluster) setServing(name, service string, serving bool) {
	status := healthpb.HealthCheckResponse_SERVING
	if !serving {
		status = healthpb.HealthCheckResponse_NOT_SERVING
	}
	c.health[name].setServingStatus(service, status)
}

// stubHealthServer is a minimal grpc.health.v1 server implementing
// only the Watch stream, which is what grpc-go's client-side health
// check consumes. Watch is not optional: grpc-go marks a subconn
// READY without further checks when the server answers Watch with
// Unimplemented (grpc@v1.83.2 health/client.go), so a Check-only stub
// would make every backend permanently selectable.
type stubHealthServer struct {
	healthpb.UnimplementedHealthServer

	mu       sync.Mutex
	status   map[string]healthpb.HealthCheckResponse_ServingStatus
	watchers map[string][]chan healthpb.HealthCheckResponse_ServingStatus
}

func newStubHealthServer() *stubHealthServer {
	return &stubHealthServer{
		status:   make(map[string]healthpb.HealthCheckResponse_ServingStatus),
		watchers: make(map[string][]chan healthpb.HealthCheckResponse_ServingStatus),
	}
}

func (s *stubHealthServer) setServingStatus(service string, status healthpb.HealthCheckResponse_ServingStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status[service] = status
	for _, ch := range s.watchers[service] {
		// Coalesce to the latest status: the channel holds one slot,
		// and only this method sends, so draining then sending cannot
		// block.
		select {
		case ch <- status:
		default:
			select {
			case <-ch:
			default:
			}
			ch <- status
		}
	}
}

func (s *stubHealthServer) Watch(req *healthpb.HealthCheckRequest, stream healthpb.Health_WatchServer) error {
	service := req.GetService()

	s.mu.Lock()
	ch := make(chan healthpb.HealthCheckResponse_ServingStatus, 1)
	s.watchers[service] = append(s.watchers[service], ch)
	status, known := s.status[service]
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		watchers := s.watchers[service]
		for i, w := range watchers {
			if w == ch {
				s.watchers[service] = append(watchers[:i], watchers[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
	}()

	if !known {
		status = healthpb.HealthCheckResponse_SERVICE_UNKNOWN
	}
	if err := stream.Send(&healthpb.HealthCheckResponse{Status: status}); err != nil {
		return err
	}
	for {
		select {
		case status := <-ch:
			if err := stream.Send(&healthpb.HealthCheckResponse{Status: status}); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return nil
		}
	}
}

// testResolverBuilder publishes a fixed endpoint set together with a
// non-nil ServiceConfig, mirroring clientv3's EtcdManualResolver.
type testResolverBuilder struct {
	scheme    string
	endpoints []resolver.Endpoint
}

func (b *testResolverBuilder) Build(_ resolver.Target, cc resolver.ClientConn, _ resolver.BuildOptions) (resolver.Resolver, error) {
	sc := cc.ParseServiceConfig(resolverServiceConfig)
	if sc.Err != nil {
		return nil, sc.Err
	}
	if err := cc.UpdateState(resolver.State{Endpoints: b.endpoints, ServiceConfig: sc}); err != nil {
		return nil, err
	}
	return noopResolver{}, nil
}

func (b *testResolverBuilder) Scheme() string { return b.scheme }

type noopResolver struct{}

func (noopResolver) ResolveNow(resolver.ResolveNowOptions) {}
func (noopResolver) Close()                                {}

// dial connects to the cluster with the given extra dial options.
func (c *cluster) dial(t *testing.T, opts ...grpc.DialOption) *grpc.ClientConn {
	t.Helper()
	dialer := func(_ context.Context, addr string) (net.Conn, error) {
		lis, ok := c.backends[addr]
		if !ok {
			return nil, fmt.Errorf("unknown backend %q", addr)
		}
		return lis.Dial()
	}
	allOpts := append([]grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(dialer),
	}, opts...)
	conn, err := grpc.NewClient(c.scheme+":///etcd", allOpts...)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	conn.Connect()
	return conn
}

// identify issues one identifyMethod RPC and returns which backend
// served it.
func identify(t *testing.T, conn *grpc.ClientConn) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()
	resp := &wrapperspb.StringValue{}
	if err := conn.Invoke(ctx, identifyMethod, &wrapperspb.StringValue{}, resp); err != nil {
		t.Fatalf("Invoke(%s): %v", identifyMethod, err)
	}
	return resp.GetValue()
}

// hitCount issues n RPCs and returns how many landed on each backend.
func hitCount(t *testing.T, conn *grpc.ClientConn, n int) map[string]int {
	t.Helper()
	hits := make(map[string]int, 2)
	for i := 0; i < n; i++ {
		hits[identify(t, conn)]++
	}
	return hits
}

// TestGRPCClientHealthCheckOptions_OverridesResolverServiceConfig:
// the resolver publishes pick_first, the helper asks for
// round_robin. Because the helper disables resolver-provided service
// config, requests must spread over both backends.
func TestGRPCClientHealthCheckOptions_OverridesResolverServiceConfig(t *testing.T) {
	c := newCluster(t, "a", "b")
	conn := c.dial(t, GRPCClientHealthCheckOptions()...)

	hits := hitCount(t, conn, 20)
	if len(hits) != 2 {
		t.Fatalf("round_robin should reach both backends, got %v", hits)
	}
}

// TestGRPCClientHealthCheckOptions_ResolverServiceConfigWinsWithoutHelper
// is the control for the test above: without the helper the
// resolver's pick_first takes effect and every request lands on one
// backend. It pins that WithDisableServiceConfig is the load-bearing
// half of the helper rather than a no-op.
func TestGRPCClientHealthCheckOptions_ResolverServiceConfigWinsWithoutHelper(t *testing.T) {
	c := newCluster(t, "a", "b")
	conn := c.dial(t)

	hits := hitCount(t, conn, 20)
	if len(hits) != 1 {
		t.Fatalf("pick_first should pin every request to one backend, got %v", hits)
	}
}

// TestGRPCClientHealthCheckOptions_ExcludesNotServingBackend: a
// backend reporting NOT_SERVING must stop receiving requests, and
// must start receiving them again once it reports SERVING. This also
// covers the blank import of google.golang.org/grpc/health: without
// it grpc-go silently skips health checking and "b" would keep
// serving.
func TestGRPCClientHealthCheckOptions_ExcludesNotServingBackend(t *testing.T) {
	c := newCluster(t, "a", "b")
	conn := c.dial(t, GRPCClientHealthCheckOptions()...)

	// Warm up both backends so the exclusion below cannot be
	// explained by "b" never having been picked.
	if hits := hitCount(t, conn, 20); len(hits) != 2 {
		t.Fatalf("expected both backends in rotation before the flip, got %v", hits)
	}

	c.setServing("b", "", false)
	if err := waitForExclusion(t, conn, "b"); err != nil {
		t.Fatal(err)
	}

	c.setServing("b", "", true)
	if err := waitForInclusion(t, conn, "b"); err != nil {
		t.Fatal(err)
	}
}

// TestGRPCClientHealthCheckOptions_PerCheckServiceName covers the
// etcd fork's per-check entries: health checking is scoped to the
// serviceName in the service config, so a backend that is
// NOT_SERVING on the aggregate entry but SERVING on the entry being
// watched stays in rotation.
func TestGRPCClientHealthCheckOptions_PerCheckServiceName(t *testing.T) {
	c := newCluster(t, "a", "b")
	c.health["b"].setServingStatus("etcd.health/defrag", healthpb.HealthCheckResponse_SERVING)
	c.setServing("b", "", false)

	conn := c.dial(t, grpc.WithDisableServiceConfig(),
		grpc.WithDefaultServiceConfig(`{"loadBalancingPolicy": "round_robin", "healthCheckConfig": {"serviceName": "etcd.health/defrag"}}`))

	if err := waitForInclusion(t, conn, "b"); err != nil {
		t.Fatal(err)
	}
}

// waitForExclusion polls until backend stops serving requests.
func waitForExclusion(t *testing.T, conn *grpc.ClientConn, backend string) error {
	t.Helper()
	deadline := time.Now().Add(dialTimeout)
	for time.Now().Before(deadline) {
		if hits := hitCount(t, conn, 10); hits[backend] == 0 {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("backend %q still receiving requests after %v", backend, dialTimeout)
}

// waitForInclusion polls until backend serves at least one request.
func waitForInclusion(t *testing.T, conn *grpc.ClientConn, backend string) error {
	t.Helper()
	deadline := time.Now().Add(dialTimeout)
	for time.Now().Before(deadline) {
		if hits := hitCount(t, conn, 10); hits[backend] > 0 {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("backend %q never returned to rotation within %v", backend, dialTimeout)
}
