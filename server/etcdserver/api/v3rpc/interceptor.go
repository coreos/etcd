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

package v3rpc

import (
	"context"
	"net"
	"strconv"
	"sync"
	"time"

	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	"go.etcd.io/etcd/pkg/v3/types"
	"go.etcd.io/etcd/raft/v3"
	"go.etcd.io/etcd/server/v3/etcdserver"
	"go.etcd.io/etcd/server/v3/etcdserver/api"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"

	cmap "github.com/orcaman/concurrent-map"
)

const (
	maxNoLeaderCnt          = 3
	warnUnaryRequestLatency = 300 * time.Millisecond
)

type processList struct {
	Ctx        context.Context
	Cancel     *context.CancelFunc
	ID         int64
	StartTime  time.Time
	SourceIP   string
	FullMethod string
	RequestStr string
}

var smap *streamsMap
var processListCache cmap.ConcurrentMap

func init() {
	smap = &streamsMap{
		streams:   make(map[grpc.ServerStream]struct{}),
		idStreams: make(map[int64]grpc.ServerStream),
	}
	processListCache = cmap.New()
}

type streamsMap struct {
	mu        sync.RWMutex
	streams   map[grpc.ServerStream]struct{}
	idStreams map[int64]grpc.ServerStream
}

func newUnaryInterceptor(s *etcdserver.EtcdServer) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if !api.IsCapabilityEnabled(api.V3rpcCapability) {
			return nil, rpctypes.ErrGRPCNotCapable
		}

		if s.IsMemberExist(s.ID()) && s.IsLearner() && !isRPCSupportedForLearner(req) {
			return nil, rpctypes.ErrGPRCNotSupportedForLearner
		}

		md, ok := metadata.FromIncomingContext(ctx)
		if ok {
			ver, vs := "unknown", md.Get(rpctypes.MetadataClientAPIVersionKey)
			if len(vs) > 0 {
				ver = vs[0]
			}
			clientRequests.WithLabelValues("unary", ver).Inc()

			if ks := md[rpctypes.MetadataRequireLeaderKey]; len(ks) > 0 && ks[0] == rpctypes.MetadataHasLeader {
				if s.Leader() == types.ID(raft.None) {
					return nil, rpctypes.ErrGRPCNoLeader
				}
			}
		}

		return handler(ctx, req)
	}
}

func newLogUnaryInterceptor(s *etcdserver.EtcdServer) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		startTime := time.Now()
		resp, err := handler(ctx, req)
		lg := s.Logger()
		if lg != nil { // acquire stats if debug level is enabled or request is expensive
			defer logUnaryRequestStats(ctx, lg, info, startTime, req, resp)
		}
		return resp, err
	}
}

func GetRealAddr(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}

	rips := md.Get("x-real-ip")
	if len(rips) == 0 {
		return ""
	}

	return rips[0]
}

// GetPeerAddr get peer addr
func GetPeerAddr(ctx context.Context) string {
	var addr string
	if pr, ok := peer.FromContext(ctx); ok {
		if tcpAddr, ok := pr.Addr.(*net.TCPAddr); ok {
			addr = tcpAddr.IP.String()
		} else {
			addr = pr.Addr.String()
		}
	}
	return addr
}

func newProcessListInterceptor(s *etcdserver.EtcdServer) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		ctx, cancel := context.WithCancel(ctx)

		startTime := time.Now()
		id := startTime.Local().UnixNano()
		idStr := strconv.FormatInt(id, 10)
		p := new(processList)
		p.Ctx = ctx
		p.Cancel = &cancel
		p.ID = id
		p.StartTime = startTime
		if ip := GetRealAddr(ctx); ip != "" {
			p.SourceIP = ip
		} else {
			p.SourceIP = GetPeerAddr(ctx)
		}
		p.FullMethod = info.FullMethod

		switch _req := req.(type) {
		case *pb.RangeRequest:
			p.RequestStr = _req.String()
		case *pb.PutRequest:
			p.RequestStr = pb.NewLoggablePutRequest(_req).String()
		case *pb.DeleteRangeRequest:
			p.RequestStr = _req.String()
		case *pb.TxnRequest:
			p.RequestStr = pb.NewLoggableTxnRequest(_req).String()
		default:
		}

		processListCache.Set(idStr, p)
		defer func() {
			processListCache.Remove(idStr)
			cancel()
		}()

		resp, err := handler(ctx, req)
		return resp, err
	}
}

