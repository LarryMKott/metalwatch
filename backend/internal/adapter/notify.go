package adapter

import (
	"context"
	"time"
)

// NotifyChannel 是一行通知渠道配置（docs/03 §1.3）。
// config 为 JSON：webhook 类型含 {"url":"https://…","secret":"HMAC 密钥"}。
type NotifyChannel struct {
	ID          int64
	Name        string
	Type        string // webhook | email | syslog
	Config      string
	MinSeverity string // critical | major | info
	Enabled     bool
	LastResult  string // 最近一次投递结果（回写用；List 时 COALESCE 为空串）
}

// NotifyChannelRepo 读取通知渠道并回写最近一次投递结果。
// email / syslog 类型按计划属后续工作流；当前只暴露 webhook 的通用读写。
type NotifyChannelRepo struct{ s *Store }

// NotifyChannels 是通知渠道仓储入口。
func (s *Store) NotifyChannels() *NotifyChannelRepo { return &NotifyChannelRepo{s: s} }

// ListWebhook 返回启用的 webhook 渠道。
func (r *NotifyChannelRepo) ListWebhook(ctx context.Context) ([]NotifyChannel, error) {
	rows, err := r.s.QueryContext(ctx, r.s.Rebind(
		`SELECT id, name, type, config, min_severity, enabled, COALESCE(last_result, '')
		 FROM notify_channel WHERE type = 'webhook' AND enabled = 1 ORDER BY id`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []NotifyChannel
	for rows.Next() {
		var c NotifyChannel
		var enabled int
		if err := rows.Scan(&c.ID, &c.Name, &c.Type, &c.Config, &c.MinSeverity, &enabled, &c.LastResult); err != nil {
			return nil, err
		}
		c.Enabled = enabled == 1
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdateResult 回写渠道最近一次投递结果与时间（WebUI 渠道列表展示用）。
func (r *NotifyChannelRepo) UpdateResult(ctx context.Context, id int64, result string, at time.Time) error {
	_, err := r.s.ExecContext(ctx, r.s.Rebind(
		`UPDATE notify_channel SET last_result = ?, last_tried_at = ? WHERE id = ?`),
		result, formatTime(at), id)
	return err
}

// SeedWebhook 幂等写入一个 webhook 渠道（测试与初始化用；UNIQUE(name) 冲突时跳过）。
func (r *NotifyChannelRepo) SeedWebhook(ctx context.Context, name, config, minSeverity string) error {
	_, err := r.s.ExecContext(ctx, r.s.Rebind(
		`INSERT INTO notify_channel (name, type, config, min_severity, enabled, created_at)
		 VALUES (?, 'webhook', ?, ?, 1, ?) ON CONFLICT(name) DO NOTHING`),
		name, config, minSeverity, formatTime(time.Now().UTC()))
	return err
}
