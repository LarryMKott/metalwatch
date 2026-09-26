//go:build linux

// MetalWatch Agent —— Linux 平台入口。
//
// 采集方式：直接读 /sys/class/hwmon 与 /proc，不依赖 lm-sensors；SMART 走随包内置的 smartctl。
// 以 root 运行（systemd unit），因为读 DMI 与磁盘设备需要权限。
//
// 传输层（--transport）：
//   - grpc（默认）：gRPC 双向流 + bbolt 断网续传 + 指数退避重连（W6）
//   - json：JSON HTTP 兼容模式，用于排障与旧服务端对接
//
// 运行环境（METALWATCH_ENV，见 docs/01 D35）：
//   - dev：spool 落仓库内 bin-local/spool；令牌与 host_id 自动读本地凭据，
//     注册一次后即可无参数直接运行
//   - prod：spool 落 /var/lib；凭据必须由 systemd EnvironmentFile 显式注入
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/LarryMKott/metalwatch/agent/internal/agentcfg"
	"github.com/LarryMKott/metalwatch/agent/internal/collect"
	"github.com/LarryMKott/metalwatch/agent/internal/grpcstream"
	"github.com/LarryMKott/metalwatch/agent/internal/heartbeat"
	"github.com/LarryMKott/metalwatch/agent/internal/report"
	"github.com/LarryMKott/metalwatch/agent/internal/spool"
	"github.com/LarryMKott/metalwatch/pkg/env"
)

const version = "0.1.0"

func main() {
	enrollCode := flag.String("code", "", "一次性注册码（首次接入使用）")
	server := flag.String("server", "", "服务端地址（默认取本地凭据记录的地址，否则 "+
		agentcfg.DefaultServerURL+"）")
	transport := flag.String("transport", "grpc", "传输协议：grpc（默认，双向流+断网续传）或 json（兼容模式）")
	tlsSkip := flag.Bool("tls-skip-verify", false, "https 自签证书时跳过校验（内网部署选项）")
	interval := flag.Duration("interval", 30*time.Second, "指标上报周期")
	spoolFlag := flag.String("spool", "", "断网缓存目录（开发环境默认 bin-local/spool，"+
		"生产默认 "+agentcfg.DefaultSpoolDir()+"）")
	flag.Parse()

	// 运行环境决定默认路径与凭据来源。两个平台入口共用同一套解析，
	// 避免 Windows 与 Linux 的行为各自漂移。
	environment, err := env.Resolve(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	spoolDir := agentcfg.ResolveSpoolDir(*spoolFlag, environment)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// 采集侧要拿版本号上报（否则 host_info 里的 agent_version 恒为 dev）
	collect.SetVersion(version)
	collector := collect.New()

	opts := runOpts{
		code: *enrollCode, server: *server, tlsSkip: *tlsSkip,
		interval: *interval, spoolDir: spoolDir, collector: collector,
		env: environment,
	}
	if *transport == "grpc" {
		runGRPC(ctx, log, opts)
		return
	}
	runJSON(ctx, log, opts)
}

type runOpts struct {
	code      string
	server    string
	tlsSkip   bool
	interval  time.Duration
	spoolDir  string
	collector *collect.Collector
	env       env.Environment
}

// runGRPC 是默认传输层：gRPC 双向流 + bbolt 断网续传（W6）。
func runGRPC(ctx context.Context, log *slog.Logger, o runOpts) {
	if o.code != "" {
		enrollGRPC(ctx, o)
		return
	}

	cred, err := agentcfg.ResolveIdentity(o.spoolDir, o.env, os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	server := agentcfg.ResolveServer(o.server, cred)

	if err := os.MkdirAll(o.spoolDir, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "创建 spool 目录失败:", err)
		os.Exit(1)
	}
	sp, err := spool.Open(filepath.Join(o.spoolDir, "spool.db"), spool.Options{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "打开断网缓存失败:", err)
		os.Exit(1)
	}
	defer func() { _ = sp.Close() }()

	log.Info("Agent 启动（gRPC 模式）", "version", version, "server", server,
		"host_id", cred.HostID, "interval", o.interval,
		"env", o.env.Name(), "spool", o.spoolDir)
	stream := grpcstream.New(grpcstream.Options{
		Server: server, Token: cred.Token, HostID: cred.HostID,
		Interval: o.interval, TLSInsecure: o.tlsSkip,
		Spool: sp, Log: log,
	}, o.collector, version)

	if err := stream.Run(ctx); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "上报会话退出:", err)
		os.Exit(1)
	}
}

