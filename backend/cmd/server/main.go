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
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/LarryMKott/metalwatch/api/agent"
	"github.com/LarryMKott/metalwatch/api/web"
	"github.com/LarryMKott/metalwatch/internal/adapter"
	_ "github.com/LarryMKott/metalwatch/internal/adapter/sqlite" // 默认元数据后端
	_ "github.com/LarryMKott/metalwatch/internal/adapter/tsdb"   // 默认时序后端（W1c）
	"github.com/LarryMKott/metalwatch/internal/app"
	"github.com/LarryMKott/metalwatch/internal/engine"
	"github.com/LarryMKott/metalwatch/internal/notify"
	"github.com/LarryMKott/metalwatch/internal/pipeline"
	"github.com/LarryMKott/metalwatch/internal/service"
	"github.com/LarryMKott/metalwatch/internal/task"
	"github.com/LarryMKott/metalwatch/internal/ws"
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
	fmt.Println("请在 Agent 端用该注册码完成注册（详见 docs/04 §2.1）：")
	fmt.Println("  metalwatch-agent --server http://<server>:18080 --code <注册码> --spool <缓存目录>")
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

	// 时序存储（W1c，docs/01 D25：SQLite 分块压缩自研实现，零新依赖）。
	ts, err := adapter.OpenTimeSeries(adapter.TimeSeriesConfig{
		Driver:  cfg.Timeseries.Driver,
		RootDir: cfg.Server.DataDir,
		RawDays: cfg.Retention.RawDays, AggDays: cfg.Retention.AggDays,
	})
	if err != nil {
		return fmt.Errorf("打开时序存储失败: %w", err)
	}
	defer func() { _ = ts.Close() }()

	pl := pipeline.New(ts, log, pipeline.Options{})
	defer pl.Stop()

	// 实时推送（W7）：WebSocket Hub 挂到 /ws/alerts；出站 Webhook 按 notify_channel 配置分发
	hub := ws.NewHub(log)
	notifier := notify.New(db.NotifyChannels(), db.Alerts(), log)

	// 告警服务（W7）：种子内置阈值 → 加载规则；规则刷新由维护循环兜底
	alerts := service.NewAlertService(db, db.Thresholds(), db.Alerts(),
		service.AlertDeps{Notifier: notifier, Broadcaster: hub}, log)
	if err := alerts.SeedBuiltinTemplates(ctx); err != nil {
		log.Warn("内置阈值模板写入失败（可继续启动）", "err", err)
	}
	if err := alerts.ReloadRules(ctx); err != nil {
		log.Warn("告警规则加载失败（可继续启动）", "err", err)
	}

	// Agent 注册服务：JSON 与 gRPC 两条通道共享同一套流程（docs/01 D31 第 1 条）
	interval := time.Duration(cfg.Collect.SensorInterval) * time.Second
	assetInterval := time.Duration(cfg.Collect.AssetInterval) * time.Second
	enroll := service.NewEnrollService(db, hosts, interval, assetInterval, log)
	inventory := service.NewInventoryService(db, alerts, log)

	// 带外采集（W4）：凭据加密主密钥 → 调度引擎 + 任务池 → IPMI 轮询器
	masterKey, err := loadMasterKey(cfg.Server.DataDir, log)
	if err != nil {
		return err
	}
	sched := engine.NewScheduleEngine(engine.ScheduleConfig{
		SensorInterval: interval, AssetInterval: assetInterval, Pool: pool,
	})
	poller := service.NewIPMIPoller(db, pool, sched, pl, alerts, masterKey, interval, nil, log)
	if err := poller.SyncTargets(ctx); err != nil {
		log.Warn("带外采集目标同步失败（可继续启动）", "err", err)
	}
	go sched.Run(ctx)
	defer sched.Stop()

	deps := &app.Deps{
		Config: cfg, MasterKey: masterKey,
		Store: db, TSDB: ts, Pipeline: pl, Alerts: alerts,
		Hosts: hosts, Pool: pool, Hub: hub, IPMI: poller, Inventory: inventory,
		Log: log, Version: version, Started: time.Now(),
	}

	// gRPC 通道（W3）与 REST 共用单端口，按协议分流（docs/01 D22）
	grpcSrv := agent.NewGrpcServer(agent.NewStreamServer(
		db, enroll, inventory, ts, pl, alerts, interval, assetInterval, log))

	// 离线判定（W7/M3）：超 2×采集周期未上报 → offline + up=0 + agent_offline 告警
	offline := service.NewOfflineMonitor(db, pl, alerts,
		2*interval, 15*time.Second, log)
	go offline.Run(ctx)

	// 维护循环：5m 一轮 TSDB rollup/清理 + collect_run 7 天保留期（docs/03 §1.5/§2.4）
	stopMaint := startMaintenance(ctx, ts, log, func(ctx context.Context) {
		if n, err := db.CollectRuns().PruneBefore(ctx, time.Now().UTC().AddDate(0, 0, -7)); err != nil {
			log.Warn("collect_run 清理失败", "err", err)
		} else if n > 0 {
			log.Info("collect_run 已清理", "rows", n)
		}
	})
	defer stopMaint()

	srv := &http.Server{
		Addr: fmt.Sprintf(":%d", cfg.Server.Port),
		// 明文监听需要 h2c：gRPC 客户端以 HTTP/2 prior-knowledge 建连，
		// 不包 h2c 的话 HTTP/2 前导（PRI *）会被 gin 当成 HTTP/1.1 请求拒绝（D22）
		Handler:           h2c.NewHandler(agent.Multiplex(grpcSrv, web.NewRouter(deps)), &http2.Server{}),
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
			"tsdb_driver", ts.Name(),
			"grpc_stream", "enabled",
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

	// gRPC 优雅停止：先发 GOAWAY 等在途消息落盘（docs/03 §3.4），超时硬停
	grpcDone := make(chan struct{})
	go func() { grpcSrv.GracefulStop(); close(grpcDone) }()
	select {
	case <-grpcDone:
	case <-time.After(5 * time.Second):
		grpcSrv.Stop()
	}
	log.Info("已停止")
	return nil
}

// loadMasterKey 加载（必要时生成）凭据加密主密钥：data/secret/master.key，0600（docs/01 D9）。
func loadMasterKey(dataDir string, log *slog.Logger) ([]byte, error) {
	path := filepath.Join(dataDir, "secret", "master.key")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		key, err := crypto.GenerateMasterKey(path)
		if err != nil {
			return nil, err
		}
		log.Info("凭据加密主密钥已生成", "path", path)
		return key, nil
	}
	return crypto.LoadMasterKey(path)
}

// startMaintenance 周期执行 TSDB 维护与附加清理任务；返回停止函数。
func startMaintenance(ctx context.Context, ts adapter.TimeSeriesStore, log *slog.Logger,
	extra func(context.Context)) func() {
	maint, ok := ts.(adapter.MaintenanceStore)
	if !ok {
		return func() {} // 外部时序服务自行维护
	}
	gctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-gctx.Done():
				return
			case <-ticker.C:
				rolled, pruned, err := maint.Maintenance(gctx)
				if err != nil {
					log.Warn("时序维护失败", "err", err)
				} else if rolled > 0 || pruned > 0 {
					log.Info("时序维护完成", "rolled_up", rolled, "pruned", pruned)
				}
				if extra != nil {
					extra(gctx)
				}
			}
		}
	}()
	return func() { cancel(); <-done }
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
