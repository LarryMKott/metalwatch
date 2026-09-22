package web_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LarryMKott/metalwatch/api/web"
	"github.com/LarryMKott/metalwatch/internal/adapter"
	_ "github.com/LarryMKott/metalwatch/internal/adapter/sqlite"
	"github.com/LarryMKott/metalwatch/internal/app"
	"github.com/LarryMKott/metalwatch/internal/service"
	"github.com/LarryMKott/metalwatch/migrations"
	"github.com/LarryMKott/metalwatch/pkg/config"
	"github.com/LarryMKott/metalwatch/pkg/crypto"
)

func newTestServer(t *testing.T) (*gin.Engine, *adapter.Store, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	cfg := config.Default()
	cfg.Server.DataDir = t.TempDir()
	cfg.DB.File = filepath.Join(cfg.Server.DataDir, "metalwatch.db")

	ctx := context.Background()
	db, err := adapter.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("打开存储失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	fsys, err := migrations.DialectFS(string(db.Dialect()))
	if err != nil {
		t.Fatalf("取迁移目录失败: %v", err)
	}
	if _, err := db.Migrate(ctx, fsys, string(db.Dialect())); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	// 会话令牌靠主密钥签名，测试也走真实路径生成（W11）
	masterKey, err := crypto.GenerateMasterKey(filepath.Join(cfg.Server.DataDir, "master.key"))
	if err != nil {
		t.Fatalf("生成主密钥失败: %v", err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	deps := &app.Deps{
		Config: cfg, MasterKey: masterKey,
		Store:   db,
		Hosts:   service.NewHostService(db.Hosts(), log),
		Log:     log,
		Version: "test",
		Started: time.Now(),
	}
	router := web.NewRouter(deps)

	// 管理接口默认鉴权（W11），测试也要先登录再用令牌访问
	hash, err := crypto.HashPassword(testAdminPassword)
	if err != nil {
		t.Fatalf("生成口令哈希失败: %v", err)
	}
	if _, err := db.Users().Create(ctx, &adapter.AppUser{
		Username: testAdminUser, DisplayName: "测试管理员",
		PasswordHash: hash, Role: "admin", State: "active",
	}); err != nil {
		t.Fatalf("创建测试管理员失败: %v", err)
	}
	w := doJSON(t, router, http.MethodPost, "/api/v1/auth/login", "", map[string]any{
		"username": testAdminUser, "password": testAdminPassword,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("测试管理员登录失败: %d body=%s", w.Code, w.Body.String())
	}
	var lr struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &lr); err != nil {
		t.Fatalf("解析登录响应失败: %v", err)
	}
	if lr.Token == "" {
		t.Fatal("登录响应缺少令牌")
	}
	return router, db, lr.Token
}

// 测试管理员凭据（仅用于测试库，生产库的初始账号由 service.EnsureFirstAdmin 随机生成）
const (
	testAdminUser     = "testadmin"
	testAdminPassword = "test-pass-12345"
)

func doJSON(t *testing.T, r *gin.Engine, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求体失败: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestHealthz(t *testing.T) {
	r, _, _ := newTestServer(t)
	w := doJSON(t, r, http.MethodGet, "/healthz", "", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("healthz 状态码 = %d, body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("status 应为 ok，实际 %v", body["status"])
	}
	storage := body["storage"].(map[string]any)
	meta := storage["metadata"].(map[string]any)
	if meta["driver"] != "sqlite" {
		t.Fatalf("默认元数据驱动应为 sqlite，实际 %v", meta["driver"])
	}
}

func TestHostLifecycleOverAPI(t *testing.T) {
	r, _, adminToken := newTestServer(t)

	// 非法输入：主机名带非法字符
	bad := doJSON(t, r, http.MethodPost, "/api/v1/hosts", adminToken, map[string]any{
		"hostname": "bad host!", "primary_ip": "10.0.0.1",
	})
	if bad.Code != http.StatusUnprocessableEntity {
		t.Fatalf("非法主机名应 422，实际 %d body=%s", bad.Code, bad.Body.String())
	}

	// 非法 IP
	badIP := doJSON(t, r, http.MethodPost, "/api/v1/hosts", adminToken, map[string]any{
		"hostname": "node-1", "primary_ip": "not-an-ip",
	})
	if badIP.Code != http.StatusUnprocessableEntity {
		t.Fatalf("非法 IP 应 422，实际 %d", badIP.Code)
	}

	// 正常创建
	created := doJSON(t, r, http.MethodPost, "/api/v1/hosts", adminToken, map[string]any{
		"hostname": "node-1", "primary_ip": "10.0.0.1",
		"bmc_ip": "10.0.0.201", "os_type": "linux", "collect_ipmi": true,
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("创建应 201，实际 %d body=%s", created.Code, created.Body.String())
	}
	var h struct {
		ID        int64  `json:"id"`
		Hostname  string `json:"hostname"`
		BMCIP     string `json:"bmc_ip"`
		Status    string `json:"status"`
		CreatedAt string `json:"created_at"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &h); err != nil {
		t.Fatalf("解析创建响应失败: %v", err)
	}
	if h.ID <= 0 || h.Hostname != "node-1" || h.BMCIP != "10.0.0.201" {
		t.Fatalf("创建响应异常: %+v", h)
	}
	if h.Status != "unknown" {
		t.Fatalf("新资产状态应为 unknown，实际 %s", h.Status)
	}

	// 列表 + 分页字段
	list := doJSON(t, r, http.MethodGet, "/api/v1/hosts?q=node-1&page=1&page_size=10", adminToken, nil)
	if list.Code != http.StatusOK {
		t.Fatalf("列表应 200，实际 %d", list.Code)
	}
	var listed struct {
		Items    []map[string]any `json:"items"`
		Total    int              `json:"total"`
		Page     int              `json:"page"`
		PageSize int              `json:"page_size"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if listed.Total != 1 || len(listed.Items) != 1 || listed.PageSize != 10 {
		t.Fatalf("列表结果异常: %+v", listed)
	}

	// 详情
	got := doJSON(t, r, http.MethodGet, "/api/v1/hosts/"+strconv.FormatInt(h.ID, 10), adminToken, nil)
	if got.Code != http.StatusOK {
		t.Fatalf("详情应 200，实际 %d", got.Code)
	}

	// 不存在的 ID
	missing := doJSON(t, r, http.MethodGet, "/api/v1/hosts/999999", adminToken, nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("不存在的主机应 404，实际 %d", missing.Code)
	}

	// 删除
	del := doJSON(t, r, http.MethodDelete, "/api/v1/hosts/"+strconv.FormatInt(h.ID, 10), adminToken, nil)
	if del.Code != http.StatusNoContent {
		t.Fatalf("删除应 204，实际 %d", del.Code)
	}
	again := doJSON(t, r, http.MethodDelete, "/api/v1/hosts/"+strconv.FormatInt(h.ID, 10), adminToken, nil)
	if again.Code != http.StatusNotFound {
		t.Fatalf("重复删除应 404，实际 %d", again.Code)
	}
}

func TestAgentEnrollReportHeartbeat(t *testing.T) {
	r, db, adminToken := newTestServer(t)
	ctx := context.Background()

	enrollBody := map[string]any{
		"enroll_code": "MW-TEST-CODE", "hostname": "node-agent",
		"primary_ip": "10.0.1.5", "smbios_uuid": "uuid-agent-1",
		"os_type": "linux", "agent_version": "0.1.0",
	}

	// 未签发的注册码 → 401
	denied := doJSON(t, r, http.MethodPost, "/api/v1/agent/enroll", adminToken, enrollBody)
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("无效注册码应 401，实际 %d body=%s", denied.Code, denied.Body.String())
	}

	// 签发注册码后再注册 → 201
	if err := db.EnrollCodes().Issue(ctx, crypto.HashToken("MW-TEST-CODE"),
		time.Now().UTC().Add(time.Hour), 1, "test"); err != nil {
		t.Fatalf("签发注册码失败: %v", err)
	}
	ok := doJSON(t, r, http.MethodPost, "/api/v1/agent/enroll", "", enrollBody)
	if ok.Code != http.StatusCreated {
		t.Fatalf("注册应 201，实际 %d body=%s", ok.Code, ok.Body.String())
	}
	var en struct {
		HostID     int64  `json:"host_id"`
		AgentToken string `json:"agent_token"`
		Interval   int    `json:"report_interval_sec"`
	}
	if err := json.Unmarshal(ok.Body.Bytes(), &en); err != nil {
		t.Fatal(err)
	}
	if en.HostID <= 0 || en.AgentToken == "" || en.Interval <= 0 {
		t.Fatalf("注册响应异常: %+v", en)
	}
	if len(en.AgentToken) < 12 || en.AgentToken[:4] != "mwa_" {
		t.Fatalf("令牌前缀异常: %s", en.AgentToken)
	}

	// 注册码一次性：再用必须失败
	reused := doJSON(t, r, http.MethodPost, "/api/v1/agent/enroll", "", enrollBody)
	if reused.Code != http.StatusUnauthorized {
		t.Fatalf("注册码应一次性，第二次应 401，实际 %d", reused.Code)
	}

	// 上报：无令牌 401
	report := map[string]any{
		"batch_id": "batch-1", "host_id": en.HostID, "mode": "agent",
		"collected_at": time.Now().UTC().Format(time.RFC3339),
		"metrics": []map[string]any{
			{"name": "cpu_temp_celsius", "labels": map[string]string{"chip": "Socket0"}, "value": 47.5},
			{"name": "fan_rpm", "labels": map[string]string{"slot": "Fan1"}, "value": 4200},
		},
	}
	noToken := doJSON(t, r, http.MethodPost, "/api/v1/agent/report", "", report)
	if noToken.Code != http.StatusUnauthorized {
		t.Fatalf("无令牌应 401，实际 %d", noToken.Code)
	}

	// 上报：带令牌 202
	accepted := doJSON(t, r, http.MethodPost, "/api/v1/agent/report", en.AgentToken, report)
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("上报应 202，实际 %d body=%s", accepted.Code, accepted.Body.String())
	}
	var rep struct {
		Accepted int `json:"accepted"`
	}
	_ = json.Unmarshal(accepted.Body.Bytes(), &rep)
	if rep.Accepted != 2 {
		t.Fatalf("accepted 应为 2，实际 %d", rep.Accepted)
	}

	// 上报后主机应变为 online
	hosts := doJSON(t, r, http.MethodGet, "/api/v1/hosts/"+strconv.FormatInt(en.HostID, 10), adminToken, nil)
	var host struct {
		Status     string `json:"status"`
		LastSeenAt string `json:"last_seen_at"`
	}
	_ = json.Unmarshal(hosts.Body.Bytes(), &host)
	if host.Status != "online" || host.LastSeenAt == "" {
		t.Fatalf("上报后应更新为 online: %+v", host)
	}

	// 越权：host_id 与令牌绑定的主机不一致
	other := map[string]any{
		"batch_id": "batch-2", "host_id": en.HostID + 999, "mode": "agent",
		"metrics": []map[string]any{},
	}
	forbidden := doJSON(t, r, http.MethodPost, "/api/v1/agent/report", en.AgentToken, other)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("host_id 不匹配应 403，实际 %d", forbidden.Code)
	}

	// 白名单：未知指标应 422
	badMetric := map[string]any{
		"batch_id": "batch-3", "host_id": en.HostID, "mode": "agent",
		"metrics": []map[string]any{{"name": "cpu_usage_percent", "value": 10}},
	}
	rejected := doJSON(t, r, http.MethodPost, "/api/v1/agent/report", en.AgentToken, badMetric)
	if rejected.Code != http.StatusUnprocessableEntity {
		t.Fatalf("未知指标应 422，实际 %d", rejected.Code)
	}

	// 高基数标签：sn 应被拒
	badLabel := map[string]any{
		"batch_id": "batch-4", "host_id": en.HostID, "mode": "agent",
		"metrics": []map[string]any{
			{"name": "cpu_temp_celsius", "labels": map[string]string{"sn": "CN123"}, "value": 40},
		},
	}
	badLabelResp := doJSON(t, r, http.MethodPost, "/api/v1/agent/report", en.AgentToken, badLabel)
	if badLabelResp.Code != http.StatusUnprocessableEntity {
		t.Fatalf("高基数标签应 422，实际 %d", badLabelResp.Code)
	}

	// 心跳
	hb := doJSON(t, r, http.MethodPost, "/api/v1/agent/heartbeat", en.AgentToken,
		map[string]any{"host_id": en.HostID, "agent_version": "0.1.0"})
	if hb.Code != http.StatusOK {
		t.Fatalf("心跳应 200，实际 %d body=%s", hb.Code, hb.Body.String())
	}

	// 吊销令牌后上报立即 401
	toks, err := db.AgentTokens().FindActiveByHash(ctx, crypto.HashToken(en.AgentToken))
	if err != nil {
		t.Fatalf("查询令牌失败: %v", err)
	}
	if err := db.AgentTokens().Revoke(ctx, toks.ID); err != nil {
		t.Fatalf("吊销失败: %v", err)
	}
	revoked := doJSON(t, r, http.MethodPost, "/api/v1/agent/heartbeat", en.AgentToken,
		map[string]any{"host_id": en.HostID})
	if revoked.Code != http.StatusUnauthorized {
		t.Fatalf("吊销后应 401，实际 %d", revoked.Code)
	}
}

func TestStorageBackendsEndpoint(t *testing.T) {
	r, _, adminToken := newTestServer(t)
	w := doJSON(t, r, http.MethodGet, "/api/v1/system/storage/backends", adminToken, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d", w.Code)
	}

	var body struct {
		Current  map[string]string `json:"current"`
		Backends []struct {
			Name        string `json:"name"`
			Kind        string `json:"kind"`
			Implemented bool   `json:"implemented"`
		} `json:"backends"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Current["metadata_driver"] != "sqlite" {
		t.Fatalf("当前驱动应为 sqlite: %+v", body.Current)
	}
	if len(body.Backends) < 6 {
		t.Fatalf("后端目录过少: %d", len(body.Backends))
	}

	metrics := map[string]string{}
	for _, b := range body.Backends {
		metrics[b.Kind+"|"+b.Name] = strconv.FormatBool(b.Implemented)
	}
	if metrics["metadata|sqlite"] != "true" {
		t.Fatal("sqlite 应标记为已实现")
	}
	for _, want := range []string{"metadata|postgres", "metadata|mysql", "timeseries|prometheus", "timeseries|victoriametrics"} {
		if _, ok := metrics[want]; !ok {
			t.Fatalf("后端目录缺少 %s", want)
		}
		if metrics[want] != "false" {
			t.Fatalf("%s 应标记为未实现（规划中）", want)
		}
	}
}
