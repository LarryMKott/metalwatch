package main

import "testing"

// TestSplitListen 覆盖 --listen 的解析：主机部分必须原样保留，
// 否则「生产听所有网卡 / 开发只听本机」的默认值会被显式参数悄悄改掉。
func TestSplitListen(t *testing.T) {
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
			host, port, err := splitListen(tc.in)
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if host != tc.wantHost || port != tc.wantPort {
				t.Fatalf("got (%q, %d), want (%q, %d)", host, port, tc.wantHost, tc.wantPort)
			}
		})
	}
}

func TestSplitListenRejectsBadInput(t *testing.T) {
	for _, bad := range []string{"18080", "127.0.0.1", "127.0.0.1:0", "127.0.0.1:70000",
		"127.0.0.1:abc", ""} {
		t.Run(bad, func(t *testing.T) {
			if _, _, err := splitListen(bad); err == nil {
				t.Fatalf("%q 应被拒绝", bad)
			}
		})
	}
}
