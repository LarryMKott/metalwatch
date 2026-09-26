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
	"github.com/LarryMKott/metalwatch/pkg/env"
	"github.com/LarryMKott/metalwatch/pkg/logger"
)

// 开发环境的相对路径约定（相对 backend 工作目录）。
// 两个都已被 .gitignore 覆盖，不会污染仓库。
const (
	// devConfigPath 是开发环境未指定 --config 时自动拾取的样例配置。
	devConfigPath = "configs/app.yaml"
	// devDataDir 是开发环境未指定 --data 时的数据目录（与 start-backend 脚本一致）。
	devDataDir = "tmp-data"
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
	// 运行环境先于一切解析：它决定所有「未显式指定」时的默认值（docs/01 D35）。
	environment, err := env.Resolve(os.Getenv)
	if err != nil {
		return err
	}

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

	// 区分「用户显式传入」与「flag 默认值」：只有前者才该压过环境默认值。
	// flag.Visit 只遍历被显式设置过的 flag。
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })

	// 开发环境：未指定配置时拾取样例配置；文件不存在则退回内置默认值。
	if !given["config"] && environment.IsDev() {
		if _, statErr := os.Stat(devConfigPath); statErr == nil {
			*configPath = devConfigPath
		}
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

	// 数据目录：命令行、环境变量、配置文件都没给时，按运行环境决定。
	// 生产环境拒绝隐式默认——数据写错位置的代价（写失败 / 被卸载清理）远高于启动报错。
	if cfg.Server.DataDir == "" {
		if environment.IsDev() {
			cfg.Server.DataDir = devDataDir
		} else {
			return fmt.Errorf("生产环境未指定数据目录：请用 --data 指定，或设置 " +
				"METALWATCH_DATA_DIR / 配置文件 server.data_dir（FPK 下对应 TRIM_PKGVAR/data）")
		}
	}

	// 监听地址：生产听所有网卡（由飞牛网关转发）；开发只听本机，避免开发机端口外露。
	host := ""
	if environment.IsDev() {
		host = "127.0.0.1"
	}
	if *listen != "" {
		h, port, listenErr := splitListen(*listen)
		if listenErr != nil {
			return listenErr
		}
		host, cfg.Server.Port = h, port
	}
	addr := net.JoinHostPort(host, strconv.Itoa(cfg.Server.Port))

	log, closeLog, err := logger.New(logger.Options{
		Level: cfg.Server.LogLevel, LogDir: cfg.Server.LogDir,
		// 开发环境日志同时打到终端：否则改完代码要先 tail 文件才知道起没起来。
		AlsoStdout: cmd != "serve" || environment.IsDev(),
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
		log.Info("迁移完成", "applied", applied, "dialect", string(db.Dialect()),
			"env", environment.Name(), "data_dir", cfg.Server.DataDir)
		return nil
	case "agent-code":
		log.Info("运行环境", "env", environment.Name(), "data_dir", cfg.Server.DataDir)
		return issueAgentCode(ctx, db, *codeDays, *codeUses, log)
	case "serve":
		return serve(ctx, cfg, db, applied, serveOptions{
			pidFile: *pidFile, addr: addr, env: environment, configPath: *configPath,
		}, log)
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

// serveOptions 聚合 serve 的运行参数（参数较多，聚成对象而非继续拉长参数列表）。
type serveOptions struct {
	// pidFile 为 PID 文件路径，FPK 生命周期脚本据此管理进程；开发环境为空。
	pidFile string
	// addr 为最终监听地址（host:port），已按运行环境补好默认值。
	addr string
	// env 为运行环境，用于启动日志与开发模式横幅。
	env env.Environment
	// configPath 为实际使用的配置文件路径，为空表示用的是内置默认值。
	configPath string
}

// serve 启动 HTTP 服务并等待退出信号。
func serve(ctx context.Context, cfg config.Config, db *adapter.Store, applied int,
	opts serveOptions, log *slog.Logger) error {

	if os.Getenv("GIN_MODE") == "" {
		gin.SetMode(gin.ReleaseMode)
	}

	hosts := service.NewHostService(db.Hosts(), log, service.WithAlertReconciler(db.Alerts()))
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
	// 通知投递改为有界队列异步执行：webhook 对不可达目标最坏 ~71s/渠道，
	// 原先同步跑在 Agent 上报协程上，会拖住上报链路、让断网补传越积越多。
	// 事件一律先落库，队列满时丢弃的只是「外发通知」，WebUI 仍可查。
	alerts.StartDispatch(ctx, 4, 256)

	// W11：新装环境必须有一个管理员，否则所有管理接口都 401，用户进不去系统
	if err := service.EnsureFirstAdmin(ctx, db, cfg.Server.DataDir, log); err != nil {
		return err
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
		Addr: opts.addr,
		// 明文监听需要 h2c：gRPC 客户端以 HTTP/2 prior-knowledge 建连，
		// 不包 h2c 的话 HTTP/2 前导（PRI *）会被 gin 当成 HTTP/1.1 请求拒绝（D22）
		Handler:           h2c.NewHandler(agent.Multiplex(grpcSrv, web.NewRouter(deps)), &http2.Server{}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// 先绑定端口、再写 PID 文件。反过来的话，端口被占时（旧实例仍在服务）
	// 新进程会先用新 PID 覆盖旧文件、随后在失败退出时把它删掉，
	// 于是 FPK 的 stop/status 找不到在运行的实例 → 误判「已停止」→ 重复启动，
	// 两个进程写同一数据目录。
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return fmt.Errorf("监听 %s 失败: %w", srv.Addr, err)
	}

	if opts.pidFile != "" {
		if err := os.WriteFile(opts.pidFile, []byte(strconv.Itoa(os.Getpid())), 0o640); err != nil {
			_ = ln.Close()
			return fmt.Errorf("写入 PID 文件失败: %w", err)
		}
		defer func() { _ = os.Remove(opts.pidFile) }()
	}

	// 开发环境把关键信息直接打在终端：没有 FPK 生命周期脚本帮忙提示，
	// 日志又容易被忽略，「起没起来 / 数据落在哪 / 从哪访问」必须一眼可见。
	if opts.env.IsDev() {
		printDevBanner(opts, cfg)
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("MetalWatch 已启动",
			"version", version, "listen", srv.Addr,
			"env", opts.env.Name(),
			"metadata_driver", string(db.Dialect()),
			"tsdb_driver", ts.Name(),
			"grpc_stream", "enabled",
			"migrations_applied", applied,
			"data_dir", cfg.Server.DataDir)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
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

// printDevBanner 打印开发环境的启动指引。
//
// 之所以要横幅而不是只依赖日志：开发环境常年在「改代码 → 重启 → 开浏览器」的循环里，
// 最耗时的失败模式是「进程起来了但访问的是另一个数据目录 / 另一个端口」。
func printDevBanner(opts serveOptions, cfg config.Config) {
	src := "内置默认值（未找到 " + devConfigPath + "）"
	if opts.configPath != "" {
		src = opts.configPath
	}
	dataDir := cfg.Server.DataDir
	if abs, err := filepath.Abs(dataDir); err == nil {
		dataDir = abs
	}

	w := os.Stderr
	fmt.Fprint(w, "\n────────────── MetalWatch 开发模式 ──────────────\n")
	fmt.Fprintf(w, "  环境判定：%s（%s）\n", opts.env.Name(), opts.env.Source())
	fmt.Fprintf(w, "  配置文件：%s\n", src)
	fmt.Fprintf(w, "  数据目录：%s\n", dataDir)
	fmt.Fprintf(w, "  访问地址：http://%s\n", opts.addr)
	fmt.Fprintf(w, "  签发注册码：go run ./cmd/server agent-code --days 7\n")
	fmt.Fprintf(w, "  切生产模式：%s=%s（生产环境必须显式指定数据目录）\n", env.Name, env.Prod)
	fmt.Fprint(w, "────────────────────────────────────────────────\n\n")
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

// splitListen 从 "0.0.0.0:18080" / ":18080" / "127.0.0.1:18080" 解析主机与端口。
// 主机部分保留原样（空字符串代表监听所有网卡），由调用方与运行环境默认值合并。
func splitListen(listen string) (host string, port int, err error) {
	host, portStr, err := net.SplitHostPort(listen)
	if err != nil {
		return "", 0, fmt.Errorf("--listen 格式应为 host:port（收到 %q）: %w", listen, err)
	}
	p, err := strconv.Atoi(portStr)
	if err != nil || p < 1 || p > 65535 {
		return "", 0, fmt.Errorf("--listen 端口非法: %q", portStr)
	}
	return host, p, nil
}
