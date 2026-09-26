package web

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/LarryMKott/metalwatch/api/agentpb"
	"github.com/LarryMKott/metalwatch/api/web/handler"
	"github.com/LarryMKott/metalwatch/internal/app"
	"github.com/LarryMKott/metalwatch/pkg/utils"
)

// NewRouter 是 REST 层的组合根：从依赖容器拆出各 Handler 需要的依赖并完成装配
// （docs/01 D31 第 4/8 条：Deps 只在组合根出现，下层组件只认构造注入的依赖）。
//
// 双协议严格隔离：
//   - /api/v1/*        → 前端 Vue：RESTful JSON（本包）
//   - /api/v1/agent/*  → Agent：HTTP/2 + Protobuf 二进制（api/agentpb）
func NewRouter(d *app.Deps) *gin.Engine {
	r := gin.New()
	r.Use(utils.RequestID(), utils.AccessLog(d.Log), utils.Recovery(d.Log))

	v1 := r.Group("/api/v1")

	// W11 鉴权：挂在 /api/v1 组上，覆盖全部管理接口
	// （Agent 通道 /api/v1/agent/* 由 agentpb 自己的 Bearer 校验负责，不经过这里）
	//
	// 这里**不做**「没传主密钥就跳过鉴权」的兜底：主密钥缺失时会话本来也签不出来，
	// 与其静默放行造成裸奔，不如让登录直接失败暴露配置问题。
	v1.Use(NewAuthenticator(AuthConfig{
		MasterKey: d.MasterKey,
		Store:     d.Store,
		Log:       d.Log,
	}).Middleware())

	// 各路由域各自持有自己的路由表：构造注入依赖 → Register 挂路由
	handler.NewSystemHandler(d.Store, d.TSDB, d.Pool, d.Config, d.Version, d.Started).
		Register(r, v1)
	handler.NewHostHandler(d.Hosts).Register(v1)
	handler.NewMetricHandler(d.TSDB, d.Store).Register(v1)
	handler.NewAlertHandler(d.Store).Register(v1)
	handler.NewBMCHandler(d.Store, d.Hosts, d.MasterKey, d.Pool, d.IPMI, d.Log).Register(v1)
	handler.NewAssetHandler(d.Store, d.Hosts).Register(v1)

	// W11：鉴权 / 开放令牌 / 审计
	handler.NewAuthHandler(d.Store, d.MasterKey, 0).Register(v1)
	handler.NewTokenHandler(d.Store).Register(v1)
	handler.NewAuditHandler(d.Store).Register(v1)

	// WebSocket 实时推送（W7）：/api/v1/ws/alerts
	if d.Hub != nil {
		d.Hub.Register(v1)
	}

	agentpb.RegisterRoutes(r, d)

	r.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, utils.ErrorBody{
			Code: "not_found", Message: "接口不存在", RequestID: utils.RequestIDOf(c),
		})
	})
	return r
}
