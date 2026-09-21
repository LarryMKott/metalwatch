package agentpb

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	"github.com/LarryMKott/metalwatch/internal/app"
	"github.com/LarryMKott/metalwatch/internal/service"
	"github.com/LarryMKott/metalwatch/pkg/crypto"
	"github.com/LarryMKott/metalwatch/pkg/utils"
)

// 令牌前缀便于识别与轮换。
const TokenPrefix = "mwa_"

// EnrollRequest 对应 docs/04 §2.1。
type EnrollRequest struct {
	EnrollCode   string `json:"enroll_code"`
	Hostname     string `json:"hostname"`
	PrimaryIP    string `json:"primary_ip"`
	SMBIOSUUID   string `json:"smbios_uuid"`
	OSType       string `json:"os_type"`
	OSVersion    string `json:"os_version"`
	AgentVersion string `json:"agent_version"`
}

// ReportRequest 对应 docs/04 §2.2（硬件清单与 SMART 明细待 W1b/W2 接入）。
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

// allowedMetrics 指标白名单：只接受已知指标，避免脏数据污染时序库。
var allowedMetrics = map[string]bool{
	"up": true, "collect_duration_seconds": true,
	"cpu_temp_celsius": true, "memory_temp_celsius": true,
	"fan_rpm": true, "voltage_volts": true, "power_watts": true,
	"psu_status": true, "disk_temp_celsius": true, "disk_smart_status": true,
	"disk_wear_percent": true, "disk_reallocated_sectors": true,
	"disk_power_on_hours": true, "raid_state": true, "raid_rebuild_percent": true,
	"nic_error_total": true, "host_info": true,
}

// forbiddenLabelKeys 是高基数标签黑名单（会打爆时序索引，见 docs/03 §3）。
var forbiddenLabelKeys = map[string]bool{
	"sn": true, "serial": true, "timestamp": true, "ts": true,
	"message": true, "title": true, "alert": true,
}

// Enroll 处理 Agent 首次注册：校验一次性注册码 → 复用或新建资产 → 签发令牌。
func Enroll(d *app.Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
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

		req.Hostname = strings.TrimSpace(req.Hostname)
		req.PrimaryIP = strings.TrimSpace(req.PrimaryIP)
		req.EnrollCode = strings.TrimSpace(req.EnrollCode)
		req.SMBIOSUUID = strings.TrimSpace(req.SMBIOSUUID)

		if req.EnrollCode == "" || req.Hostname == "" || net.ParseIP(req.PrimaryIP) == nil {
			c.JSON(http.StatusUnprocessableEntity, utils.ErrorBody{
				Code: "invalid_input",
				Message: "enroll_code、hostname 与合法的 primary_ip 均为必填",
				RequestID: requestID(c),
			})
			return
		}

		// 注册码一次性、有有效期
		if err := d.Store.EnrollCodes().Consume(ctx, crypto.HashToken(req.EnrollCode)); err != nil {
			if errors.Is(err, adapter.ErrCodeExhausted) {
				c.JSON(http.StatusUnauthorized, utils.ErrorBody{
					Code: "enroll_code_used", Message: "注册码无效、已过期或已被使用",
					RequestID: requestID(c),
				})
				return
			}
			utils.Fail(c, err)
			return
		}

		// 硬件指纹相同视为同一台机器：复用 host_id，重装 Agent 不产生重复资产
		host, err := d.Store.Hosts().GetBySMBIOSUUID(ctx, req.SMBIOSUUID)
		if errors.Is(err, adapter.ErrNotFound) || req.SMBIOSUUID == "" {
			host, err = d.Hosts.Create(ctx, service.CreateHostInput{
				Hostname:   req.Hostname,
				PrimaryIP:  req.PrimaryIP,
				SMBIOSUUID: ptrOrNil(req.SMBIOSUUID),
				OSType:     req.OSType,
				OSVersion:  ptrOrNil(req.OSVersion),
			})
		}
		if err != nil {
			utils.Fail(c, err)
			return
		}

		token, err := crypto.RandomToken(TokenPrefix, 32)
		if err != nil {
			utils.Fail(c, err)
			return
		}
		now := time.Now().UTC()
		if _, err := d.Store.AgentTokens().Create(ctx, &adapter.AgentToken{
			HostID: &host.ID, TokenHash: crypto.HashToken(token),
			EnrolledAt: &now, State: "active",
		}); err != nil {
			utils.Fail(c, err)
			return
		}

		d.Log.Info("Agent 注册成功", "host_id", host.ID, "hostname", host.Hostname)

		// 明文令牌仅此一次返回
		c.JSON(http.StatusCreated, gin.H{
			"host_id":             host.ID,
			"agent_token":         token,
			"report_interval_sec": d.Config.Collect.SensorInterval,
			"asset_interval_sec":  d.Config.Collect.AssetInterval,
			"report_url":          "/api/v1/agent/report",
			"codec":               respCodec(c),
			"server_time":         now.Format(time.RFC3339),
		})
	}
}

