//go:build windows

// MetalWatch Agent —— Windows 平台入口。
//
// 采集方式：PowerShell CIM（WMI）查询，Server Core 亦可；不引入 cgo 依赖。
// 以 LocalSystem 运行（注册为 Windows 服务），因为读 WMI 硬件类需要权限。
//
// 传输层（--transport）：
//   - grpc（默认）：gRPC 双向流 + bbolt 断网续传 + 指数退避重连（W6）
//   - json：JSON HTTP 兼容模式，用于排障与旧服务端对接
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

	"github.com/LarryMKott/metalwatch/agent/internal/collect"
	"github.com/LarryMKott/metalwatch/agent/internal/grpcstream"
	"github.com/LarryMKott/metalwatch/agent/internal/heartbeat"
	"github.com/LarryMKott/metalwatch/agent/internal/report"
	"github.com/LarryMKott/metalwatch/agent/internal/spool"
)

const version = "0.1.0"

func main() {
	enrollCode := flag.String("code", "", "一次性注册码（首次接入使用）")
	server := flag.String("server", "http://127.0.0.1:18080", "服务端地址（https 时启用 TLS）")
	transport := flag.String("transport", "grpc", "传输协议：grpc（默认，双向流+断网续传）或 json（兼容模式）")
	tlsSkip := flag.Bool("tls-skip-verify", false, "https 自签证书时跳过校验（内网部署选项）")
	interval := flag.Duration("interval", 30*time.Second, "指标上报周期")
	// Windows 默认落在 ProgramData，避免写入系统目录触发权限问题
	spoolDir := flag.String("spool", "C:\\ProgramData\\MetalWatch\\spool", "断网缓存目录")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	defer stop()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// 采集侧要拿版本号上报（否则 host_info 里的 agent_version 恒为 dev）
	collect.SetVersion(version)
	collector := collect.New()

	opts := runOpts{
		code: *enrollCode, server: *server, tlsSkip: *tlsSkip,
		interval: *interval, spoolDir: *spoolDir, collector: collector,
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
}

// runGRPC 是默认传输层：gRPC 双向流 + bbolt 断网续传（W6）。
func runGRPC(ctx context.Context, log *slog.Logger, o runOpts) {
	if o.code != "" {
		token, hostID, err := grpcstream.EnrollOnce(ctx, o.server, o.tlsSkip, o.code,
			collect.HostFingerprint(ctx), version)
		if err != nil {
			fmt.Fprintln(os.Stderr, "注册失败:", err)
			os.Exit(1)
		}
		if err := grpcstream.SaveToken(o.spoolDir, token); err != nil {
			fmt.Fprintln(os.Stderr, "令牌已获取但写入失败（请手工保存）:", err)
		}
		fmt.Println("注册成功，令牌已写入本地（请妥善保管）:")
		fmt.Println("  METALWATCH_AGENT_TOKEN=", token)
		fmt.Println("  METALWATCH_HOST_ID=", hostID)
		fmt.Println("\n请把以上两个值写入 Windows 服务配置的环境变量。")
		return
	}

	token := os.Getenv("METALWATCH_AGENT_TOKEN")
	if token == "" {
		fmt.Fprintln(os.Stderr, "缺少令牌：请设置 METALWATCH_AGENT_TOKEN，或先用 --code 完成注册")
		os.Exit(1)
	}
	hostID := report.HostIDFromEnv()
	if hostID == 0 {
		fmt.Fprintln(os.Stderr, "缺少 host_id：请设置 METALWATCH_HOST_ID（注册时由服务端分配）")
		os.Exit(1)
	}

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

	log.Info("Agent 启动（gRPC 模式）", "version", version, "server", o.server,
		"host_id", hostID, "interval", o.interval)
	stream := grpcstream.New(grpcstream.Options{
		Server: o.server, Token: token, HostID: hostID,
		Interval: o.interval, TLSInsecure: o.tlsSkip,
		Spool: sp, Log: log,
	}, o.collector, version)

	if err := stream.Run(ctx); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "上报会话退出:", err)
		os.Exit(1)
	}
}

// runJSON 是兼容传输层：JSON HTTP + 文件式 spool（W6 前的通道，用于排障与旧服务端）。
func runJSON(ctx context.Context, log *slog.Logger, o runOpts) {
	client := report.NewClient(report.Options{
		Server:   o.server,
		SpoolDir: o.spoolDir,
		Version:  version,
	})

	if o.code != "" {
		token, err := client.Enroll(ctx, o.code, collect.HostFingerprint(ctx))
		if err != nil {
			fmt.Fprintln(os.Stderr, "注册失败:", err)
			os.Exit(1)
		}
		fmt.Println("注册成功，令牌已写入本地（请妥善保管）:")
		fmt.Println(" ", token)
		fmt.Println("\n请在 Windows 服务配置中通过环境变量提供该令牌：")
		fmt.Println("  METALWATCH_AGENT_TOKEN=<令牌>")
		fmt.Println("  METALWATCH_HOST_ID=<服务端返回的 host_id>")
		return
	}

	token := os.Getenv("METALWATCH_AGENT_TOKEN")
	if token == "" {
		fmt.Fprintln(os.Stderr, "缺少令牌：请设置 METALWATCH_AGENT_TOKEN，或先用 --code 完成注册")
		os.Exit(1)
	}
	client.SetToken(token)

	log.Info("Agent 启动（JSON 兼容模式）", "version", version, "server", o.server)
	go heartbeat.New(client, report.HostIDFromEnv(), 10*time.Second).Run(ctx)

	if err := report.NewReporter(client, o.collector, o.interval).Run(ctx); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "上报循环退出:", err)
		os.Exit(1)
	}
}
