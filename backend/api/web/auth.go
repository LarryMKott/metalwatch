package web

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	"github.com/LarryMKott/metalwatch/internal/authz"
	"github.com/LarryMKott/metalwatch/pkg/crypto"
	"github.com/LarryMKott/metalwatch/pkg/utils"
)

// W11 鉴权与审计（契约见 docs/02-设计/02-接口契约 §三）。
//
// 设计取舍：
//   - 令牌形态：登录签发自签 HMAC 会话令牌（无状态，密钥取 master.key）；
//     开放接口用库里的 api_token（只存 SHA-256 摘要）
//   - 权限判定放在中间件而不是逐个 handler：写操作一律要求 operator 及以上，
//     高危路径（用户 / Token / BMC 凭据 / 主机增删 / 明文导出）要求 admin ——
//     新增接口默认受保护，不会因为漏写判断而裸奔
//   - 审计由中间件统一记录写操作与登录结果

// adminOnly 是「仅管理员」路由（method + gin 路由模板）。
// 依据契约权限矩阵：主机增删 / BMC 凭据 / 用户 / Token / 明文导出。
var adminOnly = map[string]bool{
	"POST /api/v1/hosts":            true, // 手工录入主机
	"DELETE /api/v1/hosts/:id":      true,
	"PUT /api/v1/hosts/:id/bmc":     true, // BMC 凭据（明文口令，必须收口）
	"DELETE /api/v1/hosts/:id/bmc":  true,
	"GET /api/v1/users":             true,
	"POST /api/v1/users":            true,
	"PUT /api/v1/users/:id":         true,
	"DELETE /api/v1/users/:id":      true,
	"GET /api/v1/api-tokens":        true,
	"POST /api/v1/api-tokens":       true,
	"DELETE /api/v1/api-tokens/:id": true,
	"GET /api/v1/audit-logs":        true,
	"GET /api/v1/assets/export":     true,
}

// publicRoutes 是免鉴权路由：登录必须公开，否则无人能拿到令牌。
var publicRoutes = map[string]bool{
	"POST /api/v1/auth/login": true,
}

// AuthConfig 是鉴权中间件依赖。
type AuthConfig struct {
	MasterKey []byte
	Store     adapter.MetadataStore
	Log       *slog.Logger
}

// Auth 是管理接口的鉴权 + 授权 + 审计中间件，挂在 /api/v1 组上。
// Agent 通道（/api/v1/agent/*）用自己的 Bearer 校验，不经过这里。
func Auth(cfg AuthConfig) gin.HandlerFunc {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	guard := authz.NewLoginGuard()

	return func(c *gin.Context) {
		route := c.Request.Method + " " + c.FullPath()

		if publicRoutes[route] {
			// 登录接口本身也要防爆破：失败达阈值后一段时间内直接拒绝
			ip := c.ClientIP()
			if !guard.Allow(c, ip, func(retryAfter int) {
				c.Header("Retry-After", itoa(retryAfter))
				c.AbortWithStatusJSON(http.StatusTooManyRequests, utils.ErrorBody{
					Code:      "too_many_attempts",
					Message:   "登录失败次数过多，请稍后再试",
					RequestID: utils.RequestIDOf(c),
				})
			}) {
				return
			}
			authz.SetGuard(c, guard)
			c.Next()
			return
		}

		sub, ok := authenticate(c, cfg)
		if !ok {
			return // 失败响应已写入
		}
		authz.SetSubject(c, sub)

		if adminOnly[route] && !sub.IsAdmin() {
			audit(c, cfg, sub, "denied", "权限不足：该操作仅管理员可用")
			utils.Forbidden(c, "forbidden_scope", "该操作仅管理员可用")
			return
		}
		if !authz.IsReadOnly(c.Request.Method) && !sub.CanWrite() {
			audit(c, cfg, sub, "denied", "权限不足：当前角色为只读")
			utils.Forbidden(c, "forbidden_scope", "当前角色无写操作权限")
			return
		}

		c.Next()

		// 审计：写操作按响应码记 ok / fail
		if !authz.IsReadOnly(c.Request.Method) {
			result := "ok"
			if c.Writer.Status() >= 400 {
				result = "fail"
			}
			audit(c, cfg, sub, result, "")
		}
	}
}

