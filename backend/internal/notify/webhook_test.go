package notify_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	_ "github.com/LarryMKott/metalwatch/internal/adapter/sqlite"
	"github.com/LarryMKott/metalwatch/internal/notify"
	"github.com/LarryMKott/metalwatch/migrations"
	"github.com/LarryMKott/metalwatch/pkg/config"
)

func newStore(t *testing.T) *adapter.Store {
	t.Helper()
	cfg := config.Default()
	cfg.Server.DataDir = t.TempDir()
	cfg.DB.File = filepath.Join(cfg.Server.DataDir, "metalwatch.db")

	ctx := context.Background()
	s, err := adapter.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	fsys, err := migrations.DialectFS(string(s.Dialect()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Migrate(ctx, fsys, string(s.Dialect())); err != nil {
		t.Fatal(err)
	}
	return s
}

// newHost 建一台测试主机并返回 ID（alert_event.host_id 有外键约束）。
func newHost(t *testing.T, s *adapter.Store, hostname string) int64 {
	t.Helper()
	id, err := s.Hosts().Create(context.Background(), &adapter.Host{
		Hostname: hostname, PrimaryIP: "10.0.0.1", OSType: "linux", Status: "online",
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func samplePayload(id int64, severity string) *notify.EventPayload {
	v := 91.5
	return &notify.EventPayload{
		Event: "alert.firing", Severity: severity,
		Alert: notify.EventInfo{ID: id, Category: "sensor", Metric: "cpu_temp_celsius",
			Value: &v, Title: "CPU 温度过高", FirstSeenAt: time.Now().UTC().Format(time.RFC3339)},
		Host: notify.HostInfo{ID: 1, Hostname: "node-x", PrimaryIP: "10.0.0.1"},
		TS:   time.Now().UTC(),
	}
}

func TestWebhookDeliveryWithRetryAndSignature(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n := calls.Add(1); n <= 2 {
			w.WriteHeader(http.StatusInternalServerError) // 前两次失败，验证重试
			return
		}
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("X-MetalWatch-Event") != "alert.firing" {
			t.Errorf("缺少事件头")
		}
		mac := hmac.New(sha256.New, []byte("sec-1"))
		mac.Write(body)
		want := hex.EncodeToString(mac.Sum(nil))
		if r.Header.Get("X-MetalWatch-Signature") != want {
			t.Errorf("HMAC 签名不符: got %s want %s", r.Header.Get("X-MetalWatch-Signature"), want)
		}
		var p map[string]any
		_ = json.Unmarshal(body, &p)
		if p["event"] != "alert.firing" || p["severity"] != "critical" {
			t.Errorf("载荷不符: %v", p)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	if err := s.NotifyChannels().SeedWebhook(ctx, "ops", `{"url":"`+srv.URL+`","secret":"sec-1"}`, "info"); err != nil {
		t.Fatal(err)
	}
	// 造一条 firing 事件供 notify_state 回写
	hostID := newHost(t, s, "node-x")
	created, _, err := s.Alerts().UpsertFiring(ctx, &adapter.AlertEvent{
		HostID: ptr(hostID), Severity: "critical", Category: "sensor",
		Title: "CPU 温度过高", FirstSeenAt: time.Now().UTC(), LastSeenAt: time.Now().UTC(),
	}, "k-1", "2026-09-22T00:00:00Z")
	if err != nil || !created {
		t.Fatalf("造事件失败: created=%v err=%v", created, err)
	}
	rows, _, _ := s.Alerts().List(ctx, adapter.AlertFilter{})
	p := samplePayload(rows[0].ID, "critical")

	n := notify.New(s.NotifyChannels(), s.Alerts(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	n.SetRetries([]time.Duration{0, time.Millisecond, time.Millisecond, time.Millisecond})
	n.AlertEvent(ctx, p)

	if got := calls.Load(); got != 3 {
		t.Fatalf("应重试至第 3 次成功, got %d 次请求", got)
	}
	// 投递结果回写
	rows, _, _ = s.Alerts().List(ctx, adapter.AlertFilter{})
	if rows[0].NotifyState != "sent" {
		t.Fatalf("notify_state 应为 sent, got %s", rows[0].NotifyState)
	}
	channels, _ := s.NotifyChannels().ListWebhook(ctx)
	if channels[0].LastResult == "" {
		t.Fatal("渠道 last_result 未回写")
	}
}

func TestWebhookSeverityGate(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	// 门槛 major：info 事件应被跳过
	if err := s.NotifyChannels().SeedWebhook(ctx, "gated", `{"url":"`+srv.URL+`"}`, "major"); err != nil {
		t.Fatal(err)
	}
	hostID := newHost(t, s, "node-y")
	_, _, _ = s.Alerts().UpsertFiring(ctx, &adapter.AlertEvent{
		HostID: ptr(hostID), Severity: "info", Category: "agent_offline",
		Title: "Agent 离线", FirstSeenAt: time.Now().UTC(), LastSeenAt: time.Now().UTC(),
	}, "k-2", "2026-09-22T00:00:00Z")
	rows, _, _ := s.Alerts().List(ctx, adapter.AlertFilter{})
	p := samplePayload(rows[0].ID, "info")
	p.Event = "agent.offline"

	n := notify.New(s.NotifyChannels(), s.Alerts(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	n.AlertEvent(ctx, p)

	if calls.Load() != 0 {
		t.Fatalf("低于门槛不应投递, got %d 次请求", calls.Load())
	}
	rows, _, _ = s.Alerts().List(ctx, adapter.AlertFilter{}) // 派发后重查
	if rows[0].NotifyState != "skipped" {
		t.Fatalf("notify_state 应为 skipped, got %s", rows[0].NotifyState)
	}
}

// 多渠道时事件级结果取最差：一路成功、一路失败必须记 failed。
// 旧实现逐个渠道回写 notify_state，最后处理的渠道覆盖前面 ——
// 失败会因为「另一个渠道成功了」而被静默抹平。
func TestWebhookAggregatesWorstStateAcrossChannels(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(ok.Close)
	// 127.0.0.1:1 必然拒绝连接，拿到确定的传输错误
	dead := "http://127.0.0.1:1/hook"

	if err := s.NotifyChannels().SeedWebhook(ctx, "ok-chan", `{"url":"`+ok.URL+`"}`, "info"); err != nil {
		t.Fatal(err)
	}
	if err := s.NotifyChannels().SeedWebhook(ctx, "dead-chan", `{"url":"`+dead+`"}`, "info"); err != nil {
		t.Fatal(err)
	}

	hostID := newHost(t, s, "node-agg")
	if _, _, err := s.Alerts().UpsertFiring(ctx, &adapter.AlertEvent{
		HostID: ptr(hostID), Severity: "critical", Category: "sensor",
		Title: "聚合测试", FirstSeenAt: time.Now().UTC(), LastSeenAt: time.Now().UTC(),
	}, "k-agg", "2026-09-22T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	rows, _, _ := s.Alerts().List(ctx, adapter.AlertFilter{})
	p := samplePayload(rows[0].ID, "critical")

	n := notify.New(s.NotifyChannels(), s.Alerts(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	n.SetRetries([]time.Duration{0}) // 失败即失败，不拖慢用例
	n.AlertEvent(ctx, p)

	rows, _, _ = s.Alerts().List(ctx, adapter.AlertFilter{})
	if rows[0].NotifyState != "failed" {
		t.Fatalf("多渠道部分失败应记 failed, got %s", rows[0].NotifyState)
	}
}

func ptr(v int64) *int64 { return &v }
