// Package cryptox 是加密相关的基础设施：随机令牌、哈希、AES-256-GCM 信封加密。
//
// 安全约定（见 docs/01 D9）：
//   - BMC 凭据用 AES-256-GCM 加密，AAD 必须绑定 host_id，防止密文在主机间搬运
//   - 主密钥独立于数据目录之外生成，权限 0600
//   - 任何密钥、明文口令都不得进入日志
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// KeySize 是主密钥长度（AES-256）。
const KeySize = 32

// ErrKeyMissing 表示主密钥不存在。
var ErrKeyMissing = errors.New("主密钥不存在")

// RandomToken 生成 URL 安全的随机令牌，prefix 便于识别用途（如 "mwa_"）。
func RandomToken(prefix string, nBytes int) (string, error) {
	if nBytes < 16 {
		nBytes = 16
	}
	buf := make([]byte, nBytes)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", fmt.Errorf("生成随机数失败: %w", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

// HashToken 返回令牌的 SHA-256 十六进制摘要。
// 数据库只存摘要，令牌明文仅在签发时返回一次。
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// GenerateMasterKey 生成并落盘主密钥（权限 0600）。已存在则直接返回。
func GenerateMasterKey(path string) ([]byte, error) {
	if key, err := LoadMasterKey(path); err == nil {
		return key, nil
	} else if !errors.Is(err, ErrKeyMissing) {
		return nil, err
	}

	key := make([]byte, KeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("生成主密钥失败: %w", err)
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("创建密钥目录失败: %w", err)
		}
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, fmt.Errorf("写入主密钥失败: %w", err)
	}
	return key, nil
}

// LoadMasterKey 读取主密钥；文件不存在返回 ErrKeyMissing。
func LoadMasterKey(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrKeyMissing
	}
	if err != nil {
		return nil, fmt.Errorf("读取主密钥失败: %w", err)
	}
	if len(raw) != KeySize {
		return nil, fmt.Errorf("主密钥长度非法：期望 %d 字节，实际 %d", KeySize, len(raw))
	}
	return raw, nil
}

// Seal 用 AES-256-GCM 加密明文，返回 nonce 与密文（密文尾部含认证标签）。
// aad 必须传入稳定且唯一的上下文（本项目用 host_id），防止密文被搬运复用。
func Seal(key, plaintext, aad []byte) (nonce, ciphertext []byte, err error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("生成 nonce 失败: %w", err)
	}
	return nonce, gcm.Seal(nil, nonce, plaintext, aad), nil
}

// Open 解密 Seal 产出的密文。AAD 不一致时解密必然失败。
func Open(key, nonce, ciphertext, aad []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("nonce 长度非法：期望 %d，实际 %d", gcm.NonceSize(), len(nonce))
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("解密失败（密钥不符或 AAD 不匹配）: %w", err)
	}
	return plain, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("密钥长度必须为 %d 字节，实际 %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("初始化 AES 失败: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("初始化 GCM 失败: %w", err)
	}
	return gcm, nil
}

// AADForHost 生成 BMC 凭据加密用的 AAD。
func AADForHost(hostID int64) []byte {
	return []byte(fmt.Sprintf("metalwatch:host:%d", hostID))
}