// enrollGRPC 用一次性注册码换取凭据并落盘，使后续启动无需任何参数。
func enrollGRPC(ctx context.Context, o runOpts) {
	server := agentcfg.ResolveServer(o.server, agentcfg.Credentials{})
	token, hostID, err := grpcstream.EnrollOnce(ctx, server, o.tlsSkip, o.code,
		collect.HostFingerprint(ctx), version)
	if err != nil {
		fmt.Fprintln(os.Stderr, "注册失败:", err)
		os.Exit(1)
	}

	// 写两份：token 文件（历史布局，外部脚本仍读它）+ 凭据文件（含 host_id）。
	// 任一失败都算注册没完成：注册码已在服务端消耗，本地却没落盘，必须让用户知道，
	// 否则会出现「提示注册成功、下次启动仍报缺少令牌」的死循环。
	var writeErr error
	if err := grpcstream.SaveToken(o.spoolDir, token); err != nil {
		fmt.Fprintln(os.Stderr, "写入令牌文件失败:", err)
		writeErr = err
	}
	if err := agentcfg.SaveIdentity(o.spoolDir, agentcfg.Credentials{
		Token: token, HostID: hostID, Server: server,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "写入凭据文件失败:", err)
		writeErr = err
	}

	if writeErr != nil {
		// 把值打出来，用户手工保存后仍可用环境变量方式运行，不必再换一个注册码。
		fmt.Fprintln(os.Stderr, "\n注册已在服务端完成，但本地凭据写入失败。请手工保存以下值：")
		fmt.Fprintln(os.Stderr, "  METALWATCH_AGENT_TOKEN=", token)
		fmt.Fprintln(os.Stderr, "  METALWATCH_HOST_ID=", hostID)
		os.Exit(1)
	}

	fmt.Println("注册成功，凭据已写入本地（请妥善保管）:")
	fmt.Println("  METALWATCH_AGENT_TOKEN=", token)
	fmt.Println("  METALWATCH_HOST_ID=", hostID)
	if o.env.IsDev() {
		fmt.Println("\n开发环境可直接再次启动，无需再带任何参数。")
		return
	}
	fmt.Println("\n请把以上两个值写入 systemd unit 的 EnvironmentFile。")
}

// runJSON 是兼容传输层：JSON HTTP + 文件式 spool（W6 前的通道，用于排障与旧服务端）。
func runJSON(ctx context.Context, log *slog.Logger, o runOpts) {
	// 注册路径下还没有本地凭据，地址只能来自显式参数或兜底默认值。
	var cred agentcfg.Credentials
	if o.code == "" {
		var err error
		if cred, err = agentcfg.ResolveIdentity(o.spoolDir, o.env, os.Getenv); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	server := agentcfg.ResolveServer(o.server, cred)
	client := report.NewClient(report.Options{
		Server:   server,
		SpoolDir: o.spoolDir,
		Version:  version,
	})

	if o.code != "" {
		token, hostID, err := client.Enroll(ctx, o.code, collect.HostFingerprint(ctx))
		if err != nil {
			fmt.Fprintln(os.Stderr, "注册失败:", err)
			os.Exit(1)
		}
		if err := agentcfg.SaveIdentity(o.spoolDir, agentcfg.Credentials{
			Token: token, HostID: hostID, Server: server,
		}); err != nil {
			fmt.Fprintln(os.Stderr, "凭据已获取但写入失败（请手工保存）:", err)
		}
		fmt.Println("注册成功，凭据已写入本地（请妥善保管）:")
		fmt.Println("  METALWATCH_AGENT_TOKEN=", token)
		fmt.Println("  METALWATCH_HOST_ID=", hostID)
		if o.env.IsDev() {
			fmt.Println("\n开发环境可直接再次启动，无需再带任何参数。")
			return
		}
		fmt.Println("\n请把以上两个值写入 systemd unit 的 EnvironmentFile。")
		return
	}

	client.SetToken(cred.Token)
	log.Info("Agent 启动（JSON 兼容模式）", "version", version, "server", server,
		"host_id", cred.HostID, "env", o.env.Name())
	go heartbeat.New(client, cred.HostID, 10*time.Second).Run(ctx)

	if err := report.NewReporter(client, o.collector, o.interval, cred.HostID).Run(ctx); err != nil &&
		ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "上报循环退出:", err)
		os.Exit(1)
	}
}
