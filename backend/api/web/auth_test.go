package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	"github.com/LarryMKott/metalwatch/pkg/crypto"
)

// loginAs 建一个指定角色的账号并登录，返回会话令牌。
func loginAs(t *testing.T, r *gin.Engine, db *adapter.Store, name, role string) string {
	t.Helper()
	hash, err := crypto.HashPassword("pass-" + name)
	if err != nil {
		t.Fatalf("生成口令哈希失败: %v", err)
	}
	if _, err := db.Users().Create(context.Background(), &adapter.AppUser{
		Username: name, PasswordHash: hash, Role: role, State: "active",
	}); err != nil {
		t.Fatalf("创建账号 %s 失败: %v", name, err)
	}
	w := doJSON(t, r, http.MethodPost, "/api/v1/auth/login", "", map[string]any{
		"username": name, "password": "pass-" + name,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("%s 登录失败: %d body=%s", name, w.Code, w.Body.String())
	}
	var lr struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &lr); err != nil {
		t.Fatalf("解析登录响应失败: %v", err)
	}
	return lr.Token
}

// TestAuthRequiresToken 未带令牌的管理接口一律 401。
func TestAuthRequiresToken(t *testing.T) {
	r, _, _ := newTestServer(t)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/hosts"},
		{http.MethodGet, "/api/v1/overview"},
		{http.MethodGet, "/api/v1/audit-logs"},
		{http.MethodPost, "/api/v1/hosts"},
	} {
		w := doJSON(t, r, tc.method, tc.path, "", nil)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s 应 401，实际 %d", tc.method, tc.path, w.Code)
		}
	}
	w := doJSON(t, r, http.MethodGet, "/api/v1/hosts", "not-a-real-token", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("无效令牌应 401，实际 %d", w.Code)
	}
}

// TestAuthRoleMatrix 验证契约权限矩阵：viewer 只读、operator 可写业务、
// 主机增删与审计仅 admin。
func TestAuthRoleMatrix(t *testing.T) {
	r, db, adminToken := newTestServer(t)
	viewer := loginAs(t, r, db, "viewer-1", "viewer")
	operator := loginAs(t, r, db, "operator-1", "operator")

	for _, tk := range []string{viewer, operator, adminToken} {
		w := doJSON(t, r, http.MethodGet, "/api/v1/hosts", tk, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("只读接口应 200，实际 %d", w.Code)
		}
	}

	// 主机增删是 admin 专属：viewer / operator 均 403
	for _, tk := range []string{viewer, operator} {
		w := doJSON(t, r, http.MethodPost, "/api/v1/hosts", tk, map[string]any{
			"hostname": "x-node", "primary_ip": "10.9.0.1",
		})
		if w.Code != http.StatusForbidden {
			t.Fatalf("非 admin 建主机应 403，实际 %d body=%s", w.Code, w.Body.String())
		}
	}

	w := doJSON(t, r, http.MethodPost, "/api/v1/hosts", adminToken, map[string]any{
		"hostname": "a-node", "primary_ip": "10.9.0.3",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("admin 建主机应 201，实际 %d body=%s", w.Code, w.Body.String())
	}

	// 审计日志：viewer / operator 403，admin 200
	for _, tk := range []string{viewer, operator} {
		w := doJSON(t, r, http.MethodGet, "/api/v1/audit-logs", tk, nil)
		if w.Code != http.StatusForbidden {
			t.Fatalf("非 admin 查审计应 403，实际 %d", w.Code)
		}
	}
	w = doJSON(t, r, http.MethodGet, "/api/v1/audit-logs", adminToken, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("admin 查审计应 200，实际 %d body=%s", w.Code, w.Body.String())
	}
}

// TestAuthAPITokenScope 开放令牌只读：能查、不能写；吊销后 401。
func TestAuthAPITokenScope(t *testing.T) {
	r, _, adminToken := newTestServer(t)

	created := doJSON(t, r, http.MethodPost, "/api/v1/api-tokens", adminToken, map[string]any{
		"name": "readonly-1", "scopes": "asset:read,metric:read",
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("签发开放令牌应 201，实际 %d body=%s", created.Code, created.Body.String())
	}
	var ct struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &ct); err != nil {
		t.Fatalf("解析签发响应失败: %v", err)
	}
	if ct.Token == "" {
		t.Fatal("签发响应缺少明文令牌")
	}
	// 明文只在签发响应出现一次，列表里不应包含
	if list := doJSON(t, r, http.MethodGet, "/api/v1/api-tokens", adminToken, nil); list.Code == http.StatusOK {
		var lt struct {
			Items []struct {
				ID    int64  `json:"id"`
				Name  string `json:"name"`
				State string `json:"state"`
			} `json:"items"`
		}
		if err := json.Unmarshal(list.Body.Bytes(), &lt); err != nil {
			t.Fatalf("解析令牌列表失败: %v", err)
		}
		if len(lt.Items) == 0 {
			t.Fatal("应至少有一枚令牌")
		}
		if jsonBodyHas(list.Body.String(), ct.Token) {
			t.Fatal("令牌列表不应包含明文令牌")
		}

		// 只读令牌：查 200、写 403
		w := doJSON(t, r, http.MethodGet, "/api/v1/hosts", ct.Token, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("只读令牌查询应 200，实际 %d body=%s", w.Code, w.Body.String())
		}
		w = doJSON(t, r, http.MethodPost, "/api/v1/hosts", ct.Token, map[string]any{
			"hostname": "tok-node", "primary_ip": "10.8.0.1",
		})
		if w.Code != http.StatusForbidden {
			t.Fatalf("只读令牌写操作应 403，实际 %d", w.Code)
		}

		// 吊销后 401
		rev := doJSON(t, r, http.MethodDelete,
			"/api/v1/api-tokens/"+strconv.FormatInt(lt.Items[0].ID, 10), adminToken, nil)
		if rev.Code != http.StatusOK {
			t.Fatalf("吊销应 200，实际 %d body=%s", rev.Code, rev.Body.String())
		}
		w = doJSON(t, r, http.MethodGet, "/api/v1/hosts", ct.Token, nil)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("吊销后应 401，实际 %d", w.Code)
		}
	}
}

