// Package agentapp 收敛 Agent 两个平台入口共用的启动流程。
//
// 为什么单独成包：Windows 与 Linux 是两个 package main（平台文件只放平台实现），
// 于是 runOpts / runGRPC / enrollGRPC / runJSON 曾在两处逐行复制。复制的代价是
// 真实发生过的：同款「凭据写入失败仍打印注册成功」的缺陷只修了 Windows 一侧，
// Linux 的 runJSON 路径继续谎报成功。把流程收进一个类型、平台差异收进 Platform
// 接口之后，这类不一致由结构排除，而不是靠人肉同步。
package agentapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/LarryMKott/metalwatch/agent/internal/agentcfg"
	"github.com/LarryMKott/metalwatch/agent/internal/collect"
	"github.com/LarryMKott/metalwatch/agent/internal/grpcstream"
	"github.com/LarryMKott/metalwatch/agent/internal/heartbeat"
	"github.com/LarryMKott/metalwatch/agent/internal/report"
	"github.com/LarryMKott/metalwatch/agent/internal/spool"
	"github.com/LarryMKott/metalwatch/pkg/env"
)

// 传输层取值（--transport）。
const (
	TransportGRPC = "grpc"
	TransportJSON = "json"
)

// ErrEnrollNotSaved 表示「服务端已发放凭据、本地没落盘」。
//
// 细节（令牌与 host_id 明文）已由 persistCredentials 打到 stderr，
// 入口只需据此置非 0 退出码，不该重复打印——凭据明文只应出现一次。
var ErrEnrollNotSaved = errors.New("注册未完成：本地凭据写入失败（上方已列出需手工保存的值）")

// Platform 是 Agent 的平台差异面。两个平台入口各提供一份实现，其余流程全部共用。
type Platform interface {
	// CredentialHint 返回注册成功后「凭据该写到哪」的平台指引（不含句末标点）。
	CredentialHint() string
	// Signals 返回需要捕获的退出信号。
	Signals() []os.Signal
}

// Options 是命令行解析结果。
//
// 与运行环境相关的默认值（spool 目录、凭据来源）不在这里，而由 New 按
// METALWATCH_ENV 推导——见 docs/01 D35。
type Options struct {
	// Code 为一次性注册码；非空时本次运行只做注册后退出。
	Code string
	// Server 为显式指定的服务端地址；为空则取本地凭据记录的地址，再兜底默认值。
	Server string
	// Transport 为传输层，取 TransportGRPC 或 TransportJSON。
	Transport string
	// TLSSkip 为 true 时跳过 https 自签证书校验（内网部署选项）。
	TLSSkip bool
	// Interval 是指标上报周期。
	Interval time.Duration
	// SpoolDir 为显式指定的断网缓存目录；为空则按运行环境取默认值。
	SpoolDir string
}

// App 是一次 Agent 运行：持有运行环境、日志、采集器与解析后的参数。
//
// 用类型而不是一串自由函数：运行环境、spool 目录、版本号这些参数在原先的
// 自由函数里要逐个透传（runOpts 就是为了缓解这个才出现的），收进结构体后
// 方法集直接读字段，入口也不再需要中转变量。
type App struct {
	platform  Platform
	version   string
	opts      Options
	env       env.Environment
	log       *slog.Logger
	collector *collect.Collector
	spoolDir  string

	// 以下三项供测试替换，生产一律走 os 默认值。
	getenv env.Getenv
	out    io.Writer
	errOut io.Writer
}

// New 判定运行环境并装配 App。
func New(p Platform, version string, opts Options) (*App, error) {
	if p == nil {
		return nil, errors.New("未提供平台实现")
	}
	environment, err := env.Resolve(os.Getenv)
	if err != nil {
		return nil, err
	}
	if opts.Transport == "" {
		opts.Transport = TransportGRPC
	}
	// 非法取值直接报错而不是静默回落。原先的写法是「等于 grpc 走流、其余走 JSON」，
	// 于是 --transport 打错一个字母就会悄悄切到兼容通道，排障时极难看出。
	if opts.Transport != TransportGRPC && opts.Transport != TransportJSON {
		return nil, fmt.Errorf("未知的传输协议 %q（可用: %s / %s）",
			opts.Transport, TransportGRPC, TransportJSON)
	}
	// 采集侧要拿版本号上报（否则 host_info 里的 agent_version 恒为 dev）。
	collect.SetVersion(version)
	return &App{
		platform:  p,
		version:   version,
		opts:      opts,
		env:       environment,
		log:       slog.New(slog.NewTextHandler(os.Stderr, nil)),
		collector: collect.New(),
		spoolDir:  agentcfg.ResolveSpoolDir(opts.SpoolDir, environment),
		getenv:    os.Getenv,
		out:       os.Stdout,
		errOut:    os.Stderr,
	}, nil
}

// SignalContext 返回带退出信号捕获的 context。
//
// 信号集是平台差异，收在这里之后入口不必各自写一遍 signal.NotifyContext；
// 返回的 CancelFunc 直接 defer 即可。
func (a *App) SignalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), a.platform.Signals()...)
}

// Run 执行一次 Agent 会话，按传输层分流。
//
// 返回 nil 表示正常退出（含收到退出信号）；否则是可读的失败原因。
// 不在内部 os.Exit：退出码与打印由入口统一决定，流程本身可被测试。
func (a *App) Run(ctx context.Context) error {
	if a.opts.Transport == TransportJSON {
		return a.runJSON(ctx)
	}
	return a.runGRPC(ctx)
}

