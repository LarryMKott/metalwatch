package adapter

import (
	"context"
	"time"
)

// Component 是一行统一部件（docs/03 §1.3 host_component）。
// 不做物理删除：removed_at 非空表示「已移除/待确认移除」，硬盘拔了再插能识别为回归。
type Component struct {
	ID            int64
	HostID        int64
	Category      string // cpu/memory/disk/nic/raid_controller/psu/fan/gpu/mainboard/bios/bmc_fw/chassis/other
	Slot          string
	Name          string
	Vendor        string
	Serial        string
	Firmware      string
	CapacityBytes *int64
	MediaType     string
	Health        string
	Extra         string // JSON（非筛选字段）
	FirstSeenAt   time.Time
	LastSeenAt    time.Time
	RemovedAt     *time.Time
}

// ComponentRepo 是部件树仓储。
type ComponentRepo struct{ s *Store }

// Components 是部件仓储入口。
func (s *Store) Components() *ComponentRepo { return &ComponentRepo{s: s} }

const componentCols = `id, host_id, category, slot, name, COALESCE(vendor,''), COALESCE(serial,''),
	COALESCE(firmware,''), capacity_bytes, COALESCE(media_type,''), COALESCE(health,''),
	COALESCE(extra,''), first_seen_at, last_seen_at, removed_at`

// UpsertBatch 批量写入当前快照中的部件：存在则刷新属性与 last_seen（并清除 removed_at——回归），
// 不存在则新建（first_seen 记首次纳管时刻）。
func (r *ComponentRepo) UpsertBatch(ctx context.Context, hostID int64, comps []Component, at time.Time) error {
	ts := formatTime(at)
	for _, cp := range comps {
		_, err := r.s.ExecContext(ctx, r.s.Rebind(
			`INSERT INTO host_component
			   (host_id, category, slot, name, vendor, serial, firmware,
			    capacity_bytes, media_type, health, extra, first_seen_at, last_seen_at, removed_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
			 ON CONFLICT(host_id, category, slot) DO UPDATE SET
			   name = excluded.name,
			   vendor = excluded.vendor,
			   serial = excluded.serial,
			   firmware = excluded.firmware,
			   capacity_bytes = excluded.capacity_bytes,
			   media_type = excluded.media_type,
			   health = excluded.health,
			   extra = excluded.extra,
			   last_seen_at = excluded.last_seen_at,
			   removed_at = NULL`),
			hostID, cp.Category, cp.Slot, cp.Name, cp.Vendor, cp.Serial, cp.Firmware,
			cp.CapacityBytes, cp.MediaType, cp.Health, cp.Extra, ts, ts)
		if err != nil {
			return err
		}
	}
	return nil
}

// ListActive 返回在役部件（removed_at IS NULL），可按 category 过滤。
func (r *ComponentRepo) ListActive(ctx context.Context, hostID int64, category string) ([]Component, error) {
	q := `SELECT ` + componentCols + ` FROM host_component WHERE host_id = ? AND removed_at IS NULL`
	args := []any{hostID}
	if category != "" {
		q += ` AND category = ?`
		args = append(args, category)
	}
	q += ` ORDER BY category, slot, id`
	return r.query(ctx, q, args...)
}

// ListAll 返回该主机的全部部件（**含**已标记移除的），可按 category 过滤。
// diff 需要它来区分「全新部件 added」与「移除后又回归 return」——
// 只查在役部件（ListActive）会把回归件误判成新增。
func (r *ComponentRepo) ListAll(ctx context.Context, hostID int64, category string) ([]Component, error) {
	q := `SELECT ` + componentCols + ` FROM host_component WHERE host_id = ?`
	args := []any{hostID}
	if category != "" {
		q += ` AND category = ?`
		args = append(args, category)
	}
	q += ` ORDER BY category, slot, id`
	return r.query(ctx, q, args...)
}

// ListMissingSince 返回 last_seen 早于 at 的部件（本轮快照缺席）。
// 含已进入待确认态（removed_at 非空）的部件：连续缺席的第二轮确认靠它们驱动。
func (r *ComponentRepo) ListMissingSince(ctx context.Context, hostID int64, at time.Time) ([]Component, error) {
	q := `SELECT ` + componentCols + ` FROM host_component
	      WHERE host_id = ? AND last_seen_at < ? ORDER BY id`
	return r.query(ctx, q, hostID, formatTime(at))
}

// MarkRemoved 把部件标记为移除候选（D11 第一次缺席：只标记不产生事件）。
func (r *ComponentRepo) MarkRemoved(ctx context.Context, id int64, at time.Time) error {
	_, err := r.s.ExecContext(ctx, r.s.Rebind(
		`UPDATE host_component SET removed_at = ? WHERE id = ? AND removed_at IS NULL`),
		formatTime(at), id)
	return err
}

// ClearRemoved 撤销移除标记（缺席一轮后回归：不算变更）。
func (r *ComponentRepo) ClearRemoved(ctx context.Context, id int64, at time.Time) error {
	_, err := r.s.ExecContext(ctx, r.s.Rebind(
		`UPDATE host_component SET removed_at = NULL, last_seen_at = ? WHERE id = ?`),
		formatTime(at), id)
	return err
}

func (r *ComponentRepo) query(ctx context.Context, q string, args ...any) ([]Component, error) {
	rows, err := r.s.QueryContext(ctx, r.s.Rebind(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Component
	for rows.Next() {
		var c Component
		var first, last string
		var removed *string
		if err := rows.Scan(&c.ID, &c.HostID, &c.Category, &c.Slot, &c.Name, &c.Vendor,
			&c.Serial, &c.Firmware, &c.CapacityBytes, &c.MediaType, &c.Health, &c.Extra,
			&first, &last, &removed); err != nil {
			return nil, err
		}
		c.FirstSeenAt = parseTime(first)
		c.LastSeenAt = parseTime(last)
		if removed != nil {
			if t := parseTime(*removed); !t.IsZero() {
				c.RemovedAt = &t
			}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
