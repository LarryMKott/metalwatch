package agent

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	"github.com/LarryMKott/metalwatch/pkg/crypto"
)

// ctxTokenKey 携带鉴权后的 Agent 令牌（导出读取函数，隐藏键类型）。
type ctxTokenKey struct{}

// EnrollFullMethod 是免鉴权的一元方法（注册本身没有令牌）。
const EnrollFullMethod = "/metalwatch.agent.AgentStreamService/Enroll"

// TokenFromContext 取出拦截器注入的 Agent 令牌；未鉴权的连接返回 nil。
func TokenFromContext(ctx context.Context) *adapter.AgentToken {
	if tok, ok := ctx.Value(ctxTokenKey{}).(*adapter.AgentToken); ok {
		return tok
	}
	return nil
}

// bearerTokenFromMD 从 gRPC metadata 提取 Bearer 令牌（键统一为小写 authorization）。
func bearerTokenFromMD(ctx context.Context) (string, bool) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", false
	}
	for _, v := range md.Get("authorization") {
		if raw := strings.TrimSpace(v); strings.HasPrefix(raw, "Bearer ") {
			token := strings.TrimSpace(strings.TrimPrefix(raw, "Bearer "))
			if token != "" {
				return token, true
			}
		}
	}
	return "", false
}

// authenticate 校验令牌并返回令牌对象；失败返回 gRPC 错误（映射 docs/04 §1 错误码表）。
func (s *StreamServer) authenticate(ctx context.Context) (*adapter.AgentToken, error) {
	token, ok := bearerTokenFromMD(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "缺少 Bearer 令牌")
	}
	tok, err := s.store.AgentTokens().FindActiveByHash(ctx, crypto.HashToken(token))
	switch {
	case err == nil:
		return tok, nil
	case isNotFound(err):
		return nil, status.Error(codes.Unauthenticated, "令牌无效、已吊销或已过期")
	default:
		return nil, status.Error(codes.Internal, "鉴权查询失败")
	}
}

// unaryAuth 一元拦截器：除 Enroll 外全部要求令牌。
func (s *StreamServer) unaryAuth(
	ctx context.Context, req any, info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (any, error) {
	if info.FullMethod == EnrollFullMethod {
		return handler(ctx, req)
	}
	tok, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	return handler(context.WithValue(ctx, ctxTokenKey{}, tok), req)
}

// streamAuth 流拦截器：所有流都要求令牌（Stream 承载数据，无免鉴权场景）。
func (s *StreamServer) streamAuth(
	srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo,
	handler grpc.StreamHandler,
) error {
	tok, err := s.authenticate(ss.Context())
	if err != nil {
		return err
	}
	return handler(srv, &authorizedStream{ServerStream: ss, token: tok})
}

// authorizedStream 把令牌注入下游可见的 context（gRPC 的 ServerStream.Context 不可替换，
// 以包装层覆盖 RecvMsg/SendMsg 使用的 context）。
type authorizedStream struct {
	grpc.ServerStream
	token *adapter.AgentToken
}

func (w *authorizedStream) Context() context.Context {
	return context.WithValue(w.ServerStream.Context(), ctxTokenKey{}, w.token)
}
