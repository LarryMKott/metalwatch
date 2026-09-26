package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	"github.com/LarryMKott/metalwatch/internal/engine"
	"github.com/LarryMKott/metalwatch/internal/notify"
)

// Notifier 是出站通知的抽象（由 notify.Notifier 实现，docs/04 §5）。
type Notifier interface {
	AlertEvent(ctx context.Context, p *notify.EventPayload)
}

// Broadcaster 是 WebSocket 实时推送的抽象（由 ws.Hub 实现，docs/03 §7）。
type Broadcaster interface {
	BroadcastJSON(v any)
}

// AlertDeps 是告警服务的可选外联依赖；nil 表示对应能力未启用。
type AlertDeps struct {
	Notifier    Notifier
	Broadcaster Broadcaster
}

// ruleMeta 是规则背后的落库元数据：事件写入 alert_event 时还原 category/threshold 等。
type ruleMeta struct {
	title     string // 模板名，作为事件标题（前端「规则」列）
	category  string
	metric    string
	op        string
	threshold float64
	hostID    int64 // 单主机覆盖专属；模板规则为 0
}

// AlertService 把阈值规则接入告警引擎：上报样本 → 引擎评估 → 事件落库 → 通知/推送。
// 引擎（engine.AlarmEngine）是纯逻辑；本服务负责规则装配、对象维度提取、持久化与外联。
type AlertService struct {
	store       adapter.MetadataStore
	eng         *engine.AlarmEngine
	thr         *adapter.ThresholdRepo
	alerts      *adapter.AlertEventRepo
	notifier    Notifier
	broadcaster Broadcaster
	log         *slog.Logger

	mu    sync.RWMutex
	rules *ruleSet // 不可变规则集，ReloadRules 整体替换（读写锁保护指针）

	// worker 非 nil 时 dispatch 走异步投递（见 StartDispatch）。
	// 未启动（单测、未接线）时退化为同步，保持既有语义。
	worker atomic.Pointer[dispatchWorker]
}

// dispatchItem 是一次待投递的通知。
type dispatchItem struct {
	event *adapter.AlertEvent
	kind  string
	at    time.Time
}

// dispatchWorker 是异步投递的后台工作者。
type dispatchWorker struct {
	ch chan dispatchItem
}

// StartDispatch 启动异步通知投递，必须在 serve 期间调用一次。
//
// 为什么需要它：dispatch 会走 webhook 投递，而 webhook 对不可达目标最坏要
// 4×10s 超时 + 0/1/5/25s 退避 ≈ 71s/渠道，多渠道串行。原先是同步调用，
// 且发生在 Agent 上报协程里 —— 目标一挂，上报链路就被拖住、断网补传越积越多。
//
// 队列满时丢弃并记 warn：事件已经落库（WebUI 仍可查），通知可丢，上报链路不能被拖住。
func (s *AlertService) StartDispatch(ctx context.Context, workers, queue int) {
	if workers < 1 {
		workers = 1
	}
	if queue < 1 {
		queue = 128
	}
	w := &dispatchWorker{ch: make(chan dispatchItem, queue)}
	s.worker.Store(w)

	for i := 0; i < workers; i++ {
		go func() {
			for {
				select {
				case it := <-w.ch:
					s.dispatchSync(ctx, it.event, it.kind, it.at)
				case <-ctx.Done():
					// 退出时不强求排空：进程正在停，投递用同一个已取消的 ctx 也发不出去。
					return
				}
			}
		}()
	}
	s.log.Info("告警通知改为异步投递", "workers", workers, "queue", queue)
}

// NewAlertService 构造告警服务。store 供通知载荷补全主机信息；extra 两项允许为零值。
func NewAlertService(store adapter.MetadataStore, thr *adapter.ThresholdRepo,
	alerts *adapter.AlertEventRepo, extra AlertDeps, log *slog.Logger) *AlertService {
	if log == nil {
		log = slog.Default()
	}
	return &AlertService{
		store:       store,
		eng:         engine.NewAlarmEngine(),
		thr:         thr,
		alerts:      alerts,
		notifier:    extra.Notifier,
		broadcaster: extra.Broadcaster,
		log:         log,
		rules:       emptyRuleSet(),
	}
}

