package agentcfg

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/LarryMKott/metalwatch/pkg/env"
)

// devEnvironment / prodEnvironment 构造确定的环境对象，
// 避免依赖进程级环境变量（并行用例下 t.Setenv 会互相干扰）。
func devEnvironment(t *testing.T) env.Environment {
	t.Helper()
	e, err := env.Resolve(func(string) string { return "" })
	if err != nil {
		t.Fatalf("构造开发环境失败: %v", err)
	}
	return e
}

func prodEnvironment(t *testing.T) env.Environment {
	t.Helper()
	e, err := env.Resolve(func(k string) string {
		if k == env.Name {
			return env.Prod
		}
		return ""
	})
	if err != nil {
		t.Fatalf("构造生产环境失败: %v", err)
	}
	return e
}

func fakeGetenv(m map[string]string) env.Getenv {
	return func(k string) string { return m[k] }
}

func TestResolveSpoolDir(t *testing.T) {
	dev, prod := devEnvironment(t), prodEnvironment(t)

	if got := ResolveSpoolDir("custom/spool", dev); got != "custom/spool" {
		t.Fatalf("显式 --spool 应优先，实际 %q", got)
	}
	if got := ResolveSpoolDir("", dev); got != devSpoolDir {
		t.Fatalf("开发环境应落仓库内 %q，实际 %q", devSpoolDir, got)
	}
	if got := ResolveSpoolDir("", prod); got != DefaultSpoolDir() {
		t.Fatalf("生产环境应用平台默认值 %q，实际 %q", DefaultSpoolDir(), got)
	}
	if got := ResolveSpoolDir("   ", dev); got != devSpoolDir {
		t.Fatalf("空白 --spool 应视为未提供，实际 %q", got)
	}
}

func TestResolveServer(t *testing.T) {
	if got := ResolveServer("http://10.0.0.1:18080", Credentials{}); got != "http://10.0.0.1:18080" {
		t.Fatalf("显式 --server 应优先，实际 %q", got)
	}
	if got := ResolveServer("", Credentials{Server: "http://10.0.0.2:18080"}); got != "http://10.0.0.2:18080" {
		t.Fatalf("应回退到凭据里记录的地址，实际 %q", got)
	}
	if got := ResolveServer("", Credentials{}); got != DefaultServerURL {
		t.Fatalf("无任何来源时应返回兜底默认值，实际 %q", got)
	}
}

func TestStoreSaveLoadRoundTrip(t *testing.T) {
	spool := filepath.Join(t.TempDir(), "bin-local", "spool")
	want := Credentials{Token: "tok-1", HostID: 12, Server: "http://127.0.0.1:18080"}
	if err := SaveIdentity(spool, want); err != nil {
		t.Fatalf("保存凭据失败: %v", err)
	}

	store, err := NewStore(spool)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.Load()
	if err != nil || !ok {
		t.Fatalf("读取失败: ok=%v err=%v", ok, err)
	}
	if got != want {
		t.Fatalf("往返不一致: got=%+v want=%+v", got, want)
	}

	// 凭据文件必须收紧权限（Windows 上权限位语义不同，跳过）
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(store.Dir(), credFileName))
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Fatalf("凭据文件权限应为 0600，实际 %o", perm)
		}
	}

	// host_id.txt 同步写出：既有 start-agent 脚本仍读它
	raw, err := os.ReadFile(filepath.Join(store.Dir(), legacyHostIDFile))
	if err != nil || strings.TrimSpace(string(raw)) != "12" {
		t.Fatalf("host_id.txt 应写出 12: %q err=%v", string(raw), err)
	}
}

func TestStoreRejectsEmptyToken(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(Credentials{HostID: 1}); err == nil {
		t.Fatal("空 token 应被拒绝写入")
	}
}

func TestStoreLoadMissingFiles(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	_, ok, err := store.Load()
	if err != nil {
		t.Fatalf("目录不存在不应报错: %v", err)
	}
	if ok {
		t.Fatal("无凭据文件时 ok 应为 false")
	}
}

