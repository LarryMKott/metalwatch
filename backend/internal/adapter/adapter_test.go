package adapter_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	_ "github.com/LarryMKott/metalwatch/internal/adapter/sqlite"
	"github.com/LarryMKott/metalwatch/migrations"
	"github.com/LarryMKott/metalwatch/pkg/config"
)

// newStore 建一个临时目录里的 SQLite 存储并跑完迁移。
func newStore(t *testing.T) *adapter.Store {
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
		t.Fatalf("取迁移目录失败: %v", err)
	}
	if _, err := s.Migrate(ctx, fsys, string(s.Dialect())); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	return s
}

func TestRebind(t *testing.T) {
	tests := []struct {
		name    string
		dialect adapter.Dialect
		in      string
		want    string
	}{
		{"sqlite 保持问号", adapter.DialectSQLite, "SELECT * FROM host WHERE id = ? AND name = ?",
			"SELECT * FROM host WHERE id = ? AND name = ?"},
		{"postgres 转 $N", adapter.DialectPostgres, "SELECT * FROM host WHERE id = ? AND name = ?",
			"SELECT * FROM host WHERE id = $1 AND name = $2"},
		{"无占位符", adapter.DialectPostgres, "SELECT 1", "SELECT 1"},
		{"十个以上占位符", adapter.DialectPostgres,
			"?,?,?,?,?,?,?,?,?,?,?",
			"$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := adapter.Rebind(tc.in, tc.dialect); got != tc.want {
				t.Fatalf("Rebind() = %q, 期望 %q", got, tc.want)
			}
		})
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	v, err := s.CurrentVersion(ctx)
	if err != nil {
		t.Fatalf("读取版本失败: %v", err)
	}
	if v < 1 {
		t.Fatalf("迁移后版本应 >= 1，实际 %d", v)
	}

	// 再跑一次：不应重复应用，也不应报错
	fsys, _ := migrations.DialectFS(string(s.Dialect()))
	applied, err := s.Migrate(ctx, fsys, string(s.Dialect()))
	if err != nil {
		t.Fatalf("重复迁移报错: %v", err)
	}
	if applied != 0 {
		t.Fatalf("重复迁移不应有新应用，实际 %d", applied)
	}
}

func TestHostCRUD(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	repo := s.Hosts()

	sn := "CN1234567"
	bmc := "10.0.0.200"
	h := &adapter.Host{
		Hostname: "node-001", PrimaryIP: "10.0.0.10",
		SN: &sn, BMCIP: &bmc, SMBIOSUUID: strPtr("uuid-1"),
		OSType: "linux", Status: "unknown", CollectAgent: true,
	}
	id, err := repo.Create(ctx, h)
	if err != nil {
		t.Fatalf("创建主机失败: %v", err)
	}

	got, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("读取主机失败: %v", err)
	}
	if got.Hostname != "node-001" || got.SN == nil || *got.SN != sn {
		t.Fatalf("读回数据不一致: %+v", got)
	}
	if !got.CreatedAt.After(time.Time{}) {
		t.Fatal("created_at 未写入")
	}

	// 可空列 UNIQUE + 多 NULL：第二台不带 UUID/BMC 的机器应能写入
	if _, err := repo.Create(ctx, &adapter.Host{
		Hostname: "node-002", PrimaryIP: "10.0.0.11", CollectAgent: true,
	}); err != nil {
		t.Fatalf("不带唯一标识的主机应可写入: %v", err)
	}
	// 重复 UUID 必须被拒
	_, err = repo.Create(ctx, &adapter.Host{
		Hostname: "dup", PrimaryIP: "10.0.0.12",
		SMBIOSUUID: strPtr("uuid-1"), CollectAgent: true,
	})
	if !adapter.IsUniqueViolation(err) {
		t.Fatalf("重复 SMBIOS UUID 应触发唯一约束，实际: %v", err)
	}

	items, total, err := repo.List(ctx, adapter.ListFilter{Limit: 10})
	if err != nil {
		t.Fatalf("列表失败: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("总数应为 2，实际 total=%d len=%d", total, len(items))
	}

	// 关键字过滤
	items, total, err = repo.List(ctx, adapter.ListFilter{Keyword: "node-001", Limit: 10})
	if err != nil || total != 1 || items[0].Hostname != "node-001" {
		t.Fatalf("关键字过滤失败: total=%d err=%v", total, err)
	}

	// 状态更新（Agent 上报路径）
	if err := repo.UpdateStatus(ctx, id, "online", time.Now().UTC()); err != nil {
		t.Fatalf("更新状态失败: %v", err)
	}
	got, _ = repo.GetByID(ctx, id)
	if got.Status != "online" || got.LastSeenAt == nil {
		t.Fatalf("状态未更新: %+v", got)
	}

	if err := repo.Delete(ctx, id); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, err := repo.GetByID(ctx, id); !errors.Is(err, adapter.ErrNotFound) {
		t.Fatalf("删除后应返回 ErrNotFound，实际: %v", err)
	}
}

