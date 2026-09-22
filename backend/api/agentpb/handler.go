package agentpb

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	"github.com/LarryMKott/metalwatch/internal/pipeline"
	"github.com/LarryMKott/metalwatch/internal/service"
	"github.com/LarryMKott/metalwatch/pkg/utils"
)

// 补传窗口：collected_at 允许回溯的最长时间（docs/04 §2.2）。
const backfillWindow = 24 * time.Hour

// configVersion 是随注册/心跳/上报下发的配置版本。
// 配置中心（W7 收尾）落地前固定为 1。
const configVersion = 1

// EnrollRequest 对应 docs/04 §2.1（JSON 形态）。校验逻辑在 service.EnrollService，
// 两条传输通道共用同一套规则（docs/01 D31 第 1 条）。
type EnrollRequest struct {
	EnrollCode   string `json:"enroll_code"`
	Hostname     string `json:"hostname"`
	PrimaryIP    string `json:"primary_ip"`
	SMBIOSUUID   string `json:"smbios_uuid"`
	OSType       string `json:"os_type"`
	OSVersion    string `json:"os_version"`
	AgentVersion string `json:"agent_version"`
}

// ReportRequest 对应 docs/04 §2.2（硬件清单与 SMART 明细待 W2 接入）。
type ReportRequest struct {
	BatchID     string         `json:"batch_id"`
	HostID      int64          `json:"host_id"`
	Mode        string         `json:"mode"`
	CollectedAt string         `json:"collected_at"`
	Metrics     []MetricSample `json:"metrics"`
}

// MetricSample 是一次指标采样。
type MetricSample struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
	Value  float64           `json:"value"`
}

// Validate 校验上报请求：幂等键必填、补传窗口、指标白名单与标签黑名单。
// now 为服务端当前时间，用于补传窗口判定。
func (r *ReportRequest) Validate(now time.Time) error {
	if r.BatchID == "" {
		return fmt.Errorf("batch_id 为必填（幂等键）")
	}
	if !r.validCollectedAt(now) {
		return fmt.Errorf("collected_at 需为 RFC3339 且不早于 24 小时前（补传窗口）")
	}
	if bad := r.firstInvalidMetric(); bad != "" {
		return fmt.Errorf("指标不在白名单或含非法标签: %s", bad)
	}
	return nil
}

// CollectedAt 返回解析后的采集时间；请求未携带时取 fallback（须在 Validate 通过后调用）。
func (r *ReportRequest) ParsedCollectedAt(fallback time.Time) time.Time {
	if r.CollectedAt == "" {
		return fallback
	}
	if t, err := time.Parse(time.RFC3339, r.CollectedAt); err == nil {
		return t
	}
	return fallback
}

// Mode 归一采集模式；缺省按 Agent 通道处理。
func (r *ReportRequest) normalizedMode() string {
	if strings.TrimSpace(r.Mode) == "" {
		return "agent"
	}
	return strings.TrimSpace(r.Mode)
}

// ToSamples 把请求指标转换为领域样本（含公共标签 host_id/host/mode，docs/03 §3）。
func (r *ReportRequest) ToSamples(host *adapter.Host) []adapter.Sample {
	samples := make([]adapter.Sample, 0, len(r.Metrics))
	mode := r.normalizedMode()
	for _, m := range r.Metrics {
		labels := make(map[string]string, len(m.Labels)+3)
		for k, v := range m.Labels {
			labels[k] = v
		}
		labels["host_id"] = strconv.FormatInt(r.HostID, 10)
		labels["host"] = host.Hostname
		labels["mode"] = mode
		samples = append(samples, adapter.Sample{
			Metric: m.Name, Labels: labels, Value: m.Value, TS: r.ParsedCollectedAt(time.Now().UTC()),
		})
	}
	return samples
}

// validCollectedAt 校验时间戳格式与补传窗口（24h）。
func (r *ReportRequest) validCollectedAt(now time.Time) bool {
	if r.CollectedAt == "" {
		return true // 缺省由服务端取当前时间
	}
	t, err := time.Parse(time.RFC3339, r.CollectedAt)
	if err != nil {
		return false
	}
	return !t.Before(now.Add(-backfillWindow))
}

// firstInvalidMetric 返回首个不在白名单或含高基数标签的指标描述；全部合法返回空串。
func (r *ReportRequest) firstInvalidMetric() string {
	for _, s := range r.Metrics {
		if !service.ValidMetricName(s.Name) {
			return "name=" + s.Name
		}
		for k := range s.Labels {
			if !service.LabelAllowed(k) {
				return "label=" + k
			}
		}
	}
	return ""
}

