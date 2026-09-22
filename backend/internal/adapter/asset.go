package adapter

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// HardwareSnapshot 是一行资产快照（docs/03 §1.3 hardware_snapshot）。
// fingerprint 为规范化部件清单的内容哈希；payload 仅在指纹变化时留存（省空间）。
type HardwareSnapshot struct {
	ID          int64
	HostID      int64
	CollectMode string // agent | ipmi
	CapturedAt  time.Time
	Fingerprint string
	Payload     *string
}

// ChangeEvent 是一行硬件变更事件（docs/03 §1.3 change_event）。
type ChangeEvent struct {
	ID           int64
	HostID       int64
	SnapshotID   *int64
	Category     string
	Slot         string
	ChangeType   string // added | removed | modified
	Field        string // modified 时的字段名（serial/capacity_bytes/firmware）
	OldValue     *string
	NewValue     *string
	DetectedAt   time.Time
	ConfirmCount int
	AlertEventID *int64
	// Source 冗余 collect_mode，供前端「来源」列展示；不落库
	Source string
}

// AssetRepo 聚合资产快照与变更事件两组仓储。
type AssetRepo struct{ s *Store }

// Assets 是快照与变更仓储入口。
func (s *Store) Assets() *AssetRepo { return &AssetRepo{s: s} }

// LatestFingerprint 返回该主机该采集模式最近一次快照指纹；无快照返回 false。
func (r *AssetRepo) LatestFingerprint(ctx context.Context, hostID int64, mode string) (string, bool, error) {
	var fp string
	err := r.s.QueryRowContext(ctx, r.s.Rebind(
		`SELECT fingerprint FROM hardware_snapshot
		 WHERE host_id = ? AND collect_mode = ? ORDER BY captured_at DESC, id DESC LIMIT 1`),
		hostID, mode).Scan(&fp)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return fp, true, nil
}

// CreateSnapshot 落一条快照。payload 仅在指纹变化时非 nil。
func (r *AssetRepo) CreateSnapshot(ctx context.Context, s *HardwareSnapshot) (int64, error) {
	res, err := r.s.ExecContext(ctx, r.s.Rebind(
		`INSERT INTO hardware_snapshot (host_id, collect_mode, captured_at, fingerprint, payload)
		 VALUES (?, ?, ?, ?, ?)`),
		s.HostID, s.CollectMode, formatTime(s.CapturedAt), s.Fingerprint, s.Payload)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	s.ID = id
	return id, err
}

// CreateChange 落一条变更事件。
func (r *AssetRepo) CreateChange(ctx context.Context, e *ChangeEvent) (int64, error) {
	res, err := r.s.ExecContext(ctx, r.s.Rebind(
		`INSERT INTO change_event
		   (host_id, snapshot_id, category, slot, change_type, field, old_value, new_value,
		    detected_at, confirm_count, alert_event_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		e.HostID, e.SnapshotID, e.Category, e.Slot, e.ChangeType, e.Field,
		e.OldValue, e.NewValue, formatTime(e.DetectedAt), e.ConfirmCount, e.AlertEventID)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	e.ID = id
	return id, err
}

// HasChangeSince 判断该部件自 since 起是否已产生过指定类型的变更事件
// （D11 移除去抖：第二次缺席发事件，之后不再重复）。
func (r *AssetRepo) HasChangeSince(ctx context.Context, hostID int64,
	category, slot, changeType string, since time.Time) (bool, error) {
	var n int
	err := r.s.QueryRowContext(ctx, r.s.Rebind(
		`SELECT COUNT(*) FROM change_event
		 WHERE host_id = ? AND category = ? AND slot = ? AND change_type = ? AND detected_at >= ?`),
		hostID, category, slot, changeType, formatTime(since)).Scan(&n)
	return n > 0, err
}

// ListChanges 返回一台主机的变更事件（detected_at 倒序）。
func (r *AssetRepo) ListChanges(ctx context.Context, hostID int64, limit int) ([]ChangeEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := r.s.QueryContext(ctx, r.s.Rebind(
		`SELECT id, host_id, snapshot_id, category, slot, change_type, field,
		        COALESCE(old_value,''), COALESCE(new_value,''), detected_at, confirm_count, alert_event_id
		 FROM change_event WHERE host_id = ?
		 ORDER BY detected_at DESC, id DESC LIMIT ?`), hostID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ChangeEvent
	for rows.Next() {
		var e ChangeEvent
		var detected string
		var snapshotID, alertID *int64
		if err := rows.Scan(&e.ID, &e.HostID, &snapshotID, &e.Category, &e.Slot,
			&e.ChangeType, &e.Field, &e.OldValue, &e.NewValue, &detected,
			&e.ConfirmCount, &alertID); err != nil {
			return nil, err
		}
		e.DetectedAt = parseTime(detected)
		e.SnapshotID, e.AlertEventID = snapshotID, alertID
		out = append(out, e)
	}
	return out, rows.Err()
}
