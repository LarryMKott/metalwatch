package web

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
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

// implementedAdminOnly 是已经挂上路由的「仅管理员」接口表（method + gin 路由模板）。
// 依据契约权限矩阵：主机增删 / BMC 凭据 / 用户 / Token / 明文导出。
//
// ⚠️ 新增高危接口必须在这里登记。授权判定的默认档是「写操作 operator+」，
// 漏登记的后果是普通 operator 也能改，且不会有任何报错提醒。
var implementedAdminOnly = map[string]bool{
	"POST /api/v1/hosts":              true, // 手工录入主机
	"DELETE /api/v1/hosts/:id":        true,
	"PUT /api/v1/hosts/:id/bmc":       true, // BMC 凭据（明文口令，必须收口）
	"DELETE /api/v1/hosts/:id/bmc":    true, // 删除凭据
	"POST /api/v1/hosts/:id/bmc/test": true, // 用托管的 BMC 凭据对外发起探测，凭据归 admin 管辖
	"GET /api/v1/api-tokens":          true,
	"POST /api/v1/api-tokens":         true,
	"DELETE /api/v1/api-tokens/:id":   true,
	"GET /api/v1/audit-logs":          true,
}

// pendingAdminOnly 是契约已定义、代码尚未实现的高危接口。
//
// 提前登记而不是等实现时再补：实现的人多半想不起这张表，而漏登记的默认后果是
// 「普通 operator 可用」——一个不会有任何报错的越权。下面的
// TestAdminOnlyTableMatchesRegisteredRoutes 会在这些路由真的挂上之后报错，
// 提醒把条目搬进 implementedAdminOnly，表因此不会长期与实际路由脱节。
var pendingAdminOnly = map[string]bool{
	"GET /api/v1/users":         true,
	"POST /api/v1/users":        true,
	"PUT /api/v1/users/:id":     true,
	"DELETE /api/v1/users/:id":  true,
	"GET /api/v1/assets/export": true,
}

// defaultPublicRoutes 是默认的免鉴权路由：登录必须公开，否则无人能拿到令牌。
var defaultPublicRoutes = map[string]bool{
	"POST /api/v1/auth/login": true,
}

// AuthConfig 是鉴权中间件的构造入参。
type AuthConfig struct {
	MasterKey []byte
	Store     adapter.MetadataStore
	Log       *slog.Logger
}

// Authenticator 是管理接口的鉴权 + 授权 + 审计中间件。
//
// 用类型而不是「包级函数 + 包级白名单」的理由有二：
//
//   - 授权表原本是包级 var，等于把「哪些路由要管理员」变成进程级可变状态，
//     测试与其它装配方都无法替换成自己的策略；
//   - authenticate / audit 原先要把 AuthConfig 作为参数在自由函数间一路透传
//     （典型的「参数比逻辑多」），收进方法集后依赖只在构造时确定一次。
type Authenticator struct {
	masterKey []byte
	store     adapter.MetadataStore
	log       *slog.Logger

	// guard 是登录失败限流器，整个中间件共用一份计数。
	guard *authz.LoginGuard

	// adminOnly / publicRoutes 是本实例生效的授权表（默认取包级默认值）。
	adminOnly    map[string]bool
	publicRoutes map[string]bool
}

// NewAuthenticator 构造鉴权中间件。
//
// 这里**不做**「没传主密钥就跳过鉴权」的兜底：主密钥缺失时会话本来也签不出来，
// 与其静默放行造成裸奔，不如让登录直接失败暴露配置问题。
func NewAuthenticator(cfg AuthConfig) *Authenticator {
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	return &Authenticator{
		masterKey:    cfg.MasterKey,
		store:        cfg.Store,
		log:          log,
		guard:        authz.NewLoginGuard(),
		adminOnly:    implementedAdminOnly,
		publicRoutes: defaultPublicRoutes,
	}
}

// AdminOnlyRoutes 返回本实例生效的「仅管理员」路由表副本。
//
// 导出是为了让装配方与测试能核对「表里登记的路由是否真的存在」。返回副本而不是
// 原表：调用方拿到的是视图，改它不该影响鉴权行为。
func (a *Authenticator) AdminOnlyRoutes() map[string]bool {
	return cloneRoutes(a.adminOnly)
}

