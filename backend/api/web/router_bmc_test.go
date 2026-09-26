package web_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
)

// TestBMCControlEndpoints 覆盖 W15 管控接口的 HTTP 契约（路由 / 鉴权 / 错误映射）。
// 真实 ipmitool 执行路径由 internal/service 的假执行器测试覆盖，这里不碰。
func TestBMCControlEndpoints(t *testing.T) {
	r, _, adminToken := newTestServer(t)

	// 造一台主机（未配置 BMC 凭据）
	created := doJSON(t, r, http.MethodPost, "/api/v1/hosts", adminToken, map[string]any{
		"hostname": "bmc-ctl-node", "primary_ip": "10.0.0.60",
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("创建主机失败: %d body=%s", created.Code, created.Body.String())
	}
	var h struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &h); err != nil {
		t.Fatal(err)
	}

	// 未认证 → 401（管控接口在鉴权链内）
	if w := doJSON(t, r, http.MethodPost, "/api/v1/bmc/999/command", "", map[string]any{
		"cmd_type": "power", "power_action": "on",
	}); w.Code != http.StatusUnauthorized {
		t.Fatalf("未认证应 401，实际 %d", w.Code)
	}

	// 非法 cmd_type → 422（校验先于主机存在性检查）
	if w := doJSON(t, r, http.MethodPost, "/api/v1/bmc/1/command", adminToken, map[string]any{
		"cmd_type": "reboot-everything",
	}); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("非法 cmd_type 应 422，实际 %d body=%s", w.Code, w.Body.String())
	}

	// 主机不存在 → 404
	if w := doJSON(t, r, http.MethodPost, "/api/v1/bmc/999/command", adminToken, map[string]any{
		"cmd_type": "power", "power_action": "on",
	}); w.Code != http.StatusNotFound {
		t.Fatalf("主机不存在应 404，实际 %d", w.Code)
	}

	// 有主机但未配置 BMC → 404
	if w := doJSON(t, r, http.MethodPost, "/api/v1/bmc/"+strconv.FormatInt(h.ID, 10)+"/command", adminToken, map[string]any{
		"cmd_type": "power", "power_action": "on",
	}); w.Code != http.StatusNotFound {
		t.Fatalf("未配置 BMC 应 404，实际 %d body=%s", w.Code, w.Body.String())
	}

	// capability：主机不存在 → 404；未配置 BMC → 404
	if w := doJSON(t, r, http.MethodGet, "/api/v1/bmc/999/capability", adminToken, nil); w.Code != http.StatusNotFound {
		t.Fatalf("capability 主机不存在应 404，实际 %d", w.Code)
	}
	if w := doJSON(t, r, http.MethodGet, "/api/v1/bmc/"+strconv.FormatInt(h.ID, 10)+"/capability", adminToken, nil); w.Code != http.StatusNotFound {
		t.Fatalf("capability 未配置 BMC 应 404，实际 %d", w.Code)
	}

	// 审计：空列表结构完整
	w := doJSON(t, r, http.MethodGet, "/api/v1/bmc/audit", adminToken, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("审计应 200，实际 %d body=%s", w.Code, w.Body.String())
	}
	var audit struct {
		Items []map[string]any `json:"items"`
		Limit int              `json:"limit"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &audit); err != nil {
		t.Fatal(err)
	}
	if audit.Items == nil || audit.Limit != 100 {
		t.Fatalf("审计结构不符: %+v", audit)
	}
}