// AgentHandler 承载 Agent 上报通道（注册 / 指标上报 / 心跳）。
// 依赖在构造时注入；时序写入、去重、告警服务允许为 nil（对应功能未启用）。
type AgentHandler struct {
	store         adapter.MetadataStore
	enroll        *service.EnrollService
	deduper       adapter.IngestDeduper // batch 幂等；随 tsdb 注入自动解析
	pipeline      *pipeline.Pipeline
	alerts        *service.AlertService
	interval      time.Duration // 服务端口径的指标上报周期
	assetInterval time.Duration // 服务端口径的静态资产上报周期
}

// NewAgentHandler 构造 Agent 通道处理器。tsdb 同时承担 IngestDeduper 解析：
// 实现了去重能力的时序后端自动获得批次幂等，否则跳过（不影响协议兼容）。
func NewAgentHandler(store adapter.MetadataStore, enroll *service.EnrollService,
	tsdb adapter.TimeSeriesStore, pl *pipeline.Pipeline, alerts *service.AlertService,
	interval, assetInterval time.Duration) *AgentHandler {
	h := &AgentHandler{
		store: store, enroll: enroll, pipeline: pl, alerts: alerts,
		interval: interval, assetInterval: assetInterval,
	}
	if tsdb != nil {
		if d, ok := tsdb.(adapter.IngestDeduper); ok {
			h.deduper = d
		}
	}
	return h
}

// Register 把 Agent 通道路由挂到根路由上（/api/v1/agent/*）。
func (h *AgentHandler) Register(r *gin.Engine) {
	g := r.Group("/api/v1/agent")
	{
		g.POST("/enroll", h.Enroll)
		g.POST("/report", h.RequireToken(), h.Report)
		g.POST("/heartbeat", h.RequireToken(), h.Heartbeat)
	}
}

// Enroll 处理 Agent 首次注册（JSON 形态）。流程在 service.EnrollService，
// 与 gRPC 通道共享；本方法只做协议编解码与错误映射。
func (h *AgentHandler) Enroll(c *gin.Context) {
	ctx, cancel := utils.Timeout(c, 10*time.Second)
	defer cancel()

	if wantsProtobuf(c) {
		notImplementedProtobuf(c)
		return
	}

	var req EnrollRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadJSON(c, err)
		return
	}

	result, err := h.enroll.Enroll(ctx, service.EnrollInput{
		Code:         req.EnrollCode,
		Hostname:     req.Hostname,
		PrimaryIP:    req.PrimaryIP,
		SMBIOSUUID:   req.SMBIOSUUID,
		OSType:       req.OSType,
		OSVersion:    req.OSVersion,
		AgentVersion: req.AgentVersion,
	})
	if err != nil {
		switch {
		case errors.Is(err, service.ErrEnrollCodeUnavailable):
			c.JSON(http.StatusUnauthorized, utils.ErrorBody{
				Code: "enroll_code_used", Message: err.Error(),
				RequestID: requestID(c),
			})
		case errors.Is(err, service.ErrInvalidInput):
			c.JSON(http.StatusUnprocessableEntity, utils.ErrorBody{
				Code: "invalid_input", Message: err.Error(), RequestID: requestID(c),
			})
		default:
			utils.Fail(c, err)
		}
		return
	}

	// 明文令牌仅此一次返回
	c.JSON(http.StatusCreated, gin.H{
		"host_id":             result.Host.ID,
		"agent_token":         result.AgentToken,
		"report_interval_sec": int(result.ReportInterval.Seconds()),
		"asset_interval_sec":  int(result.AssetInterval.Seconds()),
		"report_url":          "/api/v1/agent/report",
		"codec":               respCodec(c),
		"server_time":         time.Now().UTC().Format(time.RFC3339),
	})
}

