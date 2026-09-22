package service

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	"github.com/LarryMKott/metalwatch/internal/pipeline"
)

// OfflineMonitor 周期判定 Agent 离线（M3 验收 3）：
//   - online 主机超过 staleAfter（2×采集周期）未上报 → 置 offline、写 up=0 样本、产生 agent_offline 告警；
//   - 已有 agent_offline 在触发中的主机恢复上报（report 路径已置回 online）→ 自动 resolved + agent.online 通知。
//
// 判定是幂等的：重复 Tick 不会产生重复告警（active_key 去重，UpsertFiring 刷新不通知）。
type OfflineMonitor struct {
	store      adapter.MetadataStore
	pipe       *pipeline.Pipeline // up=0 样本落点；可为 nil
	alerts     *AlertService
	staleAfter time.Duration
	tickEvery  time.Duration
	log        *slog.Logger

	mu     sync.Mutex
	raised map[int64]struct{} // 本进程标记过离线的主机（供测试与日志，判定以 DB 为准）
}

// NewOfflineMonitor 构造离线监视器。staleAfter 默认 2 分钟，tick 周期默认 15s。
func NewOfflineMonitor(store adapter.MetadataStore, pipe *pipeline.Pipeline,
	alerts *AlertService, staleAfter, tickEvery time.Duration, log *slog.Logger) *OfflineMonitor {
	if staleAfter <= 0 {
		staleAfter = 2 * time.Minute
	}
	if tickEvery <= 0 {
		tickEvery = 15 * time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	return &OfflineMonitor{
		store: store, pipe: pipe, alerts: alerts,
		staleAfter: staleAfter, tickEvery: tickEvery,
		log: log, raised: map[int64]struct{}{},
	}
}

// Run 阻塞执行周期判定，直到 ctx 取消。
func (m *OfflineMonitor) Run(ctx context.Context) {
	ticker := time.NewTicker(m.tickEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.Tick(ctx)
		}
	}
}

// Tick 执行一轮离线判定与恢复检测。
func (m *OfflineMonitor) Tick(ctx context.Context) {
	now := time.Now().UTC()
	hosts, _, err := m.store.Hosts().List(ctx, adapter.ListFilter{Limit: 500})
	if err != nil {
		m.log.Warn("离线判定读取主机失败", "err", err)
		return
	}
	for _, h := range hosts {
		if h.Status != "online" || h.LastSeenAt == nil {
			continue
		}
		if now.Sub(*h.LastSeenAt) <= m.staleAfter {
			continue
		}
		// 置离线 + up=0 样本（M3：metalwatch_up 归零）+ agent_offline 告警
		if err := m.store.Hosts().UpdateStatus(ctx, h.ID, "offline", now); err != nil {
			m.log.Warn("离线状态写入失败", "host_id", h.ID, "err", err)
		}
		if m.pipe != nil {
			m.pipe.Ingest([]adapter.Sample{{
				Metric: "up", Value: 0,
				Labels: map[string]string{
					"host_id": strconv.FormatInt(h.ID, 10), "host": h.Hostname, "mode": "agent",
				},
				TS: now,
			}})
		}
		if err := m.alerts.RaiseAgentOffline(ctx, h, now); err != nil {
			m.log.Warn("agent_offline 告警写入失败", "host_id", h.ID, "err", err)
		}
		m.mu.Lock()
		m.raised[h.ID] = struct{}{}
		m.mu.Unlock()
		m.log.Warn("Agent 判定离线", "host_id", h.ID, "hostname", h.Hostname,
			"last_seen", h.LastSeenAt.UTC().Format(time.RFC3339))
	}

	// 恢复检测：触发中的 agent_offline，其主机已回到 online → 解除
	firing, _, err := m.store.Alerts().List(ctx, adapter.AlertFilter{
		States: []string{"firing"}, Category: "agent_offline", Limit: 500,
	})
	if err != nil {
		m.log.Warn("离线告警读取失败", "err", err)
		return
	}
	for _, e := range firing {
		if e.HostID == nil || e.State != "firing" {
			continue
		}
		host, err := m.store.Hosts().GetByID(ctx, *e.HostID)
		if err != nil || host.Status != "online" {
			continue
		}
		resolved, err := m.alerts.ResolveAgentOffline(ctx, host, now)
		if err != nil {
			m.log.Warn("agent_offline 解除失败", "host_id", host.ID, "err", err)
			continue
		}
		if resolved {
			m.mu.Lock()
			delete(m.raised, host.ID)
			m.mu.Unlock()
			m.log.Info("Agent 恢复在线", "host_id", host.ID, "hostname", host.Hostname)
		}
	}
}
