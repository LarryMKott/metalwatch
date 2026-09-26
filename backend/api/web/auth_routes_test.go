package web_test

import (
	"testing"

	"github.com/LarryMKott/metalwatch/api/web"
)

// TestAdminOnlyTableMatchesRegisteredRoutes 钉住「授权表与实际路由不脱节」。
//
// 背景：本轮评审发现 adminOnly 里 5 条路由在代码中根本不存在（用户管理、明文导出
// 均未实现）。表错了不会报错、也不会放行，属于纯静默腐化；反方向更危险 ——
// 新增高危接口忘了登记，默认档只是「写操作 operator+」，即静默越权。
// 两个方向都由这条用例兜住：已登记的必须真实存在，待实现的必须仍未实现。
func TestAdminOnlyTableMatchesRegisteredRoutes(t *testing.T) {
	r, _, _ := newTestServer(t)

	registered := make(map[string]bool)
	for _, rt := range r.Routes() {
		registered[rt.Method+" "+rt.Path] = true
	}

	a := web.NewAuthenticator(web.AuthConfig{})
	for route := range a.AdminOnlyRoutes() {
		if !registered[route] {
			t.Errorf("adminOnly 登记了不存在的路由：%s（删掉它，或确认路由模板写对了）", route)
		}
	}
	for route := range a.PendingOnlyRoutes() {
		if registered[route] {
			t.Errorf("路由 %s 已实现，请从 pendingAdminOnly 移入 implementedAdminOnly，"+
				"否则它只按默认档要求 operator", route)
		}
	}
}
