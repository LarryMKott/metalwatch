// Package agentpb 是 Agent 上报接口层：HTTP/2 + TLS + Protobuf 二进制，与前端 JSON 通道严格隔离。
//
// 传输契约（docs/04 §2）：
//   - 方法：POST，Content-Type: application/x-protobuf，Content-Encoding: gzip（>32KB 必须压缩）
//   - 鉴权：Authorization: Bearer mwa_xxx（仅 /report 与 /heartbeat；/enroll 用一次性注册码）
//   - 响应：当前阶段为 JSON 错误信封 + JSON 成功体；
//     proto 代码生成（make pb）落地后切换为 protobuf 响应体（见 respCodec）
package agentpb

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/LarryMKott/metalwatch/internal/app"
)

// RegisterRoutes 挂载 Agent 通道路由。由 api/web 的路由装配调用。
func RegisterRoutes(r *gin.Engine, d *app.Deps) {
	g := r.Group("/api/v1/agent")
	{
		g.POST("/enroll", Enroll(d))
		g.POST("/report", RequireToken(d), Report(d))
		g.POST("/heartbeat", RequireToken(d), Heartbeat(d))
	}
}

// wantsProtobuf 判断请求体是否为 protobuf。
func wantsProtobuf(c *gin.Context) bool {
	ct := c.GetHeader("Content-Type")
	return strings.Contains(ct, "application/x-protobuf") || strings.Contains(ct, "application/protobuf")
}

// respCodec 描述响应编码，便于排障时确认 Agent 走的是哪条通道。
func respCodec(c *gin.Context) string {
	if wantsProtobuf(c) {
		return "protobuf"
	}
	return "json"
}

// notImplementedProtobuf 在 proto 代码生成落地前，对 protobuf 请求给出可执行的提示，
// 而不是静默地把二进制当 JSON 解析（那样只会得到难以定位的解析错误）。
func notImplementedProtobuf(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{
		"code": "protobuf_not_enabled",
		"message": "Protobuf 编解码随 proto 代码生成启用（make pb）；" +
			"当前请使用 application/json 联调，契约见 docs/04 §2",
		"request_id": requestID(c),
	})
}
