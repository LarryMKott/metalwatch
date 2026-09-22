package crypto

import (
	"strings"
	"testing"
	"time"
)

func TestPasswordHashVerify(t *testing.T) {
	h, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("生成哈希失败: %v", err)
	}
	if !strings.HasPrefix(h, "$argon2id$") {
		t.Fatalf("应为 argon2id 的 PHC 串: %s", h)
	}
	if h == "correct horse battery staple" {
		t.Fatal("不得存储明文")
	}

	if !VerifyPassword(h, "correct horse battery staple") {
		t.Fatal("正确口令应校验通过")
	}
	if VerifyPassword(h, "wrong") {
		t.Fatal("错误口令不应通过")
	}
	// 两次哈希同一口令应不同（盐随机）
	h2, _ := HashPassword("correct horse battery staple")
	if h == h2 {
		t.Fatal("相同口令的两次哈希不应相同（盐未随机）")
	}
}

func TestPasswordVerifyRejectsMalformed(t *testing.T) {
	for _, bad := range []string{"", "not-a-hash", "$argon2id$v=19$m=0,t=0,p=0$c2FsdA$aGFzaA",
		"$argon2i$v=19$m=19456,t=2,p=1$c2FsdA$aGFzaA", "$$$$$"} {
		if VerifyPassword(bad, "anything") {
			t.Fatalf("畸形哈希 %q 不应通过校验", bad)
		}
	}
}

func TestSessionSignVerify(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}

	tok, err := SignSession(key, Session{UserID: 7, Username: "alice", Role: "admin"}, time.Hour)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	s, err := VerifySession(key, tok)
	if err != nil {
		t.Fatalf("校验失败: %v", err)
	}
	if s.UserID != 7 || s.Username != "alice" || s.Role != "admin" {
		t.Fatalf("会话字段不符: %+v", s)
	}

	// 篡改载荷
	parts := strings.SplitN(tok, ".", 2)
	if _, err := VerifySession(key, parts[0]+"."+"AAAA"); err == nil {
		t.Fatal("篡改签名应失败")
	}
	// 换密钥
	other := make([]byte, 32)
	if _, err := VerifySession(other, tok); err == nil {
		t.Fatal("换密钥后应校验失败")
	}
	// 过期
	expired, err := SignSession(key, Session{UserID: 1, Username: "bob", Role: "viewer"}, -time.Hour)
	if err != nil {
		t.Fatalf("签发过期令牌失败: %v", err)
	}
	if _, err := VerifySession(key, expired); err == nil {
		t.Fatal("过期令牌应校验失败")
	}
	// 格式错误
	for _, bad := range []string{"", "no-dot", ".", "a.b"} {
		if _, err := VerifySession(key, bad); err == nil {
			t.Fatalf("畸形令牌 %q 应校验失败", bad)
		}
	}
	// 密钥太短
	if _, err := SignSession([]byte("short"), Session{}, time.Hour); err == nil {
		t.Fatal("短密钥应拒绝签发")
	}
}
