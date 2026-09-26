package strs_test

import (
	"testing"
	"unicode/utf8"

	"github.com/LarryMKott/metalwatch/pkg/strs"
)

func TestOrDefault(t *testing.T) {
	cases := []struct {
		name, in, def, want string
	}{
		{"空串回退", "", "cpu0", "cpu0"},
		{"纯空白回退", "   ", "cpu0", "cpu0"},
		{"有值去空白", "  Socket0  ", "cpu0", "Socket0"},
		{"有值原样", "dimm", "cpu0", "dimm"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := strs.OrDefault(c.in, c.def); got != c.want {
				t.Fatalf("OrDefault(%q, %q) = %q, want %q", c.in, c.def, got, c.want)
			}
		})
	}
}

func TestJoinNonEmpty(t *testing.T) {
	cases := []struct {
		name  string
		parts []string
		want  string
	}{
		{"两段齐全", []string{"Samsung", "M393A1"}, "Samsung M393A1"},
		{"左侧缺失", []string{"", "M393A1"}, "M393A1"},
		{"右侧缺失", []string{"Samsung", ""}, "Samsung"},
		{"全缺失", []string{"", ""}, ""},
		{"无参数", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := strs.JoinNonEmpty(" ", c.parts...); got != c.want {
				t.Fatalf("JoinNonEmpty(%q) = %q, want %q", c.parts, got, c.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		name, in string
		n        int
		want     string
	}{
		{"短于上限不动", "abc", 10, "abc"},
		{"等长不动", "abc", 3, "abc"},
		{"超长按字节截断", "abcdef", 3, "abc"},
		{"上限为零", "abc", 0, ""},
		{"上限为负不 panic", "abc", -1, ""},
		{"空串", "", 5, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := strs.Truncate(c.in, c.n); got != c.want {
				t.Fatalf("Truncate(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
			}
		})
	}
}

// 截断点必须落在 rune 边界上：错误信息里常有中文，切出半个字符落库即乱码。
func TestTruncateKeepsUTF8Valid(t *testing.T) {
	// 每个汉字 3 字节：在 4 字节处截断会切进第二个字。
	got := strs.Truncate("温度过高", 4)
	if !utf8.ValidString(got) {
		t.Fatalf("截断结果不是合法 UTF-8: %q", got)
	}
	if got != "温" {
		t.Fatalf("Truncate(\"温度过高\", 4) = %q, want \"温\"", got)
	}
	got = strs.Truncate("温度过高", 3)
	if !utf8.ValidString(got) || got != "温" {
		t.Fatalf("Truncate(\"温度过高\", 3) = %q, want \"温\"", got)
	}
	// 上限恰好是完整前缀时保持原样
	if got := strs.Truncate("温度过高", 6); got != "温度" {
		t.Fatalf("Truncate(\"温度过高\", 6) = %q, want \"温度\"", got)
	}
}
