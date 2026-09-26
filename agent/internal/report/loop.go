package report

import (
	"context"
	"time"

	"github.com/LarryMKott/metalwatch/agent/internal/model"
)

// Collector 是 Reporter 依赖的采集能力（消费方按需定义接口，D31 第 4 条；
// *collect.Collector 天然满足）。report 包不感知采集的具体实现与平台差异。
type Collector interface {
	Collect(ctx context.Context) (model.Report, error)
}

// 最短上报周期：协议约定下限，防止误配把服务端打爆。
const minInterval = 10 * time.Second

// Reporter 周期性驱动「采集 → 上报」循环，直到 ctx 取消。
type Reporter struct {
	client    *Client
	collector Collector
	interval  time.Duration
	hostID    int64
}

// NewReporter 构造上报循环，完成依赖注入与参数归一（interval 低于下限时钳到下限）。
//
// hostID 由调用方解析后注入，本包不再自己读环境变量：开发环境的 host_id
// 可能来自本地凭据文件而非环境变量，凭据来源必须只有一处决策
// （见 agentcfg.ResolveIdentity）。
func NewReporter(client *Client, collector Collector, interval time.Duration, hostID int64) *Reporter {
	if interval < minInterval {
		interval = minInterval
	}
	return &Reporter{
		client:    client,
		collector: collector,
		interval:  interval,
		hostID:    hostID,
	}
}

// Run 阻塞执行采集上报循环。
//
// 关键约定：
//   - 采集失败不中断循环（单机偶发失败是常态，靠服务端的 collect_failed 度量暴露）
//   - 每次上报都带 batch_id，服务端据此做幂等
//   - 心跳由独立 goroutine 负责（见 heartbeat 包），这里只管采集上报
func (r *Reporter) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	// 启动先采一次，避免等一个周期才有数据
	if err := r.cycle(ctx); err != nil && ctx.Err() != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := r.cycle(ctx); err != nil && ctx.Err() != nil {
				return err
			}
		}
	}
}

func (r *Reporter) cycle(ctx context.Context) error {
	rep, _ := r.collector.Collect(ctx) // 部分失败时仍上报已采集到的数据
	return r.client.Report(ctx, r.hostID, rep)
}
