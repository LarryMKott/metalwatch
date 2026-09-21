package web

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"gitee.com/zhangyilin_233/metalwatch/api/agentpb"
	"gitee.com/zhangyilin_233/metalwatch/api/web/handler"
	"gitee.com/zhangyilin_233/metalwatch/pkg/utils"
	"gitee.com/zhangyilin_233/metalwatch/internal/app"
)

// NewRouter 组装前端 REST 路由，并挂载 Agent 上报路由。
//
// 双协议严格隔离：
//   - /api/v1/*        → 前端 Vue：RESTful JSON（本包）
//   - /api/v1/agent/*  → Agent：HTTP/2 + Protobuf 二进制（api/agentpb）
func NewRouter(d *app.Deps) *gin.Engine {
	r := gin.New()
	r.Use(utils.RequestID(), utils.AccessLog(d.Log), utils.Recovery(d.Log))

	// 健康检查：免鉴权，供 FPK 的 cmd/main 做就绪探测与外部监控使用
	r.GET("/healthz", handler.Health(d))

	v1 := r.Group("/api/v1")
	{
		v1.GET("/system/status", handler.SystemStatus(d))
		v1.GET("/system/storage/backends", handler.StorageBackends(d))

		hosts := v1.Group("/hosts")
		{
			hosts.GET("", handler.ListHosts(d))
			hosts.POST("", handler.CreateHost(d))
			hosts.GET("/:id", handler.GetHost(d))
			hosts.DELETE("/:id", handler.DeleteHost(d))
		}
	}

	agentpb.RegisterRoutes(r, d)

	r.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, utils.ErrorBody{
			Code: "not_found", Message: "接口不存在", RequestID: utils.RequestIDOf(c),
		})
	})
	return r
}