// PendingOnlyRoutes 返回契约已定义但尚未实现的高危路由副本，供一致性测试比对。
func (a *Authenticator) PendingOnlyRoutes() map[string]bool {
	return cloneRoutes(pendingAdminOnly)
}

func cloneRoutes(src map[string]bool) map[string]bool {
	out := make(map[string]bool, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// Middleware 返回挂到 /api/v1 组上的处理器。
// Agent 通道（/api/v1/agent/*）用自己的 Bearer 校验，不经过这里。
func (a *Authenticator) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		route := c.Request.Method + " " + c.FullPath()

		if a.publicRoutes[route] {
			// 登录接口本身也要防爆破：失败达阈值后一段时间内直接拒绝
			ip := c.ClientIP()
			if !a.guard.Allow(c, ip, func(retryAfter int) {
				c.Header("Retry-After", strconv.Itoa(retryAfter))
				c.AbortWithStatusJSON(http.StatusTooManyRequests, utils.ErrorBody{
					Code:      "too_many_attempts",
					Message:   "登录失败次数过多，请稍后再试",
					RequestID: utils.RequestIDOf(c),
				})
			}) {
				return
			}
			authz.SetGuard(c, a.guard)
			c.Next()
			return
		}

		sub, ok := a.authenticate(c)
		if !ok {
			return // 失败响应已写入
		}
		authz.SetSubject(c, sub)

		if a.adminOnly[route] && !sub.IsAdmin() {
			a.audit(c, sub, "denied", "权限不足：该操作仅管理员可用")
			utils.Forbidden(c, "forbidden_scope", "该操作仅管理员可用")
			return
		}
		if !authz.IsReadOnly(c.Request.Method) && !sub.CanWrite() {
			a.audit(c, sub, "denied", "权限不足：当前角色为只读")
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
			a.audit(c, sub, result, "")
		}
	}
}

// authenticate 先按会话令牌解析，失败再按开放接口令牌解析。
func (a *Authenticator) authenticate(c *gin.Context) (*authz.Subject, bool) {
	token := bearerFrom(c)
	if token == "" {
		utils.Unauthorized(c, "unauthorized", "缺少 Bearer 令牌")
		return nil, false
	}

	// 1) 登录会话令牌
	if s, err := crypto.VerifySession(a.masterKey, token); err == nil {
		sub := &authz.Subject{Kind: "user", UserID: s.UserID, Username: s.Username, Role: s.Role}
		// 会话签发后角色可能已被改：以库里为准，防止权限回收后旧令牌仍生效
		if a.store != nil {
			u, err := a.store.Users().Get(c.Request.Context(), s.UserID)
			if err != nil {
				utils.Fail(c, err)
				return nil, false
			}
			if u == nil {
				// 账号已被删除。会话令牌是无状态自签凭证、没有服务端吊销表，
				// 这里放行就等于「删号后旧令牌（可能是 admin）还能用满 12h」。
				utils.Unauthorized(c, "account_not_found", "账号不存在或已注销")
				return nil, false
			}
			sub.Role = u.Role
			sub.Username = u.Username
			if u.State != "active" {
				utils.Forbidden(c, "account_disabled", "账号已停用")
				return nil, false
			}
		}
		return sub, true
	}

	// 2) 开放接口令牌（只读）：库里只存 SHA-256 摘要
	if a.store == nil {
		utils.Unauthorized(c, "unauthorized", "令牌无效")
		return nil, false
	}
	tk, err := a.store.APITokens().GetByHash(c.Request.Context(), crypto.HashToken(token))
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
		_ = a.store.APITokens().TouchUsed(c.Request.Context(), tk.ID, time.Now().UTC())
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

// audit 落一条审计记录。审计失败只记日志，绝不阻断业务请求。
func (a *Authenticator) audit(c *gin.Context, sub *authz.Subject, result, note string) {
	if a.store == nil {
		return
	}
	detail := ""
	if note != "" {
		detail = `{"note":"` + note + `","status":` + strconv.Itoa(c.Writer.Status()) + "}"
	}
	username := ""
	if sub != nil {
		username = sub.Username
	}
	rec := &adapter.AuditLog{
		Username:   username,
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
	// 用独立 context：请求可能已经结束，用请求 ctx 会直接失败。
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := a.store.AuditLogs().Append(ctx, rec); err != nil {
		a.log.Warn("审计写入失败", "action", rec.Action, "err", err)
	}
}