// SeedBuiltinTemplates 幂等写入内置阈值模板（UNIQUE(metric,name) 冲突时跳过）。
// 阈值取值参考 docs/04 模块验收 M6 的分级示例，真实环境按需在 WebUI 调整。
func (s *AlertService) SeedBuiltinTemplates(ctx context.Context) error {
	f := func(v float64) *float64 { return &v }
	return s.thr.SeedBuiltin(ctx, []adapter.ThresholdTemplate{
		{Name: "CPU 温度过高", Metric: "cpu_temp_celsius", Category: "sensor", Op: "gt",
			WarnValue: f(75), CritValue: f(85), DurationSec: 120},
		{Name: "磁盘温度过高", Metric: "disk_temp_celsius", Category: "smart", Op: "gt",
			WarnValue: f(45), CritValue: f(60), DurationSec: 120},
		{Name: "SMART 预警", Metric: "disk_smart_status", Category: "smart", Op: "eq",
			WarnValue: f(1), CritValue: f(2), DurationSec: 0},
		// 取值约定 0=Optimal 1=Degraded 2=Failed 3=Rebuilding；单 op 模板用 eq 分级：
		// 降级(1)→major，失效(2)→critical，重建中(3)不告警（进度走 raid_rebuild_percent）
		{Name: "RAID 阵列异常", Metric: "raid_state", Category: "raid", Op: "eq",
			WarnValue: f(1), CritValue: f(2), DurationSec: 0},
		{Name: "风扇转速异常", Metric: "fan_rpm", Category: "fan", Op: "lt",
			WarnValue: f(500), CritValue: f(100), DurationSec: 120},
		{Name: "电源失效", Metric: "psu_status", Category: "psu", Op: "eq",
			CritValue: f(0), DurationSec: 0},
	})
}

// ruleSet 是一份不可变的规则装配结果：模板按指标聚合，覆盖按主机×指标聚合，
// metas 供事件落库还原元数据。构造完成后只读，可被任意 goroutine 并发使用；
// 规则变更通过构建新实例整体替换（copy-on-write）。
type ruleSet struct {
	// templates: metric → 该指标的模板规则（Host 为空，匹配所有主机）
	templates map[string][]engine.AlarmRule
	// overrides: hostname → metric → 覆盖规则（优先于模板，M6 验收 2）
	overrides map[string]map[string][]engine.AlarmRule
	metas     map[string]ruleMeta // ruleID → 落库元数据
}

func emptyRuleSet() *ruleSet {
	return &ruleSet{
		templates: map[string][]engine.AlarmRule{},
		overrides: map[string]map[string][]engine.AlarmRule{},
		metas:     map[string]ruleMeta{},
	}
}

