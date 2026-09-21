// Package sqlite 是默认的元数据存储后端（零部署，单文件落 TRIM_PKGVAR）。
//
// 作为独立子包实现，通过 adapter.Register 注册进适配器工厂，
// 上层只依赖 adapter 包的接口，不感知本包存在。
// 需要显式引入（import _ "…/internal/adapter/sqlite"）才会注册。
package sqlite

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	"github.com/LarryMKott/metalwatch/pkg/config"

	_ "modernc.org/sqlite" // 纯 Go 驱动：必须免 CGO 才能静态编译（见 docs/01 D14）
)

func init() {
	adapter.Register(adapter.DialectSQLite, open)
}

// open 打开（必要时创建）SQLite 库文件，并设置生产必需的 PRAGMA。
func open(cfg config.Config) (*sql.DB, error) {
	path := cfg.DBFilePath()
	if path == "" {
		return nil, fmt.Errorf("sqlite 库文件路径为空：请设置 db.file 或 server.data_dir")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("创建数据目录 %s 失败: %w", dir, err)
		}
	}

	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")    // 读写并发
	q.Add("_pragma", "synchronous(NORMAL)")  // WAL 下兼顾安全与吞吐
	q.Add("_pragma", "foreign_keys(1)")      // 默认关闭，不打开则级联删除失效
	q.Add("_pragma", "temp_store(MEMORY)")
	q.Add("_pragma", "auto_vacuum(INCREMENTAL)") // 删除后需增量回收才会缩文件

	busy := cfg.DB.BusyTimeout
	if busy <= 0 {
		busy = 5 * time.Second
	}
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busy.Milliseconds()))
	q.Add("_pragma", "cache_size(-65536)") // 约 64MB 页缓存

	dsn := "file:" + filepath.ToSlash(path) + "?" + q.Encode()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}

	// SQLite 是单写者模型：连接数压到 1，从根上避免 SQLITE_BUSY。
	// 元数据写入低频（资产变更/告警/审计），收益远大于并发损失。
	maxOpen := cfg.DB.MaxOpenConns
	if maxOpen <= 0 {
		maxOpen = 1
	}
	maxIdle := cfg.DB.MaxIdleConns
	if maxIdle <= 0 {
		maxIdle = maxOpen
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxLifetime(0)

	return db, nil
}
