package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	"github.com/LarryMKott/metalwatch/internal/authz"
	"github.com/LarryMKott/metalwatch/pkg/crypto"
	"github.com/LarryMKott/metalwatch/pkg/utils"
)

// TokenHandler 管理开放接口令牌（契约 §三：admin 才能签发与查看）。
//
// 安全约定：库里只存 SHA-256 摘要，明文令牌**仅在创建的响应里出现一次**，
// 之后任何接口都取不到（与 Agent 注册令牌同一套做法）。
type TokenHandler struct {
	store adapter.MetadataStore
}

// NewTokenHandler 构造开放令牌处理器。
func NewTokenHandler(store adapter.MetadataStore) *TokenHandler {
	return &TokenHandler{store: store}
}

// Register 挂载 /api-tokens 路由。
func (h *TokenHandler) Register(v1 *gin.RouterGroup) {
	g := v1.Group("/api-tokens")
	g.GET("", h.list)
	g.POST("", h.create)
	g.DELETE("/:id", h.revoke)
}

type createTokenReq struct {
	Name       string `json:"name" binding:"required"`
	Scopes     string `json:"scopes"`      // 逗号分隔，默认只读
	ExpireDays int    `json:"expire_days"` // 0 = 永不过期
}

type tokenVO struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	Scopes     string  `json:"scopes"`
	State      string  `json:"state"`
	ExpireAt   *string `json:"expire_at,omitempty"`
	LastUsedAt *string `json:"last_used_at,omitempty"`
	CreatedBy  string  `json:"created_by"`
	CreatedAt  string  `json:"created_at"`
}

// list 返回令牌元数据（不含任何明文段）。
func (h *TokenHandler) list(c *gin.Context) {
	ctx, cancel := utils.Timeout(c, 5*time.Second)
	defer cancel()

	rows, err := h.store.APITokens().List(ctx)
	if err != nil {
		utils.Fail(c, err)
		return
	}
	out := make([]tokenVO, 0, len(rows))
	for _, t := range rows {
		out = append(out, toTokenVO(&t))
	}
	c.JSON(http.StatusOK, gin.H{"items": out, "total": len(out)})
}

// create 签发一枚新令牌，明文只在此次响应返回。
func (h *TokenHandler) create(c *gin.Context) {
	var req createTokenReq
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadJSON(c, err)
		return
	}
	scopes := req.Scopes
	if scopes == "" {
		scopes = "asset:read,metric:read"
	}

	plain, err := crypto.RandomToken("mwo_", 24)
	if err != nil {
		utils.Fail(c, err)
		return
	}

	var expireAt *time.Time
	if req.ExpireDays > 0 {
		t := time.Now().UTC().AddDate(0, 0, req.ExpireDays)
		expireAt = &t
	}

	creator := ""
	if sub := authz.SubjectOf(c); sub != nil {
		creator = sub.Username
	}

	ctx, cancel := utils.Timeout(c, 5*time.Second)
	defer cancel()

	id, err := h.store.APITokens().Create(ctx, req.Name, crypto.HashToken(plain), scopes, expireAt, creator)
	if err != nil {
		utils.Fail(c, err)
		return
	}
	tk, err := h.store.APITokens().Get(ctx, id)
	if err != nil || tk == nil {
		utils.Fail(c, err)
		return
	}

	h.appendAudit(c, "api_tokens.create", "ok", "token:"+tk.Name)
	// 明文只出现这一次
	c.JSON(http.StatusCreated, gin.H{"token": plain, "item": toTokenVO(tk)})
}

// revoke 吊销令牌（软删除，保留审计线索）。
func (h *TokenHandler) revoke(c *gin.Context) {
	id, err := utils.IDParam(c)
	if err != nil {
		utils.Fail(c, err)
		return
	}
	ctx, cancel := utils.Timeout(c, 5*time.Second)
	defer cancel()

	if err := h.store.APITokens().Revoke(ctx, id); err != nil {
		utils.Fail(c, err)
		return
	}
	h.appendAudit(c, "api_tokens.revoke", "ok", "")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---------- 辅助 ----------

func toTokenVO(t *adapter.APIToken) tokenVO {
	vo := tokenVO{
		ID: t.ID, Name: t.Name, Scopes: t.Scopes, State: t.State,
		CreatedBy: t.CreatedBy, CreatedAt: t.CreatedAt.UTC().Format(time.RFC3339),
	}
	if t.ExpireAt != nil {
		s := t.ExpireAt.UTC().Format(time.RFC3339)
		vo.ExpireAt = &s
	}
	if t.LastUsedAt != nil {
		s := t.LastUsedAt.UTC().Format(time.RFC3339)
		vo.LastUsedAt = &s
	}
	return vo
}

func (h *TokenHandler) appendAudit(c *gin.Context, action, result, target string) {
	rec := &adapter.AuditLog{
		Action: action, TargetType: "api_token", TargetID: target,
		SourceIP: c.ClientIP(), Result: result,
	}
	if sub := authz.SubjectOf(c); sub != nil {
		rec.Username = sub.Username
		if sub.IsUser() {
			id := sub.UserID
			rec.UserID = &id
		}
	}
	ctx, cancel := utils.Timeout(c, 3*time.Second)
	defer cancel()
	_ = h.store.AuditLogs().Append(ctx, rec)
}
