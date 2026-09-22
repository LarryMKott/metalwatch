package web_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"github.com/LarryMKott/metalwatch/api/web"
	"github.com/LarryMKott/metalwatch/internal/adapter"
	_ "github.com/LarryMKott/metalwatch/internal/adapter/sqlite"
	_ "github.com/LarryMKott/metalwatch/internal/adapter/tsdb"
	"github.com/LarryMKott/metalwatch/internal/app"
	"github.com/LarryMKott/metalwatch/internal/pipeline"
	"github.com/LarryMKott/metalwatch/internal/service"
	"github.com/LarryMKott/metalwatch/internal/ws"
	"github.com/LarryMKott/metalwatch/migrations"
	"github.com/LarryMKott/metalwatch/pkg/config"
	"github.com/LarryMKott/metalwatch/pkg/crypto"
)

func testConfig(dataDir string) config.Config {
	cfg := config.Default()
	cfg.Server.DataDir = dataDir
	return cfg
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// openMigratedStore 打开临时元数据库并跑完迁移。
func openMigratedStore(t *testing.T, cfg config.Config) *adapter.Store {
	t.Helper()
	cfg.DB.File = filepath.Join(cfg.Server.DataDir, "metalwatch.db")

	ctx := context.Background()
	db, err := adapter.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("打开存储失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	fsys, err := migrations.DialectFS(string(db.Dialect()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Migrate(ctx, fsys, string(db.Dialect())); err != nil {
		t.Fatal(err)
	}
	return db
}

// testEnv 是完整链路测试环境：路由 + HTTP/2 测试服务 + 各依赖。
type testEnv struct {
	r   *gin.Engine
	srv *httptest.Server
	db  *adapter.Store
	pl  *pipeline.Pipeline
	hub *ws.Hub
}

// newTestEnv 在 router_test 的基础上补齐时序存储 / 管道 / 告警服务 / WS Hub，
// 用于验证 G2 门禁与 W7 推送闭环。
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)

	cfg := testConfig(t.TempDir())
	ctx := context.Background()
	db := openMigratedStore(t, cfg)

	ts, err := adapter.OpenTimeSeries(adapter.TimeSeriesConfig{
		Driver: adapter.TSDriverEmbedded, RootDir: cfg.Server.DataDir,
		RawDays: cfg.Retention.RawDays, AggDays: cfg.Retention.AggDays,
	})
	if err != nil {
		t.Fatalf("打开时序存储失败: %v", err)
	}
	t.Cleanup(func() { _ = ts.Close() })

	pl := pipeline.New(ts, testLogger(), pipeline.Options{FlushInterval: time.Second})
	t.Cleanup(pl.Stop)

	log := testLogger()
	hub := ws.NewHub(log)
	alerts := service.NewAlertService(db, db.Thresholds(), db.Alerts(),
		service.AlertDeps{Broadcaster: hub}, log)
	if err := alerts.SeedBuiltinTemplates(ctx); err != nil {
		t.Fatal(err)
	}
	if err := alerts.ReloadRules(ctx); err != nil {
		t.Fatal(err)
	}

	deps := &app.Deps{
		Config: cfg, Store: db, TSDB: ts, Pipeline: pl, Alerts: alerts,
		Hosts: service.NewHostService(db.Hosts(), log), Hub: hub,
		Log: log, Version: "test", Started: time.Now(),
	}
	engine := web.NewRouter(deps)

	srv := httptest.NewUnstartedServer(engine)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)

	return &testEnv{r: engine, srv: srv, db: db, pl: pl, hub: hub}
}