// buildRuleSet 把启用的模板与覆盖装配成一份新的不可变规则集。
func buildRuleSet(tpls []adapter.ThresholdTemplate, ovs []adapter.ThresholdOverride) *ruleSet {
	rs := emptyRuleSet()

	for _, t := range tpls {
		critID := ""
		if t.CritValue != nil {
			critID = fmt.Sprintf("tpl-%d-crit", t.ID)
			rs.templates[t.Metric] = append(rs.templates[t.Metric], engine.AlarmRule{
				ID: critID, Metric: t.Metric, Comparator: comparatorOf(t.Op),
				Threshold: *t.CritValue, ForDuration: time.Duration(t.DurationSec) * time.Second,
				Level: "critical", Enabled: true,
			})
			rs.metas[critID] = ruleMeta{title: t.Name, category: t.Category, metric: t.Metric,
				op: t.Op, threshold: *t.CritValue}
		}
		if t.WarnValue != nil {
			warnID := fmt.Sprintf("tpl-%d-warn", t.ID)
			rs.templates[t.Metric] = append(rs.templates[t.Metric], engine.AlarmRule{
				ID: warnID, Metric: t.Metric, Comparator: comparatorOf(t.Op),
				Threshold: *t.WarnValue, ForDuration: time.Duration(t.DurationSec) * time.Second,
				// 严重级触发时抑制同级警告，避免 RAID Failed 同时报「阵列异常」两条
				Level: "major", Parent: critID, Enabled: true,
			})
			rs.metas[warnID] = ruleMeta{title: t.Name, category: t.Category, metric: t.Metric,
				op: t.Op, threshold: *t.WarnValue}
		}
	}

	for _, o := range ovs {
		dur := o.DurationSec
		if dur == 0 {
			// 未显式给 duration 的覆盖沿用同名模板的持续判定，避免覆盖反而更灵敏
			for _, t := range tpls {
				if t.Metric == o.Metric {
					dur = t.DurationSec
				}
			}
		}
		if rs.overrides[o.Hostname] == nil {
			rs.overrides[o.Hostname] = map[string][]engine.AlarmRule{}
		}
		critID := ""
		if o.CritValue != nil {
			critID = fmt.Sprintf("ovr-%d-crit", o.ID)
			rs.overrides[o.Hostname][o.Metric] = append(rs.overrides[o.Hostname][o.Metric], engine.AlarmRule{
				ID: critID, Metric: o.Metric, Comparator: comparatorOf(o.Op),
				Threshold: *o.CritValue, ForDuration: time.Duration(dur) * time.Second,
				Level: "critical", Host: o.Hostname, Enabled: true,
			})
			rs.metas[critID] = ruleMeta{title: "覆盖 · " + o.Metric, category: "sensor",
				metric: o.Metric, op: o.Op, threshold: *o.CritValue, hostID: o.HostID}
		}
		if o.WarnValue != nil {
			warnID := fmt.Sprintf("ovr-%d-warn", o.ID)
			rs.overrides[o.Hostname][o.Metric] = append(rs.overrides[o.Hostname][o.Metric], engine.AlarmRule{
				ID: warnID, Metric: o.Metric, Comparator: comparatorOf(o.Op),
				Threshold: *o.WarnValue, ForDuration: time.Duration(dur) * time.Second,
				Level: "major", Host: o.Hostname, Parent: critID, Enabled: true,
			})
			rs.metas[warnID] = ruleMeta{title: "覆盖 · " + o.Metric, category: "sensor",
				metric: o.Metric, op: o.Op, threshold: *o.WarnValue, hostID: o.HostID}
		}
	}
	return rs
}

// rulesFor 返回一台主机适用的规则集：有覆盖的指标用覆盖，其余用模板（同批互斥，避免双重告警）。
func (rs *ruleSet) rulesFor(hostname string) ([]engine.AlarmRule, map[string]ruleMeta) {
	ov, hasOv := rs.overrides[hostname]
	rules := make([]engine.AlarmRule, 0, len(rs.templates))
	for metric, r := range rs.templates {
		if _, covered := ov[metric]; hasOv && covered {
			continue
		}
		rules = append(rules, r...)
	}
	if hasOv {
		for _, r := range ov {
			rules = append(rules, r...)
		}
	}
	return rules, rs.metas
}

// ReloadRules 全量加载启用的模板与覆盖规则。规则变更后调用即可生效（当前无规则 CRUD 接口，
// 由服务端定期刷新 + 启动时加载兜底）。
func (s *AlertService) ReloadRules(ctx context.Context) error {
	tpls, err := s.thr.ListTemplates(ctx, true)
	if err != nil {
		return fmt.Errorf("读取阈值模板失败: %w", err)
	}
	ovs, err := s.thr.ListOverrides(ctx)
	if err != nil {
		return fmt.Errorf("读取阈值覆盖失败: %w", err)
	}

	next := buildRuleSet(tpls, ovs)
	s.mu.Lock()
	s.rules = next
	s.mu.Unlock()
	s.log.Info("告警规则已加载", "templates", len(tpls), "overrides", len(ovs))
	return nil
}

// currentRules 取当前生效的规则集（不可变，取 出 后可无锁使用）。
func (s *AlertService) currentRules() *ruleSet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rules
}

