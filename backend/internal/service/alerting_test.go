package service_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	_ "github.com/LarryMKott/metalwatch/internal/adapter/sqlite" // 注册 SQLite 后端
	"github.com/LarryMKott/metalwatch/internal/service"
	"github.com/LarryMKott/metalwatch/migrations"
	"github.com/LarryMKott/metalwatch/pkg/config"
)

// newAlerting 建一套带真实 SQLite 的告警服务：种子模板 + 规则加载完成。
func newAlerting(t *testing.T) (*service.AlertService, *adapter.Store) {
	t.Helper()

	cfg := config.Default()
	cfg.Server.DataDir = t.TempDir()
	cfg.DB.File = filepath.Join(cfg.Server.DataDir, "metalwatch.db")

	ctx := context.Background()
	s, err := adapter.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("打开存储失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	fsys, err := migrations.DialectFS(string(s.Dialect()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Migrate(ctx, fsys, string(s.Dialect())); err != nil {
		t.Fatal(err)
	}

	svc := service.NewAlertService(s, s.Thresholds(), s.Alerts(), service.AlertDeps{}, nil)
	if err := svc.SeedBuiltinTemplates(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.ReloadRules(ctx); err != nil {
		t.Fatal(err)
	}
	return svc, s
}

func hostOf(t *testing.T, s *adapter.Store, hostname string) *adapter.Host {
	t.Helper()
	id, err := s.Hosts().Create(context.Background(), &adapter.Host{
		Hostname: hostname, PrimaryIP: "10.0.0.1", OSType: "linux", Status: "online",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Hosts().GetByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func firingRows(t *testing.T, s *adapter.Store) []adapter.AlertEvent {
	t.Helper()
	rows, _, err := s.Alerts().List(context.Background(), adapter.AlertFilter{States: []string{"firing"}})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestEvaluatePersistsFiringAndRecovery(t *testing.T) {
	svc, s := newAlerting(t)
	ctx := context.Background()
	h := hostOf(t, s, "node-a1")
	at := time.Now().UTC().Truncate(time.Minute)

	temp := func(ts time.Time, chip string, v float64) []adapter.Sample {
		return []adapter.Sample{{Metric: "cpu_temp_celsius", Value: v, Labels: map[string]string{"chip": chip}, TS: ts}}
	}
	// cpu_temp 阈值 warn 75 / crit 85，duration 120s；时间轴 1 拍 = 3 分钟
	eval := func(ts time.Time, chip string, v float64) {
		svc.Evaluate(ctx, h.ID, h.Hostname, temp(ts, chip, v), ts)
	}

	// 10:00 首拍越界：未持续 → 不告警
	eval(at, "Core0", 90)
	if rows := firingRows(t, s); len(rows) != 0 {
		t.Fatalf("持续时间不足不应告警, got %d", len(rows))
	}

	// 10:03 持续超 120s → firing（severity=critical，active_key 含对象维度）
	eval(at.Add(3*time.Minute), "Core0", 90)
	rows := firingRows(t, s)
	if len(rows) != 1 {
		t.Fatalf("应产生 1 条 firing, got %d", len(rows))
	}
	if rows[0].Severity != "critical" || rows[0].Category != "sensor" ||
		rows[0].ObjectName == nil || *rows[0].ObjectName != "Core0" {
		t.Fatalf("事件字段不符: %+v", rows[0])
	}

	// 10:06 Core0 抑制窗口内重复触发（仅刷新），Core1 首拍越界建基线
	eval(at.Add(6*time.Minute), "Core0", 90)
	eval(at.Add(6*time.Minute), "Core1", 88)
	if rows := firingRows(t, s); len(rows) != 1 {
		t.Fatalf("Core1 首拍不应触发, got %d", len(rows))
	}

	// 10:09 Core1 持续超 120s → 触发：两颗 CPU 各 1 条（对象维度独立去重）
	eval(at.Add(9*time.Minute), "Core1", 88)
	if rows := firingRows(t, s); len(rows) != 2 {
		t.Fatalf("两颗 CPU 应有 2 条 firing, got %d", len(rows))
	}

	// 恢复需连续 3 个周期低于阈值：前两次不解除
	eval(at.Add(12*time.Minute), "Core0", 60)
	eval(at.Add(15*time.Minute), "Core0", 60)
	if rows := firingRows(t, s); len(rows) != 2 {
		t.Fatalf("恢复去抖期内不应解除, got %d", len(rows))
	}
	eval(at.Add(18*time.Minute), "Core0", 60)
	rows = firingRows(t, s)
	if len(rows) != 1 {
		t.Fatalf("第三周期后 Core0 应恢复, 剩 %d 条", len(rows))
	}
	// 故障史保留：Core0 的 resolved 行 + Core1 的 firing 行都还在
	_, total, err := s.Alerts().List(ctx, adapter.AlertFilter{})
	if err != nil || total != 2 {
		t.Fatalf("故障史应保留 2 行, got total=%d err=%v", total, err)
	}
}

func TestEvaluateRAIDParentSuppression(t *testing.T) {
	svc, s := newAlerting(t)
	ctx := context.Background()
	h := hostOf(t, s, "node-a2")
	at := time.Now().UTC()

	// raid_state 取值约定：1=Degraded 2=Failed；warn = eq 1，crit = eq 2（warn 的父）
	// 值为 2（Failed）：crit firing
	svc.Evaluate(ctx, h.ID, h.Hostname, []adapter.Sample{
		{Metric: "raid_state", Value: 2, Labels: map[string]string{"array": "vd0"}, TS: at},
	}, at)
	rows := firingRows(t, s)
	if len(rows) != 1 || rows[0].Severity != "critical" {
		t.Fatalf("Failed 应只产生 critical 一条, got %+v", rows)
	}

	// 值为 1（Degraded）：crit 进入 3 周期恢复去抖，warn 被仍在 firing 的父抑制
	for i := 1; i <= 2; i++ {
		t2 := at.Add(time.Duration(i) * time.Minute)
		svc.Evaluate(ctx, h.ID, h.Hostname, []adapter.Sample{
			{Metric: "raid_state", Value: 1, Labels: map[string]string{"array": "vd0"}, TS: t2},
		}, t2)
	}
	rows = firingRows(t, s)
	if len(rows) != 1 || rows[0].Severity != "critical" {
		t.Fatalf("父恢复去抖期内应维持 critical, got %+v", rows)
	}

	// 第 3 个周期 crit 恢复；第 4 个周期 warn（Degraded）触发
	t4 := at.Add(4 * time.Minute)
	svc.Evaluate(ctx, h.ID, h.Hostname, []adapter.Sample{
		{Metric: "raid_state", Value: 1, Labels: map[string]string{"array": "vd0"}, TS: t4},
	}, t4)
	rows = firingRows(t, s)
	if len(rows) != 1 || rows[0].Severity != "major" {
		t.Fatalf("父恢复后 Degraded 应产生 major 一条, got %+v", rows)
	}
}

func TestOverrideTakesPrecedence(t *testing.T) {
	svc, s := newAlerting(t)
	ctx := context.Background()
	h := hostOf(t, s, "node-a3")

	// 该主机对 cpu_temp_celsius 的覆盖：阈值放宽到 100，模板不再适用
	warn := 95.0
	if _, err := s.DB.Exec(
		`INSERT INTO threshold_override (host_id, metric, op, warn_value, duration_sec, enabled, updated_at)
		 VALUES (?, 'cpu_temp_celsius', 'gt', ?, 0, 1, '2026-01-01T00:00:00Z')`, h.ID, warn); err != nil {
		t.Fatal(err)
	}
	if err := svc.ReloadRules(ctx); err != nil {
		t.Fatal(err)
	}

	at := time.Now().UTC()
	// 90℃：模板会告警，但该主机被覆盖接管 → 不告警
	svc.Evaluate(ctx, h.ID, h.Hostname, []adapter.Sample{
		{Metric: "cpu_temp_celsius", Value: 90, Labels: map[string]string{"chip": "Core0"}, TS: at},
	}, at)
	svc.Evaluate(ctx, h.ID, h.Hostname, []adapter.Sample{
		{Metric: "cpu_temp_celsius", Value: 90, Labels: map[string]string{"chip": "Core0"}, TS: at},
	}, at.Add(5*time.Minute))
	if rows := firingRows(t, s); len(rows) != 0 {
		t.Fatalf("覆盖生效后 90℃ 不应告警, got %d", len(rows))
	}

	// 超过覆盖阈值 100 → 按覆盖告警（duration 继承模板的 120s，需连续两个周期）
	at2 := at.Add(10 * time.Minute)
	svc.Evaluate(ctx, h.ID, h.Hostname, []adapter.Sample{
		{Metric: "cpu_temp_celsius", Value: 101, Labels: map[string]string{"chip": "Core0"}, TS: at2},
	}, at2)
	if rows := firingRows(t, s); len(rows) != 0 {
		t.Fatalf("覆盖持续时间未满足不应告警, got %d", len(rows))
	}
	at3 := at.Add(15 * time.Minute)
	svc.Evaluate(ctx, h.ID, h.Hostname, []adapter.Sample{
		{Metric: "cpu_temp_celsius", Value: 101, Labels: map[string]string{"chip": "Core0"}, TS: at3},
	}, at3)
	if rows := firingRows(t, s); len(rows) != 1 {
		t.Fatalf("超覆盖阈值应告警, got %d", len(rows))
	}
}

func TestAckFlow(t *testing.T) {
	svc, s := newAlerting(t)
	ctx := context.Background()
	h := hostOf(t, s, "node-a4")
	at := time.Now().UTC()

	// psu_status = 0：crit 立即触发（duration 0）
	svc.Evaluate(ctx, h.ID, h.Hostname, []adapter.Sample{
		{Metric: "psu_status", Value: 0, Labels: map[string]string{"slot": "PSU1"}, TS: at},
	}, at)
	rows := firingRows(t, s)
	if len(rows) != 1 {
		t.Fatalf("电源失效应立即触发, got %d", len(rows))
	}

	ok, err := s.Alerts().Ack(ctx, rows[0].ID, "tester", "2026-09-22T00:00:00Z")
	if err != nil || !ok {
		t.Fatalf("ack 失败: ok=%v err=%v", ok, err)
	}
	// 幂等：已确认的事件再次 ack 返回 false
	if ok, _ := s.Alerts().Ack(ctx, rows[0].ID, "tester", "2026-09-22T00:00:01Z"); ok {
		t.Fatal("重复 ack 应返回 false")
	}
}
