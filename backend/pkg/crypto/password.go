package crypto

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

// 口令哈希与会话令牌（W11）。
//
// 安全约定：
//   - 口令用 Argon2id，只存 PHC 串，绝不存明文；校验用恒定时间比较
//   - 会话令牌是自签的 HMAC-SHA256 结构（不引入第三方 JWT 库）：
//     base64url(payload) + "." + base64url(hmac)，密钥取主密钥（master.key）
//   - 令牌只在有效期内有效；服务端不维护会话表，登出即客户端丢弃
//     （单机 FPK 场景足够；如需强制下线再引入黑名单表）

// Argon2id 参数（OWASP 推荐的下限档：19 MiB / 2 轮 / 并行 1）。
// 选这一档是因为服务端可能跑在飞牛的低端 x86 或 ARM 上，64 MiB 档会明显拖慢登录。
const (
	argonTime    = 2
	argonMemory  = 19 * 1024 // KiB
	argonThreads = 1
	argonSaltLen = 16
	argonKeyLen  = 32
)

// ErrBadCredential 表示令牌格式错误、签名不符或已过期。
var ErrBadCredential = errors.New("凭据无效或已过期")

// HashPassword 生成 Argon2id 的 PHC 串：
//
//	$argon2id$v=19$m=19456,t=2,p=1$<salt b64>$<hash b64>
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", fmt.Errorf("生成盐失败: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword 校验明文口令是否匹配 PHC 串。口令错误、格式错误一律返回 false。
func VerifyPassword(hash, password string) bool {
	salt, params, err := parsePHC(hash)
	if err != nil {
		return false
	}
	key := argon2.IDKey([]byte(password), salt, params.time, params.memory, params.threads, argonKeyLen)
	// 恒定时间比较：长度不同时先按哈希长度比较，避免长度泄漏后仍走常量比较
	return subtle.ConstantTimeCompare(key, params.key) == 1
}

type argonParams struct {
	memory  uint32
	time    uint32
	threads uint8
	key     []byte
}

// parsePHC 解析 HashPassword 产出的串（只接受 argon2id）。
func parsePHC(hash string) (salt []byte, p argonParams, err error) {
	parts := strings.Split(hash, "$")
	// ["", "argon2id", "v=19", "m=...,t=...,p=...", salt, key]
	if len(parts) != 6 || parts[1] != "argon2id" {
		return nil, p, errors.New("口令哈希格式不正确")
	}
	var m, t uint32
	var th uint8
	if _, err = fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &th); err != nil {
		return nil, p, fmt.Errorf("解析 Argon2 参数失败: %w", err)
	}
	if m == 0 || t == 0 || th == 0 {
		return nil, p, errors.New("Argon2 参数非法")
	}
	salt, err = base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return nil, p, errors.New("盐解码失败")
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return nil, p, errors.New("哈希解码失败")
	}
	return salt, argonParams{memory: m, time: t, threads: th, key: key}, nil
}

// ---------- 会话令牌 ----------

// Session 是登录成功后写入令牌的会话声明。
type Session struct {
	UserID   int64  `json:"uid"`
	Username string `json:"sub"`
	Role     string `json:"role"`
	// Exp 是过期时刻（Unix 秒）。
	Exp int64 `json:"exp"`
}

// SignSession 签发会话令牌。ttl 为有效期：
//
//	ttl == 0  → 使用默认 12 小时
//	ttl <  0  → 签发一枚「已过期」的令牌（测试与强制失效场景）
//
// 注意不要把 ttl <= 0 一律折算成默认值：那样就再也表达不出「已过期」，
// 测试也就无法覆盖过期分支。
func SignSession(key []byte, s Session, ttl time.Duration) (string, error) {
	if len(key) < 16 {
		return "", errors.New("会话密钥长度不足")
	}
	if ttl == 0 {
		ttl = 12 * time.Hour
	}
	s.Exp = time.Now().Add(ttl).Unix()
	raw, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	mac := sessionMAC(key, payload)
	return payload + "." + mac, nil
}

// VerifySession 校验并解析会话令牌：签名不符或已过期返回 ErrBadCredential。
func VerifySession(key []byte, token string) (*Session, error) {
	if len(key) < 16 {
		return nil, errors.New("会话密钥长度不足")
	}
	dot := strings.LastIndex(token, ".")
	if dot <= 0 {
		return nil, ErrBadCredential
	}
	payload, mac := token[:dot], token[dot+1:]
	want := sessionMAC(key, payload)
	// 恒定时间比较签名，避免按字节差异逐位泄漏
	if subtle.ConstantTimeCompare([]byte(mac), []byte(want)) != 1 {
		return nil, ErrBadCredential
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, ErrBadCredential
	}
	var s Session
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, ErrBadCredential
	}
	if time.Now().Unix() > s.Exp {
		return nil, ErrBadCredential
	}
	return &s, nil
}

func sessionMAC(key []byte, payload string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