func TestEnrollCodeConsume(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	codes := s.EnrollCodes()

	hash := "hash-of-code"
	if err := codes.Issue(ctx, hash, time.Now().UTC().Add(time.Hour), 1, "test"); err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	if err := codes.Consume(ctx, hash); err != nil {
		t.Fatalf("首次消费应成功: %v", err)
	}
	if err := codes.Consume(ctx, hash); !errors.Is(err, adapter.ErrCodeExhausted) {
		t.Fatalf("二次消费应失败，实际: %v", err)
	}

	// 过期码
	expired := "expired-code"
	if err := codes.Issue(ctx, expired, time.Now().UTC().Add(-time.Minute), 1, "test"); err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	if err := codes.Consume(ctx, expired); !errors.Is(err, adapter.ErrCodeExhausted) {
		t.Fatalf("过期码应被拒，实际: %v", err)
	}
}

func TestAgentTokenLifecycle(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	repo := s.Hosts()
	tokens := s.AgentTokens()

	id, err := repo.Create(ctx, &adapter.Host{
		Hostname: "node-t", PrimaryIP: "10.0.1.1", CollectAgent: true,
	})
	if err != nil {
		t.Fatalf("创建主机失败: %v", err)
	}

	tok := &adapter.AgentToken{HostID: &id, TokenHash: "sha256-abc", State: "active"}
	if _, err := tokens.Create(ctx, tok); err != nil {
		t.Fatalf("创建令牌失败: %v", err)
	}

	got, err := tokens.FindActiveByHash(ctx, "sha256-abc")
	if err != nil {
		t.Fatalf("查询令牌失败: %v", err)
	}
	if got.HostID == nil || *got.HostID != id {
		t.Fatalf("令牌未绑定到主机: %+v", got)
	}

	if err := tokens.Revoke(ctx, got.ID); err != nil {
		t.Fatalf("吊销失败: %v", err)
	}
	if _, err := tokens.FindActiveByHash(ctx, "sha256-abc"); !errors.Is(err, adapter.ErrNotFound) {
		t.Fatalf("吊销后应查不到，实际: %v", err)
	}
}

func TestCatalogMarksImplementedBackends(t *testing.T) {
	found := map[string]adapter.Backend{}
	for _, b := range adapter.Catalog() {
		found[b.Name] = b
	}
	sqliteB, ok := found[string(adapter.DialectSQLite)]
	if !ok {
		t.Fatal("catalog 缺少 sqlite")
	}
	if !sqliteB.Implemented {
		t.Fatal("sqlite 应标记为已实现")
	}
	if pg, ok := found[string(adapter.DialectPostgres)]; !ok || pg.Implemented {
		t.Fatal("postgres 应存在且标记为未实现（规划项）")
	}
}

func strPtr(s string) *string { return &s }
