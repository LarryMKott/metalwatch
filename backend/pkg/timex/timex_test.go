package timex_test

import (
	"testing"
	"time"

	"github.com/LarryMKott/metalwatch/pkg/timex"
)

// 这条用例钉住的是「落库时间与宿主机时区无关」：只要有人把实现改成本地时区，
// 或让调用方自己传 Location，这里就会红。
func TestRFC3339AlwaysUTC(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	instant := time.Date(2026, 9, 26, 20, 30, 0, 0, shanghai)

	got := timex.RFC3339(instant)
	if got != "2026-09-26T12:30:00Z" {
		t.Fatalf("RFC3339 = %q, want 2026-09-26T12:30:00Z", got)
	}

	// 同一时刻换个时区表达，字面量必须完全一致（字符串比较依赖这一点）。
	if other := timex.RFC3339(instant.UTC()); other != got {
		t.Fatalf("时区表达不同导致字面量不同: %q vs %q", got, other)
	}
}

func TestRFC3339RoundTrip(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	parsed, err := time.Parse(time.RFC3339, timex.RFC3339(at))
	if err != nil {
		t.Fatalf("输出应可被 RFC3339 解析: %v", err)
	}
	if !parsed.Equal(at) {
		t.Fatalf("往返后不相等: %v vs %v", parsed, at)
	}
}
