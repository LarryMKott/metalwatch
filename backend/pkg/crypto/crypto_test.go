package crypto

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRandomTokenAndHash(t *testing.T) {
	tok, err := RandomToken("mwa_", 32)
	if err != nil {
		t.Fatalf("生成令牌失败: %v", err)
	}
	if !strings.HasPrefix(tok, "mwa_") {
		t.Fatalf("令牌缺少前缀: %s", tok)
	}
	if len(tok) < 40 {
		t.Fatalf("令牌过短: %d", len(tok))
	}

	tok2, _ := RandomToken("mwa_", 32)
	if tok == tok2 {
		t.Fatal("两次生成的令牌不应相同")
	}

	h1, h2 := HashToken(tok), HashToken(tok)
	if h1 != h2 || len(h1) != 64 {
		t.Fatalf("哈希应稳定且为 64 位十六进制: %s", h1)
	}
	if HashToken(tok2) == h1 {
		t.Fatal("不同令牌的哈希不应相同")
	}
}

func TestSealOpenWithAAD(t *testing.T) {
	key := make([]byte, KeySize)
	for i := range key {
		key[i] = byte(i)
	}
	plain := []byte("bmc-password-1")
	aad := AADForHost(42)

	nonce, ct, err := Seal(key, plain, aad)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	if bytes.Contains(ct, plain) {
		t.Fatal("密文中不应出现明文")
	}

	got, err := Open(key, nonce, ct, aad)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("解密结果不一致: %q", got)
	}

	// AAD 绑定 host_id：换成别的主机必须解密失败（防密文搬运）
	if _, err := Open(key, nonce, ct, AADForHost(43)); err == nil {
		t.Fatal("AAD 不匹配时应解密失败")
	}
	// 密钥不对也必须失败
	other := make([]byte, KeySize)
	if _, err := Open(other, nonce, ct, aad); err == nil {
		t.Fatal("错误密钥应解密失败")
	}
	// nonce 长度非法
	if _, err := Open(key, nonce[:4], ct, aad); err == nil {
		t.Fatal("非法 nonce 应报错")
	}
}

func TestSealRejectsBadKeySize(t *testing.T) {
	if _, _, err := Seal([]byte("short"), []byte("x"), nil); err == nil {
		t.Fatal("非 32 字节密钥应报错")
	}
}

func TestMasterKeyGenerateAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret", "master.key")

	if _, err := LoadMasterKey(path); err == nil {
		t.Fatal("未生成时应返回错误")
	}

	key, err := GenerateMasterKey(path)
	if err != nil {
		t.Fatalf("生成主密钥失败: %v", err)
	}
	if len(key) != KeySize {
		t.Fatalf("主密钥长度应为 %d，实际 %d", KeySize, len(key))
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("主密钥文件不存在: %v", err)
	}
	// Unix 权限位在 Windows 上不生效（Go 只会映射只读位），因此仅在类 Unix 平台断言。
	if runtime.GOOS != "windows" {
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Fatalf("主密钥权限应为 0600，实际 %o", perm)
		}
	}

	loaded, err := LoadMasterKey(path)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if !bytes.Equal(key, loaded) {
		t.Fatal("加载到的密钥与生成的不一致")
	}

	// 幂等：再次生成应返回同一把密钥，不得覆盖
	again, err := GenerateMasterKey(path)
	if err != nil || !bytes.Equal(again, key) {
		t.Fatalf("重复生成应返回原密钥: err=%v", err)
	}
}

func TestLoadMasterKeyRejectsWrongSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.key")
	if err := os.WriteFile(path, []byte("too-short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMasterKey(path); err == nil {
		t.Fatal("长度非法的主密钥应报错")
	}
}
