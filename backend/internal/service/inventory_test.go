package service_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	_ "github.com/LarryMKott/metalwatch/internal/adapter/sqlite"
	"github.com/LarryMKott/metalwatch/internal/service"
	"github.com/LarryMKott/metalwatch/migrations"
	"github.com/LarryMKott/metalwatch/pkg/config"
	gen "github.com/LarryMKott/metalwatch/proto/gen"
)

func newInventoryEnv(t *testing.T) (*service.InventoryService, *adapter.Store) {
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
	return service.NewInventoryService(s, service.NewAlertService(s, s.Thresholds(), s.Alerts(),
		service.AlertDeps{}, nil), nil), s
}

func hostFor(t *testing.T, s *adapter.Store, hostname string) int64 {
	t.Helper()
	id, err := s.Hosts().Create(context.Background(), &adapter.Host{
		Hostname: hostname, PrimaryIP: "10.9.9.9", OSType: "linux", Status: "online",
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func snapshotOf(cpu, dimm1, dimm2 string) *gen.AssetSnapshot {
	snap := &gen.AssetSnapshot{Fingerprint: "agent-provided"}
	if cpu != "" {
		snap.Cpu = &gen.CpuInfo{Model: cpu, Socket: "CPU1", Cores: 12}
	}
	if dimm1 != "" {
		snap.Memory = append(snap.Memory, &gen.MemInfo{Slot: "DIMM_A1", Manufacturer: "Samsung",
			PartNumber: dimm1, SizeBytes: 32 << 30})
	}
	if dimm2 != "" {
		snap.Memory = append(snap.Memory, &gen.MemInfo{Slot: "DIMM_B1", Manufacturer: "Samsung",
			PartNumber: dimm2, SizeBytes: 32 << 30})
	}
	return snap
}

func ingest(t *testing.T, svc *service.InventoryService, hostID int64, snap *gen.AssetSnapshot, at time.Time) {
	t.Helper()
	if err := svc.IngestSnapshot(context.Background(), hostID, "inv-node", snap, "agent", at); err != nil {
		t.Fatal(err)
	}
}

func changes(t *testing.T, s *adapter.Store, hostID int64) []adapter.ChangeEvent {
	t.Helper()
	rows, err := s.Assets().ListChanges(context.Background(), hostID, 100)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestInventoryBaselineNoChanges(t *testing.T) {
	svc, s := newInventoryEnv(t)
	hostID := hostFor(t, s, "inv-1")
	at := time.Now().UTC()

	ingest(t, svc, hostID, snapshotOf("Xeon 4310", "M393A1", "M393B2"), at)

	// D11 ①：首次采集只建基线
	if rows := changes(t, s, hostID); len(rows) != 0 {
		t.Fatalf("基线不应产生变更: %+v", rows)
	}
	comps, err := s.Components().ListActive(context.Background(), hostID, "")
	if err != nil || len(comps) != 4 { // cpu + 2 dimm + bios
		t.Fatalf("部件数 = %d err=%v, want 4", len(comps), err)
	}

	// 指纹未变：仍无变更
	ingest(t, svc, hostID, snapshotOf("Xeon 4310", "M393A1", "M393B2"), at.Add(time.Minute))
	if rows := changes(t, s, hostID); len(rows) != 0 {
		t.Fatalf("指纹未变不应产生变更: %+v", rows)
	}
}

func TestInventoryRemovedNeedsTwoConfirmations(t *testing.T) {
	svc, s := newInventoryEnv(t)
	hostID := hostFor(t, s, "inv-2")
	at := time.Now().UTC().Truncate(time.Second)

	ingest(t, svc, hostID, snapshotOf("Xeon", "DIMM-A", "DIMM-B"), at)

	// 第二轮少报 DIMM_B：第一次缺席只标记，不产生事件（D11 ②）
	ingest(t, svc, hostID, snapshotOf("Xeon", "DIMM-A", ""), at.Add(time.Minute))
	if rows := changes(t, s, hostID); len(rows) != 0 {
		t.Fatalf("第一次缺席不应产生变更: %+v", rows)
	}
	// 第一次缺席即标记移除候选（从在役清单消失），但不产生事件——D11 ② 的待确认态
	comps, _ := s.Components().ListActive(context.Background(), hostID, "memory")
	if len(comps) != 1 {
		t.Fatalf("缺席部件应进入待确认态: %d", len(comps))
	}

	// 第三轮继续缺席：确认移除，落事件
	ingest(t, svc, hostID, snapshotOf("Xeon", "DIMM-A", ""), at.Add(2*time.Minute))
	rows := changes(t, s, hostID)
	if len(rows) != 1 || rows[0].ChangeType != "removed" || rows[0].Slot != "DIMM_B1" {
		t.Fatalf("连续缺席应产生 removed: %+v", rows)
	}

	// 第四轮仍缺席：不重复报
	ingest(t, svc, hostID, snapshotOf("Xeon", "DIMM-A", ""), at.Add(3*time.Minute))
	if rows := changes(t, s, hostID); len(rows) != 1 {
		t.Fatalf("已报过的移除不应重复: %d", len(rows))
	}
}

func TestInventoryAddAndModifyWhitelist(t *testing.T) {
	svc, s := newInventoryEnv(t)
	hostID := hostFor(t, s, "inv-3")
	at := time.Now().UTC().Truncate(time.Second)

	ingest(t, svc, hostID, snapshotOf("Xeon", "DIMM-A", ""), at)

	// 新增 DIMM_B（连续两轮在场即可见；added 当轮落事件）
	ingest(t, svc, hostID, snapshotOf("Xeon", "DIMM-A", "DIMM-B"), at.Add(time.Minute))
	rows := changes(t, s, hostID)
	if len(rows) != 1 || rows[0].ChangeType != "added" || rows[0].Slot != "DIMM_B1" {
		t.Fatalf("新增部件应产生 added: %+v", rows)
	}

	// 白名单字段（容量）变化 → modified
	snap := snapshotOf("Xeon", "DIMM-A", "DIMM-B")
	snap.Memory[1].SizeBytes = 64 << 30
	ingest(t, svc, hostID, snap, at.Add(2*time.Minute))
	rows = changes(t, s, hostID)
	var found *adapter.ChangeEvent
	for i := range rows {
		if rows[i].ChangeType == "modified" {
			found = &rows[i]
		}
	}
	if found == nil || found.Field != "capacity_bytes" {
		t.Fatalf("容量变化应产生 modified: %+v", rows)
	}

	// 非白名单字段（速度）变化 → 不产生变更（容量保持 64GB，只动速度）
	snap = snapshotOf("Xeon", "DIMM-A", "DIMM-B")
	snap.Memory[1].SizeBytes = 64 << 30
	snap.Memory[1].Speed = "4800 MT/s"
	ingest(t, svc, hostID, snap, at.Add(3*time.Minute))
	if n := len(changes(t, s, hostID)); n != 2 {
		t.Fatalf("非白名单变化不应产生变更, got %d", n)
	}

	// 每条变更都应有 asset_change 告警（W18）
	alerts, total, err := s.Alerts().List(context.Background(), adapter.AlertFilter{Category: "asset_change"})
	if err != nil || total < 2 {
		t.Fatalf("asset_change 告警缺失: total=%d err=%v", total, err)
	}
	for _, a := range alerts {
		if a.Severity != "info" || a.State != "firing" {
			t.Fatalf("asset_change 应为 info/firing: %+v", a)
		}
	}
}

func TestInventoryRemovedThenReturn(t *testing.T) {
	svc, s := newInventoryEnv(t)
	hostID := hostFor(t, s, "inv-4")
	at := time.Now().UTC().Truncate(time.Second)

	ingest(t, svc, hostID, snapshotOf("Xeon", "DIMM-A", "DIMM-B"), at)
	ingest(t, svc, hostID, snapshotOf("Xeon", "DIMM-A", ""), at.Add(time.Minute))   // 缺席 1
	ingest(t, svc, hostID, snapshotOf("Xeon", "DIMM-A", ""), at.Add(2*time.Minute)) // 缺席 2 → removed
	// 回归：拔了再插是同一部件回归，不是新增两次
	ingest(t, svc, hostID, snapshotOf("Xeon", "DIMM-A", "DIMM-B"), at.Add(3*time.Minute))

	rows := changes(t, s, hostID)
	if len(rows) != 1 {
		t.Fatalf("回归不应新增变更事件: %+v", rows)
	}
	comps, _ := s.Components().ListActive(context.Background(), hostID, "memory")
	if len(comps) != 2 {
		t.Fatalf("回归后应在役 2 条内存: %d", len(comps))
	}
}
