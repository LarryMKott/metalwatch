package agent

import (
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	gen "github.com/LarryMKott/metalwatch/proto/gen"
)

// NewGrpcServer 组装 gRPC Server：注册流服务、鉴权拦截器与 keepalive 参数
// （docs/03 §3.4：Time=60s / Timeout=20s，允许无调用保活；客户端重连退避下限 30s）。
func NewGrpcServer(s *StreamServer) *grpc.Server {
	g := grpc.NewServer(
		grpc.ChainUnaryInterceptor(s.unaryAuth),
		grpc.ChainStreamInterceptor(s.streamAuth),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    60 * time.Second,
			Timeout: 20 * time.Second,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             30 * time.Second,
			PermitWithoutStream: true,
		}),
	)
	gen.RegisterAgentStreamServiceServer(g, s)
	return g
}
