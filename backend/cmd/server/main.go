// MetalWatch 服务端入口。
//
// 子命令：
//
//	metalwatch serve      启动服务（默认；FPK 的 cmd/main 以该方式拉起进程）
//	metalwatch migrate    仅执行数据库结构迁移后退出
//	metalwatch agent-code 签发一次性 Agent 注册码
//
// 生命周期：进程由 FPK 的 cmd/main 管理（PID 文件 + TERM→KILL），
// 因此这里只负责「优雅退出」与 PID 文件写入，不自行 daemon 化。
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LarryMKott/metalwatch/api/web"
	"github.com/LarryMKott/metalwatch/internal/adapter"
	_ "github.com/LarryMKott/metalwatch/internal/adapter/sqlite" // 默认元数据后端
	"github.com/LarryMKott/metalwatch/internal/app"
	"github.com/LarryMKott/metalwatch/internal/service"
	"github.com/LarryMKott/metalwatch/internal/task"
	"github.com/LarryMKott/metalwatch/migrations"
	"github.com/LarryMKott/metalwatch/pkg/config"
	"github.com/LarryMKott/metalwatch/pkg/crypto"
	"github.com/LarryMKott/metalwatch/pkg/logger"
)

// version 由构建时注入：-ldflags "-X main.version=x.y.z"。
var version = "0.1.0"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "启动失败:", err)
		os.Exit(1)
	}
}

func run() error {
	cmd := "serve"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd = args[0]
		args = args[1:]
	}

	fs := flag.NewFlagSet("metalwatch "+cmd, flag.ExitOnError)
	configPath := fs.String("config", "", "配置文件路径（FPK: $TRIM_PKGETC/metalwatch.yaml）")
	dataDir := fs.String("data", "", "运行数据目录（FPK: $TRIM_PKGVAR/data）")
	logDir := fs.String("log-dir", "", "日志目录（FPK: $TRIM_PKGVAR/logs）")
	listen := fs.String("listen", "", "监听地址，如 0.0.0.0:18080（默认取配置 server.port）")
	pidFile := fs.String("pid-file", "", "写入进程 PID 的文件（FPK 生命周期脚本使用）")
	codeDays := fs.Int("days", 7, "agent-code 子命令：注册码有效期（天）")
	codeUses := fs.Int("uses", 1, "agent-code 子命令：注册码可用次数")
	migrateOnly := fs.Bool("migrate", false, "等价于 migrate 子命令")
	fs.Parse(args)

	if *migrateOnly || cmd == "migrate" {
		cmd = "migrate"
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *dataDir != "" {
		cfg.Server.DataDir = *dataDir
	}
	if *logDir != "" {
		cfg.Server.LogDir = *logDir
	}
	if *listen != "" {
		port, err := portOf(*listen)
		if err != nil {
			return err
		}
		cfg.Server.Port = port
	}
	if cfg.Server.DataDir == "" {
		return errors.New("未指定数据目录：请通过 --data 或配置文件 server.data_dir 指定")
	}

	log, closeLog, err := logger.New(logger.Options{
		Level: cfg.Server.LogLevel, LogDir: cfg.Server.LogDir, AlsoStdout: cmd != "serve",
	})
	if err != nil {
		return fmt.Errorf("初始化日志失败: %w", err)
	}
	defer func() { _ = closeLog() }()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := adapter.Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	applied, err := migrate(ctx, db, log)
	if err != nil {
		return err
	}

	switch cmd {
	case "migrate":
		log.Info("迁移完成", "applied", applied, "dialect", string(db.Dialect()))
		return nil
	case "agent-code":
		return issueAgentCode(ctx, db, *codeDays, *codeUses, log)
	case "serve":
		return serve(ctx, cfg, db, applied, *pidFile, log)
	default:
		return fmt.Errorf("未知子命令 %q（可用: serve / migrate / agent-code）", cmd)
	}
}

// migrate 应用内嵌迁移。
func migrate(ctx context.Context, db *adapter.Store, log *slog.Logger) (int, error) {
	fsys, err := migrations.DialectFS(string(db.Dialect()))
	if err != nil {
		return 0, fmt.Errorf("未找到 %s 方言的迁移目录: %w", db.Dialect(), err)
	}
	return db.Migrate(ctx, fsys, string(db.Dialect()))
}

// issueAgentCode 签发一次性注册码并打印（明文只出现这一次）。
func issueAgentCode(ctx context.Context, db *adapter.Store, days, uses int, log *slog.Logger) error {
	raw, err := crypto.RandomToken("MW-", 16)
	if err != nil {
		return err
	}
	expireAt := time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour)
	if err := db.EnrollCodes().Issue(ctx, crypto.HashToken(raw), expireAt, uses, "cli"); err != nil {
		return err
	}
	log.Info("注册码已签发", "expire_at", expireAt.Format(time.RFC3339), "max_uses", uses)
	fmt.Printf("\n注册码: %s\n有效期至: %s\n可用次数: %d\n\n", raw, expireAt.Format(time.RFC3339), uses)
	fmt.Println("请在 Agent 端以该注册码执行 enroll（详见 docs/04 §2.1）：")
	fmt.Println("  metalwatch-agent enroll --server https://<server>:18080 --code <注册码>")
	return nil
}

// serve 启动 HTTP 服务并等待退出信号。
func serve(ctx context.Context, cfg config.Config, db *adapter.Store, applied int,
	pidFile string, log *slog.Logger) error {

	if os.Getenv("GIN_MODE") == "" {
		gin.SetMode(gin.ReleaseMode)
	}

	hosts := service.NewHostService(db.Hosts(), log)
	pool := task.NewPool(task.Options{
		Concurrency:    cfg.Collect.IPMIConcurrency,
		Timeout:        time.Duration(cfg.Collect.CollectTimeoutSec) * time.Second,
		RetryThreshold: cfg.Collect.BMCRetryMax,
		BackoffBase:    time.Duration(cfg.Collect.SensorInterval) * time.Second,
		BackoffMax:     time.Duration(cfg.Collect.BMCBackoffMaxSec) * time.Second,
		Logger:         log,
	})

	deps := &app.Deps{
		Config: cfg, Store: db, Hosts: hosts, Pool: pool,
		Log: log, Version: version, Started: time.Now(),
	}

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:           web.NewRouter(deps),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	if pidFile != "" {
		if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o640); err != nil {
			return fmt.Errorf("写入 PID 文件失败: %w", err)
		}
		defer func() { _ = os.Remove(pidFile) }()
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("MetalWatch 已启动",
			"version", version, "listen", srv.Addr,
			"metadata_driver", string(db.Dialect()),
			"migrations_applied", applied,
			"data_dir", cfg.Server.DataDir)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("HTTP 服务异常退出: %w", err)
	case <-ctx.Done():
		log.Info("收到退出信号，开始优雅停止")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn("优雅停止超时，进程即将退出", "err", err)
	}
	log.Info("已停止")
	return nil
}

// portOf 从 "0.0.0.0:18080" 或 ":18080" 中解析端口。
func portOf(listen string) (int, error) {
	_, portStr, err := net.SplitHostPort(listen)
	if err != nil {
		return 0, fmt.Errorf("--listen 格式应为 host:port（收到 %q）: %w", listen, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("--listen 端口非法: %q", portStr)
	}
	return port, nil
}
