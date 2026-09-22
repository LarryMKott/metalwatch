package adapter

import (
	"context"
	"fmt"
	"time"
)

// ThresholdTemplate 是一行阈值模板（docs/03 §1.3 告警域）。
// warn_value / crit_value 允许只配其一；severity 由服务端推导：
// crit → critical，warn → major（对外展示层可再映射为 minor）。
type ThresholdTemplate struct {
	ID          int64
	Name        string
	Metric      string
	Category    string // sensor | smart | raid | psu | fan | agent | asset
	Op          string // gt | lt | eq | ne
	WarnValue   *float64
	CritValue   *float64
	DurationSec int
	Enabled     bool
	Builtin     bool
}

// ThresholdOverride 是单主机覆盖：优先于同名指标的全局模板（M6 验收 2）。
type ThresholdOverride struct {
	ID          int64
	HostID      int64
	Hostname    string // JOIN host 得到，便于规则直接按主机名匹配
	Metric      string
	Op          string
	WarnValue   *float64
	CritValue   *float64
	DurationSec int // 模板值或显式覆盖值归一后的秒数
	Enabled     bool
}

// ThresholdRepo 读写阈值模板与单主机覆盖。
type ThresholdRepo struct{ s *Store }

// Thresholds 是告警阈值仓储入口。
func (s *Store) Thresholds() *ThresholdRepo { return &ThresholdRepo{s: s} }

// ListTemplates 返回阈值模板，enabledOnly 时只取启用行。
func (r *ThresholdRepo) ListTemplates(ctx context.Context, enabledOnly bool) ([]ThresholdTemplate, error) {
	q := `SELECT id, name, metric, category, op, warn_value, crit_value, duration_sec, enabled, builtin
	      FROM threshold_template`
	if enabledOnly {
		q += " WHERE enabled = 1"
	}
	q += " ORDER BY id"
	rows, err := r.s.QueryContext(ctx, r.s.Rebind(q))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ThresholdTemplate
	for rows.Next() {
		var t ThresholdTemplate
		var enabled, builtin int
		if err := rows.Scan(&t.ID, &t.Name, &t.Metric, &t.Category, &t.Op,
			&t.WarnValue, &t.CritValue, &t.DurationSec, &enabled, &builtin); err != nil {
			return nil, err
		}
		t.Enabled = enabled == 1
		t.Builtin = builtin == 1
		out = append(out, t)
	}
	return out, rows.Err()
}

// SeedBuiltin 幂等写入内置模板（UNIQUE(metric, name) 冲突时跳过）。
func (r *ThresholdRepo) SeedBuiltin(ctx context.Context, ts []ThresholdTemplate) error {
	for _, t := range ts {
		_, err := r.s.ExecContext(ctx, r.s.Rebind(
			`INSERT OR IGNORE INTO threshold_template
			   (name, metric, category, op, warn_value, crit_value, duration_sec, enabled, builtin)
			 VALUES (?, ?, ?, ?, ?, ?, ?, 1, 1)`),
			t.Name, t.Metric, t.Category, t.Op, t.WarnValue, t.CritValue, t.DurationSec)
		if err != nil {
			return err
		}
	}
	return nil
}

