// Package heartbeat 负责 Agent 的心跳保活，独立于采集循环运行。
package heartbeat

import (
	"context"
	"log"
	"time"
)

// Sender 是心跳发送端（由 report.Client 实现，避免本包依赖具体 HTTP 实现）。
type Sender interface {
	Heartbeat(ctx context.Context, hostID int64, uptimeSec uint64) error
}

// Worker 周期性发送心跳。
type Worker struct {
	sender   Sender
	interval time.Duration
	started  time.Time
	hostID   int64
}

// New 构造心跳器。interval 建议为采集周期的 2~3 倍（默认 10s）。
func New(sender Sender, interval time.Duration) *Worker {
	if interval <= 0 {
		interval = 20 * time.Second
	}
	return &Worker{sender: sender, interval: interval, started: time.Now()}
}

// SetHostID 设置服务端分配的 host_id。
func (w *Worker) SetHostID(id int64) { w.hostID = id }

// Run 阻塞运行直到 ctx 取消。
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			uptime := uint64(time.Since(w.started).Seconds())
			if err := w.sender.Heartbeat(ctx, w.hostID, uptime); err != nil {
				// 心跳失败只记日志：连续失败由服务端侧判定 agent_offline，避免 Agent 自造告警
				log.Printf("heartbeat: %v", err)
			}
		}
	}
}