// authenticate 先按会话令牌解析，失败再按开放接口令牌解析。
func authenticate(c *gin.Context, cfg AuthConfig) (*authz.Subject, bool) {
	token := bearerFrom(c)
	if token == "" {
		utils.Unauthorized(c, "unauthorized", "缺少 Bearer 令牌")
		return nil, false
	}

	// 1) 登录会话令牌
	if s, err := crypto.VerifySession(cfg.MasterKey, token); err == nil {
		sub := &authz.Subject{Kind: "user", UserID: s.UserID, Username: s.Username, Role: s.Role}
		// 会话签发后角色可能已被改：以库里为准，防止权限回收后旧令牌仍生效
		if cfg.Store != nil {
			if u, err := cfg.Store.Users().Get(c.Request.Context(), s.UserID); err == nil && u != nil {
				sub.Role = u.Role
				sub.Username = u.Username
				if u.State != "active" {
					utils.Forbidden(c, "account_disabled", "账号已停用")
					return nil, false
				}
			}
		}
		return sub, true
	}

	// 2) 开放接口令牌（只读）：库里只存 SHA-256 摘要
	if cfg.Store == nil {
		utils.Unauthorized(c, "unauthorized", "令牌无效")
		return nil, false
	}
	tk, err := cfg.Store.APITokens().GetByHash(c.Request.Context(), crypto.HashToken(token))
	if err != nil || tk == nil {
		utils.Unauthorized(c, "unauthorized", "令牌无效")
		return nil, false
	}
	if tk.State != "active" {
		utils.Unauthorized(c, "token_revoked", "令牌已吊销")
		return nil, false
	}
	if tk.ExpireAt != nil && !tk.ExpireAt.After(time.Now()) {
		utils.Unauthorized(c, "token_expired", "令牌已过期")
		return nil, false
	}
	sub := &authz.Subject{
		Kind: "token", UserID: tk.ID, Username: "token:" + tk.Name,
		Scopes: strings.Split(tk.Scopes, ","),
	}
	// last_used_at 只做分钟级回写，避免每个请求都产生一次 UPDATE
	if tk.LastUsedAt == nil || time.Since(*tk.LastUsedAt) > time.Minute {
		_ = cfg.Store.APITokens().TouchUsed(c.Request.Context(), tk.ID, time.Now().UTC())
	}
	return sub, true
}

// bearerFrom 取令牌：优先 Authorization 头，其次 query（WebSocket 握手带不了自定义头）。
func bearerFrom(c *gin.Context) string {
	if raw := c.GetHeader("Authorization"); raw != "" {
		if strings.HasPrefix(raw, "Bearer ") {
			return strings.TrimSpace(strings.TrimPrefix(raw, "Bearer "))
		}
		return ""
	}
	if q := c.Query("token"); q != "" {
		return strings.TrimSpace(q)
	}
	return ""
}

// ---------- 审计 ----------

// audit 落一条审计记录。审计失败只记日志，绝不阻断业务请求。
func audit(c *gin.Context, cfg AuthConfig, sub *authz.Subject, result, note string) {
	if cfg.Store == nil {
		return
	}
	detail := ""
	if note != "" {
		detail = `{"note":"` + note + `","status":` + itoa(c.Writer.Status()) + "}"
	}
	rec := &adapter.AuditLog{
		Username:   usernameOf(sub),
		Action:     authz.ActionFor(c.Request.Method, c.FullPath()),
		TargetType: "http",
		TargetID:   c.FullPath(),
		SourceIP:   c.ClientIP(),
		Result:     result,
		Detail:     detail,
	}
	if sub != nil && sub.IsUser() {
		id := sub.UserID
		rec.UserID = &id
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := cfg.Store.AuditLogs().Append(ctx, rec); err != nil {
		cfg.Log.Warn("审计写入失败", "action", rec.Action, "err", err)
	}
}

func usernameOf(sub *authz.Subject) string {
	if sub == nil {
		return ""
	}
	return sub.Username
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	s := string(buf[i:])
	if neg {
		return "-" + s
	}
	return s
}
