package adapter

import (
	"context"
	"time"
)

// ============ 开放接口令牌（只读） ============

// APIToken 是一行开放接口令牌（docs/03 §1.3 api_token）。
// 库里只存 SHA-256 摘要，明文仅在签发时返回一次。
type APIToken struct {
	ID         int64
	Name       string
	Scopes     string // 逗号分隔，如 "asset:read,metric:read"
	State      string // active | revoked
	ExpireAt   *time.Time
	LastUsedAt *time.Time
	CreatedBy  string
	CreatedAt  time.Time
}

// APITokenRepo 是开放接口令牌仓储。
type APITokenRepo struct{ s *Store }

// APITokens 是开放令牌仓储入口。
func (s *Store) APITokens() *APITokenRepo { return &APITokenRepo{s: s} }

const apiTokenCols = `id, name, scopes, state, expire_at, last_used_at,
	COALESCE(created_by,''), created_at`

// Create 写入一枚新令牌（只存摘要），返回新 ID。
func (r *APITokenRepo) Create(ctx context.Context, name, tokenHash, scopes string,
	expireAt *time.Time, createdBy string) (int64, error) {
	var exp any
	if expireAt != nil {
		exp = formatTime(*expireAt)
	}
	// created_at 由应用层写（迁移里不带 DEFAULT，跨方言一致）
	res, err := r.s.ExecContext(ctx, r.s.Rebind(
		`INSERT INTO api_token (name, token_hash, scopes, state, expire_at, created_by, created_at)
		 VALUES (?, ?, ?, 'active', ?, ?, ?)`),
		name, tokenHash, scopes, exp, nullString(createdBy), formatTime(time.Now().UTC()))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetByHash 按 SHA-256 摘要查令牌（鉴权中间件用）。
func (r *APITokenRepo) GetByHash(ctx context.Context, hash string) (*APIToken, error) {
	row := r.s.QueryRowContext(ctx, r.s.Rebind(
		`SELECT `+apiTokenCols+` FROM api_token WHERE token_hash = ?`), hash)
	return scanAPIToken(row)
}

// Get 按 ID 查令牌。
func (r *APITokenRepo) Get(ctx context.Context, id int64) (*APIToken, error) {
	row := r.s.QueryRowContext(ctx, r.s.Rebind(
		`SELECT `+apiTokenCols+` FROM api_token WHERE id = ?`), id)
	return scanAPIToken(row)
}

// List 返回全部令牌（明文不在此列，只有元数据）。
func (r *APITokenRepo) List(ctx context.Context) ([]APIToken, error) {
	rows, err := r.s.QueryContext(ctx, r.s.Rebind(
		`SELECT `+apiTokenCols+` FROM api_token ORDER BY id DESC`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []APIToken
	for rows.Next() {
		t, err := scanAPIToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// Revoke 吊销令牌（软删除：保留历史，鉴权时直接拒绝）。
func (r *APITokenRepo) Revoke(ctx context.Context, id int64) error {
	_, err := r.s.ExecContext(ctx, r.s.Rebind(
		`UPDATE api_token SET state = 'revoked' WHERE id = ?`), id)
	return err
}

// TouchUsed 刷新最近使用时间（不做每次请求都写：只在分钟级变化时写，见调用方）。
func (r *APITokenRepo) TouchUsed(ctx context.Context, id int64, at time.Time) error {
	_, err := r.s.ExecContext(ctx, r.s.Rebind(
		`UPDATE api_token SET last_used_at = ? WHERE id = ?`), formatTime(at), id)
	return err
}

func scanAPIToken(row scannable) (*APIToken, error) {
	var t APIToken
	var exp, used *string
	var created string
	if err := row.Scan(&t.ID, &t.Name, &t.Scopes, &t.State, &exp, &used,
		&t.CreatedBy, &created); err != nil {
		return nil, err
	}
	t.CreatedAt = parseTime(created)
	if exp != nil {
		if v := parseTime(*exp); !v.IsZero() {
			t.ExpireAt = &v
		}
	}
	if used != nil {
		if v := parseTime(*used); !v.IsZero() {
			t.LastUsedAt = &v
		}
	}
	return &t, nil
}

// ============ 审计日志 ============

// AuditLog 是一行审计记录（docs/03 §1.3 audit_log）。
// user_id 故意不加外键：审计是高频写，避免锁竞争（docs/03 §外键策略）。
type AuditLog struct {
	ID         int64
	UserID     *int64
	Username   string
	Action     string // 如 host.create、auth.login、auth.login_failed
	TargetType string
	TargetID   string
	SourceIP   string
	Result     string // ok | denied | fail
	Detail     string // JSON
	CreatedAt  time.Time
}

// AuditRepo 是审计日志仓储（只追加，不提供更新/删除）。
type AuditRepo struct{ s *Store }

// AuditLogs 是审计仓储入口。
func (s *Store) AuditLogs() *AuditRepo { return &AuditRepo{s: s} }

// AuditFilter 是审计查询条件，零值字段表示不过滤。
type AuditFilter struct {
	Username string
	Action   string // 前缀匹配，便于按域查（如 "auth." / "host."）
	Result   string
	From     *time.Time
	To       *time.Time
	Limit    int
	Offset   int
}

// Append 写入一条审计记录。created_at 由应用层写（跨方言一致）。
// 审计写入失败会让调用方降级（只记日志），不会阻断业务。
func (r *AuditRepo) Append(ctx context.Context, a *AuditLog) error {
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	if a.Result == "" {
		a.Result = "ok"
	}
	_, err := r.s.ExecContext(ctx, r.s.Rebind(
		`INSERT INTO audit_log (user_id, username, action, target_type, target_id, source_ip, result, detail, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		a.UserID, nullString(a.Username), a.Action,
		nullString(a.TargetType), nullString(a.TargetID),
		nullString(a.SourceIP), a.Result, nullString(a.Detail),
		formatTime(a.CreatedAt))
	return err
}

// List 按条件倒序返回审计记录。
func (r *AuditRepo) List(ctx context.Context, f AuditFilter) ([]AuditLog, int64, error) {
	q := `SELECT id, user_id, COALESCE(username,''), action, COALESCE(target_type,''),
	        COALESCE(target_id,''), COALESCE(source_ip,''), result, COALESCE(detail,''), created_at
	      FROM audit_log WHERE 1=1`
	args := []any{}
	if f.Username != "" {
		q += ` AND username = ?`
		args = append(args, f.Username)
	}
	if f.Action != "" {
		q += ` AND action LIKE ?`
		args = append(args, f.Action+"%")
	}
	if f.Result != "" {
		q += ` AND result = ?`
		args = append(args, f.Result)
	}
	if f.From != nil {
		q += ` AND created_at >= ?`
		args = append(args, formatTime(*f.From))
	}
	if f.To != nil {
		q += ` AND created_at <= ?`
		args = append(args, formatTime(*f.To))
	}

	var total int64
	row := r.s.QueryRowContext(ctx, r.s.Rebind(
		`SELECT COUNT(*) FROM (`+q+`)`), args...)
	if err := row.Scan(&total); err != nil {
		return nil, 0, err
	}

	q += ` ORDER BY id DESC`
	if f.Limit > 0 {
		q += ` LIMIT ? OFFSET ?`
		args = append(args, f.Limit, f.Offset)
	}

	rows, err := r.s.QueryContext(ctx, r.s.Rebind(q), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []AuditLog
	for rows.Next() {
		var a AuditLog
		var created string
		if err := rows.Scan(&a.ID, &a.UserID, &a.Username, &a.Action, &a.TargetType,
			&a.TargetID, &a.SourceIP, &a.Result, &a.Detail, &created); err != nil {
			return nil, 0, err
		}
		a.CreatedAt = parseTime(created)
		out = append(out, a)
	}
	return out, total, rows.Err()
}
