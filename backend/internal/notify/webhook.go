// Package notify 是出站告警通知（docs/04 §5）：Webhook 渠道的投递、签名与重试。
// 邮件 / syslog 渠道属后续工作流，复用同一 EventPayload 契约。
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/LarryMKott/metalwatch/internal/adapter"
)

// 默认重试节奏（M6 验收 4：失败按 3 次指数退避 1s/5s/25s）。
var defaultRetries = []time.Duration{0, time.Second, 5 * time.Second, 25 * time.Second}

// EventPayload 是出站事件的统一载荷（docs/04 §5，WebSocket 推送同构）。
type EventPayload struct {
	Event    string    `json:"event"`
	Severity string    `json:"severity"`
	Alert    EventInfo `json:"alert"`
	Host     HostInfo  `json:"host"`
	TS       time.Time `json:"ts"`
}

// EventInfo 是载荷中的告警摘要。
type EventInfo struct {
	ID          int64    `json:"id"`
	Category    string   `json:"category"`
	Metric      string   `json:"metric,omitempty"`
	Object      string   `json:"object,omitempty"`
	Value       *float64 `json:"value,omitempty"`
	Threshold   *float64 `json:"threshold,omitempty"`
	Title       string   `json:"title"`
	FirstSeenAt string   `json:"first_seen_at"`
	ActiveKey   string   `json:"active_key,omitempty"`
	NotifyState string   `json:"notify_state,omitempty"`
}

// HostInfo 是载荷中的主机摘要。
type HostInfo struct {
	ID         int64  `json:"id"`
	Hostname   string `json:"hostname,omitempty"`
	PrimaryIP  string `json:"primary_ip,omitempty"`
	SN         string `json:"sn,omitempty"`
	Site       string `json:"site,omitempty"`
	Rack       string `json:"rack,omitempty"`
	GeoCountry string `json:"geo_country,omitempty"`
}

