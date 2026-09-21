package report

import (
	"context"
	"os"
	"strconv"
	"time"

	"gitee.com/zhangyilin_233/metalwatch/agent/internal/collect"
)

// Loop 按固定周期采集并上报，直到 ctx 取消。
//
// 关键约定：
//   - 采集失败不中断循环（单机偶发失败是常态，靠服务端的 collect_failed 度量暴露）
//   - 每次上报都带自增序列号与 batch_id，服务端据此做幂等
//   - 心跳由独立 goroutine 负责（见 heartbeat 包），这里只管采集上报
func Loop(ctx context.Context, c *Client, collector *collect.Collector, interval time.Duration) error {
	if interval < 10*time.Second {
		interval = 10 * time.Second
	}

	hostID := hostIDFromEnv()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// 启动先采一次，避免等一个周期才有数据
	if err := cycle(ctx, c, collector, hostID); err != nil && ctx.Err() != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := cycle(ctx, c, collector, hostID); err != nil && ctx.Err() != nil {
				return err
			}
		}
	}
}

func cycle(ctx context.Context, c *Client, collector *collect.Collector, hostID int64) error {
	rep, _ := collector.Collect(ctx) // 部分失败时仍上报已采集到的数据
	return c.Report(ctx, hostID, rep)
}

// hostIDFromEnv 读取注册时服务端分配的 host_id。
// 由 systemd EnvironmentFile 或 Windows 服务配置注入；缺失时返回 0，服务端会以 403 提示。
func hostIDFromEnv() int64 {
	v, err := strconv.ParseInt(os.Getenv("METALWATCH_HOST_ID"), 10, 64)
	if err != nil {
		return 0
	}
	return v
}