// Evaluate 处理一台主机的一批上报样本：命中规则产生的事件写入 alert_event。
// 在 HTTP 处理路径上同步调用（阈值判定用原始值，不等时序落库，docs/03 §2.2）。
func (s *AlertService) Evaluate(ctx context.Context, hostID int64, hostname string,
	samples []adapter.Sample, at time.Time) {

	rules, metas := s.currentRules().rulesFor(hostname)
	if len(rules) == 0 {
		return
	}

	// 提取对象维度并转换为引擎样本；记录每条时间线的最新值供事件落库
	valueOf := map[string]float64{}
	es := make([]engine.Sample, 0, len(samples))
	for _, sm := range samples {
		obj := objectOf(sm.Labels)
		valueOf[sm.Metric+"\x1f"+obj] = sm.Value
		es = append(es, engine.Sample{
			Metric: sm.Metric, Value: sm.Value, Host: hostname, Object: obj, At: at,
		})
	}

	// 以采样时间为引擎时钟：持续时间/抑制窗口都在数据时间轴上度量，
	// 断网补传（spool 回放数小时前的批次）也能得到与实时一致的计算结果。
	for _, ev := range s.eng.Process(es, rules, at) {
		meta, ok := metas[ev.RuleID]
		if !ok {
			continue
		}
		val := valueOf[meta.metric+"\x1f"+ev.Object]

		switch ev.State {
		case engine.StateFiring:
			e := s.toEvent(ev, meta, hostID, hostname, val)
			created, id, err := s.alerts.UpsertFiring(ctx, e, ev.ActiveKey, formatUTC(at))
			if err != nil {
				s.log.Warn("告警事件写入失败", "key", ev.ActiveKey, "err", err)
				continue
			}
			if created {
				// 回填事件 ID：通知载荷与 notify_state 回写都依赖它
				// （此前 ID 被丢弃，payload 里 id=0，notify_state 永远停在 pending）
				e.ID = id
				// 只在「新触发」时对外通知；抑制窗口内的重复触发仅刷新落库行
				s.dispatch(ctx, e, "alert.firing", at)
			}
		case engine.StateRecovered:
			resolved, err := s.alerts.Resolve(ctx, ev.ActiveKey, formatUTC(at))
			if err != nil {
				s.log.Warn("告警恢复写入失败", "key", ev.ActiveKey, "err", err)
				continue
			}
			if resolved {
				s.dispatch(ctx, &adapter.AlertEvent{
					HostID: &hostID, Severity: sevOf(ev.Level), Category: meta.category,
					Metric: &meta.metric, FirstSeenAt: at, LastSeenAt: at,
				}, "alert.resolved", at)
			}
		case engine.StateSuppressed:
			// 抑制窗口内重复触发：仅刷新既有 firing 行的 last_seen，不产生新行
			if err := s.alerts.Touch(ctx, ev.ActiveKey, formatUTC(at), &val); err != nil {
				s.log.Warn("告警刷新失败", "key", ev.ActiveKey, "err", err)
			}
		case engine.StateMuted:
			if err := s.alerts.InsertSilenced(ctx, s.toEvent(ev, meta, hostID, hostname, val), formatUTC(at)); err != nil {
				s.log.Warn("维护窗口事件写入失败", "key", ev.ActiveKey, "err", err)
			}
		}
	}
}

func (s *AlertService) toEvent(ev engine.AlarmEvent, meta ruleMeta, hostID int64, hostname string, val float64) *adapter.AlertEvent {
	sev := "major"
	if ev.Level == "critical" {
		sev = "critical"
	}
	obj := strings.TrimSpace(ev.Object)
	objPtr := (*string)(nil)
	if obj != "" {
		objPtr = &obj
	}
	host := &hostID
	detail, _ := json.Marshal(map[string]any{
		"rule_id": ev.RuleID, "metric": meta.metric, "op": meta.op,
		"threshold": meta.threshold, "object": obj, "host": hostname,
		"value": val, "message": ev.Message,
	})
	title := fmt.Sprintf("%s：%s %s %s（阈值 %s）", meta.title, hostname, meta.metric, formatValue(val), opText(meta.op, meta.threshold))
	return &adapter.AlertEvent{
		HostID: host, Severity: sev, Category: meta.category,
		Metric: &meta.metric, ObjectName: objPtr, Value: &val, Threshold: &meta.threshold,
		Title: title, Detail: ptrString(string(detail)), State: ev.State,
	}
}

// objectOf 从样本标签提取告警对象（磁盘设备 / 槽位 / 传感器芯片）。
func objectOf(labels map[string]string) string {
	if labels == nil {
		return ""
	}
	for _, k := range []string{"device", "slot", "array", "chip", "rail"} {
		if v := strings.TrimSpace(labels[k]); v != "" {
			return v
		}
	}
	return ""
}

func comparatorOf(op string) engine.Comparator {
	switch op {
	case "lt":
		return engine.CmpLT
	case "eq":
		return engine.CmpEQ
	case "ne":
		return engine.CmpNE
	default:
		return engine.CmpGT
	}
}

