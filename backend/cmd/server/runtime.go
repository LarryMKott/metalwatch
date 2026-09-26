package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"

	"github.com/LarryMKott/metalwatch/api/agent"
	"github.com/LarryMKott/metalwatch/api/web"
	"github.com/LarryMKott/metalwatch/internal/adapter"
	"github.com/LarryMKott/metalwatch/internal/app"
	"github.com/LarryMKott/metalwatch/internal/engine"
	"github.com/LarryMKott/metalwatch/internal/notify"
	"github.com/LarryMKott/metalwatch/internal/pipeline"
	"github.com/LarryMKott/metalwatch/internal/service"
	"github.com/LarryMKott/metalwatch/internal/task"
	"github.com/LarryMKott/metalwatch/internal/ws"
	"github.com/LarryMKott/metalwatch/pkg/config"
	"github.com/LarryMKott/metalwatch/pkg/crypto"
	"github.com/LarryMKott/metalwatch/pkg/env"
)

// serveCommand 启动 HTTP/gRPC 服务并等待退出信号。
type serveCommand struct{ boot *bootstrap }

func (c *serveCommand) run(ctx context.Context) error {
	rt, err := newRuntime(c.boot)
	if err != nil {
		return err
	}
	defer rt.close()

	ln, err := rt.listen()
	if err != nil {
		return err
	}
	return rt.serve(ctx, ln)
}

// runtime 是一次 serve 运行的全部协作对象。
//
// 为什么成类型：原先 serve() 是一个 168 行的函数，从「建主机服务」一路写到
// 「优雅停止」，十多个协作对象（任务池 / 时序库 / 采集管线 / WebSocket / 告警 /
// 注册 / 资产 / BMC 轮询 / 调度 / gRPC / HTTP）全挤在同一作用域里，
// 想复盘任何一个对象的装配都要从头读，且每个对象各自靠 defer 分散归还。
//
// 拆成四段，每段可单独阅读：
//
//	build  —— 装配协作对象（newRuntime）
//	listen —— 绑定端口、写 PID 文件、打印开发横幅
//	serve  —— 起服务并等退出信号，然后优雅停止
//	close  —— 逆序释放（HTTP/gRPC 的优雅停止在 serve 内完成）
type runtime struct {
	boot *bootstrap
	cfg  config.Config
	log  *slog.Logger

	hosts     *service.HostService
	pool      *task.Pool
	ts        adapter.TimeSeriesStore
	pipe      *pipeline.Pipeline
	hub       *ws.Hub
	alerts    *service.AlertService
	inventory *service.InventoryService
	poller    *service.IPMIPoller
	sched     *engine.ScheduleEngine
	grpcSrv   *grpc.Server
	httpSrv   *http.Server

	// stopMaintenance 停止 5m 维护循环；pidFile 非空时 close 负责删除。
	stopMaintenance func()
	pidFile         string
}

