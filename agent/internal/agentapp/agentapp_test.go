package agentapp

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LarryMKott/metalwatch/pkg/env"
)

// testPlatform 是 Platform 的最小实现。
type testPlatform struct{ hint string }

func (p testPlatform) CredentialHint() string { return p.hint }
func (p testPlatform) Signals() []os.Signal   { return []os.Signal{os.Interrupt} }

func envFor(t *testing.T, value string) env.Environment {
	t.Helper()
	e, err := env.Resolve(func(string) string { return value })
	if err != nil {
		t.Fatalf("构造 %s 环境失败: %v", value, err)
	}
	return e
}

// newTestApp 直接构造 App（绕开对进程环境变量的读取），并把输出接到缓冲区以便断言。
func newTestApp(t *testing.T, environment env.Environment, spoolDir string) (*App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	return &App{
		platform: testPlatform{hint: "systemd unit 的 EnvironmentFile"},
		version:  "0.0.0-test",
		env:      environment,
		log:      slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		spoolDir: spoolDir,
		getenv:   func(string) string { return "" },
		out:      out,
		errOut:   errOut,
	}, out, errOut
}

func TestNewFlagsParseDefaultsAndNormalize(t *testing.T) {
	opts, err := NewFlags(flag.NewFlagSet("t", flag.ContinueOnError)).Parse(nil)
	if err != nil {
		t.Fatalf("解析空参数失败: %v", err)
	}
	if opts.Transport != TransportGRPC {
		t.Fatalf("传输层应默认 %s，实际 %q", TransportGRPC, opts.Transport)
	}
	if opts.Interval != 30*time.Second {
		t.Fatalf("上报周期应默认 30s，实际 %v", opts.Interval)
	}
	if opts.SpoolDir != "" {
		t.Fatalf("未指定 --spool 时应留空由运行环境补默认值，实际 %q", opts.SpoolDir)
	}

	got, err := NewFlags(flag.NewFlagSet("t", flag.ContinueOnError)).Parse([]string{
		"--code", " MW-abc\n", "--server", " http://1.2.3.4:18080 ",
		"--transport", TransportJSON, "--spool", " bin-local/spool ",
	})
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if got.Code != "MW-abc" || got.Server != "http://1.2.3.4:18080" || got.SpoolDir != "bin-local/spool" {
		t.Fatalf("参数未做首尾空白归一: %+v", got)
	}
	if got.Transport != TransportJSON {
		t.Fatalf("--transport 未生效: %q", got.Transport)
	}
}

