package ptr_test

import (
	"testing"

	"github.com/LarryMKott/metalwatch/pkg/ptr"
)

func TestDeref(t *testing.T) {
	empty := ""
	cases := []struct {
		name string
		in   *string
		want string
	}{
		{"nil 得空串", nil, ""},
		{"有值原样返回", strPtr("eth0"), "eth0"},
		{"空串指针得空串", &empty, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ptr.Deref(c.in); got != c.want {
				t.Fatalf("Deref = %q, want %q", got, c.want)
			}
		})
	}
}

// Of 与 OfNonEmpty 的差别是「空串算不算有值」：前者算（写出空串），后者不算（写出 NULL）。
// 这条边界是两个调用场景的全部区别，必须钉住。
func TestOfAndOfNonEmptyDifferOnEmpty(t *testing.T) {
	if p := ptr.Of(""); p == nil || *p != "" {
		t.Fatalf("Of(\"\") 应为指向空串的非 nil 指针, got %v", p)
	}
	if p := ptr.OfNonEmpty(""); p != nil {
		t.Fatalf("OfNonEmpty(\"\") 应为 nil, got %q", *p)
	}
	if p := ptr.OfNonEmpty("   "); p != nil {
		t.Fatalf("OfNonEmpty 纯空白应为 nil, got %q", *p)
	}
	if p := ptr.OfNonEmpty(" abc "); p == nil || *p != " abc " {
		t.Fatalf("OfNonEmpty 保留原值（不裁剪）, got %v", p)
	}
}

func TestTrim(t *testing.T) {
	cases := []struct {
		name    string
		in      *string
		wantNil bool
		want    string
	}{
		{"nil 保持 nil", nil, true, ""},
		{"空串指针转 nil", strPtr(""), true, ""},
		{"纯空白转 nil", strPtr("   "), true, ""},
		{"去首尾空白", strPtr("  10.0.0.1  "), false, "10.0.0.1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ptr.Trim(c.in)
			if c.wantNil {
				if got != nil {
					t.Fatalf("Trim 应返回 nil, got %q", *got)
				}
				return
			}
			if got == nil || *got != c.want {
				t.Fatalf("Trim = %v, want %q", got, c.want)
			}
		})
	}
}

func strPtr(s string) *string { return &s }
