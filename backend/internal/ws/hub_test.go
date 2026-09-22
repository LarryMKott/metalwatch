package ws

import (
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestHubBroadcastJSON(t *testing.T) {
	h := NewHub(slog.New(slog.NewTextHandler(io.Discard, nil)))

	ch := h.subscribe()
	if h.ClientCount() != 1 {
		t.Fatalf("订阅数 = %d, want 1", h.ClientCount())
	}

	h.BroadcastJSON(map[string]any{"event": "alert.firing", "severity": "critical"})
	msg, ok := <-ch
	if !ok {
		t.Fatal("订阅通道被提前关闭")
	}
	var got map[string]any
	if err := json.Unmarshal(msg, &got); err != nil {
		t.Fatalf("广播不是合法 JSON: %v", err)
	}
	if got["event"] != "alert.firing" {
		t.Fatalf("广播内容不符: %v", got)
	}

	// 慢消费者：缓冲满后广播不阻塞、继续服务其他订阅者
	for i := 0; i < 32; i++ { // 填满缓冲
		h.BroadcastJSON(map[string]any{"i": i})
	}
	done := make(chan struct{})
	go func() { h.BroadcastJSON(map[string]any{"i": "overflow"}); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("缓冲满时广播被阻塞")
	}

	h.unsubscribe(ch)
	if h.ClientCount() != 0 {
		t.Fatalf("退订后订阅数 = %d, want 0", h.ClientCount())
	}
}