// Report 接收指标上报：校验协议 → batch 幂等 → 告警判定 → 异步入时序（W1c 数据链路）。
func (h *AgentHandler) Report(c *gin.Context) {
	ctx, cancel := utils.Timeout(c, 10*time.Second)
	defer cancel()

	if wantsProtobuf(c) {
		notImplementedProtobuf(c)
		return
	}

	tok := TokenOf(c)
	if tok == nil {
		utils.Unauthorized(c, "unauthorized", "缺少令牌上下文")
		return
	}

	var req ReportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadJSON(c, err)
		return
	}
	now := time.Now().UTC()
	if tok.HostID == nil || req.HostID != *tok.HostID {
		c.JSON(http.StatusForbidden, utils.ErrorBody{
			Code: "forbidden_scope", Message: "host_id 与令牌绑定的主机不一致",
			RequestID: requestID(c),
		})
		return
	}
	if err := req.Validate(now); err != nil {
		c.JSON(http.StatusUnprocessableEntity, utils.ErrorBody{
			Code: reportErrorCode(err), Message: err.Error(), RequestID: requestID(c),
		})
		return
	}

	host, err := h.store.Hosts().GetByID(ctx, req.HostID)
	if errors.Is(err, adapter.ErrNotFound) {
		c.JSON(http.StatusNotFound, utils.ErrorBody{
			Code: "host_not_found", Message: "主机不存在，请重新注册",
			RequestID: requestID(c),
		})
		return
	}
	if err != nil {
		utils.Fail(c, err)
		return
	}
	collectedAt := req.ParsedCollectedAt(now)

	// batch 幂等：断网续传会重放同一 batch_id，重复批次不入库（M3 验收 2）
	if h.deduper != nil {
		seen, err := h.deduper.SeenBatch(ctx, req.BatchID, req.HostID, len(req.Metrics))
		if err != nil {
			utils.Fail(c, err)
			return
		}
		if seen {
			c.JSON(http.StatusAccepted, gin.H{
				"accepted": 0, "rejected": 0, "batch_id": req.BatchID,
				"tsdb": "deduplicated", "config_version": configVersion,
				"next_report_at": now.Add(h.interval).Format(time.RFC3339),
			})
			return
		}
	}

	// 组装领域样本并追加服务端视角的 up 指标（大盘在线趋势的数据源，M3）
	samples := req.ToSamples(host)
	mode := req.normalizedMode()
	samples = append(samples, adapter.Sample{
		Metric: "up", Value: 1,
		Labels: map[string]string{
			"host_id": strconv.FormatInt(req.HostID, 10),
			"host":    host.Hostname, "mode": mode,
		},
		TS: collectedAt,
	})

	// 告警判定走原始值同步评估，不等时序落库（docs/03 §2.2 写入路径）
	if h.alerts != nil {
		h.alerts.Evaluate(ctx, host.ID, host.Hostname, samples, collectedAt)
	}

	if err := h.store.Hosts().UpdateStatus(ctx, req.HostID, "online", now); err != nil {
		utils.Fail(c, err)
		return
	}
	_ = h.store.AgentTokens().Touch(ctx, tok.ID, now)

	// 时序写入经 pipeline 异步批量提交（单写者）
	if h.pipeline != nil {
		h.pipeline.Ingest(samples)
	}

	c.JSON(http.StatusAccepted, gin.H{
		"accepted":       len(req.Metrics),
		"rejected":       0,
		"batch_id":       req.BatchID,
		"tsdb":           "accepted",
		"config_version": configVersion,
		"next_report_at": now.Add(h.interval).Format(time.RFC3339),
	})
}

// Heartbeat 处理轻量心跳（无指标），用于在线状态与配置变更通知。
func (h *AgentHandler) Heartbeat(c *gin.Context) {
	ctx, cancel := utils.Timeout(c, 5*time.Second)
	defer cancel()

	tok := TokenOf(c)
	if tok == nil || tok.HostID == nil {
		c.JSON(http.StatusForbidden, utils.ErrorBody{
			Code: "forbidden_scope", Message: "令牌未绑定主机，请先完成注册",
			RequestID: requestID(c),
		})
		return
	}

	now := time.Now().UTC()
	if err := h.store.Hosts().UpdateStatus(ctx, *tok.HostID, "online", now); err != nil {
		utils.Fail(c, err)
		return
	}
	_ = h.store.AgentTokens().Touch(ctx, tok.ID, now)

	c.JSON(http.StatusOK, gin.H{
		"ok":                  true,
		"config_version":      configVersion,
		"report_interval_sec": int(h.interval.Seconds()),
		"server_time":         now.Format(time.RFC3339),
	})
}

// reportErrorCode 把校验错误映射为协议错误码（docs/04 §1 错误码表）。
func reportErrorCode(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "batch_id"):
		return "invalid_input"
	case strings.Contains(msg, "collected_at"):
		return "timestamp_out_of_range"
	default:
		return "metric_not_allowed"
	}
}
