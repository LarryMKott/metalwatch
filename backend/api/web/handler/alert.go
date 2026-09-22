// alert.go 承载告警中心接口（docs/04 §3 告警域）：
//
//	GET  /api/v1/alerts             事件列表（前端契约：扁平数组）
//	POST /api/v1/alerts/{id}/ack    确认
//	GET  /api/v1/alerts/templates   阈值模板（只读展示）
//
// 路由域对象化约定见 HostHandler 头注（docs/01 D31 第 6 条）。
package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	"github.com/LarryMKott/metalwatch/pkg/utils"
)

// AlertDTO 对外告警事件（字段与前端 types/api.ts 的 Alert 一致）。
// state 是面向展示的归一值：active（未确认 firing）/ acked / resolved / suppressed。
type AlertDTO struct {
	ID       int64    `json:"id"`
	HostID   int64    `json:"host_id"`
	HostName string   `json:"host_name,omitempty"`
	Severity string   `json:"severity"`
	Rule     string   `json:"rule"`
	State    string   `json:"state"`
	FiredAt  string   `json:"fired_at"`
	Value    *float64 `json:"value,omitempty"`
	Message  string   `json:"message,omitempty"`
}

// AlertHandler 承载告警中心接口。依赖：告警事件与阈值仓储（经 MetadataStore 暴露）。
type AlertHandler struct {
	store adapter.MetadataStore
}

// NewAlertHandler 构造告警处理器。
func NewAlertHandler(store adapter.MetadataStore) *AlertHandler {
	return &AlertHandler{store: store}
}

// Register 把本域路由挂到 /api/v1 分组上。/alerts 为静态段，与 POST /alerts/:id/ack
// 分属不同方法树，无路由冲突。
func (h *AlertHandler) Register(v1 *gin.RouterGroup) {
	v1.GET("/alerts", h.List)
	v1.GET("/alerts/templates", h.Templates)
	v1.POST("/alerts/:id/ack", h.Ack)
}

// List 查询告警事件。state 取值：active（默认）/ acked / resolved / suppressed / all。
func (h *AlertHandler) List(c *gin.Context) {
	ctx, cancel := utils.Timeout(c, 10*time.Second)
	defer cancel()

	f, ok := alertFilterOf(c)
	if !ok {
		return
	}
	rows, _, err := h.store.Alerts().List(ctx, f)
	if err != nil {
		utils.Fail(c, err)
		return
	}
	out := make([]AlertDTO, 0, len(rows))
	for i := range rows {
		out = append(out, newAlertDTO(&rows[i]))
	}
	c.JSON(http.StatusOK, out)
}

// Ack 确认一条告警。鉴权（W11）落地前操作人固定为 webui。
func (h *AlertHandler) Ack(c *gin.Context) {
	id, err := utils.IDParam(c)
	if err != nil {
		utils.BadJSON(c, err)
		return
	}
	ctx, cancel := utils.Timeout(c, 5*time.Second)
	defer cancel()

	ok, err := h.store.Alerts().Ack(ctx, id, "webui", time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		utils.Fail(c, err)
		return
	}
	if !ok {
		c.JSON(http.StatusNotFound, utils.ErrorBody{
			Code: "alert_not_found", Message: "告警不存在或已确认", RequestID: utils.RequestIDOf(c),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// Templates 返回阈值模板（前端只读表格：单阈值 + 单级别展示）。
func (h *AlertHandler) Templates(c *gin.Context) {
	ctx, cancel := utils.Timeout(c, 5*time.Second)
	defer cancel()

	tpls, err := h.store.Thresholds().ListTemplates(ctx, false)
	if err != nil {
		utils.Fail(c, err)
		return
	}
	type thresholdTemplateDTO struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		Metric      string `json:"metric"`
		Op          string `json:"op"`
		Threshold   float64
		Severity    string `json:"severity"`
		ForDuration string `json:"for_duration"`
		Enabled     bool   `json:"enabled"`
	}
	out := make([]thresholdTemplateDTO, 0, len(tpls))
	for _, t := range tpls {
		dto := thresholdTemplateDTO{
			ID: t.ID, Name: t.Name, Metric: t.Metric, Op: opSymbol(t.Op),
			Severity: "minor", Enabled: t.Enabled,
			ForDuration: fmt.Sprintf("%ds", t.DurationSec),
		}
		switch {
		case t.CritValue != nil:
			dto.Threshold, dto.Severity = *t.CritValue, "critical"
		case t.WarnValue != nil:
			dto.Threshold = *t.WarnValue
		default:
			continue
		}
		out = append(out, dto)
	}
	c.JSON(http.StatusOK, out)
}

// alertFilterOf 把 state/severity/host_id 查询参数映射为仓储过滤条件。
// 返回 false 表示已直接写出 422。
func alertFilterOf(c *gin.Context) (adapter.AlertFilter, bool) {
	limit, offset := utils.Pagination(c)
	f := adapter.AlertFilter{Limit: limit, Offset: offset}
	switch strings.TrimSpace(c.Query("state")) {
	case "", "active":
		no := false
		f.States = []string{"firing"}
		f.Acked = &no
	case "acked":
		yes := true
		f.States = []string{"firing"}
		f.Acked = &yes
	case "resolved":
		f.States = []string{"resolved"}
	case "suppressed":
		f.States = []string{"silenced", "suppressed"}
	case "all":
		// 不加状态过滤
	default:
		c.JSON(http.StatusUnprocessableEntity, utils.ErrorBody{
			Code: "invalid_input", Message: "state 取值: active/acked/resolved/suppressed/all",
			RequestID: utils.RequestIDOf(c),
		})
		return adapter.AlertFilter{}, false
	}
	if v := strings.TrimSpace(c.Query("severity")); v != "" {
		f.Severity = v
	}
	if v := c.Query("host_id"); v != "" {
		var id int64
		if _, err := fmt.Sscanf(v, "%d", &id); err == nil {
			f.HostID = id
		}
	}
	return f, true
}

// newAlertDTO 把存储层事件转为对外 DTO（纯转换工具）。
func newAlertDTO(e *adapter.AlertEvent) AlertDTO {
	dto := AlertDTO{
		ID: e.ID, Severity: e.Severity, Rule: e.Title,
		FiredAt: e.FirstSeenAt.UTC().Format(time.RFC3339),
		Value:   e.Value,
	}
	if e.HostID != nil {
		dto.HostID = *e.HostID
	}
	dto.HostName = e.Hostname
	switch {
	case e.State == "resolved":
		dto.State = "resolved"
	case e.State == "silenced" || e.State == "suppressed":
		dto.State = "suppressed"
	case e.AckAt != nil:
		dto.State = "acked"
	default:
		dto.State = "active"
	}
	dto.Message = alertMessage(e)
	return dto
}

// alertMessage 还原人类可读说明：优先取落库 detail JSON，取不到退回标题。
func alertMessage(e *adapter.AlertEvent) string {
	if e.Detail == nil {
		return e.Title
	}
	var d struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(*e.Detail), &d); err == nil && d.Message != "" {
		return d.Message
	}
	return e.Title
}

func opSymbol(op string) string {
	switch op {
	case "gt":
		return ">"
	case "lt":
		return "<"
	case "eq":
		return "=="
	case "ne":
		return "!="
	default:
		return op
	}
}
