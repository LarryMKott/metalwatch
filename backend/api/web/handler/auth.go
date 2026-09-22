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

// AuthHandler 承载登录 / 登出 / 当前用户 / 改密（契约 §三）。
type AuthHandler struct {
	store     adapter.MetadataStore
	masterKey []byte
	ttl       time.Duration
}

// NewAuthHandler 构造鉴权处理器。ttl <= 0 时用默认 12 小时。
func NewAuthHandler(store adapter.MetadataStore, masterKey []byte, ttl time.Duration) *AuthHandler {
	return &AuthHandler{store: store, masterKey: masterKey, ttl: ttl}
}

// Register 挂载 /auth 路由。/auth/login 是公开端点（见 web.Auth 的白名单）。
func (h *AuthHandler) Register(v1 *gin.RouterGroup) {
	g := v1.Group("/auth")
	g.POST("/login", h.login)
	g.DELETE("/logout", h.logout)
	g.GET("/me", h.me)
	g.POST("/change-password", h.changePassword)
}

type loginReq struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

type loginResp struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
	User      userVO `json:"user"`
}

type userVO struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	LastLoginAt string `json:"last_login_at,omitempty"`
}

// login 校验账号口令并签发会话令牌。
//
// 安全要点：
//   - 用户不存在与口令错误返回**同一**错误文案，避免用户名枚举
//   - 用户不存在时也走一次 Argon2id 校验（用固定假哈希），消除响应耗时差异
//   - 失败计数交给登录守卫节流，成功登录清空计数
func (h *AuthHandler) login(c *gin.Context) {
	var req loginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadJSON(c, err)
		return
	}

	ctx, cancel := utils.Timeout(c, 10*time.Second)
	defer cancel()

	u, err := h.store.Users().GetByName(ctx, req.Username)
	if err != nil {
		utils.Fail(c, err)
		return
	}
	guard := authz.GuardOf(c)

	ok := crypto.VerifyPassword(passwordOf(u), req.Password) && u != nil && u.State == "active"
	if !ok {
		if guard != nil {
			guard.Fail(c.ClientIP())
		}
		h.appendAudit(c, "auth.login_failed", "denied", req.Username, `{"reason":"bad_credential"}`)
		utils.Unauthorized(c, "bad_credential", "用户名或口令错误")
		return
	}

	token, err := crypto.SignSession(h.masterKey, crypto.Session{
		UserID: u.ID, Username: u.Username, Role: u.Role,
	}, h.ttl)
	if err != nil {
		utils.Fail(c, err)
		return
	}

	if guard != nil {
		guard.Success(c.ClientIP())
	}
	_ = h.store.Users().TouchLogin(ctx, u.ID, time.Now().UTC(), c.ClientIP())
	h.appendAudit(c, "auth.login", "ok", u.Username, "")

	c.JSON(http.StatusOK, loginResp{
		Token:     token,
		ExpiresAt: time.Now().Add(h.effectiveTTL()).UTC().Format(time.RFC3339),
		User:      toUserVO(u),
	})
}

// logout 客户端丢弃令牌即可；服务端只记审计（无状态令牌没有会话表可清）。
func (h *AuthHandler) logout(c *gin.Context) {
	if sub := authz.SubjectOf(c); sub != nil {
		h.appendAudit(c, "auth.logout", "ok", sub.Username, "")
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// me 返回当前主体信息，供前端渲染用户态与按角色隐藏菜单。
func (h *AuthHandler) me(c *gin.Context) {
	sub := authz.SubjectOf(c)
	if sub == nil {
		utils.Unauthorized(c, "unauthorized", "未登录")
		return
	}
	out := gin.H{
		"kind":      sub.Kind,
		"username":  sub.Username,
		"role":      sub.Role,
		"scopes":    sub.Scopes,
		"can_write": sub.CanWrite(),
		"is_admin":  sub.IsAdmin(),
	}
	if u, err := h.store.Users().Get(c.Request.Context(), sub.UserID); err == nil && u != nil && sub.IsUser() {
		out["user"] = toUserVO(u)
	}
	c.JSON(http.StatusOK, out)
}

type changePasswordReq struct {
	OldPassword string `json:"old_password" binding:"required"`
	NewPassword string `json:"new_password" binding:"required,min=8"`
}

// changePassword 修改当前账号口令（任何登录用户可改自己的）。
func (h *AuthHandler) changePassword(c *gin.Context) {
	sub := authz.SubjectOf(c)
	if sub == nil || !sub.IsUser() {
		utils.Forbidden(c, "forbidden_scope", "仅登录用户可修改口令")
		return
	}
	var req changePasswordReq
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadJSON(c, err)
		return
	}

	ctx, cancel := utils.Timeout(c, 10*time.Second)
	defer cancel()

	u, err := h.store.Users().Get(ctx, sub.UserID)
	if err != nil {
		utils.Fail(c, err)
		return
	}
	if !crypto.VerifyPassword(u.PasswordHash, req.OldPassword) {
		h.appendAudit(c, "auth.change_password", "denied", u.Username, `{"reason":"bad_credential"}`)
		utils.Unauthorized(c, "bad_credential", "原口令不正确")
		return
	}
	hash, err := crypto.HashPassword(req.NewPassword)
	if err != nil {
		utils.Fail(c, err)
		return
	}
	if err := h.store.Users().SetPassword(ctx, u.ID, hash); err != nil {
		utils.Fail(c, err)
		return
	}
	h.appendAudit(c, "auth.change_password", "ok", u.Username, "")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---------- 辅助 ----------

func (h *AuthHandler) effectiveTTL() time.Duration {
	if h.ttl > 0 {
		return h.ttl
	}
	return 12 * time.Hour
}

// passwordOf 取用户口令哈希；用户不存在时返回固定假哈希，
// 使「用户不存在」与「口令错误」的耗时一致（防用户名枚举）。
func passwordOf(u *adapter.AppUser) string {
	if u == nil || u.PasswordHash == "" {
		return dummyHash
	}
	return u.PasswordHash
}

// dummyHash 是一次性生成的假哈希，仅用于凑齐校验耗时。
var dummyHash = mustHashPassword("metalwatch-dummy-for-timing")

func mustHashPassword(pw string) string {
	h, err := crypto.HashPassword(pw)
	if err != nil {
		// 随机源故障极不可能发生；退化成空串会让 VerifyPassword 直接返回 false，可接受
		return ""
	}
	return h
}

func toUserVO(u *adapter.AppUser) userVO {
	vo := userVO{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Role: u.Role}
	if u.LastLoginAt != nil {
		vo.LastLoginAt = u.LastLoginAt.UTC().Format(time.RFC3339)
	}
	return vo
}

func (h *AuthHandler) appendAudit(c *gin.Context, action, result, username, detail string) {
	rec := &adapter.AuditLog{
		Username:   username,
		Action:     action,
		TargetType: "auth",
		SourceIP:   c.ClientIP(),
		Result:     result,
		Detail:     detail,
	}
	if sub := authz.SubjectOf(c); sub != nil && sub.IsUser() {
		id := sub.UserID
		rec.UserID = &id
	}
	ctx, cancel := utils.Timeout(c, 3*time.Second)
	defer cancel()
	_ = h.store.AuditLogs().Append(ctx, rec)
}
