// Package agent 是 Agent 的 gRPC 通道（W3）：双向流 Stream + 一元 Enroll。
//
// 单端口分流（docs/01 D22）：飞牛 manifest 只允许一个 service_port，
// gRPC（HTTP/2 + application/grpc）与前端 REST（gin）共用监听器，由 Multiplex 按协议分流。
// 业务逻辑不落在本包：注册走 service.EnrollService，指标入时序经 pipeline，
// 告警经 service.AlertService——本包只做协议编解码、鉴权与会话管理。
package agent

import (
	"net/http"
	"strings"

	"google.golang.org/grpc"
)

// Multiplex 按 D22 约定分流：HTTP/2 且 Content-Type 为 application/grpc 的请求交给
// gRPC Server，其余交给 REST。判定必须同时看 ProtoMajor 与 Content-Type，
// 防止把普通 HTTP/2 请求误喂给 gRPC。
func Multiplex(grpcSrv *grpc.Server, rest http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			grpcSrv.ServeHTTP(w, r)
			return
		}
		rest.ServeHTTP(w, r)
	})
}
