//go:build windows

// MetalWatch Agent —— Windows 平台入口。
//
// 采集方式：PowerShell CIM（WMI）查询，Server Core 亦可；不引入 cgo 依赖。
// 以 LocalSystem 运行（注册为 Windows 服务），因为读 WMI 硬件类需要权限。
//
// 本文件只声明平台差异（信号集与凭据落点指引）；环境判定、凭据解析、
// 传输层选择与注册流程全在 internal/agentapp，与 Linux 入口共用同一份实现。
//
// 传输层（--transport）：
//   - grpc（默认）：gRPC 双向流 + bbolt 断网续传 + 指数退避重连（W6）
//   - json：JSON HTTP 兼容模式，用于排障与旧服务端对接
//
// 运行环境（METALWATCH_ENV，见 docs/01 D35）：
//   - dev：spool 落仓库内 bin-local/spool；令牌与 host_id 自动读本地凭据，
//     注册一次后即可无参数直接运行
//   - prod：spool 落 ProgramData；凭据必须由服务配置显式注入环境变量
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

// windowsPlatform 是 Agent 在 Windows 上的差异面。
type windowsPlatform struct{}

// CredentialHint 指向 Windows 服务的凭据注入方式（生产环境唯一认可的来源）。
func (windowsPlatform) CredentialHint() string { return "Windows 服务配置的环境变量" }

// Signals 捕获 Ctrl+C、服务停止与操作系统中断。
func (windowsPlatform) Signals() []os.Signal {
	return []os.Signal{syscall.SIGINT, syscall.SIGTERM, os.Interrupt}
}

func main() {
	opts, err := agentapp.NewFlags(flag.CommandLine).Parse(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	app, err := agentapp.New(windowsPlatform{}, version, opts)
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