// TestAuthLoginFailureAndAudit 登录失败节流 + 审计落库。
func TestAuthLoginFailureAndAudit(t *testing.T) {
	r, db, _ := newTestServer(t)
	ctx := context.Background()

	codes := []int{}
	for i := 0; i < 6; i++ {
		w := doJSON(t, r, http.MethodPost, "/api/v1/auth/login", "", map[string]any{
			"username": testAdminUser, "password": "wrong-password",
		})
		codes = append(codes, w.Code)
	}
	if codes[0] != http.StatusUnauthorized {
		t.Fatalf("首次失败应 401，实际 %d", codes[0])
	}
	if codes[len(codes)-1] != http.StatusTooManyRequests {
		t.Fatalf("连续失败后应 429，实际序列 %v", codes)
	}

	rows, _, err := db.AuditLogs().List(ctx, adapter.AuditFilter{Action: "auth"})
	if err != nil {
		t.Fatal(err)
	}
	var denied int
	for _, a := range rows {
		if a.Action == "auth.login_failed" && a.Result == "denied" {
			denied++
		}
	}
	if denied == 0 {
		t.Fatalf("应记录登录失败审计，实际 %+v", rows)
	}
}

// TestAuthWriteAudit 写操作落审计（ok），越权落审计（denied）。
func TestAuthWriteAudit(t *testing.T) {
	r, db, adminToken := newTestServer(t)
	ctx := context.Background()

	w := doJSON(t, r, http.MethodPost, "/api/v1/hosts", adminToken, map[string]any{
		"hostname": "audit-node", "primary_ip": "10.7.0.1",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("创建应 201，实际 %d body=%s", w.Code, w.Body.String())
	}

	rows, _, err := db.AuditLogs().List(ctx, adapter.AuditFilter{Action: "hosts.create"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("写操作应产生 hosts.create 审计")
	}
	if rows[0].Result != "ok" {
		t.Fatalf("审计结果应为 ok，实际 %s", rows[0].Result)
	}
	if rows[0].Username != testAdminUser {
		t.Fatalf("审计应记录操作者，实际 %q", rows[0].Username)
	}

	_ = loginAs(t, r, db, "viewer-2", "viewer")
	if doJSON(t, r, http.MethodPost, "/api/v1/hosts", loginAs(t, r, db, "viewer-3", "viewer"),
		map[string]any{"hostname": "v3", "primary_ip": "10.6.0.1"}); true {
		rows, _, err = db.AuditLogs().List(ctx, adapter.AuditFilter{Result: "denied"})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 {
			t.Fatal("越权拒绝应产生 denied 审计")
		}
	}
}

// TestAuthMeAndChangePassword 当前主体与改密。
func TestAuthMeAndChangePassword(t *testing.T) {
	r, _, adminToken := newTestServer(t)

	me := doJSON(t, r, http.MethodGet, "/api/v1/auth/me", adminToken, nil)
	if me.Code != http.StatusOK {
		t.Fatalf("me 应 200，实际 %d body=%s", me.Code, me.Body.String())
	}
	var m struct {
		Kind     string `json:"kind"`
		Username string `json:"username"`
		Role     string `json:"role"`
		IsAdmin  bool   `json:"is_admin"`
	}
	if err := json.Unmarshal(me.Body.Bytes(), &m); err != nil {
		t.Fatalf("解析 me 失败: %v", err)
	}
	if m.Kind != "user" || m.Role != "admin" || !m.IsAdmin {
		t.Fatalf("me 响应异常: %+v", m)
	}

	bad := doJSON(t, r, http.MethodPost, "/api/v1/auth/change-password", adminToken, map[string]any{
		"old_password": "nope", "new_password": "new-pass-12345",
	})
	if bad.Code != http.StatusUnauthorized {
		t.Fatalf("原口令错误应 401，实际 %d", bad.Code)
	}
	ok := doJSON(t, r, http.MethodPost, "/api/v1/auth/change-password", adminToken, map[string]any{
		"old_password": testAdminPassword, "new_password": "new-pass-12345",
	})
	if ok.Code != http.StatusOK {
		t.Fatalf("改密应 200，实际 %d body=%s", ok.Code, ok.Body.String())
	}
	again := doJSON(t, r, http.MethodPost, "/api/v1/auth/login", "", map[string]any{
		"username": testAdminUser, "password": "new-pass-12345",
	})
	if again.Code != http.StatusOK {
		t.Fatalf("新口令应能登录，实际 %d body=%s", again.Code, again.Body.String())
	}
}

// jsonBodyHas 判断响应体里是否出现某字符串（用于断言明文令牌不外泄）。
func jsonBodyHas(body, sub string) bool {
	return len(body) > 0 && len(sub) > 0 && contains(body, sub)
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