func logUnaryRequestStats(ctx context.Context, lg *zap.Logger, info *grpc.UnaryServerInfo, startTime time.Time, req interface{}, resp interface{}) {
	duration := time.Since(startTime)
	var enabledDebugLevel, expensiveRequest bool
	if lg.Core().Enabled(zap.DebugLevel) {
		enabledDebugLevel = true
	}
	if duration > warnUnaryRequestLatency {
		expensiveRequest = true
	}
	if !enabledDebugLevel && !expensiveRequest {
		return
	}
	remote := "No remote client info."
	peerInfo, ok := peer.FromContext(ctx)
	if ok {
		remote = peerInfo.Addr.String()
	}
	responseType := info.FullMethod
	var reqCount, respCount int64
	var reqSize, respSize int
	var reqContent string
	switch _resp := resp.(type) {
	case *pb.RangeResponse:
		_req, ok := req.(*pb.RangeRequest)
		if ok {
			reqCount = 0
			reqSize = _req.Size()
			reqContent = _req.String()
		}
		if _resp != nil {
			respCount = _resp.GetCount()
			respSize = _resp.Size()
		}
	case *pb.PutResponse:
		_req, ok := req.(*pb.PutRequest)
		if ok {
			reqCount = 1
			reqSize = _req.Size()
			reqContent = pb.NewLoggablePutRequest(_req).String()
			// redact value field from request content, see PR #9821
		}
		if _resp != nil {
			respCount = 0
			respSize = _resp.Size()
		}
	case *pb.DeleteRangeResponse:
		_req, ok := req.(*pb.DeleteRangeRequest)
		if ok {
			reqCount = 0
			reqSize = _req.Size()
			reqContent = _req.String()
		}
		if _resp != nil {
			respCount = _resp.GetDeleted()
			respSize = _resp.Size()
		}
	case *pb.TxnResponse:
		_req, ok := req.(*pb.TxnRequest)
		if ok && _resp != nil {
			if _resp.GetSucceeded() { // determine the 'actual' count and size of request based on success or failure
				reqCount = int64(len(_req.GetSuccess()))
				reqSize = 0
				for _, r := range _req.GetSuccess() {
					reqSize += r.Size()
				}
			} else {
				reqCount = int64(len(_req.GetFailure()))
				reqSize = 0
				for _, r := range _req.GetFailure() {
					reqSize += r.Size()
				}
			}
			reqContent = pb.NewLoggableTxnRequest(_req).String()
			// redact value field from request content, see PR #9821
		}
		if _resp != nil {
			respCount = 0
			respSize = _resp.Size()
		}
	default:
		reqCount = -1
		reqSize = -1
		respCount = -1
		respSize = -1
	}

	if enabledDebugLevel {
		logGenericRequestStats(lg, startTime, duration, remote, responseType, reqCount, reqSize, respCount, respSize, reqContent)
	} else if expensiveRequest {
		logExpensiveRequestStats(lg, startTime, duration, remote, responseType, reqCount, reqSize, respCount, respSize, reqContent)
	}
}

func logGenericRequestStats(lg *zap.Logger, startTime time.Time, duration time.Duration, remote string, responseType string,
	reqCount int64, reqSize int, respCount int64, respSize int, reqContent string) {
	lg.Debug("request stats",
		zap.Time("start time", startTime),
		zap.Duration("time spent", duration),
		zap.String("remote", remote),
		zap.String("response type", responseType),
		zap.Int64("request count", reqCount),
		zap.Int("request size", reqSize),
		zap.Int64("response count", respCount),
		zap.Int("response size", respSize),
		zap.String("request content", reqContent),
	)
}

