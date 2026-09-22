package service_test

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	_ "github.com/LarryMKott/metalwatch/internal/adapter/sqlite"
	_ "github.com/LarryMKott/metalwatch/internal/adapter/tsdb"
	"github.com/LarryMKott/metalwatch/internal/pipeline"
	"github.com/LarryMKott/metalwatch/internal/service"
	"github.com/LarryMKott/metalwatch/migrations"
	"github.com/LarryMKott/metalwatch/pkg/config"
)

// newOfflineEnv 建离线监视器的完整环境：元数据 + 时序 + 管道 + 告警服务。
func newOfflineEnv(t *testing.T) (*service.OfflineMonitor, *adapter.Store, adapter.TimeSeriesStore, *pipeline.Pipeline) {
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

	ts, err := adapter.OpenTimeSeries(adapter.TimeSeriesConfig{
		Driver: adapter.TSDriverEmbedded, RootDir: cfg.Server.DataDir, RawDays: 7, AggDays: 180,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ts.Close() })
	pipe := pipeline.New(ts, nil, pipeline.Options{FlushInterval: 20 * time.Millisecond})
	t.Cleanup(pipe.Stop)

	alerts := service.NewAlertService(s, s.Thresholds(), s.Alerts(), service.AlertDeps{}, nil)
	m := service.NewOfflineMonitor(s, pipe, alerts, time.Minute, 15*time.Second, nil)
	return m, s, ts, pipe
}

func hostWithLastSeen(t *testing.T, s *adapter.Store, hostname string, lastSeen time.Time) *adapter.Host {
	t.Helper()
	id, err := s.Hosts().Create(context.Background(), &adapter.Host{
		Hostname: hostname, PrimaryIP: "10.0.0.9", OSType: "linux", Status: "online",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Hosts().UpdateStatus(context.Background(), id, "online", lastSeen); err != nil {
		t.Fatal(err)
	}
	h, err := s.Hosts().GetByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestOfflineMonitorRaisesAndResolves(t *testing.T) {
	m, s, ts, pipe := newOfflineEnv(t)
	ctx := context.Background()

	// 主机 10 分钟未上报（阈值 1 分钟）→ 离线 + up=0 + agent_offline firing
	stale := hostWithLastSeen(t, s, "stale-1", time.Now().UTC().Add(-10*time.Minute))
	m.Tick(ctx)

	h, _ := s.Hosts().GetByID(ctx, stale.ID)
	if h.Status != "offline" {
		t.Fatalf("应置为 offline, got %s", h.Status)
	}
	rows, _, err := s.Alerts().List(ctx, adapter.AlertFilter{States: []string{"firing"}, Category: "agent_offline"})
	if err != nil || len(rows) != 1 {
		t.Fatalf("应有 1 条 agent_offline firing: n=%d err=%v", len(rows), err)
	}
	if rows[0].Severity != "info" {
		t.Fatalf("agent_offline 应为 info 级, got %s", rows[0].Severity)
	}

	// up=0 样本落库（M3：metalwatch_up 归零）
	pipe.Stop()
	up, err := ts.Query(ctx, adapter.Query{
		Metric: "up", Labels: map[string]string{"host_id": strconv.FormatInt(stale.ID, 10)},
		Start: time.Now().Add(-time.Hour), End: time.Now().Add(time.Hour),
	})
	if err != nil || len(up) != 1 || len(up[0].Points) != 1 || up[0].Points[0].Value != 0 {
		t.Fatalf("up=0 样本未落库: %+v err=%v", up, err)
	}

	// 幂等：再次 Tick 不产生第二条告警
	m.Tick(ctx)
	rows, _, _ = s.Alerts().List(ctx, adapter.AlertFilter{States: []string{"firing"}, Category: "agent_offline"})
	if len(rows) != 1 {
		t.Fatalf("重复 Tick 不应产生新告警, got %d", len(rows))
	}

	// 主机恢复上报（report 路径置回 online）→ Tick 自动 resolved（M3 验收 3）
	if err := s.Hosts().UpdateStatus(ctx, stale.ID, "online", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	m.Tick(ctx)
	rows, _, _ = s.Alerts().List(ctx, adapter.AlertFilter{States: []string{"firing"}, Category: "agent_offline"})
	if len(rows) != 0 {
		t.Fatalf("恢复后不应再有 firing, got %d", len(rows))
	}
	all, total, _ := s.Alerts().List(ctx, adapter.AlertFilter{})
	if total < 1 || all[0].State != "resolved" {
		t.Fatalf("故障史应保留且为 resolved: %+v", all)
	}
}
