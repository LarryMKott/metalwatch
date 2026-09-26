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
//
// 文件分工：
//
//	options.go   命令行参数与「生效配置」两个值对象（含全部默认值规则）
//	bootstrap.go 公共准备：日志 / 退出信号 / 元数据库 / 结构迁移
//	commands.go  子命令接口，以及 migrate、agent-code 两个实现
//	runtime.go   serve 子命令：协作对象装配与生命周期
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/LarryMKott/metalwatch/pkg/env"
)

// version 由构建时注入：-ldflags "-X main.version=x.y.z"。
var version = "0.1.0"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "启动失败:", err)
		os.Exit(1)
	}
}

// run 解析子命令与参数，装配 bootstrap，然后交给对应 command 执行。
//
// 它只做「判定环境 → 选择子命令 → 串联」三件事：具体的默认值规则在
// resolveSettings，资源准备在 newBootstrap，实际行为在 command 实现里。
// 原先这里是一个 116 行的函数，把参数解析、默认值补齐、日志、信号、
// 开库、迁移与三个子命令的分发全揽在身上。
func run() error {
	// 运行环境先于一切解析：它决定所有「未显式指定」时的默认值（docs/01 D35）。
	environment, err := env.Resolve(os.Getenv)
	if err != nil {
		return err
	}

	// 首参不以 '-' 开头时视为子命令名，其余参数交给 flag。
	name, args := cmdServe, os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[0], args[1:]
	}

	fs := flag.NewFlagSet("metalwatch "+name, flag.ExitOnError)
	o := newOptions(fs)
	if err := o.parse(fs, args); err != nil {
		return err
	}
	if o.migrateOnly {
		name = cmdMigrate
	}

	s, err := resolveSettings(o, environment)
	if err != nil {
		return err
	}

	// 非 serve 子命令与开发环境都把日志打到终端：前者用户就等着看输出，
	// 后者没有 FPK 生命周期脚本帮忙提示，只落文件等于「起没起来全靠猜」。
	boot, err := newBootstrap(s, name != cmdServe || s.env.IsDev())
	if err != nil {
		return err
	}
	defer boot.close()

	cmd, err := boot.newCommand(name, o)
	if err != nil {
		return err
	}
	return cmd.run(boot.ctx)
}