// TestStoreLoadLegacyLayout 覆盖历史布局：token（纯文本）+ host_id.txt。
// 两者齐全的历史开发机不因本次改动而需要重新注册。
// 只有 token 的老机器会读到 HostID=0，继而被 ResolveIdentity 拒绝——这是预期语义
// （错误文案会指引重新注册），本用例显式断言它，避免把「回退不完整」误读成「回退完全透明」。
func TestStoreLoadLegacyLayout(t *testing.T) {
	dir := t.TempDir()
	spool := filepath.Join(dir, "spool")
	if err := os.MkdirAll(spool, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, legacyTokenFile), []byte("tok-legacy\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(spool)
	if err != nil {
		t.Fatal(err)
	}
	c, ok, err := store.Load()
	if err != nil || !ok {
		t.Fatalf("应读到历史布局: ok=%v err=%v", ok, err)
	}
	if c.Token != "tok-legacy" {
		t.Fatalf("token 解析错误: %q", c.Token)
	}
	if c.HostID != 0 {
		t.Fatalf("历史布局缺 host_id.txt 时应为 0，实际 %d", c.HostID)
	}

	if err := os.WriteFile(filepath.Join(dir, legacyHostIDFile), []byte("42"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, ok, err = store.Load()
	if err != nil || !ok || c.HostID != 42 {
		t.Fatalf("host_id.txt 应被读到: %+v ok=%v err=%v", c, ok, err)
	}
}

// TestStoreLoadCorruptCredentialFile 覆盖损坏文件：必须报错而不是当成「未注册」，
// 否则用户会看到「缺少令牌，请重新注册」这种误导性提示。
func TestStoreLoadCorruptCredentialFile(t *testing.T) {
	dir := t.TempDir()
	spool := filepath.Join(dir, "spool")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, credFileName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(spool)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Load(); err == nil {
		t.Fatal("损坏的凭据文件应报错")
	}
}

// TestResolveIdentityFallsBackToLocalCredentialsInDev 是开发环境「无参数直接运行」的核心保障：
// 注册一次之后，不再需要手工 export 两个环境变量。
func TestResolveIdentityFallsBackToLocalCredentialsInDev(t *testing.T) {
	spool := filepath.Join(t.TempDir(), "bin-local", "spool")
	local := Credentials{Token: "tok-local", HostID: 7, Server: "http://10.0.0.9:18080"}
	if err := SaveIdentity(spool, local); err != nil {
		t.Fatal(err)
	}

	c, err := ResolveIdentity(spool, devEnvironment(t), fakeGetenv(nil))
	if err != nil {
		t.Fatalf("开发环境应回退本地凭据: %v", err)
	}
	if c != local {
		t.Fatalf("凭据解析错误: got=%+v want=%+v", c, local)
	}

	// 环境变量优先于本地凭据
	c, err = ResolveIdentity(spool, devEnvironment(t), fakeGetenv(map[string]string{
		EnvToken: "tok-env", EnvHostID: "9",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Token != "tok-env" || c.HostID != 9 {
		t.Fatalf("环境变量应优先于本地凭据: %+v", c)
	}

	// 只给 token：host_id 由本地凭据补齐（便于只轮换令牌的场景）
	c, err = ResolveIdentity(spool, devEnvironment(t), fakeGetenv(map[string]string{
		EnvToken: "tok-env",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Token != "tok-env" || c.HostID != 7 {
		t.Fatalf("缺失的 host_id 应由本地凭据补齐: %+v", c)
	}
}

// TestResolveIdentityIgnoresLocalCredentialsInProd 保证生产环境只认显式注入，
// 不让「谁写的凭据文件」变成隐式来源。
func TestResolveIdentityIgnoresLocalCredentialsInProd(t *testing.T) {
	spool := filepath.Join(t.TempDir(), "spool")
	if err := SaveIdentity(spool, Credentials{Token: "tok-local", HostID: 7}); err != nil {
		t.Fatal(err)
	}

	_, err := ResolveIdentity(spool, prodEnvironment(t), fakeGetenv(nil))
	if err == nil {
		t.Fatal("生产环境不应读取本地凭据文件，应报缺少令牌")
	}
	if !strings.Contains(err.Error(), EnvToken) {
		t.Fatalf("错误信息应指引 %s，实际: %v", EnvToken, err)
	}

	// 显式注入后正常
	c, err := ResolveIdentity(spool, prodEnvironment(t), fakeGetenv(map[string]string{
		EnvToken: "tok-prod", EnvHostID: "3",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Token != "tok-prod" || c.HostID != 3 {
		t.Fatalf("显式注入应生效: %+v", c)
	}
}

func TestResolveIdentityRejectsBadHostID(t *testing.T) {
	prod := prodEnvironment(t)
	for _, bad := range []string{"abc", "0", "-5", "1.5"} {
		t.Run(bad, func(t *testing.T) {
			_, err := ResolveIdentity("", prod, fakeGetenv(map[string]string{
				EnvToken: "t", EnvHostID: bad,
			}))
			if err == nil {
				t.Fatalf("host_id=%q 应被拒绝", bad)
			}
			if !strings.Contains(err.Error(), EnvHostID) {
				t.Fatalf("错误信息应指明 %s，实际: %v", EnvHostID, err)
			}
		})
	}
}

func TestResolveIdentityMissingHostID(t *testing.T) {
	_, err := ResolveIdentity("", prodEnvironment(t), fakeGetenv(map[string]string{EnvToken: "t"}))
	if err == nil || !strings.Contains(err.Error(), EnvHostID) {
		t.Fatalf("缺 host_id 应报错并指明变量名，实际: %v", err)
	}
}
