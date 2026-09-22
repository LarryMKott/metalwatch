package adapter

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ============ 用户 ============

// AppUser 是一行管理账号（docs/03 §1.3 app_user）。
// PasswordHash 是 Argon2id 的 PHC 串，任何接口都不返回该字段。
type AppUser struct {
	ID           int64
	Username     string
	DisplayName  string
	PasswordHash string
	Role         string // admin | operator | viewer
	State        string // active | disabled
	LastLoginAt  *time.Time
	LastLoginIP  string
	CreatedAt    time.Time
}

// UserRepo 是管理账号仓储。
type UserRepo struct{ s *Store }

// Users 是用户仓储入口。
func (s *Store) Users() *UserRepo { return &UserRepo{s: s} }

const userCols = `id, username, COALESCE(display_name,''), password_hash, role, state,
	last_login_at, COALESCE(last_login_ip,''), created_at`

// Create 新建账号，返回新 ID。用户名重复由调用方按 IsUniqueViolation 判定。
// created_at 由应用层写入 UTC RFC3339（跨方言约定：迁移里不带 DEFAULT）。
func (r *UserRepo) Create(ctx context.Context, u *AppUser) (int64, error) {
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now().UTC()
	}
	if u.Role == "" {
		u.Role = "viewer"
	}
	if u.State == "" {
		u.State = "active"
	}
	res, err := r.s.ExecContext(ctx, r.s.Rebind(
		`INSERT INTO app_user (username, display_name, password_hash, role, state, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`),
		u.Username, nullString(u.DisplayName), u.PasswordHash, u.Role, u.State,
		formatTime(u.CreatedAt))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetByName 按用户名取账号（登录用）。无此用户返回 (nil, nil)。
func (r *UserRepo) GetByName(ctx context.Context, username string) (*AppUser, error) {
	row := r.s.QueryRowContext(ctx, r.s.Rebind(
		`SELECT `+userCols+` FROM app_user WHERE username = ?`), username)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return u, err
}

// Get 按 ID 取账号。不存在返回 (nil, nil)。
func (r *UserRepo) Get(ctx context.Context, id int64) (*AppUser, error) {
	row := r.s.QueryRowContext(ctx, r.s.Rebind(
		`SELECT `+userCols+` FROM app_user WHERE id = ?`), id)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return u, err
}

// List 返回全部账号（WebUI 用户管理）。
func (r *UserRepo) List(ctx context.Context) ([]AppUser, error) {
	rows, err := r.s.QueryContext(ctx, r.s.Rebind(
		`SELECT `+userCols+` FROM app_user ORDER BY id`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AppUser
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// Count 返回账号总数（启动引导：判断是否首次运行）。
func (r *UserRepo) Count(ctx context.Context) (int64, error) {
	var n int64
	row := r.s.QueryRowContext(ctx, `SELECT COUNT(*) FROM app_user`)
	return n, row.Scan(&n)
}

// SetState 启用/停用账号。
func (r *UserRepo) SetState(ctx context.Context, id int64, state string) error {
	_, err := r.s.ExecContext(ctx, r.s.Rebind(
		`UPDATE app_user SET state = ? WHERE id = ?`), state, id)
	return err
}

// SetRole 变更角色。
func (r *UserRepo) SetRole(ctx context.Context, id int64, role string) error {
	_, err := r.s.ExecContext(ctx, r.s.Rebind(
		`UPDATE app_user SET role = ? WHERE id = ?`), role, id)
	return err
}

// SetPassword 更新口令哈希（改密 / 重置）。
func (r *UserRepo) SetPassword(ctx context.Context, id int64, hash string) error {
	_, err := r.s.ExecContext(ctx, r.s.Rebind(
		`UPDATE app_user SET password_hash = ? WHERE id = ?`), hash, id)
	return err
}

// TouchLogin 记录最近一次登录时间与来源 IP。
func (r *UserRepo) TouchLogin(ctx context.Context, id int64, at time.Time, ip string) error {
	_, err := r.s.ExecContext(ctx, r.s.Rebind(
		`UPDATE app_user SET last_login_at = ?, last_login_ip = ? WHERE id = ?`),
		formatTime(at), nullString(ip), id)
	return err
}

// nullString 把空串转成 NULL：可空文本列统一用 NULL 表示「无」，避免空串与空值两种语义。
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

type scannable interface{ Scan(dest ...any) error }

func scanUser(row scannable) (*AppUser, error) {
	var u AppUser
	var last *string
	var created string
	if err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.PasswordHash, &u.Role,
		&u.State, &last, &u.LastLoginIP, &created); err != nil {
		return nil, err
	}
	u.CreatedAt = parseTime(created)
	if last != nil {
		if t := parseTime(*last); !t.IsZero() {
			u.LastLoginAt = &t
		}
	}
	return &u, nil
}
