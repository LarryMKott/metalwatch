// MetalWatch Agent —— Linux 平台入口。
//
// 采集方式：直接读 /sys/class/hwmon 与 /proc，不依赖 lm-sensors；SMART 走随包内置的 smartctl。
// 以 root 运行（systemd unit），因为读 DMI 与磁盘设备需要权限。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gitee.com/zhangyilin_233/metalwatch/agent/internal/collect"
	"gitee.com/zhangyilin_233/metalwatch/agent/internal/heartbeat"
	"gitee.com/zhangyilin_233/metalwatch/agent/internal/report"
)

const version = "0.1.0"

func main() {
	enrollCode := flag.String("code", "", "一次性注册码（首次接入使用）")
	server := flag.String("server", "https://127.0.0.1:18080", "服务端地址")
	interval := flag.Duration("interval", 30*time.Second, "指标上报周期")
	spoolDir := flag.String("spool", "/var/lib/metalwatch-agent/spool", "断网缓存目录")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	client := report.NewClient(report.Options{
		Server:   *server,
		SpoolDir: *spoolDir,
		Version:  version,
	})

	if *enrollCode != "" {
		token, err := client.Enroll(ctx, *enrollCode, collect.HostFingerprint(ctx))
		if err != nil {
			fmt.Fprintln(os.Stderr, "注册失败:", err)
			os.Exit(1)
		}
		fmt.Println("注册成功，令牌已写入本地（请妥善保管）:")
		fmt.Println(" ", token)
		fmt.Println("\n请在 systemd unit 中通过 --token 或 EnvironmentFile 提供该令牌。")
		return
	}

	token := os.Getenv("METALWATCH_AGENT_TOKEN")
	if token == "" {
		fmt.Fprintln(os.Stderr, "缺少令牌：请设置 METALWATCH_AGENT_TOKEN，或先用 --code 完成注册")
		os.Exit(1)
	}
	client.SetToken(token)

	collector := collect.New()
	go heartbeat.New(client, 10*time.Second).Run(ctx)

	if err := report.Loop(ctx, client, collector, *interval); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "上报循环退出:", err)
		os.Exit(1)
	}
}