func opText(op string, threshold float64) string {
	sym := map[string]string{"gt": ">", "lt": "<", "eq": "==", "ne": "!="}[op]
	if sym == "" {
		sym = op
	}
	return fmt.Sprintf("%s %g", sym, threshold)
}

func formatValue(v float64) string {
	return fmt.Sprintf("%g", v)
}

func formatUTC(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func ptrString(s string) *string { return &s }

// dispatch 把一次状态变化推给 WebSocket 与 Webhook 通道（均允许未启用）。
// 通知失败只记日志：通知链路故障不应影响采集与判定主链路。
//
// 已调用 StartDispatch 时只入队（不阻塞调用方），否则同步执行 ——
// 单测与未接线场景保持既有语义。
func (s *AlertService) dispatch(ctx context.Context, e *adapter.AlertEvent, event string, at time.Time) {
	w := s.worker.Load()
	if w == nil {
		s.dispatchSync(ctx, e, event, at)
		return
	}
	select {
	case w.ch <- dispatchItem{event: e, kind: event, at: at}:
	default:
		// 队列积压说明下游长期不可达；丢弃并留下痕迹，不要无声无息地丢通知
		s.log.Warn("通知队列已满，本次投递被丢弃", "event", event, "alert_id", e.ID)
	}
}

// dispatchSync 是实际投递实现：补全载荷后推 WebSocket 与 webhook。
func (s *AlertService) dispatchSync(ctx context.Context, e *adapter.AlertEvent, event string, at time.Time) {
	var host *adapter.Host
	if e.HostID != nil {
		if h, err := s.store.Hosts().GetByID(ctx, *e.HostID); err == nil {
			host = h
		}
	}
	p := notify.BuildPayload(event, e, host, at)
	if s.broadcaster != nil {
		s.broadcaster.BroadcastJSON(p)
	}
	if s.notifier != nil {
		s.notifier.AlertEvent(ctx, p)
	}
}

// agentOfflineKey 是 Agent 离线告警的去重键（每台主机同时只有一条）。
func agentOfflineKey(hostID int64) string { return fmt.Sprintf("agent-offline:%d", hostID) }

// RaiseAgentOffline 记录 Agent 离线告警（M3：断连后 metalwatch_up 归零 + agent_offline）。
// 已在 firing 中时仅刷新，不重复通知。
func (s *AlertService) RaiseAgentOffline(ctx context.Context, host *adapter.Host, at time.Time) error {
	val, threshold := 0.0, 0.0
	metric := "up"
	e := &adapter.AlertEvent{
		HostID:    &host.ID,
		Severity:  "info",
		Category:  "agent_offline",
		Metric:    &metric,
		Value:     &val,
		Threshold: &threshold,
		Title:     fmt.Sprintf("Agent 离线：%s 超过 2×采集周期未上报", host.Hostname),
		Detail:    ptrString(fmt.Sprintf(`{"host_id":%d,"last_seen_at":%q}`, host.ID, formatUTC(at))),
	}
	created, id, err := s.alerts.UpsertFiring(ctx, e, agentOfflineKey(host.ID), formatUTC(at))
	if err != nil {
		return err
	}
	if created {
		// 回填事件 ID：通知层靠它把 notify_state 写回（webhook.markEvent 对 ID<=0 直接跳过）。
		// 与 Evaluate 里的处理保持一致——漏了这步，离线告警会永远停在 pending。
		e.ID = id
		s.dispatch(ctx, e, "agent.offline", at)
	}
	return nil
}

// ResolveAgentOffline 解除 Agent 离线告警（恢复上报后调用），返回是否确有解除。
func (s *AlertService) ResolveAgentOffline(ctx context.Context, host *adapter.Host, at time.Time) (bool, error) {
	resolved, err := s.alerts.Resolve(ctx, agentOfflineKey(host.ID), formatUTC(at))
	if err != nil {
		return false, err
	}
	if resolved {
		metric := "up"
		s.dispatch(ctx, &adapter.AlertEvent{
			HostID: &host.ID, Severity: "info", Category: "agent_offline", Metric: &metric,
			FirstSeenAt: at, LastSeenAt: at,
		}, "agent.online", at)
	}
	return resolved, nil
}

func sevOf(level string) string {
	if level == "critical" {
		return "critical"
	}
	return "major"
}
