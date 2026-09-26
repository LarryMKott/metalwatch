// bmc.go 承载带外（BMC/IPMI）管理接口（W4 / W15）：
//
//	PUT    /api/v1/hosts/{id}/bmc       录入/更新凭据（密码 AES-256-GCM 加密落库，明文不落盘）
//	DELETE /api/v1/hosts/{id}/bmc       移除凭据并停用带外采集
//	POST   /api/v1/hosts/{id}/bmc/test  连通性测试（读电源状态，最轻量）
//	GET    /api/v1/collect-runs         采集执行记录（docs/04 §3）
//	GET    /api/v1/bmc/{id}/capability  能力探测（W15；mc info）
//	POST   /api/v1/bmc/{id}/command     指令下发：风扇/电源/指示灯/交还自动（W15）
//	GET    /api/v1/bmc/audit            管控操作审计（W15；最近 N 条）
//
// 密码只在请求与内存中出现：日志、DB、错误响应均不含明文（docs/01 D9 硬性要求）。
package handler

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	"github.com/LarryMKott/metalwatch/internal/authz"
	"github.com/LarryMKott/metalwatch/internal/service"
	"github.com/LarryMKott/metalwatch/internal/task"
	"github.com/LarryMKott/metalwatch/pkg/crypto"
	"github.com/LarryMKott/metalwatch/pkg/utils"
)

// BMCHandler 承载带外管理接口。
// masterKey 为凭据加密主密钥（docs/01 D9）；pool/poller/ctrl 允许为 nil
// （带外未启用时 test / 管控接口分别降级）。
type BMCHandler struct {
	store     adapter.MetadataStore
	hosts     *service.HostService // 主机存在性经 service 层校验（404 语义）
	masterKey []byte
	pool      *task.Pool
	poller    *service.IPMIPoller
	ctrl      *service.BMCControlService // W15 管控；nil 时管控接口返回 503
	log       *slog.Logger
}

// NewBMCHandler 构造带外管理处理器。
func NewBMCHandler(store adapter.MetadataStore, hosts *service.HostService, masterKey []byte,
	pool *task.Pool, poller *service.IPMIPoller, ctrl *service.BMCControlService,
	log *slog.Logger) *BMCHandler {
	if log == nil {
		log = slog.Default()
	}
	return &BMCHandler{store: store, hosts: hosts, masterKey: masterKey,
		pool: pool, poller: poller, ctrl: ctrl, log: log}
}

// Register 挂载本域路由。
func (h *BMCHandler) Register(v1 *gin.RouterGroup) {
	g := v1.Group("/hosts")
	{
		g.PUT("/:id/bmc", h.SetCredential)
		g.DELETE("/:id/bmc", h.RemoveCredential)
		g.POST("/:id/bmc/test", h.Test)
	}
	v1.GET("/collect-runs", h.ListRuns)
	v1.GET("/bmc/audit", h.ListBmcAudit)
	bmc := v1.Group("/bmc")
	{
		bmc.GET("/:id/capability", h.BmcCapability)
		bmc.POST("/:id/command", h.SendBmcCommand)
	}
}

// bmcCredentialInput 是凭据录入请求体。
type bmcCredentialInput struct {
	BMCIP    string `json:"bmc_ip"`
	Username string `json:"username"`
	Password string `json:"password"`
	Protocol string `json:"protocol"` // ipmi20（默认）| ipmi15 | redfish
}

