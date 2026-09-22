// asset.go 承载资产部件与硬件变更查询接口（W2/W18，docs/04 §3）：
//
//	GET /api/v1/hosts/{id}/components   部件树（?category=memory 过滤）
//	GET /api/v1/hosts/{id}/changes      硬件变更记录（前端 Asset 页「硬件变更记录」tab）
package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	"github.com/LarryMKott/metalwatch/pkg/utils"
)

// AssetHandler 承载部件与变更查询。
type AssetHandler struct {
	store adapter.MetadataStore
}

// NewAssetHandler 构造资产查询处理器。
func NewAssetHandler(store adapter.MetadataStore) *AssetHandler {
	return &AssetHandler{store: store}
}

// Register 挂载本域路由。
func (h *AssetHandler) Register(v1 *gin.RouterGroup) {
	g := v1.Group("/hosts")
	{
		g.GET("/:id/components", h.Components)
		g.GET("/:id/changes", h.Changes)
	}
}

// componentDTO 对外部件表示。
type componentDTO struct {
	Category      string `json:"category"`
	Slot          string `json:"slot"`
	Name          string `json:"name"`
	Vendor        string `json:"vendor,omitempty"`
	Serial        string `json:"serial,omitempty"`
	Firmware      string `json:"firmware,omitempty"`
	CapacityBytes *int64 `json:"capacity_bytes,omitempty"`
	MediaType     string `json:"media_type,omitempty"`
	Health        string `json:"health,omitempty"`
	FirstSeenAt   string `json:"first_seen_at"`
	LastSeenAt    string `json:"last_seen_at"`
}

// Components 返回一台主机的在役部件树。
func (h *AssetHandler) Components(c *gin.Context) {
	id, err := utils.IDParam(c)
	if err != nil {
		utils.BadJSON(c, err)
		return
	}
	ctx, cancel := utils.Timeout(c, 5*time.Second)
	defer cancel()

	if _, err := h.store.Hosts().GetByID(ctx, id); err != nil {
		utils.Fail(c, err)
		return
	}
	comps, err := h.store.Components().ListActive(ctx, id, c.Query("category"))
	if err != nil {
		utils.Fail(c, err)
		return
	}
	out := make([]componentDTO, 0, len(comps))
	for _, cp := range comps {
		out = append(out, componentDTO{
			Category: cp.Category, Slot: cp.Slot, Name: cp.Name,
			Vendor: cp.Vendor, Serial: cp.Serial, Firmware: cp.Firmware,
			CapacityBytes: cp.CapacityBytes, MediaType: cp.MediaType, Health: cp.Health,
			FirstSeenAt: cp.FirstSeenAt.UTC().Format(time.RFC3339),
			LastSeenAt:  cp.LastSeenAt.UTC().Format(time.RFC3339),
		})
	}
	c.JSON(http.StatusOK, out)
}

// changeDTO 对外变更记录（字段与前端 types/api.ts 的 ChangeRecord 一致）。
type changeDTO struct {
	ID         int64  `json:"id"`
	HostID     int64  `json:"host_id"`
	DetectedAt string `json:"detected_at"`
	Category   string `json:"category"`
	Slot       string `json:"slot,omitempty"`
	ChangeType string `json:"change_type"`
	Field      string `json:"field,omitempty"`
	OldValue   string `json:"old_value,omitempty"`
	NewValue   string `json:"new_value,omitempty"`
	Source     string `json:"source"`
}

// Changes 返回一台主机的硬件变更记录。
func (h *AssetHandler) Changes(c *gin.Context) {
	id, err := utils.IDParam(c)
	if err != nil {
		utils.BadJSON(c, err)
		return
	}
	ctx, cancel := utils.Timeout(c, 5*time.Second)
	defer cancel()

	if _, err := h.store.Hosts().GetByID(ctx, id); err != nil {
		utils.Fail(c, err)
		return
	}
	events, err := h.store.Assets().ListChanges(ctx, id, 200)
	if err != nil {
		utils.Fail(c, err)
		return
	}
	out := make([]changeDTO, 0, len(events))
	for _, e := range events {
		dto := changeDTO{
			ID: e.ID, HostID: e.HostID,
			DetectedAt: e.DetectedAt.UTC().Format(time.RFC3339),
			Category:   e.Category, Slot: e.Slot, ChangeType: e.ChangeType,
			Field: e.Field, Source: "agent",
		}
		if e.OldValue != nil {
			dto.OldValue = *e.OldValue
		}
		if e.NewValue != nil {
			dto.NewValue = *e.NewValue
		}
		out = append(out, dto)
	}
	c.JSON(http.StatusOK, out)
}