// newRuntime 装配 serve 需要的全部协作对象。
//
// 命名返回值 + defer 清理：装配过程有多处会失败的分支，而失败时必须把已经
// 打开的资源（时序库、采集管线、调度器、维护循环）还回去——原先这些靠函数末尾
// 的 defer 兜着，一旦拆成多段就很容易漏，这里用「失败即 close」统一收口。
func newRuntime(boot *bootstrap) (r *runtime, err error) {
	r = &runtime{boot: boot, cfg: boot.settings.config, log: boot.log}
	defer func() {
		if err != nil {
			r.close()
		}
	}()

	ctx, log, cfg := boot.ctx, boot.log, boot.settings.config

	if os.Getenv("GIN_MODE") == "" {
		gin.SetMode(gin.ReleaseMode)
	}

	r.hosts = service.NewHostService(boot.db.Hosts(), log,
		service.WithAlertReconciler(boot.db.Alerts()))
	r.pool = task.NewPool(task.Options{
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
		RootDir: boot.settings.dataDir(),
		RawDays: cfg.Retention.RawDays, AggDays: cfg.Retention.AggDays,
	})
	if err != nil {
		return nil, fmt.Errorf("打开时序存储失败: %w", err)
	}
	r.ts = ts
	r.pipe = pipeline.New(ts, log, pipeline.Options{})

	// 实时推送（W7）：WebSocket Hub 挂到 /ws/alerts；出站 Webhook 按 notify_channel 配置分发
	r.hub = ws.NewHub(log)
	notifier := notify.New(boot.db.NotifyChannels(), boot.db.Alerts(), log)

	// 告警服务（W7）：种子内置阈值 → 加载规则；规则刷新由维护循环兜底
	r.alerts = service.NewAlertService(boot.db, boot.db.Thresholds(), boot.db.Alerts(),
		service.AlertDeps{Notifier: notifier, Broadcaster: r.hub}, log)
	if err := r.alerts.SeedBuiltinTemplates(ctx); err != nil {
		log.Warn("内置阈值模板写入失败（可继续启动）", "err", err)
	}
	if err := r.alerts.ReloadRules(ctx); err != nil {
		log.Warn("告警规则加载失败（可继续启动）", "err", err)
	}
	// 通知改有界队列异步投递：webhook 对不可达目标最坏 ~71s/渠道且串行，
	// 原先同步跑在 Agent 上报协程上会拖住上报链路、让断网补传越积越多。
	// 事件一律先落库，队列满时丢弃的只是「外发通知」，WebUI 仍可查。
	r.alerts.StartDispatch(ctx, 4, 256)

	// W11：新装环境必须有一个管理员，否则所有管理接口都 401，用户进不去系统
	if err := service.EnsureFirstAdmin(ctx, boot.db, boot.settings.dataDir(), log); err != nil {
		return nil, err
	}

	// Agent 注册服务：JSON 与 gRPC 两条通道共享同一套流程（docs/01 D31 第 1 条）
	interval := time.Duration(cfg.Collect.SensorInterval) * time.Second
	assetInterval := time.Duration(cfg.Collect.AssetInterval) * time.Second
	enroll := service.NewEnrollService(boot.db, r.hosts, interval, assetInterval, log)
	r.inventory = service.NewInventoryService(boot.db, r.alerts, log)

	// 带外采集（W4）：凭据加密主密钥 → 调度引擎 + 任务池 → IPMI 轮询器
	masterKey, err := loadMasterKey(boot.settings.dataDir(), log)
	if err != nil {
		return nil, err
	}
	r.sched = engine.NewScheduleEngine(engine.ScheduleConfig{
		SensorInterval: interval, AssetInterval: assetInterval, Pool: r.pool,
	})
	r.poller = service.NewIPMIPoller(boot.db, r.pool, r.sched, r.pipe, r.alerts,
		masterKey, interval, nil, log)
	if err := r.poller.SyncTargets(ctx); err != nil {
		log.Warn("带外采集目标同步失败（可继续启动）", "err", err)
	}
	go r.sched.Run(ctx)

	deps := &app.Deps{
		Config: cfg, MasterKey: masterKey,
		Store: boot.db, TSDB: ts, Pipeline: r.pipe, Alerts: r.alerts,
		Hosts: r.hosts, Pool: r.pool, Hub: r.hub, IPMI: r.poller, Inventory: r.inventory,
		Log: log, Version: version, Started: time.Now(),
	}

	// gRPC 通道（W3）与 REST 共用单端口，按协议分流（docs/01 D22）
	r.grpcSrv = agent.NewGrpcServer(agent.NewStreamServer(
		boot.db, enroll, r.inventory, ts, r.pipe, r.alerts, interval, assetInterval, log))

	// 离线判定（W7/M3）：超 2×采集周期未上报 → offline + up=0 + agent_offline 告警
	offline := service.NewOfflineMonitor(boot.db, r.pipe, r.alerts, 2*interval, 15*time.Second, log)
	go offline.Run(ctx)

	// 维护循环：5m 一轮 TSDB rollup/清理 + collect_run 7 天保留期（docs/03 §1.5/§2.4）
	r.stopMaintenance = startMaintenance(ctx, ts, log, func(ctx context.Context) {
		if n, err := boot.db.CollectRuns().PruneBefore(ctx, time.Now().UTC().AddDate(0, 0, -7)); err != nil {
			log.Warn("collect_run 清理失败", "err", err)
		} else if n > 0 {
			log.Info("collect_run 已清理", "rows", n)
		}
	})

	r.httpSrv = &http.Server{
		Addr: boot.settings.addr.String(),
		// 明文监听需要 h2c：gRPC 客户端以 HTTP/2 prior-knowledge 建连，
		// 不包 h2c 的话 HTTP/2 前导（PRI *）会被 gin 当成 HTTP/1.1 请求拒绝（D22）
		Handler: h2c.NewHandler(agent.Multiplex(r.grpcSrv, web.NewRouter(deps)), &http2.Server{}),
		// 无 ReadTimeout 会让慢速请求长期占住连接（/api/v1/agent/enroll 等免鉴权端点
		// 尤其显眼）；读 header 与空闲超时已足以拦住慢速攻击，读写体则受各 handler 控制。
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return r, nil
}

// close 逆序释放协作对象。
//
// 顺序与原先 defer 的 LIFO 一致，且不可调换：
// 维护循环与调度器都会继续用时序库，采集管线的 Stop 要等在途批次落盘，
// 因此 stopMaintenance → sched → pipe → ts，最后才轮到 PID 文件与库。
// 幂等：装配中途失败时也会调用，未装好的部分按 nil 跳过。
func (r *runtime) close() {
	if r.stopMaintenance != nil {
		r.stopMaintenance()
		r.stopMaintenance = nil
	}
	if r.sched != nil {
		r.sched.Stop()
		r.sched = nil
	}
	if r.pipe != nil {
		r.pipe.Stop()
		r.pipe = nil
	}
	if r.ts != nil {
		_ = r.ts.Close()
		r.ts = nil
	}
	if r.pidFile != "" {
		_ = os.Remove(r.pidFile)
		r.pidFile = ""
	}
}

// listen 绑定端口、写 PID 文件、打印开发横幅，返回已就绪的监听器。
//
// 先绑定端口、再写 PID 文件。反过来的话，端口被占时（旧实例仍在服务）
// 新进程会先用新 PID 覆盖旧文件、随后在失败退出时把它删掉，
// 于是 FPK 的 stop/status 找不到在运行的实例 → 误判「已停止」→ 重复启动，
// 两个进程写同一数据目录。
func (r *runtime) listen() (net.Listener, error) {
	ln, err := net.Listen("tcp", r.httpSrv.Addr)
	if err != nil {
		return nil, fmt.Errorf("监听 %s 失败: %w", r.httpSrv.Addr, err)
	}
	if path := r.boot.settings.pidFile; path != "" {
		if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o640); err != nil {
			_ = ln.Close()
			return nil, fmt.Errorf("写入 PID 文件失败: %w", err)
		}
		r.pidFile = path
	}
	// 开发环境把关键信息直接打在终端：没有 FPK 生命周期脚本帮忙提示，
	// 日志又容易被忽略，「起没起来 / 数据落在哪 / 从哪访问」必须一眼可见。
	if r.boot.settings.env.IsDev() {
		r.printDevBanner()
	}
	return ln, nil
}

