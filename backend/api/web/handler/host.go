// Package handler 承载前端 REST 接口的具体处理函数。
// 每个路由域一个 Handler 结构体（D31 第 6 条）：构造函数注入依赖 + Register 注册路由 +
// 每条路由一个方法；只做三件事：解析参数、调用服务层、封装响应，不写业务规则。
package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	"github.com/LarryMKott/metalwatch/internal/service"
	"github.com/LarryMKott/metalwatch/pkg/ptr"
	"github.com/LarryMKott/metalwatch/pkg/utils"
)

// HostDTO 是资产对外的 JSON 表示（字段名与 docs/04 契约一致）。
type HostDTO struct {
	ID           int64  `json:"id"`
	Hostname     string `json:"hostname"`
	PrimaryIP    string `json:"primary_ip"`
	BMCIP        string `json:"bmc_ip,omitempty"`
	SN           string `json:"sn,omitempty"`
	SMBIOSUUID   string `json:"smbios_uuid,omitempty"`
	Site         string `json:"site,omitempty"`
	Rack         string `json:"rack,omitempty"`
	RackUnit     *int   `json:"rack_unit,omitempty"`
	OSType       string `json:"os_type"`
	OSVersion    string `json:"os_version,omitempty"`
	CollectAgent bool   `json:"collect_agent"`
	CollectIPMI  bool   `json:"collect_ipmi"`
	AgentVersion string `json:"agent_version,omitempty"`
	Status       string `json:"status"`
	LastSeenAt   string `json:"last_seen_at,omitempty"`
	GeoCountry   string `json:"geo_country,omitempty"`
	Remark       string `json:"remark,omitempty"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

// toHostDTO 把存储层模型转为对外 DTO（纯转换工具，无状态）。
func toHostDTO(h *adapter.Host) HostDTO {
	d := HostDTO{
		ID: h.ID, Hostname: h.Hostname, PrimaryIP: h.PrimaryIP, OSType: h.OSType,
		CollectAgent: h.CollectAgent, CollectIPMI: h.CollectIPMI, Status: h.Status,
		RackUnit:  h.RackUnit,
		CreatedAt: h.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: h.UpdatedAt.UTC().Format(time.RFC3339),
	}
	d.BMCIP = ptr.Deref(h.BMCIP)
	d.SN = ptr.Deref(h.SN)
	d.SMBIOSUUID = ptr.Deref(h.SMBIOSUUID)
	d.Site = ptr.Deref(h.Site)
	d.Rack = ptr.Deref(h.Rack)
	d.OSVersion = ptr.Deref(h.OSVersion)
	d.AgentVersion = ptr.Deref(h.AgentVersion)
	d.GeoCountry = ptr.Deref(h.GeoCountry)
	d.Remark = ptr.Deref(h.Remark)
	if h.LastSeenAt != nil {
		d.LastSeenAt = h.LastSeenAt.UTC().Format(time.RFC3339)
	}
	return d
}

// HostHandler 承载资产台账接口。
type HostHandler struct {
	hosts *service.HostService
}

// NewHostHandler 构造资产处理器。
func NewHostHandler(hosts *service.HostService) *HostHandler {
	return &HostHandler{hosts: hosts}
}

// Register 把资产域路由挂到 /api/v1 分组上。
func (h *HostHandler) Register(v1 *gin.RouterGroup) {
	g := v1.Group("/hosts")
	{
		g.GET("", h.List)
		g.POST("", h.Create)
		g.GET("/:id", h.Get)
		g.DELETE("/:id", h.Delete)
	}
}

// List 返回分页资产列表。
func (h *HostHandler) List(c *gin.Context) {
	ctx, cancel := utils.Timeout(c, 5*time.Second)
	defer cancel()

	limit, offset := utils.Pagination(c)
	items, total, err := h.hosts.List(ctx, adapter.ListFilter{
		Keyword: c.Query("q"), Status: c.Query("status"),
		Limit: limit, Offset: offset,
	})
	if err != nil {
		utils.Fail(c, err)
		return
	}

	out := make([]HostDTO, 0, len(items))
	for _, host := range items {
		out = append(out, toHostDTO(host))
	}
	c.JSON(http.StatusOK, gin.H{
		"items": out, "total": total,
		"page": offset/limit + 1, "page_size": limit,
	})
}

// Create 手工录入一台资产。
func (h *HostHandler) Create(c *gin.Context) {
	ctx, cancel := utils.Timeout(c, 5*time.Second)
	defer cancel()

	var in service.CreateHostInput
	if err := c.ShouldBindJSON(&in); err != nil {
		utils.BadJSON(c, err)
		return
	}
	host, err := h.hosts.Create(ctx, in)
	if err != nil {
		utils.Fail(c, err)
		return
	}
	c.JSON(http.StatusCreated, toHostDTO(host))
}

// Get 读取单台资产详情。
func (h *HostHandler) Get(c *gin.Context) {
	ctx, cancel := utils.Timeout(c, 5*time.Second)
	defer cancel()

	id, err := utils.IDParam(c)
	if err != nil {
		utils.Fail(c, err)
		return
	}
	host, err := h.hosts.Get(ctx, id)
	if err != nil {
		utils.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, toHostDTO(host))
}

// Delete 删除资产（部件/快照级联，告警历史保留）。
func (h *HostHandler) Delete(c *gin.Context) {
	ctx, cancel := utils.Timeout(c, 5*time.Second)
	defer cancel()

	id, err := utils.IDParam(c)
	if err != nil {
		utils.Fail(c, err)
		return
	}
	if err := h.hosts.Delete(ctx, id); err != nil {
		utils.Fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