// runGRPC 是默认传输层：gRPC 双向流 + bbolt 断网续传（W6）。
func (a *App) runGRPC(ctx context.Context) error {
	if a.opts.Code != "" {
		return a.enrollGRPC(ctx)
	}

	cred, err := agentcfg.ResolveIdentity(a.spoolDir, a.env, a.getenv)
	if err != nil {
		return err
	}
	server := agentcfg.ResolveServer(a.opts.Server, cred)

	if err := os.MkdirAll(a.spoolDir, 0o700); err != nil {
		return fmt.Errorf("创建 spool 目录失败: %w", err)
	}
	sp, err := spool.Open(filepath.Join(a.spoolDir, "spool.db"), spool.Options{})
	if err != nil {
		return fmt.Errorf("打开断网缓存失败: %w", err)
	}
	defer func() { _ = sp.Close() }()

	a.log.Info("Agent 启动（gRPC 模式）", "version", a.version, "server", server,
		"host_id", cred.HostID, "interval", a.opts.Interval,
		"env", a.env.Name(), "spool", a.spoolDir)
	stream := grpcstream.New(grpcstream.Options{
		Server: server, Token: cred.Token, HostID: cred.HostID,
		Interval: a.opts.Interval, TLSInsecure: a.opts.TLSSkip,
		Spool: sp, Log: a.log,
	}, a.collector, a.version)

	if err := stream.Run(ctx); err != nil && ctx.Err() == nil {
		return fmt.Errorf("上报会话退出: %w", err)
	}
	return nil
}

// enrollGRPC 用一次性注册码换取凭据并落盘，使后续启动无需任何参数。
func (a *App) enrollGRPC(ctx context.Context) error {
	server := agentcfg.ResolveServer(a.opts.Server, agentcfg.Credentials{})
	token, hostID, err := grpcstream.EnrollOnce(ctx, server, a.opts.TLSSkip, a.opts.Code,
		collect.HostFingerprint(ctx), a.version)
	if err != nil {
		return fmt.Errorf("注册失败: %w", err)
	}
	return a.persistCredentials(token, hostID, server)
}

// runJSON 是兼容传输层：JSON HTTP + 文件式 spool（W6 之前的通道，用于排障与旧服务端）。
func (a *App) runJSON(ctx context.Context) error {
	// 注册路径下还没有本地凭据，地址只能来自显式参数或兜底默认值。
	var cred agentcfg.Credentials
	if a.opts.Code == "" {
		var err error
		if cred, err = agentcfg.ResolveIdentity(a.spoolDir, a.env, a.getenv); err != nil {
			return err
		}
	}
	server := agentcfg.ResolveServer(a.opts.Server, cred)
	client := report.NewClient(report.Options{
		Server:   server,
		SpoolDir: a.spoolDir,
		Version:  a.version,
	})

	if a.opts.Code != "" {
		token, hostID, err := client.Enroll(ctx, a.opts.Code, collect.HostFingerprint(ctx))
		if err != nil {
			return fmt.Errorf("注册失败: %w", err)
		}
		return a.persistCredentials(token, hostID, server)
	}

	client.SetToken(cred.Token)
	a.log.Info("Agent 启动（JSON 兼容模式）", "version", a.version, "server", server,
		"host_id", cred.HostID, "env", a.env.Name())
	go heartbeat.New(client, cred.HostID, 10*time.Second).Run(ctx)

	if err := report.NewReporter(client, a.collector, a.opts.Interval, cred.HostID).Run(ctx); err != nil &&
		ctx.Err() == nil {
		return fmt.Errorf("上报循环退出: %w", err)
	}
	return nil
}

// persistCredentials 把注册结果落盘并打印后续指引，两条传输通道共用。
//
// 落地两份文件：token（历史布局，外部脚本仍读它）+ credentials.json（含 host_id）。
// 任一失败都算注册没完成——注册码已在服务端消耗、本地却没落盘，必须让用户知道，
// 否则会出现「提示注册成功、下次启动仍报缺少令牌」的死循环。
//
// 之所以强调「共用」：Linux 的 JSON 路径此前漏掉了这条修复，写盘失败后仍打印
// 「注册成功，凭据已写入本地」，用户据此以为已注册、下次启动却报缺令牌。
func (a *App) persistCredentials(token string, hostID int64, server string) error {
	var writeErr error
	if err := grpcstream.SaveToken(a.spoolDir, token); err != nil {
		fmt.Fprintln(a.errOut, "写入令牌文件失败:", err)
		writeErr = err
	}
	if err := agentcfg.SaveIdentity(a.spoolDir, agentcfg.Credentials{
		Token: token, HostID: hostID, Server: server,
	}); err != nil {
		fmt.Fprintln(a.errOut, "写入凭据文件失败:", err)
		writeErr = err
	}
	if writeErr != nil {
		// 把值打出来：用户手工保存后仍可用环境变量方式运行，不必再换一个注册码。
		fmt.Fprintln(a.errOut, "\n注册已在服务端完成，但本地凭据写入失败。请手工保存以下值：")
		fmt.Fprintln(a.errOut, "  "+agentcfg.EnvToken+"=", token)
		fmt.Fprintln(a.errOut, "  "+agentcfg.EnvHostID+"=", hostID)
		return ErrEnrollNotSaved
	}

	fmt.Fprintln(a.out, "注册成功，凭据已写入本地（请妥善保管）:")
	fmt.Fprintln(a.out, "  "+agentcfg.EnvToken+"=", token)
	fmt.Fprintln(a.out, "  "+agentcfg.EnvHostID+"=", hostID)
	if a.env.IsDev() {
		fmt.Fprintln(a.out, "\n开发环境可直接再次启动，无需再带任何参数。")
		return nil
	}
	fmt.Fprintln(a.out, "\n请把以上两个值写入 "+a.platform.CredentialHint()+"。")
	return nil
}
