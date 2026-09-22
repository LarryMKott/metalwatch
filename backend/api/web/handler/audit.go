package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	"github.com/LarryMKott/metalwatch/pkg/utils"
)

// AuditHandler 提供审计日志查询（契约 §三：GET /api/v1/audit-logs，仅管理员）。
type AuditHandler struct {
	store adapter.MetadataStore
}

// NewAuditHandler 构造审计处理器。
func NewAuditHandler(store adapter.MetadataStore) *AuditHandler {
	return &AuditHandler{store: store}
}

// Register 挂载 /audit-logs 路由。
func (h *AuditHandler) Register(v1 *gin.RouterGroup) {
	v1.GET("/audit-logs", h.list)
}

// list 倒序返回审计记录，支持按用户名 / 动作前缀 / 结果 / 时间区间过滤。
func (h *AuditHandler) list(c *gin.Context) {
	ctx, cancel := utils.Timeout(c, 5*time.Second)
	defer cancel()

	limit, offset := utils.Pagination(c)
	f := adapter.AuditFilter{
		Username: c.Query("username"),
		Action:   c.Query("action"),
		Result:   c.Query("result"),
		Limit:    limit,
		Offset:   offset,
	}
	if v := c.Query("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.From = &t
		}
	}
	if v := c.Query("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.To = &t
		}
	}

	rows, total, err := h.store.AuditLogs().List(ctx, f)
	if err != nil {
		utils.Fail(c, err)
		return
	}
	out := make([]gin.H, 0, len(rows))
	for _, a := range rows {
		out = append(out, gin.H{
			"id": a.ID, "user_id": a.UserID, "username": a.Username,
			"action": a.Action, "target_type": a.TargetType, "target_id": a.TargetID,
			"source_ip": a.SourceIP, "result": a.Result, "detail": a.Detail,
			"created_at": a.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": out, "total": total})
}