// TestDataFlowEnrollReportQuery 是 G2 门禁的进程内端到端用例：
// 注册 → 上报（含告警触发与批次重放）→ 时序落库 → 曲线/告警/大盘查询。
func TestDataFlowEnrollReportQuery(t *testing.T) {
	env := newTestEnv(t)
	r, db, pl := env.r, env.db, env.pl
	ctx := context.Background()

	// 1. 注册
	if err := db.EnrollCodes().Issue(ctx, crypto.HashToken("MW-E2E"),
		time.Now().UTC().Add(time.Hour), 1, "test"); err != nil {
		t.Fatal(err)
	}
	enroll := doJSON(t, r, http.MethodPost, "/api/v1/agent/enroll", "", map[string]any{
		"enroll_code": "MW-E2E", "hostname": "node-e2e",
		"primary_ip": "10.0.2.7", "smbios_uuid": "uuid-e2e",
		"os_type": "linux", "agent_version": "0.1.0",
	})
	if enroll.Code != http.StatusCreated {
		t.Fatalf("注册应 201，实际 %d body=%s", enroll.Code, enroll.Body.String())
	}
	var en struct {
		HostID     int64  `json:"host_id"`
		AgentToken string `json:"agent_token"`
	}
	if err := json.Unmarshal(enroll.Body.Bytes(), &en); err != nil {
		t.Fatal(err)
	}

	// 2. 上报两批：cpu_temp 90℃ 间隔 3 分钟（阈值 85 / 持续 120s → 第二批应触发 critical）
	now := time.Now().UTC().Truncate(time.Minute)
	report := func(batch string, at time.Time) *httptest.ResponseRecorder {
		return doJSON(t, r, http.MethodPost, "/api/v1/agent/report", en.AgentToken, map[string]any{
			"batch_id": batch, "host_id": en.HostID, "mode": "agent",
			"collected_at": at.Format(time.RFC3339),
			"metrics": []map[string]any{
				{"name": "cpu_temp_celsius", "labels": map[string]string{"chip": "Socket0"}, "value": 90},
			},
		})
	}
	if w := report("e2e-b1", now.Add(-3*time.Minute)); w.Code != http.StatusAccepted {
		t.Fatalf("第一批上报应 202，实际 %d body=%s", w.Code, w.Body.String())
	}
	w2 := report("e2e-b2", now)
	if w2.Code != http.StatusAccepted {
		t.Fatalf("第二批上报应 202，实际 %d body=%s", w2.Code, w2.Body.String())
	}
	var rep struct {
		TSDB string `json:"tsdb"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &rep); err != nil || rep.TSDB != "accepted" {
		t.Fatalf("tsdb 状态应 accepted: %+v err=%v", rep, err)
	}

	// 3. 批次重放：幂等拒绝，accepted=0
	replay := report("e2e-b2", now)
	if replay.Code != http.StatusAccepted {
		t.Fatalf("重放应 202，实际 %d", replay.Code)
	}
	var rrep struct {
		Accepted int    `json:"accepted"`
		TSDB     string `json:"tsdb"`
	}
	_ = json.Unmarshal(replay.Body.Bytes(), &rrep)
	if rrep.Accepted != 0 || rrep.TSDB != "deduplicated" {
		t.Fatalf("重放应幂等: %+v", rrep)
	}

	// 4. 刷管道后查询曲线：3 个点（两批 cpu_temp + 服务端写入的 up 不在该指标内）
	pl.Stop()
	metrics := doJSON(t, r, http.MethodGet,
		"/api/v1/hosts/"+strconv.FormatInt(en.HostID, 10)+
			"/metrics?metric=cpu_temp_celsius&from="+now.Add(-time.Hour).Format(time.RFC3339)+
			"&to="+now.Add(time.Minute).Format(time.RFC3339)+"&step=60s", "", nil)
	if metrics.Code != http.StatusOK {
		t.Fatalf("曲线查询应 200，实际 %d body=%s", metrics.Code, metrics.Body.String())
	}
	var ms struct {
		Metric      string       `json:"metric"`
		Unit        string       `json:"unit"`
		Points      [][2]float64 `json:"points"`
		Source      string       `json:"source"`
		Downsampled bool         `json:"downsampled"`
	}
	if err := json.Unmarshal(metrics.Body.Bytes(), &ms); err != nil {
		t.Fatal(err)
	}
	if ms.Metric != "cpu_temp_celsius" || ms.Unit != "celsius" || len(ms.Points) < 2 {
		t.Fatalf("曲线数据异常: %+v", ms)
	}

	// 5. 告警事件：cpu_temp 90 持续 3 分钟 → critical firing
	alertsResp := doJSON(t, r, http.MethodGet, "/api/v1/alerts?state=active", "", nil)
	if alertsResp.Code != http.StatusOK {
		t.Fatalf("告警查询应 200，实际 %d", alertsResp.Code)
	}
	var alertList []struct {
		ID       int64    `json:"id"`
		Severity string   `json:"severity"`
		State    string   `json:"state"`
		Rule     string   `json:"rule"`
		HostName string   `json:"host_name"`
		Value    *float64 `json:"value"`
	}
	if err := json.Unmarshal(alertsResp.Body.Bytes(), &alertList); err != nil {
		t.Fatal(err)
	}
	if len(alertList) != 1 || alertList[0].Severity != "critical" || alertList[0].State != "active" {
		t.Fatalf("应有 1 条 critical 告警: %+v", alertList)
	}

	// 6. 确认 → state 变 acked
	ack := doJSON(t, r, http.MethodPost,
		"/api/v1/alerts/"+strconv.FormatInt(alertList[0].ID, 10)+"/ack", "", nil)
	if ack.Code != http.StatusOK {
		t.Fatalf("确认应 200，实际 %d body=%s", ack.Code, ack.Body.String())
	}
	afterAck := doJSON(t, r, http.MethodGet, "/api/v1/alerts?state=acked", "", nil)
	var acked []struct {
		State string `json:"state"`
	}
	_ = json.Unmarshal(afterAck.Body.Bytes(), &acked)
	if len(acked) != 1 || acked[0].State != "acked" {
		t.Fatalf("确认后应出现在 acked 列表: %+v", acked)
	}

	// 7. 大盘：在线 1 台、活跃告警 1 条（acked 仍是 firing）、趋势有序列
	ov := doJSON(t, r, http.MethodGet, "/api/v1/overview", "", nil)
	if ov.Code != http.StatusOK {
		t.Fatalf("大盘应 200，实际 %d body=%s", ov.Code, ov.Body.String())
	}
	var overview struct {
		HostTotal   int `json:"host_total"`
		HostOnline  int `json:"host_online"`
		HostOffline int `json:"host_offline"`
		AlertActive int `json:"alert_active"`
		Series      []struct {
			TS      string `json:"ts"`
			Online  int    `json:"online"`
			Offline int    `json:"offline"`
			Alert   int    `json:"alert"`
		} `json:"series"`
	}
	if err := json.Unmarshal(ov.Body.Bytes(), &overview); err != nil {
		t.Fatal(err)
	}
	if overview.HostTotal != 1 || overview.HostOnline != 1 || overview.AlertActive != 1 {
		t.Fatalf("大盘计数异常: %+v", overview)
	}
	if len(overview.Series) == 0 {
		t.Fatal("大盘趋势序列为空")
	}
	onlineSum := 0
	for _, p := range overview.Series {
		onlineSum += p.Online
	}
	if onlineSum == 0 {
		t.Fatal("up 指标聚合后 online 均为 0，趋势链路未通")
	}

	// 8. 阈值模板（前端只读表格）
	tpl := doJSON(t, r, http.MethodGet, "/api/v1/alerts/templates", "", nil)
	if tpl.Code != http.StatusOK {
		t.Fatalf("模板查询应 200，实际 %d", tpl.Code)
	}
	var templates []map[string]any
	if err := json.Unmarshal(tpl.Body.Bytes(), &templates); err != nil || len(templates) < 6 {
		t.Fatalf("内置模板应 ≥6 条: n=%d err=%v", len(templates), err)
	}
}

// TestWebSocketAlertPush 验证 W7 实时推送闭环：
// WS 订阅 → 流式触发 critical 告警 → 订阅者收到 alert.firing 载荷。
func TestWebSocketAlertPush(t *testing.T) {
	env := newTestEnv(t)
	ts, db, pl := env.srv, env.db, env.pl
	ctx := context.Background()

	// 1. 注册拿令牌
	if err := db.EnrollCodes().Issue(ctx, crypto.HashToken("MW-WS"),
		time.Now().UTC().Add(time.Hour), 1, "test"); err != nil {
		t.Fatal(err)
	}
	enroll := doJSON(t, env.r, http.MethodPost, "/api/v1/agent/enroll", "", map[string]any{
		"enroll_code": "MW-WS", "hostname": "node-ws",
		"primary_ip": "10.0.2.8", "smbios_uuid": "uuid-ws",
		"os_type": "linux", "agent_version": "0.1.0",
	})
	if enroll.Code != http.StatusCreated {
		t.Fatalf("注册应 201，实际 %d", enroll.Code)
	}
	var en struct {
		HostID     int64  `json:"host_id"`
		AgentToken string `json:"agent_token"`
	}
	_ = json.Unmarshal(enroll.Body.Bytes(), &en)

	// 2. WS 订阅（wss，自签跳过校验）
	wsURL := "wss://" + ts.Listener.Addr().String() + "/api/v1/ws/alerts"
	dialer := websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("WS 连接失败: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// 3. 上报两批 90℃（间隔 3 分钟）→ critical 新触发 → 广播
	now := time.Now().UTC().Truncate(time.Minute)
	for i, at := range []time.Time{now.Add(-3 * time.Minute), now} {
		w := doJSON(t, env.r, http.MethodPost, "/api/v1/agent/report", en.AgentToken,
			map[string]any{
				"batch_id": fmt.Sprintf("ws-b%d", i+1), "host_id": en.HostID, "mode": "agent",
				"collected_at": at.Format(time.RFC3339),
				"metrics": []map[string]any{
					{"name": "cpu_temp_celsius", "labels": map[string]string{"chip": "CPU"}, "value": 90},
				},
			})
		if w.Code != http.StatusAccepted {
			t.Fatalf("第 %d 批上报应 202，实际 %d body=%s", i+1, w.Code, w.Body.String())
		}
	}
	pl.Stop()

	// 4. 订阅者收到 alert.firing
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("未收到 WS 推送: %v", err)
	}
	var payload struct {
		Event    string `json:"event"`
		Severity string `json:"severity"`
		Alert    struct {
			Category string  `json:"category"`
			Metric   string  `json:"metric"`
			Value    float64 `json:"value"`
		} `json:"alert"`
		Host struct {
			Hostname string `json:"hostname"`
		} `json:"host"`
	}
	if err := json.Unmarshal(msg, &payload); err != nil {
		t.Fatalf("WS 载荷不是合法 JSON: %v (%s)", err, msg)
	}
	if payload.Event != "alert.firing" || payload.Severity != "critical" ||
		payload.Alert.Metric != "cpu_temp_celsius" || payload.Host.Hostname != "node-ws" {
		t.Fatalf("推送载荷不符: %+v", payload)
	}
}
