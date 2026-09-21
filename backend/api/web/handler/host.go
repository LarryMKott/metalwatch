// Package handler 承载前端 REST 接口的具体处理函数。
// 只做三件事：解析参数、调用服务层、封装响应；不写业务规则。
package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"gitee.com/zhangyilin_233/metalwatch/pkg/utils"
	"gitee.com/zhangyilin_233/metalwatch/internal/adapter"
	"gitee.com/zhangyilin_233/metalwatch/internal/app"
	"gitee.com/zhangyilin_233/metalwatch/internal/service"
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

// ToHostDTO 把存储层模型转为对外 DTO。
func ToHostDTO(h *adapter.Host) HostDTO {
	d := HostDTO{
		ID: h.ID, Hostname: h.Hostname, PrimaryIP: h.PrimaryIP, OSType: h.OSType,
		CollectAgent: h.CollectAgent, CollectIPMI: h.CollectIPMI, Status: h.Status,
		RackUnit:  h.RackUnit,
		CreatedAt: h.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: h.UpdatedAt.UTC().Format(time.RFC3339),
	}
	d.BMCIP = deref(h.BMCIP)
	d.SN = deref(h.SN)
	d.SMBIOSUUID = deref(h.SMBIOSUUID)
	d.Site = deref(h.Site)
	d.Rack = deref(h.Rack)
	d.OSVersion = deref(h.OSVersion)
	d.AgentVersion = deref(h.AgentVersion)
	d.GeoCountry = deref(h.GeoCountry)
	d.Remark = deref(h.Remark)
	if h.LastSeenAt != nil {
		d.LastSeenAt = h.LastSeenAt.UTC().Format(time.RFC3339)
	}
	return d
}

// ListHosts 返回分页资产列表。
func ListHosts(d *app.Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := utils.Timeout(c, 5*time.Second)
		defer cancel()

		limit, offset := utils.Pagination(c)
		items, total, err := d.Hosts.List(ctx, adapter.ListFilter{
			Keyword: c.Query("q"), Status: c.Query("status"),
			Limit: limit, Offset: offset,
		})
		if err != nil {
			utils.Fail(c, err)
			return
		}

		out := make([]HostDTO, 0, len(items))
		for _, h := range items {
			out = append(out, ToHostDTO(h))
		}
		c.JSON(http.StatusOK, gin.H{
			"items": out, "total": total,
			"page": offset/limit + 1, "page_size": limit,
		})
	}
}

// CreateHost 手工录入一台资产。
func CreateHost(d *app.Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := utils.Timeout(c, 5*time.Second)
		defer cancel()

		var in service.CreateHostInput
		if err := c.ShouldBindJSON(&in); err != nil {
			utils.BadJSON(c, err)
			return
		}
		h, err := d.Hosts.Create(ctx, in)
		if err != nil {
			utils.Fail(c, err)
			return
		}
		c.JSON(http.StatusCreated, ToHostDTO(h))
	}
}

// GetHost 读取单台资产详情。
func GetHost(d *app.Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := utils.Timeout(c, 5*time.Second)
		defer cancel()

		id, err := utils.IDParam(c)
		if err != nil {
			utils.Fail(c, err)
			return
		}
		h, err := d.Hosts.Get(ctx, id)
		if err != nil {
			utils.Fail(c, err)
			return
		}
		c.JSON(http.StatusOK, ToHostDTO(h))
	}
}

// DeleteHost 删除资产（部件/快照级联，告警历史保留）。
func DeleteHost(d *app.Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := utils.Timeout(c, 5*time.Second)
		defer cancel()

		id, err := utils.IDParam(c)
		if err != nil {
			utils.Fail(c, err)
			return
		}
		if err := d.Hosts.Delete(ctx, id); err != nil {
			utils.Fail(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
