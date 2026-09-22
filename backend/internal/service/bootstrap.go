package service

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	"github.com/LarryMKott/metalwatch/pkg/crypto"
)

// BootstrapAdminUser 是首次启动时创建的初始账号名。
const BootstrapAdminUser = "admin"

// EnsureFirstAdmin 保证系统至少有一个可用管理员（W11）。
//
// 新装环境库里没有任何账号，若不开这个口子，所有管理接口都会 401，
// 用户将永远进不去系统。做法：首次启动生成随机强口令，
//   - 打到启动日志（WARN 级，只出现一次）
//   - 同时落到数据目录的 bootstrap_admin.txt（0600）——日志滚动后仍能找回
//
// 已存在任何账号时本函数是空操作，绝不覆盖既有账号。
func EnsureFirstAdmin(ctx context.Context, store adapter.MetadataStore, dataDir string, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	n, err := store.Users().Count(ctx)
	if err != nil {
		return fmt.Errorf("检查初始账号失败: %w", err)
	}
	if n > 0 {
		return nil
	}

	password, err := crypto.RandomToken("", 12)
	if err != nil {
		return fmt.Errorf("生成初始口令失败: %w", err)
	}
	hash, err := crypto.HashPassword(password)
	if err != nil {
		return fmt.Errorf("生成口令哈希失败: %w", err)
	}

	u := &adapter.AppUser{
		Username:     BootstrapAdminUser,
		DisplayName:  "系统管理员",
		PasswordHash: hash,
		Role:         "admin",
		State:        "active",
	}
	if _, err := store.Users().Create(ctx, u); err != nil {
		return fmt.Errorf("创建初始管理员失败: %w", err)
	}

	log.Warn("首次启动：已创建初始管理员，请登录后立即修改口令",
		"username", BootstrapAdminUser, "password", password)

	if dataDir != "" {
		path := filepath.Join(dataDir, "bootstrap_admin.txt")
		body := fmt.Sprintf("MetalWatch 初始管理员账号（首次启动自动生成）\n"+
			"生成时间: %s\n\nusername: %s\npassword: %s\n\n"+
			"请登录后立即修改口令，确认无误后可删除本文件。\n",
			time.Now().UTC().Format(time.RFC3339), BootstrapAdminUser, password)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			// 写不进去不影响服务可用（日志里已有口令），只提示
			log.Warn("初始口令文件写入失败（口令已在上方日志中）", "path", path, "err", err)
			return nil
		}
		log.Warn("初始口令已保存到数据目录，确认后请删除该文件", "path", path)
	}
	return nil
}
