// Package grpcstream 是 Agent 的 gRPC 双向流客户端（W6）：
// 长连接上报指标/心跳、接收下行命令；断网时采集数据落入 spool 队列，
// 重连成功后按采集时间序补传；重连采用指数退避 + 随机抖动（docs/03 §3.4）。
//
// 与 JSON 通道（internal/report）的关系：这是 W6 计划的正式传输层，
// JSON 通道经 --transport json 保留作兼容/排障路径。
package grpcstream

import (
	"math/rand/v2"
	"time"
)

const (
	baseBackoff = time.Second
	maxBackoff  = 60 * time.Second
)

// backoff 计算第 attempt 次重连（从 0 起）的等待时长：
// 1s → 2s → 4s → … → 封顶 60s，再叠加 [-25%, +75%) 的随机抖动，避免重连风暴。
func backoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	d := baseBackoff << min(attempt, 6) // 1<<6 = 64s → 截到 60s
	if d > maxBackoff {
		d = maxBackoff
	}
	jitter := rand.Int64N(int64(d)+1) - int64(d)/4 // [-d/4, 3d/4]
	out := time.Duration(int64(d) + jitter)
	if out < 100*time.Millisecond {
		out = 100 * time.Millisecond
	}
	return out
}