// BuildPayload 把存储层事件与主机组装为出站载荷。
func BuildPayload(event string, e *adapter.AlertEvent, host *adapter.Host, ts time.Time) *EventPayload {
	p := &EventPayload{
		Event:    event,
		Severity: e.Severity,
		TS:       ts.UTC(),
		Alert: EventInfo{
			ID: e.ID, Category: e.Category, Title: e.Title,
			Value: e.Value, Threshold: e.Threshold,
			FirstSeenAt: e.FirstSeenAt.UTC().Format(time.RFC3339),
		},
	}
	if e.Metric != nil {
		p.Alert.Metric = *e.Metric
	}
	if e.ObjectName != nil {
		p.Alert.Object = *e.ObjectName
	}
	if e.ActiveKey != nil {
		p.Alert.ActiveKey = *e.ActiveKey
	}
	if host != nil {
		p.Host = HostInfo{
			ID: host.ID, Hostname: host.Hostname, PrimaryIP: host.PrimaryIP,
			Site: deref(host.Site), Rack: deref(host.Rack),
			SN: deref(host.SN), GeoCountry: deref(host.GeoCountry),
		}
	}
	return p
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// webhookConfig 是 notify_channel.config 的 webhook 形态。
type webhookConfig struct {
	URL    string `json:"url"`
	Secret string `json:"secret"` // HMAC-SHA256 签名密钥；空则不加签名头
}

// Notifier 按 webhook 渠道分发事件。实现 service.Notifier 接口（消费方定义于 service）。
type Notifier struct {
	repo    *adapter.NotifyChannelRepo
	alerts  *adapter.AlertEventRepo
	client  *http.Client
	retries []time.Duration
	log     *slog.Logger
}

// SetRetries 覆盖重试节奏（测试用；生产保持默认 0s/1s/5s/25s）。
func (n *Notifier) SetRetries(retries []time.Duration) { n.retries = retries }

// New 构造通知器。alerts 可为 nil（nil 时不同步 notify_state）。
func New(repo *adapter.NotifyChannelRepo, alerts *adapter.AlertEventRepo, log *slog.Logger) *Notifier {
	if log == nil {
		log = slog.Default()
	}
	return &Notifier{
		repo: repo, alerts: alerts,
		client:  &http.Client{Timeout: 10 * time.Second},
		retries: defaultRetries,
		log:     log,
	}
}

// AlertEvent 向全部启用的 webhook 渠道投递事件。
// 渠道按 min_severity 过滤；低于门槛记 skipped。投递结果回写渠道与事件。
func (n *Notifier) AlertEvent(ctx context.Context, p *EventPayload) {
	channels, err := n.repo.ListWebhook(ctx)
	if err != nil {
		n.log.Error("读取通知渠道失败", "err", err)
		return
	}
	for _, ch := range channels {
		if !severityAtLeast(p.Severity, ch.MinSeverity) {
			n.markChannel(ctx, &ch, "skipped: severity below "+ch.MinSeverity)
			n.markEvent(ctx, p, "skipped")
			continue
		}
		if err := n.deliver(ctx, ch, p); err != nil {
			n.log.Warn("webhook 投递失败", "channel", ch.Name, "event", p.Event, "err", err)
			n.markChannel(ctx, &ch, "failed: "+err.Error())
			n.markEvent(ctx, p, "failed")
			continue
		}
		n.markChannel(ctx, &ch, "ok")
		n.markEvent(ctx, p, "sent")
	}
}

// deliver 单渠道投递：POST JSON，非 2xx 或传输错误按 retries 节奏重试。
func (n *Notifier) deliver(ctx context.Context, ch adapter.NotifyChannel, p *EventPayload) error {
	var cfg webhookConfig
	if err := json.Unmarshal([]byte(ch.Config), &cfg); err != nil {
		return fmt.Errorf("渠道配置解析失败: %w", err)
	}
	if cfg.URL == "" {
		return fmt.Errorf("渠道未配置 url")
	}
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}

	var lastErr error
	for attempt, wait := range n.retries {
		if wait > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}
		if ok, err := n.post(ctx, cfg, body); ok {
			return nil
		} else {
			lastErr = err
		}
		if attempt == len(n.retries)-1 {
			break
		}
	}
	return lastErr
}

// post 发送一次请求，2xx 返回 true。
func (n *Notifier) post(ctx context.Context, cfg webhookConfig, body []byte) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-MetalWatch-Event", eventOf(body))
	if cfg.Secret != "" {
		req.Header.Set("X-MetalWatch-Signature", sign(cfg.Secret, body))
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, nil
	}
	return false, fmt.Errorf("HTTP %d", resp.StatusCode)
}

// eventOf 从已序列化的载荷取 event 字段（避免二次序列化）。
func eventOf(body []byte) string {
	var probe struct {
		Event string `json:"event"`
	}
	_ = json.Unmarshal(body, &probe)
	return probe.Event
}

// sign 计算 HMAC-SHA256 签名（hex）。
func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// severityAtLeast 判断 severity 是否达到门槛（info < major < critical）。
func severityAtLeast(severity, minSeverity string) bool {
	rank := map[string]int{"info": 0, "major": 1, "critical": 2}
	// 展示层的 minor 级别按 info 处理
	if severity == "minor" {
		severity = "info"
	}
	return rank[severity] >= rank[minSeverity]
}

func (n *Notifier) markChannel(ctx context.Context, ch *adapter.NotifyChannel, result string) {
	if err := n.repo.UpdateResult(ctx, ch.ID, truncate(result, 200), time.Now().UTC()); err != nil {
		n.log.Warn("渠道结果回写失败", "channel", ch.Name, "err", err)
	}
}

func (n *Notifier) markEvent(ctx context.Context, p *EventPayload, state string) {
	if n.alerts == nil || p.Alert.ID <= 0 {
		return
	}
	if err := n.alerts.MarkNotifyState(ctx, p.Alert.ID, state, time.Now().UTC()); err != nil {
		n.log.Warn("事件投递状态回写失败", "id", p.Alert.ID, "err", err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
