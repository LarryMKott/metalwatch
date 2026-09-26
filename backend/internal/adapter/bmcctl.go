package adapter

import (
	"context"
	"database/sql"
	"time"
)

// BMCCommand 是一条 BMC 管控指令记录（W15）。执行记录即操作审计：
// 下发即落 pending 行，执行终态回写 success/failed；/bmc/audit 直接读本表。
// 密码不出现在本表任何字段——params 只含指令参数，message 是脱敏后的执行输出。
type BMCCommand struct {
	ID            int64
	HostID        int64
	CommandID     string // 幂等键，与 proto BmcCommand.command_id 同值
	CmdType       string // fan | power | identify | policy（policy=交还自动调速的请求别名）
	Target        string
	Params        string // JSON：speed_percent / auto_mode / power_action / duration_sec
	Status        string // pending | success | failed
	Message       string
	ObservedValue float64
	HasObserved   bool // 区分「观测值为 0」与「无观测值」
	Operator      string
	CreatedAt     time.Time
	ExecutedAt    *time.Time
}

// BMCCommandWithHost 是审计列表行：附主机名（JOIN host，主机删除后为空串）。
type BMCCommandWithHost struct {
	BMCCommand
	Hostname string
}

// BMCCommandRepo 是 BMC 管控指令仓储。
type BMCCommandRepo struct{ s *Store }

// BMCCommands 是 BMC 管控指令仓储入口。
func (s *Store) BMCCommands() *BMCCommandRepo { return &BMCCommandRepo{s: s} }

// Create 插入一条 pending 记录，返回行 ID。CreatedAt 必填（服务层取现在）。
func (r *BMCCommandRepo) Create(ctx context.Context, c *BMCCommand) (int64, error) {
	res, err := r.s.ExecContext(ctx, r.s.Rebind(
		`INSERT INTO bmc_command
		   (host_id, command_id, cmd_type, target, params, status, operator, created_at)
		 VALUES (?, ?, ?, ?, ?, 'pending', ?, ?)`),
		c.HostID, c.CommandID, c.CmdType, c.Target, c.Params, c.Operator, formatTime(c.CreatedAt))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Finish 回写执行终态。commandID 不存在时返回 ErrNotFound（幂等键防碰撞兜底）。
func (r *BMCCommandRepo) Finish(ctx context.Context, commandID, status, message string,
	observed *float64, executedAt time.Time) error {
	res, err := r.s.ExecContext(ctx, r.s.Rebind(
		`UPDATE bmc_command
		    SET status = ?, message = ?, observed_value = ?, executed_at = ?
		  WHERE command_id = ?`),
		status, message, observed, formatTime(executedAt), commandID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// List 按时间倒序取最近 limit 条审计（附主机名）。
func (r *BMCCommandRepo) List(ctx context.Context, limit int) ([]BMCCommandWithHost, error) {
	rows, err := r.s.QueryContext(ctx, r.s.Rebind(
		`SELECT b.id, b.host_id, b.command_id, b.cmd_type, b.target, b.params,
		        b.status, b.message, b.observed_value, b.operator, b.created_at, b.executed_at,
		        COALESCE(h.hostname, '')
		 FROM bmc_command b
		 LEFT JOIN host h ON h.id = b.host_id
		 ORDER BY b.id DESC LIMIT ?`), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []BMCCommandWithHost{}
	for rows.Next() {
		var c BMCCommandWithHost
		var observed sql.NullFloat64
		var created, executed sql.NullString
		if err := rows.Scan(&c.ID, &c.HostID, &c.CommandID, &c.CmdType, &c.Target, &c.Params,
			&c.Status, &c.Message, &observed, &c.Operator, &created, &executed,
			&c.Hostname); err != nil {
			return nil, err
		}
		c.ObservedValue = observed.Float64
		c.HasObserved = observed.Valid
		c.CreatedAt = parseTime(created.String)
		c.ExecutedAt = parseTimePtr(executed)
		out = append(out, c)
	}
	return out, rows.Err()
}
