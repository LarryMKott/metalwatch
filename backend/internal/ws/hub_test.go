package ws

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
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

// TestServeRegistersSubscriberBeforeHandshake 守住一条顺序不变式：
// 订阅登记必须先于协议握手完成，否则「Dial 返回」不等于「已订阅」。
//
// 反例（修复前）：serve 里先 Upgrade 再 subscribe。客户端一收到 101 就发上报，
// 此时服务端可能还没把它放进 clients —— 这一窗口内的广播被静默丢弃。
// 实测 300 次里丢 49 次（~16%），正是 e2e 用例偶发「未收到 WS 推送 … i/o timeout」
// 的根因。这里走真实 HTTP 升级路径复现同一时序：Dial 一返回就广播，必须次次收到。
func TestServeRegistersSubscriberBeforeHandshake(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewHub(slog.New(slog.NewTextHandler(io.Discard, nil)))
	r := gin.New()
	h.Register(r.Group("/api/v1"))
	srv := httptest.NewServer(r)
	defer srv.Close()

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/ws/alerts"
	for i := 0; i < 200; i++ {
		conn, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			t.Fatalf("第 %d 轮 Dial 失败: %v", i, err)
		}
		// 不等待、不睡眠：此刻广播若收不到，说明订阅还没登记
		h.BroadcastJSON(map[string]any{"seq": i})
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _, err = conn.ReadMessage()
		_ = conn.Close()
		if err != nil {
			t.Fatalf("第 %d 轮：Dial 返回后立即广播即丢失（订阅登记晚于握手）: %v", i, err)
		}
	}
}
