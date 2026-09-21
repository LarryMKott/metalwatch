package adapter

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// AgentToken 是 Agent 接入令牌（只保存 SHA-256 摘要，明文仅在签发时返回一次）。
type AgentToken struct {
	ID         int64
	HostID     *int64
	TokenHash  string
	Remark     *string
	State      string
	ExpireAt   *time.Time
	EnrolledAt *time.Time
	LastUsedAt *time.Time
	CreatedAt  time.Time
}

// AgentTokenRepo 提供令牌的读写。
type AgentTokenRepo struct{ s *Store }

// AgentTokens 返回令牌仓储。
func (s *Store) AgentTokens() *AgentTokenRepo { return &AgentTokenRepo{s: s} }

// Create 写入一个令牌（传 tokenHash，不要传明文）。
func (r *AgentTokenRepo) Create(ctx context.Context, t *AgentToken) (int64, error) {
	if t.TokenHash == "" {
		return 0, fmt.Errorf("token_hash 不能为空")
	}
	now := time.Now().UTC()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	if t.State == "" {
		t.State = "active"
	}
	q := r.s.Rebind(`INSERT INTO agent_token
		(host_id, token_hash, remark, state, expire_at, enrolled_at, created_at)
		VALUES (?,?,?,?,?,?,?)`)
	res, err := r.s.ExecContext(ctx, q, t.HostID, t.TokenHash, t.Remark, t.State,
		tsOrNil(t.ExpireAt), tsOrNil(t.EnrolledAt), formatTime(t.CreatedAt))
	if err != nil {
		return 0, fmt.Errorf("写入接入令牌失败: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	t.ID = id
	return id, nil
}

// FindActiveByHash 按摘要查找有效令牌（未被吊销且未过期）。
func (r *AgentTokenRepo) FindActiveByHash(ctx context.Context, hash string) (*AgentToken, error) {
	q := r.s.Rebind(`SELECT id, host_id, token_hash, remark, state, expire_at, enrolled_at,
		last_used_at, created_at
		FROM agent_token
		WHERE token_hash = ? AND state = 'active'
		  AND (expire_at IS NULL OR expire_at > ?)`)
	row := r.s.QueryRowContext(ctx, q, hash, formatTime(time.Now().UTC()))

	var (
		t          AgentToken
		hostID     sql.NullInt64
		remark     sql.NullString
		expireAt   sql.NullString
		enrolledAt sql.NullString
		lastUsedAt sql.NullString
		createdAt  string
	)
	err := row.Scan(&t.ID, &hostID, &t.TokenHash, &remark, &t.State, &expireAt,
		&enrolledAt, &lastUsedAt, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if hostID.Valid {
		v := hostID.Int64
		t.HostID = &v
	}
	t.Remark = strPtr(remark)
	t.ExpireAt = parseTimePtr(expireAt)
	t.EnrolledAt = parseTimePtr(enrolledAt)
	t.LastUsedAt = parseTimePtr(lastUsedAt)
	t.CreatedAt = parseTime(createdAt)
	return &t, nil
}

// BindHost 把令牌绑定到主机（enroll 成功后调用）。
func (r *AgentTokenRepo) BindHost(ctx context.Context, id, hostID int64, at time.Time) error {
	q := r.s.Rebind(`UPDATE agent_token SET host_id = ?, enrolled_at = ? WHERE id = ?`)
	_, err := r.s.ExecContext(ctx, q, hostID, formatTime(at), id)
	return err
}

// Touch 记录令牌最近使用时间（限流与审计用）。
func (r *AgentTokenRepo) Touch(ctx context.Context, id int64, at time.Time) error {
	q := r.s.Rebind(`UPDATE agent_token SET last_used_at = ? WHERE id = ?`)
	_, err := r.s.ExecContext(ctx, q, formatTime(at), id)
	return err
}

// Revoke 吊销令牌：下次上报将立即 401。
func (r *AgentTokenRepo) Revoke(ctx context.Context, id int64) error {
	q := r.s.Rebind(`UPDATE agent_token SET state = 'revoked' WHERE id = ?`)
	res, err := r.s.ExecContext(ctx, q, id)
	if err != nil {
		return fmt.Errorf("吊销令牌失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetBySMBIOSUUID 按硬件唯一标识查主机（Agent 重装后复用同一台资产，见 docs/02 M4）。
func (r *HostRepo) GetBySMBIOSUUID(ctx context.Context, uuid string) (*Host, error) {
	if uuid == "" {
		return nil, ErrNotFound
	}
	q := r.s.Rebind(`SELECT ` + hostColumns + ` FROM host WHERE smbios_uuid = ?`)
	row := r.s.QueryRowContext(ctx, q, uuid)
	h, err := scanHost(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return h, nil
}

// ErrCodeExhausted 表示注册码已用尽或已过期。
var ErrCodeExhausted = errors.New("注册码无效或已用尽")

// EnrollCodeRepo 管理 Agent 一次性注册码。
type EnrollCodeRepo struct{ s *Store }

// EnrollCodes 返回注册码仓储。
func (s *Store) EnrollCodes() *EnrollCodeRepo { return &EnrollCodeRepo{s: s} }

// Issue 登记一个新注册码（调用方负责生成明文并只把摘要传进来）。
func (r *EnrollCodeRepo) Issue(ctx context.Context, codeHash string, expireAt time.Time, maxUses int, createdBy string) error {
	if codeHash == "" {
		return fmt.Errorf("code_hash 不能为空")
	}
	if maxUses <= 0 {
		maxUses = 1
	}
	q := r.s.Rebind(`INSERT INTO enroll_code (code_hash, expire_at, max_uses, used_count, created_by, created_at)
	                     VALUES (?,?,?,0,?,?)`)
	_, err := r.s.ExecContext(ctx, q, codeHash, formatTime(expireAt), maxUses, createdBy,
		formatTime(time.Now().UTC()))
	if err != nil {
		return fmt.Errorf("登记注册码失败: %w", err)
	}
	return nil
}

// Consume 原子地消费一个注册码：校验有效期与剩余次数后次数 +1。
// 并发安全依赖单条 UPDATE 的条件与 affected rows 判定。
func (r *EnrollCodeRepo) Consume(ctx context.Context, codeHash string) error {
	now := formatTime(time.Now().UTC())
	q := r.s.Rebind(`UPDATE enroll_code
	                     SET used_count = used_count + 1
	                     WHERE code_hash = ? AND expire_at > ? AND used_count < max_uses`)
	res, err := r.s.ExecContext(ctx, q, codeHash, now)
	if err != nil {
		return fmt.Errorf("消费注册码失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrCodeExhausted
	}
	return nil
}