// serve 起服务并等到退出信号，然后优雅停止（HTTP 先、gRPC 后）。
func (r *runtime) serve(ctx context.Context, ln net.Listener) error {
	errCh := make(chan error, 1)
	go func() {
		r.log.Info("MetalWatch 已启动",
			"version", version, "listen", r.httpSrv.Addr,
			"env", r.boot.settings.env.Name(),
			"metadata_driver", string(r.boot.db.Dialect()),
			"tsdb_driver", r.ts.Name(),
			"grpc_stream", "enabled",
			"migrations_applied", r.boot.applied,
			"data_dir", r.boot.settings.dataDir())
		if err := r.httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("HTTP 服务异常退出: %w", err)
	case <-ctx.Done():
		r.log.Info("收到退出信号，开始优雅停止")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := r.httpSrv.Shutdown(shutdownCtx); err != nil {
		r.log.Warn("优雅停止超时，进程即将退出", "err", err)
	}

	// gRPC 优雅停止：先发 GOAWAY 等在途消息落盘（docs/03 §3.4），超时硬停。
	// 必须放在 HTTP 之后：流的写入依赖连接的完整。
	grpcDone := make(chan struct{})
	go func() { r.grpcSrv.GracefulStop(); close(grpcDone) }()
	select {
	case <-grpcDone:
	case <-time.After(5 * time.Second):
		r.grpcSrv.Stop()
	}
	r.log.Info("已停止")
	return nil
}

// printDevBanner 打印开发环境的启动指引。
//
// 之所以要横幅而不是只依赖日志：开发环境常年在「改代码 → 重启 → 开浏览器」的循环里，
// 最耗时的失败模式是「进程起来了但访问的是另一个数据目录 / 另一个端口」。
func (r *runtime) printDevBanner() {
	s := r.boot.settings
	src := "内置默认值（未找到 " + devConfigPath + "）"
	if s.configPath != "" {
		src = s.configPath
	}
	dataDir := s.dataDir()
	if abs, err := filepath.Abs(dataDir); err == nil {
		dataDir = abs
	}

	w := os.Stderr
	fmt.Fprint(w, "\n────────────── MetalWatch 开发模式 ──────────────\n")
	fmt.Fprintf(w, "  环境判定：%s（%s）\n", s.env.Name(), s.env.Source())
	fmt.Fprintf(w, "  配置文件：%s\n", src)
	fmt.Fprintf(w, "  数据目录：%s\n", dataDir)
	fmt.Fprintf(w, "  访问地址：http://%s\n", s.addr)
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
// 停止函数会等到循环真正退出，保证调用方之后可以安全关闭时序库。
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
