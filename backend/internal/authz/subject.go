// Package authz 是鉴权主体与授权的共享定义（W11）。
//
// 放在独立包是为了切断依赖环：api/web（装中间件）与 api/web/handler
// （读主体）都要用它，而 web 又必须 import handler，三方不能互引。
package authz

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// Subject 是请求主体：可能是登录用户，也可能是开放接口令牌。
type Subject struct {
	Kind     string // user | token
	UserID   int64
	Username string
	Role     string // admin | operator | viewer（令牌主体为空，改用 Scopes 判定）
	Scopes   []string
}

// IsUser 判断是否登录用户主体（false 表示开放接口令牌或空）。
func (s *Subject) IsUser() bool { return s != nil && s.Kind == "user" }

// HasScope 判断开放令牌是否具备某个 scope。
func (s *Subject) HasScope(scope string) bool {
	if s == nil {
		return false
	}
	for _, sc := range s.Scopes {
		if strings.TrimSpace(sc) == scope {
			return true
		}
	}
	return false
}

// CanWrite 判断是否具备写操作权限（operator 及以上，或持写 scope 的令牌）。
func (s *Subject) CanWrite() bool {
	if s == nil {
		return false
	}
	if s.IsUser() {
		return s.Role == "admin" || s.Role == "operator"
	}
	return s.HasScope("write") || s.HasScope("*:write")
}

// IsAdmin 判断是否管理员（admin-only 路径用）。
func (s *Subject) IsAdmin() bool {
	if s == nil {
		return false
	}
	if s.IsUser() {
		return s.Role == "admin"
	}
	return s.HasScope("admin")
}

// ---------- 上下文 ----------

const (
	ctxSubjectKey    = "mw_subject"
	ctxLoginGuardKey = "mw_login_guard"
)

// SetSubject 把主体写入请求上下文。
func SetSubject(c *gin.Context, s *Subject) { c.Set(ctxSubjectKey, s) }

// SubjectOf 取出当前请求的主体；未鉴权（公开路径）返回 nil。
func SubjectOf(c *gin.Context) *Subject {
	v, ok := c.Get(ctxSubjectKey)
	if !ok {
		return nil
	}
	s, _ := v.(*Subject)
	return s
}

// SetGuard 把登录守卫写入上下文（登录 handler 用）。
func SetGuard(c *gin.Context, g *LoginGuard) { c.Set(ctxLoginGuardKey, g) }

// GuardOf 取出登录守卫。
func GuardOf(c *gin.Context) *LoginGuard {
	v, ok := c.Get(ctxLoginGuardKey)
	if !ok {
		return nil
	}
	g, _ := v.(*LoginGuard)
	return g
}

// ---------- 路由判定辅助 ----------

// IsReadOnly 判断方法是否为只读（只读不要求写权限、也不写审计）。
func IsReadOnly(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

// ActionFor 把「方法 + 路由模板」归一成审计动作名：
//
//	POST   /api/v1/hosts         → hosts.create
//	DELETE /api/v1/hosts/:id     → hosts.delete
//	PUT    /api/v1/hosts/:id/bmc → hosts.bmc.update
func ActionFor(method, path string) string {
	verb := map[string]string{
		http.MethodPost:   "create",
		http.MethodPut:    "update",
		http.MethodPatch:  "patch",
		http.MethodDelete: "delete",
	}[method]
	if verb == "" {
		verb = "read"
	}
	trimmed := strings.Trim(strings.TrimPrefix(path, "/api/v1/"), "/")
	if trimmed == "" {
		return verb
	}
	segs := strings.Split(trimmed, "/")
	keep := make([]string, 0, len(segs))
	for _, s := range segs {
		if strings.HasPrefix(s, ":") || strings.HasPrefix(s, "*") {
			continue // 占位符不进动作名，否则同一操作会散成多条
		}
		keep = append(keep, s)
	}
	if len(keep) == 0 {
		return verb
	}
	return strings.Join(keep, ".") + "." + verb
}

// ---------- 登录防爆破 ----------

// LoginGuard 是进程内的登录失败计数：同一 IP 连续失败达阈值后冷却。
// 不持久化 —— 重启即清零，单机场景足够；跨实例部署需换成存储层计数。
type LoginGuard struct {
	mu       sync.Mutex
	failed   map[string]int
	cooldown map[string]time.Time
}

const (
	LoginMaxFail  = 5
	LoginCooldown = 5 * time.Minute
)

// NewLoginGuard 构造登录守卫。
func NewLoginGuard() *LoginGuard {
	return &LoginGuard{failed: map[string]int{}, cooldown: map[string]time.Time{}}
}

// Allow 判断该来源当前是否允许尝试登录；被冷却时直接写 429 并返回 false。
// 调用方需自行 abort（返回 false 时响应已写入）。
func (g *LoginGuard) Allow(c *gin.Context, ip string, tooMany func(retryAfter int)) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if until, ok := g.cooldown[ip]; ok {
		if time.Now().Before(until) {
			wait := int(time.Until(until).Seconds()) + 1
			if tooMany != nil {
				tooMany(wait)
			}
			return false
		}
		delete(g.cooldown, ip)
		delete(g.failed, ip)
	}
	return true
}

// Fail 记录一次登录失败；达到阈值则进入冷却。
func (g *LoginGuard) Fail(ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failed[ip]++
	if g.failed[ip] >= LoginMaxFail {
		g.cooldown[ip] = time.Now().Add(LoginCooldown)
	}
}

// Success 清空该来源的失败计数。
func (g *LoginGuard) Success(ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.failed, ip)
	delete(g.cooldown, ip)
}
