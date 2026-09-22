// Package ws 是前端实时推送通道（docs/03 §7）：/api/v1/ws/alerts。
// Hub 维护订阅集合并广播事件；单订阅者消费过慢时丢弃其消息（不阻塞广播方），
// 前端有指数退避重连，丢帧只影响瞬时展示。
package ws

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// writeTimeout 是单帧写超时。
const writeTimeout = 10 * time.Second

// Hub 是订阅集合与广播器。实现 service.Broadcaster 接口（BroadcastJSON）。
type Hub struct {
	mu      sync.RWMutex
	clients map[chan []byte]struct{}
	log     *slog.Logger
}

// NewHub 构造广播中心。
func NewHub(log *slog.Logger) *Hub {
	if log == nil {
		log = slog.Default()
	}
	return &Hub{clients: map[chan []byte]struct{}{}, log: log}
}

// BroadcastJSON 序列化并向全部订阅者广播（慢消费者丢帧）。
func (h *Hub) BroadcastJSON(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		h.log.Warn("广播消息序列化失败", "err", err)
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.clients {
		select {
		case ch <- data:
		default: // 订阅者积压：丢帧优于阻塞
		}
	}
}

// ClientCount 返回当前订阅数（系统状态展示/测试用）。
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

func (h *Hub) subscribe() chan []byte {
	ch := make(chan []byte, 32)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *Hub) unsubscribe(ch chan []byte) {
	h.mu.Lock()
	delete(h.clients, ch)
	h.mu.Unlock()
	close(ch)
}

// upgrader 允许任意来源：部署形态是内网 + 飞牛桌面 iframe 同源嵌入。
var upgrader = websocket.Upgrader{
	CheckOrigin: func(*http.Request) bool { return true },
}

// Register 挂载 WebSocket 端点（/api/v1/ws/alerts，与 REST 同端口）。
func (h *Hub) Register(v1 *gin.RouterGroup) {
	v1.GET("/ws/alerts", h.serve)
}

// serve 完成协议升级后进入双泵循环：读泵仅用于感知断开，写泵转发广播。
func (h *Hub) serve(c *gin.Context) {
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return // Upgrade 已写过错误响应
	}
	defer func() { _ = conn.Close() }()

	ch := h.subscribe()
	defer h.unsubscribe(ch)

	// 读泵：不消费内容，只为及时感知客户端断开（半开连接兜底）
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn.SetReadLimit(1024)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-done:
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		}
	}
}