func TestNewResolvesDefaultsByEnvironment(t *testing.T) {
	t.Setenv("METALWATCH_ENV", "dev")
	dev, err := New(testPlatform{hint: "x"}, "0.0.0-test", Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !dev.env.IsDev() {
		t.Fatal("METALWATCH_ENV=dev 应判定为开发环境")
	}
	if dev.spoolDir != "bin-local/spool" {
		t.Fatalf("开发环境 spool 默认值应为 bin-local/spool，实际 %q", dev.spoolDir)
	}

	t.Setenv("METALWATCH_ENV", "prod")
	prod, err := New(testPlatform{hint: "x"}, "0.0.0-test", Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if prod.env.IsDev() {
		t.Fatal("METALWATCH_ENV=prod 应判定为生产环境")
	}
	if prod.spoolDir == dev.spoolDir || prod.spoolDir == "" {
		t.Fatalf("生产环境应使用独立默认 spool 目录，实际 %q", prod.spoolDir)
	}

	// 显式 --spool 一律优先于环境默认值。
	explicit, err := New(testPlatform{hint: "x"}, "0.0.0-test", Options{SpoolDir: "/custom/spool"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if explicit.spoolDir != "/custom/spool" {
		t.Fatalf("显式 spool 未被采用: %q", explicit.spoolDir)
	}
}

func TestNewRejectsBadTransportAndNilPlatform(t *testing.T) {
	// 原先的写法是「等于 grpc 走流、其余走 JSON」，打错一个字母就会静默切到
	// 兼容通道，排障时看不出问题。现在必须直接报错。
	if _, err := New(testPlatform{}, "0.0.0-test", Options{Transport: "grpcx"}); err == nil {
		t.Fatal("未知传输协议应报错，而不是静默回落")
	} else if !strings.Contains(err.Error(), "grpcx") {
		t.Fatalf("错误信息应带上非法取值，实际: %v", err)
	}
	if _, err := New(nil, "0.0.0-test", Options{}); err == nil {
		t.Fatal("平台实现为空应报错")
	}
}

// TestPersistCredentialsWriteFailureDoesNotClaimSuccess 守住本次重构的直接动机：
// Linux 的 JSON 注册路径此前在写盘失败后仍打印「注册成功，凭据已写入本地」。
func TestPersistCredentialsWriteFailureDoesNotClaimSuccess(t *testing.T) {
	// 让 spool 的父路径是普通文件 → 两份凭据都写不进去。
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("准备阻塞文件失败: %v", err)
	}
	app, out, errOut := newTestApp(t, envFor(t, "dev"), filepath.Join(blocker, "spool"))

	err := app.persistCredentials("MW-token-abc", 42, "http://127.0.0.1:18080")
	if !errors.Is(err, ErrEnrollNotSaved) {
		t.Fatalf("写盘失败应返回 ErrEnrollNotSaved，实际: %v", err)
	}
	if strings.Contains(out.String(), "注册成功") {
		t.Fatalf("写盘失败却对外声称注册成功:\n%s", out.String())
	}
	for _, want := range []string{"手工保存", "MW-token-abc", "42"} {
		if !strings.Contains(errOut.String(), want) {
			t.Fatalf("stderr 应含 %q 以便用户手工保存，实际:\n%s", want, errOut.String())
		}
	}
}

func TestPersistCredentialsSuccessWritesBothLayouts(t *testing.T) {
	spool := filepath.Join(t.TempDir(), "spool")
	app, out, errOut := newTestApp(t, envFor(t, "dev"), spool)

	if err := app.persistCredentials("MW-token-abc", 7, "http://127.0.0.1:18080"); err != nil {
		t.Fatalf("落盘应当成功: %v", err)
	}
	if errOut.Len() != 0 {
		t.Fatalf("成功路径不应写 stderr: %s", errOut.String())
	}
	if !strings.Contains(out.String(), "注册成功") ||
		!strings.Contains(out.String(), "无需再带任何参数") {
		t.Fatalf("开发环境成功提示不完整:\n%s", out.String())
	}

	// 三份文件都落在 spool 的父目录（与 grpcstream.SaveToken 的既有约定一致）。
	dir := filepath.Dir(spool)
	for _, name := range []string{"token", "credentials.json", "host_id.txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("应生成 %s: %v", name, err)
		}
	}
}

func TestPersistCredentialsProdPrintsCredentialHint(t *testing.T) {
	app, out, _ := newTestApp(t, envFor(t, "prod"),
		filepath.Join(t.TempDir(), "spool"))

	if err := app.persistCredentials("MW-token-abc", 7, "http://1.2.3.4:18080"); err != nil {
		t.Fatalf("落盘应当成功: %v", err)
	}
	got := out.String()
	if strings.Contains(got, "无需再带任何参数") {
		t.Fatalf("生产环境不应提示「无需参数」:\n%s", got)
	}
	if !strings.Contains(got, "systemd unit 的 EnvironmentFile") {
		t.Fatalf("生产环境应给出平台凭据落点指引:\n%s", got)
	}
}

// TestRunWithoutCredentialsFailsWithActionableMessage 覆盖统一后的 JSON 通道：
// 未注册时应给出可操作的提示，而不是静默启动后再以 403 报错。
func TestRunWithoutCredentialsFailsWithActionableMessage(t *testing.T) {
	app, _, _ := newTestApp(t, envFor(t, "dev"), filepath.Join(t.TempDir(), "spool"))
	app.opts.Transport = TransportJSON

	err := app.Run(context.Background())
	if err == nil {
		t.Fatal("未注册时应报错")
	}
	if !strings.Contains(err.Error(), "缺少令牌") || !strings.Contains(err.Error(), "METALWATCH_AGENT_TOKEN") {
		t.Fatalf("错误信息应指出缺什么、怎么补，实际: %v", err)
	}
}
