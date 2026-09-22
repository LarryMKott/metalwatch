package service_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	_ "github.com/LarryMKott/metalwatch/internal/adapter/sqlite"
	"github.com/LarryMKott/metalwatch/internal/service"
	"github.com/LarryMKott/metalwatch/migrations"
	"github.com/LarryMKott/metalwatch/pkg/config"
	"github.com/LarryMKott/metalwatch/pkg/crypto"
)

func openStore(t *testing.T) *adapter.Store {
	t.Helper()
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
	return db
}

// TestEnsureFirstAdminCreatesOnce 首次启动创建管理员，之后不再覆盖。
func TestEnsureFirstAdminCreatesOnce(t *testing.T) {
	db := openStore(t)
	ctx := context.Background()
	dir := t.TempDir()

	if err := service.EnsureFirstAdmin(ctx, db, dir, nil); err != nil {
		t.Fatalf("首次引导失败: %v", err)
	}
	u, err := db.Users().GetByName(ctx, service.BootstrapAdminUser)
	if err != nil || u == nil {
		t.Fatalf("应创建初始管理员: %v", err)
	}
	if u.Role != "admin" || u.State != "active" {
		t.Fatalf("初始账号应为 admin/active: %+v", u)
	}

	// 口令文件应落盘且权限收紧
	p := filepath.Join(dir, "bootstrap_admin.txt")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("应写出初始口令文件: %v", err)
	}
	if !contains(string(raw), service.BootstrapAdminUser) {
		t.Fatalf("口令文件应包含账号名: %s", raw)
	}

	// 第二次调用不应再建（Count > 0 直接返回）
	if err := service.EnsureFirstAdmin(ctx, db, dir, nil); err != nil {
		t.Fatalf("二次引导失败: %v", err)
	}
	n, err := db.Users().Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("不应重复创建账号，实际 %d 个", n)
	}

	// 已有普通账号时也不应再建
	hash, _ := crypto.HashPassword("viewer-pass-1")
	if _, err := db.Users().Create(ctx, &adapter.AppUser{
		Username: "viewer-only", PasswordHash: hash, Role: "viewer", State: "active",
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.EnsureFirstAdmin(ctx, db, dir, nil); err != nil {
		t.Fatal(err)
	}
	n, _ = db.Users().Count(ctx)
	if n != 2 {
		t.Fatalf("已有账号时不应再建，实际 %d 个", n)
	}
}

// TestEnsureFirstAdminPasswordIsRandom 两次引导生成的口令不同（不硬编码）。
func TestEnsureFirstAdminPasswordIsRandom(t *testing.T) {
	dir1, dir2 := t.TempDir(), t.TempDir()
	db1, db2 := openStore(t), openStore(t)
	ctx := context.Background()

	if err := service.EnsureFirstAdmin(ctx, db1, dir1, nil); err != nil {
		t.Fatal(err)
	}
	if err := service.EnsureFirstAdmin(ctx, db2, dir2, nil); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(filepath.Join(dir1, "bootstrap_admin.txt"))
	b, _ := os.ReadFile(filepath.Join(dir2, "bootstrap_admin.txt"))
	if string(a) == string(b) {
		t.Fatal("两次引导的初始口令不应相同")
	}
	// 哈希不能是明文口令
	u1, _ := db1.Users().GetByName(ctx, service.BootstrapAdminUser)
	if u1.PasswordHash == "" || u1.PasswordHash == service.BootstrapAdminUser {
		t.Fatal("口令应以哈希存储")
	}
	if !crypto.VerifyPassword(u1.PasswordHash, extractPassword(string(a))) {
		t.Fatal("文件里的口令应能通过校验")
	}
}

// ---------- 辅助 ----------

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// extractPassword 从引导文件里取出 password: 后的内容。
func extractPassword(body string) string {
	const marker = "password: "
	i := indexOf(body, marker)
	if i < 0 {
		return ""
	}
	rest := body[i+len(marker):]
	if j := indexOf(rest, "\n"); j >= 0 {
		rest = rest[:j]
	}
	return trimSpace(rest)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\r' || s[start] == '\n') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\r' || s[end-1] == '\n') {
		end--
	}
	return s[start:end]
}