// SetCredential 录入/更新一台主机的 BMC 凭据并启用带外采集，随后同步调度目标。
func (h *BMCHandler) SetCredential(c *gin.Context) {
	id, err := utils.IDParam(c)
	if err != nil {
		utils.BadJSON(c, err)
		return
	}
	var in bmcCredentialInput
	if err := c.ShouldBindJSON(&in); err != nil {
		utils.BadJSON(c, err)
		return
	}
	in.BMCIP = strings.TrimSpace(in.BMCIP)
	in.Username = strings.TrimSpace(in.Username)
	if net.ParseIP(in.BMCIP) == nil || in.Username == "" || in.Password == "" {
		c.JSON(http.StatusUnprocessableEntity, utils.ErrorBody{
			Code: "invalid_input", Message: "合法的 bmc_ip、username 与 password 均为必填",
			RequestID: utils.RequestIDOf(c),
		})
		return
	}
	protocol := in.Protocol
	if protocol == "" {
		protocol = "ipmi20"
	}
	switch protocol {
	case "ipmi15", "ipmi20", "redfish":
	default:
		c.JSON(http.StatusUnprocessableEntity, utils.ErrorBody{
			Code: "invalid_input", Message: "protocol 只能是 ipmi15/ipmi20/redfish",
			RequestID: utils.RequestIDOf(c),
		})
		return
	}

	ctx, cancel := utils.Timeout(c, 10*time.Second)
	defer cancel()

	if _, err := h.hosts.Get(ctx, id); err != nil {
		utils.Fail(c, err)
		return
	}

	nonce, cipher, err := crypto.Seal(h.masterKey, []byte(in.Password), crypto.AADForHost(id))
	if err != nil {
		utils.Fail(c, err)
		return
	}
	if err := h.store.BMC().Upsert(ctx, &adapter.BMCCredential{
		HostID: id, Username: in.Username, Protocol: protocol,
		KeyVersion: 1, SecretCipher: cipher, SecretNonce: nonce,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		utils.Fail(c, err)
		return
	}
	if err := h.store.Hosts().SetBMC(ctx, id, in.BMCIP, true, time.Now().UTC()); err != nil {
		utils.Fail(c, err)
		return
	}
	h.syncTargetsAsync()
	c.Status(http.StatusNoContent)
}

// RemoveCredential 移除凭据并停用带外采集。
func (h *BMCHandler) RemoveCredential(c *gin.Context) {
	id, err := utils.IDParam(c)
	if err != nil {
		utils.BadJSON(c, err)
		return
	}
	ctx, cancel := utils.Timeout(c, 10*time.Second)
	defer cancel()

	if _, err := h.hosts.Get(ctx, id); err != nil {
		utils.Fail(c, err)
		return
	}
	existed, err := h.store.BMC().Delete(ctx, id)
	if err != nil {
		utils.Fail(c, err)
		return
	}
	if err := h.store.Hosts().SetBMC(ctx, id, "", false, time.Now().UTC()); err != nil {
		utils.Fail(c, err)
		return
	}
	h.syncTargetsAsync()
	if !existed {
		c.JSON(http.StatusNotFound, utils.ErrorBody{
			Code: "bmc_credential_not_found", Message: "该主机未录入 BMC 凭据",
			RequestID: utils.RequestIDOf(c),
		})
		return
	}
	c.Status(http.StatusNoContent)
}

// Test 做一次带外连通性检查（读电源状态，走任务池的串行与退避约束）。
func (h *BMCHandler) Test(c *gin.Context) {
	if h.poller == nil {
		c.JSON(http.StatusServiceUnavailable, utils.ErrorBody{
			Code: "ipmi_disabled", Message: "带外采集未启用", RequestID: utils.RequestIDOf(c),
		})
		return
	}
	id, err := utils.IDParam(c)
	if err != nil {
		utils.BadJSON(c, err)
		return
	}
	ctx, cancel := utils.Timeout(c, 35*time.Second)
	defer cancel()

	result, err := h.poller.TestConnection(ctx, id)
	if err != nil {
		c.JSON(http.StatusBadGateway, utils.ErrorBody{
			Code: "bmc_unreachable", Message: err.Error(), RequestID: utils.RequestIDOf(c),
		})
		return
	}
	c.JSON(http.StatusOK, result)
}

// ListRuns 查询采集执行记录（?host_id=&state=&mode=）。
func (h *BMCHandler) ListRuns(c *gin.Context) {
	ctx, cancel := utils.Timeout(c, 5*time.Second)
	defer cancel()

	limit, offset := utils.Pagination(c)
	f := adapter.CollectRunFilter{Limit: limit, Offset: offset}
	if v := c.Query("host_id"); v != "" {
		var hostID int64
		if _, err := fmt.Sscanf(v, "%d", &hostID); err == nil {
			f.HostID = hostID
		}
	}
	f.State = strings.TrimSpace(c.Query("state"))
	f.Mode = strings.TrimSpace(c.Query("mode"))

	rows, total, err := h.store.CollectRuns().List(ctx, f)
	if err != nil {
		utils.Fail(c, err)
		return
	}
	type runDTO struct {
		ID         int64  `json:"id"`
		HostID     int64  `json:"host_id"`
		Mode       string `json:"mode"`
		State      string `json:"state"`
		ErrorCode  string `json:"error_code,omitempty"`
		ErrorMsg   string `json:"error_msg,omitempty"`
		DurationMS int64  `json:"duration_ms"`
		StartedAt  string `json:"started_at"`
		FinishedAt string `json:"finished_at"`
	}
	out := make([]runDTO, 0, len(rows))
	for _, r := range rows {
		dto := runDTO{
			ID: r.ID, HostID: r.HostID, Mode: r.Mode, State: r.State,
			DurationMS: r.DurationMS,
			StartedAt:  r.StartedAt.UTC().Format(time.RFC3339),
			FinishedAt: r.FinishedAt.UTC().Format(time.RFC3339),
		}
		if r.ErrorCode != nil {
			dto.ErrorCode = *r.ErrorCode
		}
		if r.ErrorMsg != nil {
			dto.ErrorMsg = *r.ErrorMsg
		}
		out = append(out, dto)
	}
	c.JSON(http.StatusOK, gin.H{
		"items": out, "total": total,
		"limit": limit, "offset": offset,
	})
}

// syncTargetsAsync 异步同步调度目标（凭据变更立即生效，不阻塞请求）。
func (h *BMCHandler) syncTargetsAsync() {
	if h.poller == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := h.poller.SyncTargets(ctx); err != nil {
			h.log.Warn("带外调度目标同步失败", "err", err)
		}
	}()
}

