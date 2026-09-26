// Package agentpb 是 Agent 上报接口层：HTTP/2 + TLS + Protobuf 二进制，与前端 JSON 通道严格隔离。
//
// 传输契约（docs/04 §2）：
//   - 方法：POST，Content-Type: application/x-protobuf，Content-Encoding: gzip（>32KB 必须压缩）
//   - 鉴权：Authorization: Bearer mwa_xxx（仅 /report 与 /heartbeat；/enroll 用一次性注册码）
//   - 响应：当前阶段为 JSON 错误信封 + JSON 成功体；
//     proto 代码生成（make pb）落地后切换为 protobuf 响应体（见 respCodec）
//
// 路由域对象化（docs/01 D31 第 6 条）：AgentHandler 持有全部依赖与路由表，
// RegisterRoutes 仅作为组合根入口，负责从依赖容器构造 Handler 并挂载。
package agentpb

import (
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LarryMKott/metalwatch/internal/app"
	"github.com/LarryMKott/metalwatch/internal/service"
)

// RegisterRoutes 挂载 Agent 通道路由。由 api/web 的组合根调用。
func RegisterRoutes(r *gin.Engine, d *app.Deps) {
	interval := time.Duration(d.Config.Collect.SensorInterval) * time.Second
	assetInterval := time.Duration(d.Config.Collect.AssetInterval) * time.Second
	h := NewAgentHandler(
		d.Store,
		service.NewEnrollService(d.Store, d.Hosts, interval, assetInterval, d.Log),
		d.TSDB, d.Pipeline, d.Alerts,
		interval, assetInterval,
	)
	h.Register(r)
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

// drainLimit / drainTimeout 给排空请求体设上限。
// /enroll 在鉴权之前（见 handler.go 的路由注册），无上限的 io.Copy 会被
// 慢速或超大 body 拖成 DoS（生产监听 0.0.0.0）。
//
// 注意：不要改用 http.Server.ReadTimeout 来兜这个底 —— gRPC 双向流是长连接，
// 全局读超时会把它一起掐断。这里只对当前请求设置读截止时间。
const (
	drainLimit   = 1 << 20 // 1MB，远大于正常注册/心跳请求体
	drainTimeout = 5 * time.Second
)

// notImplementedProtobuf 在 proto 代码生成落地前，对 protobuf 请求给出可执行的提示，
// 而不是静默地把二进制当 JSON 解析（那样只会得到难以定位的解析错误）。
// 必须先排空请求体再写响应：未读 body 直接 501 会触发连接 RST，
// Agent 端只能看到传输错误（按断网语义错误地进 spool 重试），看不到 501 提示。
func notImplementedProtobuf(c *gin.Context) {
	// SetReadDeadline 在 h2c 下可能不被支持（取决于 x/net/http2 版本），忽略该错误：
	// 大小上限本身已挡住超大 body，读截止时间只是额外兜住「只发一半就挂着」的慢速 body。
	_ = http.NewResponseController(c.Writer).SetReadDeadline(time.Now().Add(drainTimeout))
	_, _ = io.Copy(io.Discard, http.MaxBytesReader(c.Writer, c.Request.Body, drainLimit))
	c.JSON(http.StatusNotImplemented, gin.H{
		"code": "protobuf_not_enabled",
		"message": "Protobuf 编解码随 proto 代码生成启用（make pb）；" +
			"当前请使用 application/json 联调，契约见 docs/04 §2",
		"request_id": requestID(c),
	})
}