// Report 接收指标上报：校验协议 → 更新在线状态。
// 指标写入内嵌时序存储（W1b）与硬件清单落库（W2）在后续工作流实现。
func Report(d *app.Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
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
		if tok.HostID == nil || req.HostID != *tok.HostID {
			c.JSON(http.StatusForbidden, utils.ErrorBody{
				Code: "forbidden_scope", Message: "host_id 与令牌绑定的主机不一致",
				RequestID: requestID(c),
			})
			return
		}
		if req.BatchID == "" {
			c.JSON(http.StatusUnprocessableEntity, utils.ErrorBody{
				Code: "invalid_input", Message: "batch_id 为必填（幂等键）",
				RequestID: requestID(c),
			})
			return
		}
		if !validCollectedAt(req.CollectedAt) {
			c.JSON(http.StatusUnprocessableEntity, utils.ErrorBody{
				Code:    "timestamp_out_of_range",
				Message: "collected_at 需为 RFC3339 且不早于 24 小时前（补传窗口）",
				RequestID: requestID(c),
			})
			return
		}
		if bad := firstInvalidMetric(req.Metrics); bad != "" {
			c.JSON(http.StatusUnprocessableEntity, utils.ErrorBody{
				Code: "metric_not_allowed", Message: "指标不在白名单或含非法标签: " + bad,
				RequestID: requestID(c),
			})
			return
		}

		now := time.Now().UTC()
		if err := d.Store.Hosts().UpdateStatus(ctx, req.HostID, "online", now); err != nil {
			utils.Fail(c, err)
			return
		}
		_ = d.Store.AgentTokens().Touch(ctx, tok.ID, now)

		c.JSON(http.StatusAccepted, gin.H{
			"accepted":       len(req.Metrics),
			"rejected":       0,
			"batch_id":       req.BatchID,
			"tsdb":           "pending",
			"config_version": 1,
			"next_report_at": now.Add(time.Duration(d.Config.Collect.SensorInterval) * time.Second).Format(time.RFC3339),
		})
	}
}

// Heartbeat 处理轻量心跳（无指标），用于在线状态与配置变更通知。
func Heartbeat(d *app.Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
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
		if err := d.Store.Hosts().UpdateStatus(ctx, *tok.HostID, "online", now); err != nil {
			utils.Fail(c, err)
			return
		}
		_ = d.Store.AgentTokens().Touch(ctx, tok.ID, now)

		c.JSON(http.StatusOK, gin.H{
			"ok":                  true,
			"config_version":      1,
			"report_interval_sec": d.Config.Collect.SensorInterval,
			"server_time":         now.Format(time.RFC3339),
		})
	}
}

func firstInvalidMetric(samples []MetricSample) string {
	for _, s := range samples {
		if !allowedMetrics[s.Name] {
			return "name=" + s.Name
		}
		for k := range s.Labels {
			if forbiddenLabelKeys[strings.ToLower(k)] {
				return "label=" + k
			}
		}
	}
	return ""
}

// validCollectedAt 校验时间戳格式与补传窗口（24h）。
func validCollectedAt(s string) bool {
	if s == "" {
		return true // 缺省由服务端取当前时间
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return false
	}
	return !t.Before(time.Now().UTC().Add(-24 * time.Hour))
}

func ptrOrNil(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}
