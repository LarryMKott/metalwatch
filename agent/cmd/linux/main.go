//go:build linux

// MetalWatch Agent —— Linux 平台入口。
//
// 采集方式：直接读 /sys/class/hwmon 与 /proc，不依赖 lm-sensors；SMART 走随包内置的 smartctl。
// 以 root 运行（systemd unit），因为读 DMI 与磁盘设备需要权限。
//
// 本文件只声明平台差异（信号集与凭据落点指引）；环境判定、凭据解析、
// 传输层选择与注册流程全在 internal/agentapp，与 Windows 入口共用同一份实现。
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
	"flag"
	"fmt"
	"os"
	"syscall"

	"github.com/LarryMKott/metalwatch/agent/internal/agentapp"
)

// version 是 Agent 版本号，上报进 host_info.agent_version。
// 构建脚本可用 -ldflags "-X main.version=x.y.z" 覆盖。
var version = "0.1.0"

// linuxPlatform 是 Agent 在 Linux 上的差异面。
type linuxPlatform struct{}

// CredentialHint 指向 systemd 的凭据注入方式（生产环境唯一认可的来源）。
func (linuxPlatform) CredentialHint() string { return "systemd unit 的 EnvironmentFile" }

// Signals 捕获 Ctrl+C 与 systemd 的停止信号。
func (linuxPlatform) Signals() []os.Signal { return []os.Signal{syscall.SIGINT, syscall.SIGTERM} }

func main() {
	opts, err := agentapp.NewFlags(flag.CommandLine).Parse(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	app, err := agentapp.New(linuxPlatform{}, version, opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	ctx, stop := app.SignalContext()
	defer stop()

	if err := app.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