// SendBmcCommand 下发一条管控指令（W15）。同步执行：BMC 指令是亚秒级操作，
// 同步返回执行结果比「下发后轮询状态」简单且足够。执行失败返回 ok=false 的
// 200 响应——IPMI 故障是业务结果，不是 API 故障。
func (h *BMCHandler) SendBmcCommand(c *gin.Context) {
	if h.ctrl == nil {
		c.JSON(http.StatusServiceUnavailable, utils.ErrorBody{
			Code: "bmc_control_disabled", Message: "BMC 管控未启用",
			RequestID: utils.RequestIDOf(c),
		})
		return
	}
	id, err := utils.IDParam(c)
	if err != nil {
		utils.BadJSON(c, err)
		return
	}
	var in service.BMCCommandInput
	if err := c.ShouldBindJSON(&in); err != nil {
		utils.BadJSON(c, err)
		return
	}
	ctx, cancel := utils.Timeout(c, 20*time.Second)
	defer cancel()

	result, err := h.ctrl.Execute(ctx, id, operatorOf(c), in)
	if err != nil {
		utils.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// BmcCapability 探测一台主机的 BMC 控制能力（W15）。
func (h *BMCHandler) BmcCapability(c *gin.Context) {
	if h.ctrl == nil {
		c.JSON(http.StatusServiceUnavailable, utils.ErrorBody{
			Code: "bmc_control_disabled", Message: "BMC 管控未启用",
			RequestID: utils.RequestIDOf(c),
		})
		return
	}
	id, err := utils.IDParam(c)
	if err != nil {
		utils.BadJSON(c, err)
		return
	}
	ctx, cancel := utils.Timeout(c, 10*time.Second)
	defer cancel()

	cap, err := h.ctrl.Capability(ctx, id)
	if err != nil {
		utils.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, cap)
}

// ListBmcAudit 查询管控操作审计（W15；?limit= 默认 100，上限 500）。
func (h *BMCHandler) ListBmcAudit(c *gin.Context) {
	ctx, cancel := utils.Timeout(c, 5*time.Second)
	defer cancel()

	limit := 100
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	rows, err := h.store.BMCCommands().List(ctx, limit)
	if err != nil {
		utils.Fail(c, err)
		return
	}
	type auditDTO struct {
		ID        int64  `json:"id"`
		HostID    int64  `json:"host_id"`
		HostName  string `json:"host_name,omitempty"`
		Operator  string `json:"operator"`
		CmdType   string `json:"cmd_type"`
		Target    string `json:"target,omitempty"`
		Params    string `json:"params,omitempty"`
		Result    string `json:"result"` // success | failed（pending 视为 failed：同步执行下不应存在）
		CreatedAt string `json:"created_at"`
	}
	out := make([]auditDTO, 0, len(rows))
	for _, r := range rows {
		result := "failed"
		if r.Status == "success" {
			result = "success"
		}
		dto := auditDTO{
			ID: r.ID, HostID: r.HostID, HostName: r.Hostname, Operator: r.Operator,
			CmdType: r.CmdType, Target: r.Target, Params: r.Params,
			Result: result, CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339),
		}
		out = append(out, dto)
	}
	c.JSON(http.StatusOK, gin.H{"items": out, "limit": limit})
}

// operatorOf 取当前登录用户名；缺失（理论上鉴权中间件保证存在）记 unknown。
func operatorOf(c *gin.Context) string {
	if sub := authz.SubjectOf(c); sub != nil {
		return sub.Username
	}
	return "unknown"
}
