package env

import (
	"strings"
	"testing"
)

// fakeGetenv 用固定表构造环境，避免污染进程环境变量（t.Setenv 会影响并行用例）。
func fakeGetenv(m map[string]string) Getenv {
	return func(k string) string { return m[k] }
}

func TestResolveExplicitValue(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantDev bool
	}{
		{"显式 dev", "dev", true},
		{"显式 prod", "prod", false},
		{"大写 DEV", "DEV", true},
		{"混合大小写 Prod", "Prod", false},
		{"两侧空白", "  prod  ", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e, err := Resolve(fakeGetenv(map[string]string{Name: tc.value}))
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if e.IsDev() != tc.wantDev {
				t.Fatalf("环境判定错误: got=%s want_dev=%v", e.Name(), tc.wantDev)
			}
			if !strings.Contains(e.Source(), Name+"=") {
				t.Fatalf("判定依据应指明来自显式变量，实际: %s", e.Source())
			}
		})
	}
}

// TestResolveRejectsInvalidValue 覆盖非法取值：必须报错并列出合法值，
// 不能静默回退——环境决定数据落盘位置，猜错代价高。
func TestResolveRejectsInvalidValue(t *testing.T) {
	for _, bad := range []string{"staging", "test", "production", "1", "true"} {
		t.Run(bad, func(t *testing.T) {
			_, err := Resolve(fakeGetenv(map[string]string{Name: bad}))
			if err == nil {
				t.Fatalf("取值 %q 应被拒绝", bad)
			}
			for _, want := range []string{Name, Dev, Prod, bad} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("错误信息应包含 %q，实际: %v", want, err)
				}
			}
		})
	}
}

// TestResolveFPKFallback 覆盖兜底判据：FPK 环境（有 TRIM_PKGVAR）视为生产，
// 即使运维忘了写 METALWATCH_ENV 也不会落到开发用的相对路径。
func TestResolveFPKFallback(t *testing.T) {
	e, err := Resolve(fakeGetenv(map[string]string{"TRIM_PKGVAR": "/vol1/@appdata/metalwatch"}))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !e.IsProd() {
		t.Fatalf("存在 TRIM_PKGVAR 应判为生产，实际 %s", e.Name())
	}
	if !strings.Contains(e.Source(), "TRIM_PKGVAR") {
		t.Fatalf("判定依据应指明 TRIM_PKGVAR，实际: %s", e.Source())
	}

	// 显式变量优先于兜底判据
	e, err = Resolve(fakeGetenv(map[string]string{
		Name:          Dev,
		"TRIM_PKGVAR": "/vol1/@appdata/metalwatch",
	}))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !e.IsDev() {
		t.Fatalf("显式变量应优先于 TRIM_PKGVAR 兜底，实际 %s", e.Name())
	}
}

// TestResolveDefaultsToDev 覆盖无任何环境变量（源码目录里直接跑）判为开发。
func TestResolveDefaultsToDev(t *testing.T) {
	e, err := Resolve(fakeGetenv(nil))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !e.IsDev() {
		t.Fatalf("无环境变量时应判为开发，实际 %s", e.Name())
	}
	if e.String() != Dev {
		t.Fatalf("String() 应返回环境名，实际 %q", e.String())
	}
}

// TestResolveNilGetenv 覆盖便捷用法：nil 表示读取真实进程环境。
func TestResolveNilGetenv(t *testing.T) {
	t.Setenv(Name, Prod)
	e, err := Resolve(nil)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !e.IsProd() {
		t.Fatalf("应读到进程环境里的 prod，实际 %s", e.Name())
	}
}
