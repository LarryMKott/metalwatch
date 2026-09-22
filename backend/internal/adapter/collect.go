package adapter

import (
	"context"
	"time"
)

// CollectRun 是一行采集执行记录（docs/03 §1.3 collect_run）。
// host_id 故意不加外键：高频写入，避免锁竞争（docs/03 §1.4）。
type CollectRun struct {
	ID         int64
	HostID     int64
	Mode       string // agent | ipmi
	State      string // ok | fail | timeout | skip | degraded
	ErrorCode  *string
	ErrorMsg   *string
	DurationMS int64
	StartedAt  time.Time
	FinishedAt time.Time
}

// CollectRunFilter 是采集记录查询条件。
type CollectRunFilter struct {
	HostID int64
	State  string
	Mode   string
	Limit  int
	Offset int
}

// CollectRunRepo 写入与查询采集执行记录；保留期 7 天（docs/03 §1.5）。
type CollectRunRepo struct{ s *Store }

// CollectRuns 是采集记录仓储入口。
func (s *Store) CollectRuns() *CollectRunRepo { return &CollectRunRepo{s: s} }

// Create 落一条执行记录。
func (r *CollectRunRepo) Create(ctx context.Context, run *CollectRun) (int64, error) {
	res, err := r.s.ExecContext(ctx, r.s.Rebind(
		`INSERT INTO collect_run
		   (host_id, mode, state, error_code, error_msg, duration_ms, started_at, finished_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`),
		run.HostID, run.Mode, run.State, run.ErrorCode, run.ErrorMsg,
		run.DurationMS, formatTime(run.StartedAt), formatTime(run.FinishedAt))
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	run.ID = id
	return id, err
}

// List 按条件查询（started_at 倒序）。
func (r *CollectRunRepo) List(ctx context.Context, f CollectRunFilter) ([]CollectRun, int, error) {
	where := " WHERE 1=1"
	var args []any
	if f.HostID > 0 {
		where += " AND host_id = ?"
		args = append(args, f.HostID)
	}
	if f.State != "" {
		where += " AND state = ?"
		args = append(args, f.State)
	}
	if f.Mode != "" {
		where += " AND mode = ?"
		args = append(args, f.Mode)
	}

	var total int
	if err := r.s.QueryRowContext(ctx, r.s.Rebind(
		"SELECT COUNT(*) FROM collect_run"+where), args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT id, host_id, mode, state, error_code, error_msg, duration_ms, started_at, finished_at
	      FROM collect_run` + where + " ORDER BY started_at DESC, id DESC LIMIT ? OFFSET ?"
	args = append(args, limit, f.Offset)
	rows, err := r.s.QueryContext(ctx, r.s.Rebind(q), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []CollectRun
	for rows.Next() {
		var run CollectRun
		var started, finished string
		if err := rows.Scan(&run.ID, &run.HostID, &run.Mode, &run.State,
			&run.ErrorCode, &run.ErrorMsg, &run.DurationMS, &started, &finished); err != nil {
			return nil, 0, err
		}
		run.StartedAt = parseTime(started)
		run.FinishedAt = parseTime(finished)
		out = append(out, run)
	}
	return out, total, rows.Err()
}

// PruneBefore 删除早于 cutoff 的记录（M2：7 天保留，分批清理），返回删除数。
func (r *CollectRunRepo) PruneBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := r.s.ExecContext(ctx, r.s.Rebind(
		`DELETE FROM collect_run WHERE started_at < ?`), formatTime(cutoff))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
