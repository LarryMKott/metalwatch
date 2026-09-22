package adapter

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// BMCCredential 是一行 BMC 凭据（docs/03 §1.3 bmc_credential）。
// 密码不落明文：服务层用 AES-256-GCM 加密（AAD 绑定 host_id，防密文跨主机搬运，docs/01 D9），
// 只存 nonce + 密文；key_version 支持主密钥轮换。
type BMCCredential struct {
	HostID       int64
	Username     string
	Protocol     string // ipmi15 | ipmi20 | redfish
	KeyVersion   int
	SecretCipher []byte
	SecretNonce  []byte
	UpdatedAt    time.Time
}

// BMCRepo 是 BMC 凭据仓储。加解密在服务层完成，本仓储只存密文。
type BMCRepo struct{ s *Store }

// BMC 是 BMC 凭据仓储入口。
func (s *Store) BMC() *BMCRepo { return &BMCRepo{s: s} }

// Upsert 写入或更新一台主机的凭据（host_id UNIQUE）。
func (r *BMCRepo) Upsert(ctx context.Context, c *BMCCredential) error {
	now := formatTime(c.UpdatedAt)
	_, err := r.s.ExecContext(ctx, r.s.Rebind(
		`INSERT INTO bmc_credential
		   (host_id, username, secret_cipher, secret_nonce, key_version, protocol, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(host_id) DO UPDATE SET
		   username = excluded.username,
		   secret_cipher = excluded.secret_cipher,
		   secret_nonce = excluded.secret_nonce,
		   key_version = excluded.key_version,
		   protocol = excluded.protocol,
		   updated_at = excluded.updated_at`),
		c.HostID, c.Username, c.SecretCipher, c.SecretNonce, c.KeyVersion, c.Protocol, now)
	return err
}

// Get 读取一台主机的凭据（密文）；不存在返回 ErrNotFound。
func (r *BMCRepo) Get(ctx context.Context, hostID int64) (*BMCCredential, error) {
	row := r.s.QueryRowContext(ctx, r.s.Rebind(
		`SELECT host_id, username, secret_cipher, secret_nonce, key_version, protocol, updated_at
		 FROM bmc_credential WHERE host_id = ?`), hostID)
	var c BMCCredential
	var updated *string
	if err := row.Scan(&c.HostID, &c.Username, &c.SecretCipher, &c.SecretNonce,
		&c.KeyVersion, &c.Protocol, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if updated != nil {
		c.UpdatedAt = parseTime(*updated)
	}
	return &c, nil
}

// Delete 删除一台主机的凭据；返回是否存在。
func (r *BMCRepo) Delete(ctx context.Context, hostID int64) (bool, error) {
	res, err := r.s.ExecContext(ctx, r.s.Rebind(
		`DELETE FROM bmc_credential WHERE host_id = ?`), hostID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// HasAny 返回是否存在任何凭据（启动日志用）。
func (r *BMCRepo) HasAny(ctx context.Context) (bool, error) {
	var n int
	err := r.s.QueryRowContext(ctx, `SELECT COUNT(*) FROM bmc_credential`).Scan(&n)
	return n > 0, err
}