func logExpensiveRequestStats(lg *zap.Logger, startTime time.Time, duration time.Duration, remote string, responseType string,
	reqCount int64, reqSize int, respCount int64, respSize int, reqContent string) {
	lg.Warn("request stats",
		zap.Time("start time", startTime),
		zap.Duration("time spent", duration),
		zap.String("remote", remote),
		zap.String("response type", responseType),
		zap.Int64("request count", reqCount),
		zap.Int("request size", reqSize),
		zap.Int64("response count", respCount),
		zap.Int("response size", respSize),
		zap.String("request content", reqContent),
	)
}

func newStreamInterceptor(s *etcdserver.EtcdServer) grpc.StreamServerInterceptor {
	monitorLeader(s)

	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if !api.IsCapabilityEnabled(api.V3rpcCapability) {
			return rpctypes.ErrGRPCNotCapable
		}

		if s.IsMemberExist(s.ID()) && s.IsLearner() { // learner does not support stream RPC
			return rpctypes.ErrGPRCNotSupportedForLearner
		}

		md, ok := metadata.FromIncomingContext(ss.Context())
		if ok {
			ver, vs := "unknown", md.Get(rpctypes.MetadataClientAPIVersionKey)
			if len(vs) > 0 {
				ver = vs[0]
			}
			clientRequests.WithLabelValues("stream", ver).Inc()

			if ks := md[rpctypes.MetadataRequireLeaderKey]; len(ks) > 0 && ks[0] == rpctypes.MetadataHasLeader {
				if s.Leader() == types.ID(raft.None) {
					return rpctypes.ErrGRPCNoLeader
				}

				startTime := time.Now()
				id := startTime.Local().UnixNano()
				var sourceIP string
				if ip := GetRealAddr(ss.Context()); ip != "" {
					sourceIP = ip
				} else {
					sourceIP = GetPeerAddr(ss.Context())
				}

				cctx, cancel := context.WithCancel(ss.Context())
				ss = serverStreamWithCtx{
					ctx: cctx, cancel: &cancel, ServerStream: ss,
					ID: id, StartTime: startTime, SourceIP: sourceIP, FullMethod: info.FullMethod,
				}

				smap.mu.Lock()
				smap.streams[ss] = struct{}{}
				smap.idStreams[id] = ss
				smap.mu.Unlock()

				defer func() {
					smap.mu.Lock()
					delete(smap.streams, ss)
					delete(smap.idStreams, id)
					smap.mu.Unlock()
					cancel()
				}()
			}
		}

		return handler(srv, ss)
	}
}

type serverStreamWithCtx struct {
	grpc.ServerStream
	ctx        context.Context
	cancel     *context.CancelFunc
	ID         int64
	StartTime  time.Time
	SourceIP   string
	FullMethod string
	RequestStr string
}

func (ssc serverStreamWithCtx) Context() context.Context { return ssc.ctx }

func monitorLeader(s *etcdserver.EtcdServer) {
	s.GoAttach(func() {
		election := time.Duration(s.Cfg.TickMs) * time.Duration(s.Cfg.ElectionTicks) * time.Millisecond
		noLeaderCnt := 0

		for {
			select {
			case <-s.StoppingNotify():
				return
			case <-time.After(election):
				if s.Leader() == types.ID(raft.None) {
					noLeaderCnt++
				} else {
					noLeaderCnt = 0
				}

				// We are more conservative on canceling existing streams. Reconnecting streams
				// cost much more than just rejecting new requests. So we wait until the member
				// cannot find a leader for maxNoLeaderCnt election timeouts to cancel existing streams.
				if noLeaderCnt >= maxNoLeaderCnt {
					smap.mu.Lock()
					for ss := range smap.streams {
						if ssWithCtx, ok := ss.(serverStreamWithCtx); ok {
							(*ssWithCtx.cancel)()
							<-ss.Context().Done()
						}
					}
					smap.streams = make(map[grpc.ServerStream]struct{})
					smap.idStreams = make(map[int64]grpc.ServerStream)
					smap.mu.Unlock()
				}
			}
		}
	})
}