// ListOverrides 返回全部启用的单主机覆盖（带主机名，供规则按主机名匹配）。
func (r *ThresholdRepo) ListOverrides(ctx context.Context) ([]ThresholdOverride, error) {
	rows, err := r.s.QueryContext(ctx, r.s.Rebind(
		`SELECT o.id, o.host_id, h.hostname, o.metric, o.op,
		        o.warn_value, o.crit_value, o.duration_sec, o.enabled
		 FROM threshold_override o
		 JOIN host h ON h.id = o.host_id
		 WHERE o.enabled = 1
		 ORDER BY o.id`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ThresholdOverride
	for rows.Next() {
		var o ThresholdOverride
		var enabled int
		var dur *int
		if err := rows.Scan(&o.ID, &o.HostID, &o.Hostname, &o.Metric, &o.Op,
			&o.WarnValue, &o.CritValue, &dur, &enabled); err != nil {
			return nil, err
		}
		o.Enabled = enabled == 1
		if dur != nil {
			o.DurationSec = *dur
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// AlertFilter 是告警事件查询条件。
type AlertFilter struct {
	States   []string // 空表示全部（firing/resolved/silenced/suppressed）
	Acked    *bool    // 仅 firing 有效：true=已确认，false=未确认，nil=不限
	HostID   int64
	Severity string
	Category string
	Limit    int
	Offset   int
}

// AlertEvent 是一行告警事件。
type AlertEvent struct {
	ID          int64
	HostID      *int64
	Severity    string
	Category    string
	Metric      *string
	ObjectName  *string
	Value       *float64
	Threshold   *float64
	Title       string
	Detail      *string
	State       string
	ActiveKey   *string
	FirstSeenAt time.Time
	LastSeenAt  time.Time
	ResolvedAt  *time.Time
	AckBy       *string
	AckAt       *time.Time
	NotifyState string // pending/sent/failed/skipped
	Hostname    string // JOIN host 得到；主机已删除时为空
}

// AlertEventRepo 写入与查询告警事件。去重依赖 active_key 列：
// firing 时写入，resolved/silenced 时置 NULL（跨方言可移植，见 0001_init.sql 注释）。
type AlertEventRepo struct{ s *Store }

// Alerts 是告警事件仓储入口。
func (s *Store) Alerts() *AlertEventRepo { return &AlertEventRepo{s: s} }

// UpsertFiring 记录一次触发：已有同 active_key 的 firing 行则刷新，
// 否则新建（并发插入撞唯一索引时退化为刷新）。
// 返回 created=true 表示本次是新触发（调用方据此决定是否对外通知）；
// id 为事件行 ID（新建时为该行，刷新时为既有行，change_event 回链用）。
func (r *AlertEventRepo) UpsertFiring(ctx context.Context, e *AlertEvent, activeKey, at string) (bool, int64, error) {
	res, err := r.s.ExecContext(ctx, r.s.Rebind(
		`UPDATE alert_event
		 SET last_seen_at = ?, value = ?, severity = ?, title = ?
		 WHERE active_key = ? AND state = 'firing'`),
		at, e.Value, e.Severity, e.Title, activeKey)
	if err != nil {
		return false, 0, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		id, _ := res.LastInsertId() // UPDATE 语义下 LastInsertId 不可靠，回查
		var existing int64
		qerr := r.s.QueryRowContext(ctx, r.s.Rebind(
			`SELECT id FROM alert_event WHERE active_key = ? AND state = 'firing'`), activeKey).Scan(&existing)
		if qerr == nil {
			id = existing
		}
		return false, id, nil
	}
	res, err = r.s.ExecContext(ctx, r.s.Rebind(
		`INSERT INTO alert_event
		   (host_id, severity, category, metric, object_name, value, threshold,
		    title, detail, state, active_key, first_seen_at, last_seen_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'firing', ?, ?, ?)`),
		e.HostID, e.Severity, e.Category, e.Metric, e.ObjectName, e.Value, e.Threshold,
		e.Title, e.Detail, activeKey, at, at)
	if err != nil && IsUniqueViolation(err) {
		_, err = r.s.ExecContext(ctx, r.s.Rebind(
			`UPDATE alert_event SET last_seen_at = ?, value = ?
			 WHERE active_key = ? AND state = 'firing'`),
			at, e.Value, activeKey)
		var existing int64
		_ = r.s.QueryRowContext(ctx, r.s.Rebind(
			`SELECT id FROM alert_event WHERE active_key = ? AND state = 'firing'`), activeKey).Scan(&existing)
		return false, existing, err
	}
	if err != nil {
		return false, 0, err
	}
	id, _ := res.LastInsertId()
	return true, id, nil
}

// Resolve 把 active_key 对应的 firing 事件置为已恢复（active_key 置 NULL，保留故障史）。
// 返回 resolved=true 表示确有 firing 行被解除（调用方据此决定是否对外通知恢复）。
func (r *AlertEventRepo) Resolve(ctx context.Context, activeKey, at string) (bool, error) {
	res, err := r.s.ExecContext(ctx, r.s.Rebind(
		`UPDATE alert_event
		 SET state = 'resolved', resolved_at = ?, last_seen_at = ?, active_key = NULL
		 WHERE active_key = ? AND state = 'firing'`), at, at, activeKey)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Touch 刷新 firing 事件的最近触发时间与当前值（抑制窗口内的重复触发不产生新行）。
func (r *AlertEventRepo) Touch(ctx context.Context, activeKey, at string, value *float64) error {
	_, err := r.s.ExecContext(ctx, r.s.Rebind(
		`UPDATE alert_event SET last_seen_at = ?, value = ?
		 WHERE active_key = ? AND state = 'firing'`), at, value, activeKey)
	return err
}

// InsertSilenced 落一条维护窗口内的事件（active_key 置 NULL，不占用去重键）。
func (r *AlertEventRepo) InsertSilenced(ctx context.Context, e *AlertEvent, at string) error {
	_, err := r.s.ExecContext(ctx, r.s.Rebind(
		`INSERT INTO alert_event
		   (host_id, severity, category, metric, object_name, value, threshold,
		    title, detail, state, active_key, first_seen_at, last_seen_at, notify_state)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'silenced', NULL, ?, ?, 'skipped')`),
		e.HostID, e.Severity, e.Category, e.Metric, e.ObjectName, e.Value, e.Threshold,
		e.Title, e.Detail, at, at)
	return err
}

// MarkNotifyState 回写事件的投递状态（sent/failed/skipped，docs/03 §1.3）。
func (r *AlertEventRepo) MarkNotifyState(ctx context.Context, id int64, state string, at time.Time) error {
	_, err := r.s.ExecContext(ctx, r.s.Rebind(
		`UPDATE alert_event SET notify_state = ?, notified_at = ? WHERE id = ?`),
		state, formatTime(at), id)
	return err
}

// Ack 确认一条事件；返回事件是否存在。
func (r *AlertEventRepo) Ack(ctx context.Context, id int64, by, at string) (bool, error) {
	res, err := r.s.ExecContext(ctx, r.s.Rebind(
		`UPDATE alert_event SET ack_by = ?, ack_at = ? WHERE id = ? AND ack_at IS NULL`),
		by, at, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// List 按条件查询告警事件（带主机名；主机删除后 host_name 为空）。
func (r *AlertEventRepo) List(ctx context.Context, f AlertFilter) ([]AlertEvent, int, error) {
	where := " WHERE 1=1"
	var args []any
	if len(f.States) > 0 {
		where += " AND a.state IN (" + placeholders(len(f.States)) + ")"
		for _, s := range f.States {
			args = append(args, s)
		}
	}
	if f.Acked != nil {
		if *f.Acked {
			where += " AND a.ack_at IS NOT NULL"
		} else {
			where += " AND a.ack_at IS NULL"
		}
	}
	if f.HostID > 0 {
		where += " AND a.host_id = ?"
		args = append(args, f.HostID)
	}
	if f.Severity != "" {
		where += " AND a.severity = ?"
		args = append(args, f.Severity)
	}
	if f.Category != "" {
		where += " AND a.category = ?"
		args = append(args, f.Category)
	}

	var total int
	if err := r.s.QueryRowContext(ctx, r.s.Rebind(
		"SELECT COUNT(*) FROM alert_event a"+where), args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	q := `SELECT a.id, a.host_id, a.severity, a.category, a.metric, a.object_name,
	             a.value, a.threshold, a.title, a.detail, a.state, a.active_key,
	             a.first_seen_at, a.last_seen_at, a.resolved_at, a.ack_by, a.ack_at,
	             a.notify_state, h.hostname
	      FROM alert_event a
	      LEFT JOIN host h ON h.id = a.host_id` +
		where + " ORDER BY a.first_seen_at DESC, a.id DESC LIMIT ? OFFSET ?"
	args = append(args, limit, f.Offset)
	rows, err := r.s.QueryContext(ctx, r.s.Rebind(q), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []AlertEvent
	for rows.Next() {
		var e AlertEvent
		var first, last string
		var resolved, ackAt *string
		if err := rows.Scan(&e.ID, &e.HostID, &e.Severity, &e.Category, &e.Metric,
			&e.ObjectName, &e.Value, &e.Threshold, &e.Title, &e.Detail, &e.State,
			&e.ActiveKey, &first, &last, &resolved, &e.AckBy, &ackAt, &e.NotifyState,
			&e.Hostname); err != nil {
			return nil, 0, err
		}
		if e.FirstSeenAt = parseTime(first); e.FirstSeenAt.IsZero() {
			return nil, 0, fmt.Errorf("alert_event.first_seen_at 时间格式非法: %q", first)
		}
		if e.LastSeenAt = parseTime(last); e.LastSeenAt.IsZero() {
			return nil, 0, fmt.Errorf("alert_event.last_seen_at 时间格式非法: %q", last)
		}
		if resolved != nil {
			t := parseTime(*resolved)
			if t.IsZero() {
				return nil, 0, fmt.Errorf("alert_event.resolved_at 时间格式非法: %q", *resolved)
			}
			e.ResolvedAt = &t
		}
		if ackAt != nil {
			t := parseTime(*ackAt)
			if t.IsZero() {
				return nil, 0, fmt.Errorf("alert_event.ack_at 时间格式非法: %q", *ackAt)
			}
			e.AckAt = &t
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// CountFiring 返回当前 firing 的事件数（大盘「活跃告警」）。
func (r *AlertEventRepo) CountFiring(ctx context.Context) (int, error) {
	var n int
	err := r.s.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM alert_event WHERE state = 'firing'`).Scan(&n)
	return n, err
}

// CountBetween 统计 [from,to] 内首次触发的事件数（大盘告警趋势）。
func (r *AlertEventRepo) CountBetween(ctx context.Context, from, to time.Time) (int, error) {
	var n int
	err := r.s.QueryRowContext(ctx, r.s.Rebind(
		`SELECT COUNT(*) FROM alert_event WHERE first_seen_at >= ? AND first_seen_at < ?`),
		formatTime(from), formatTime(to)).Scan(&n)
	return n, err
}

// FirstSeenBetween 返回区间内首次触发事件的时刻（大盘按桶聚合告警趋势用）。
func (r *AlertEventRepo) FirstSeenBetween(ctx context.Context, from, to time.Time) ([]time.Time, error) {
	rows, err := r.s.QueryContext(ctx, r.s.Rebind(
		`SELECT first_seen_at FROM alert_event WHERE first_seen_at >= ? AND first_seen_at < ?`),
		formatTime(from), formatTime(to))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []time.Time
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		if t := parseTime(s); !t.IsZero() {
			out = append(out, t)
		}
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	b := make([]byte, 0, n*2)
	for i := 0; i < n; i++ {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '?')
	}
	return string(b)
}
