package main

import (
	"flag"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LarryMKott/metalwatch/pkg/env"
)

func envFor(t *testing.T, value string) env.Environment {
	t.Helper()
	e, err := env.Resolve(func(string) string { return value })
	if err != nil {
		t.Fatalf("构造 %s 环境失败: %v", value, err)
	}
	return e
}

// TestParseListen 覆盖 --listen 的解析：主机部分必须原样保留，
// 否则「生产听所有网卡 / 开发只听本机」的默认值会被显式参数悄悄改掉。
func TestParseListen(t *testing.T) {
	tests := []struct {
		in       string
		wantHost string
		wantPort int
	}{
		{"0.0.0.0:18080", "0.0.0.0", 18080},
		{"127.0.0.1:18099", "127.0.0.1", 18099},
		{":18080", "", 18080},
		{"[::1]:18080", "::1", 18080},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			addr, err := parseListen(tc.in)
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if addr.host != tc.wantHost || addr.port != tc.wantPort {
				t.Fatalf("got (%q, %d), want (%q, %d)",
					addr.host, addr.port, tc.wantHost, tc.wantPort)
			}
			if addr.String() != tc.in && tc.in != ":18080" {
				t.Fatalf("String() 与输入不一致: got %q, want %q", addr.String(), tc.in)
			}
		})
	}
}

func TestParseListenRejectsBadInput(t *testing.T) {
	for _, bad := range []string{"18080", "127.0.0.1", "127.0.0.1:0", "127.0.0.1:70000",
		"127.0.0.1:abc", ""} {
		t.Run(bad, func(t *testing.T) {
			if _, err := parseListen(bad); err == nil {
				t.Fatalf("%q 应被拒绝", bad)
			}
		})
	}
}

// TestOptionsWasGiven 覆盖「显式传入」与「flag 默认值」的区分。
// 这条判定决定了用户能不能用空值覆盖配置文件里的取值。
func TestOptionsWasGiven(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	o := newOptions(fs)
	if err := o.parse(fs, []string{"--log-dir", ""}); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !o.wasGiven("log-dir") {
		t.Fatal("显式传入空值也必须算「已给定」")
	}
	if o.wasGiven("data") {
		t.Fatal("未传入的参数不应算「已给定」")
	}
}

// TestResolveSettingsDevDefaults 覆盖开发环境的隐式默认值。
func TestResolveSettingsDevDefaults(t *testing.T) {
	s, err := resolveSettings(&options{given: map[string]bool{}}, envFor(t, "dev"))
	if err != nil {
		t.Fatalf("开发环境应能补齐默认值: %v", err)
	}
	if s.dataDir() != devDataDir {
		t.Fatalf("开发环境数据目录应为 %q，实际 %q", devDataDir, s.dataDir())
	}
	if s.addr.host != devHost {
		t.Fatalf("开发环境应只监听 %s，实际 %q", devHost, s.addr.host)
	}
	if s.addr.port != 18080 {
		t.Fatalf("默认端口应为 18080，实际 %d", s.addr.port)
	}
}

// TestResolveSettingsProdRequiresDataDir 覆盖生产环境拒绝隐式数据目录 ——
// 原先那个看起来无害的 "./data" 兜底正是数据被写到错误位置的源头。
func TestResolveSettingsProdRequiresDataDir(t *testing.T) {
	_, err := resolveSettings(&options{given: map[string]bool{}}, envFor(t, "prod"))
	if err == nil {
		t.Fatal("生产环境缺数据目录应当直接失败")
	}
	if !strings.Contains(err.Error(), "数据目录") {
		t.Fatalf("错误信息应指明是数据目录问题，实际: %v", err)
	}
}

// TestResolveSettingsProdListen 覆盖生产环境的监听默认值与显式覆盖。
func TestResolveSettingsProdListen(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")

	s, err := resolveSettings(&options{dataDir: dataDir, given: map[string]bool{}}, envFor(t, "prod"))
	if err != nil {
		t.Fatalf("显式指定数据目录后应通过: %v", err)
	}
	if s.addr.host != "" {
		t.Fatalf("生产环境应监听所有网卡，实际 host=%q", s.addr.host)
	}

	s2, err := resolveSettings(&options{
		dataDir: dataDir, listen: "127.0.0.1:18099", given: map[string]bool{},
	}, envFor(t, "prod"))
	if err != nil {
		t.Fatalf("解析 --listen 失败: %v", err)
	}
	if s2.addr.String() != "127.0.0.1:18099" {
		t.Fatalf("--listen 未生效: %q", s2.addr.String())
	}
	// 端口必须回写进 config：app.Deps 会把 Config 交给 handler，
	// 不回写会出现「实际听 18099、对外自称 18080」。
	if s2.config.Server.Port != 18099 {
		t.Fatalf("显式端口应回写进 config，实际 %d", s2.config.Server.Port)
	}
}
