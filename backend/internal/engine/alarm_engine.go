package engine

import (
	"sync"
	"time"
)

// Comparator 是告警规则的比较符。
type Comparator string

const (
	CmpGT  Comparator = ">"
	CmpGTE Comparator = ">="
	CmpLT  Comparator = "<"
	CmpLTE Comparator = "<="
	CmpEQ  Comparator = "=="
	CmpNE  Comparator = "!="
)

// Match 判断样本值是否满足比较条件。
func (c Comparator) Match(v, threshold float64) bool {
	switch c {
	case CmpGT:
		return v > threshold
	case CmpGTE:
		return v >= threshold
	case CmpLT:
		return v < threshold
	case CmpLTE:
		return v <= threshold
	case CmpEQ:
		return v == threshold
	case CmpNE:
		return v != threshold
	}
	return false
}

// AlarmRule 是一条告警规则。
type AlarmRule struct {
	ID         string
	Metric     string
	Comparator Comparator
	Threshold  float64
	// ForDuration 条件需持续该时长才触发，规避瞬时抖动误报；0 表示立即触发。
	ForDuration time.Duration
	// Level 为 warning 或 critical。
	Level string
	// Host 限定匹配的主机；空表示匹配任意主机（取 Sample.Host）。
	Host string
	// Parent 为父规则 ID；父规则在同主机处于 firing 时抑制本规则。
	Parent string
	// ActiveKey 去重键；空时由 ID+Host 推导。
	// 注意：项目 SQL 约定不使用部分唯一索引（MySQL 不支持），去重依赖 active_key 列。
	ActiveKey string
	Enabled   bool
}

// MaintenanceWindow 是维护窗口：窗口内告警只记录不通知。
type MaintenanceWindow struct {
	Start time.Time
	End   time.Time
	// Host 限定主机；空表示全局生效。
	Host string
}

// Covers 判断时刻 t、主机 host 是否落在窗口内。
func (m MaintenanceWindow) Covers(t time.Time, host string) bool {
	if t.Before(m.Start) || !t.Before(m.End) {
		return false
	}
	if m.Host != "" && m.Host != host {
		return false
	}
	return true
}

// Sample 是进入告警引擎的一条指标样本。
type Sample struct {
	Metric string
	Value  float64
	Host   string
	At     time.Time
	// Object 是告警对象维度（传感器槽位/磁盘设备名等，如 Disk0、Fan1）。
	// 同一主机同一指标的不同对象各自独立去重；空表示对象无关指标。
	Object string
}

// 告警事件状态。
const (
	StateFiring     = "firing"     // 已触发并应通知
	StateRecovered  = "recovered"  // 已恢复
	StateSuppressed = "suppressed" // 被父告警或抑制窗口抑制，仅记录
	StateMuted      = "muted"      // 处于维护窗口，仅记录不通知
)

// AlarmEvent 是告警引擎对一批样本处理后的输出。
type AlarmEvent struct {
	ActiveKey string
	RuleID    string
	Host      string
	// Object 与 Sample.Object 对应，供持久化层写入 object_name。
	Object  string
	Level   string
	State   string
	Message string
	At      time.Time
}

type alarmState struct {
	ruleID           string
	level            string
	breachStart      time.Time // 条件首次为真时刻
	firing           bool
	lastFiredAt      time.Time
	consecutiveBelow int
}

// AlarmEngine 维护告警状态机，处理规则匹配、分级、抑制、维护窗口与抖动恢复。
type AlarmEngine struct {
	mu             sync.Mutex
	states         map[string]*alarmState
	maint          []MaintenanceWindow
	recoverN       int
	suppressWindow time.Duration
}

// NewAlarmEngine 构造告警引擎：默认恢复需连续 3 个周期低于阈值，抑制窗口 5 分钟。
func NewAlarmEngine() *AlarmEngine {
	return &AlarmEngine{
		states:         map[string]*alarmState{},
		recoverN:       3,
		suppressWindow: 5 * time.Minute,
	}
}

// SetRecoveryThreshold 设置连续低于阈值多少个周期判恢复。
func (e *AlarmEngine) SetRecoveryThreshold(n int) {
	if n < 1 {
		n = 1
	}
	e.mu.Lock()
	e.recoverN = n
	e.mu.Unlock()
}

// SetSuppressWindow 设置同一 active_key 的重复触发抑制窗口。
func (e *AlarmEngine) SetSuppressWindow(d time.Duration) {
	if d < 0 {
		d = 0
	}
	e.mu.Lock()
	e.suppressWindow = d
	e.mu.Unlock()
}

// SetMaintenance 设置维护窗口列表（全量替换）。
func (e *AlarmEngine) SetMaintenance(windows []MaintenanceWindow) {
	e.mu.Lock()
	e.maint = windows
	e.mu.Unlock()
}

func (e *AlarmEngine) inMaintenance(t time.Time, host string) bool {
	for _, m := range e.maint {
		if m.Covers(t, host) {
			return true
		}
	}
	return false
}

// Process 处理一批样本，返回本次产生的告警事件（已按规则 ID 稳定排序）。
func (e *AlarmEngine) Process(samples []Sample, rules []AlarmRule, now time.Time) []AlarmEvent {
	e.mu.Lock()
	defer e.mu.Unlock()

	// 规则按 ID 排序，保证同批处理顺序与事件顺序确定。
	rs := make([]AlarmRule, len(rules))
	copy(rs, rules)
	sortRules(rs)

	var events []AlarmEvent
	for _, s := range samples {
		for _, r := range rs {
			if !r.Enabled {
				continue
			}
			if r.Metric != s.Metric {
				continue
			}
			if r.Host != "" && r.Host != s.Host {
				continue
			}
			events = append(events, e.evalRule(r, s, now)...)
		}
	}
	return events
}

func (e *AlarmEngine) evalRule(r AlarmRule, s Sample, now time.Time) []AlarmEvent {
	level := r.Level
	if level == "" {
		level = "warning"
	}
	activeKey := r.ActiveKey
	if activeKey == "" {
		activeKey = r.ID + "@" + s.Host
		if s.Object != "" {
			activeKey += "/" + s.Object
		}
	}
	st := e.states[activeKey]
	if st == nil {
		st = &alarmState{ruleID: r.ID, level: level}
		e.states[activeKey] = st
	}

	// 父告警抑制：父规则在同主机同对象 firing 时，本规则只记录不触发。
	parentFiring := false
	if r.Parent != "" {
		pk := r.Parent + "@" + s.Host
		if s.Object != "" {
			pk += "/" + s.Object
		}
		if ps, ok := e.states[pk]; ok && ps.firing {
			parentFiring = true
		}
	}

	cond := r.Comparator.Match(s.Value, r.Threshold)
	if cond {
		if !st.firing {
			if st.breachStart.IsZero() {
				st.breachStart = now
			}
			if r.ForDuration > 0 && now.Sub(st.breachStart) < r.ForDuration {
				return nil // 持续时间不足，暂不触发
			}
			if parentFiring {
				// 被父告警抑制：保持 breachStart，待父解除后有机会触发。
				return []AlarmEvent{{
					ActiveKey: activeKey, RuleID: r.ID, Host: s.Host, Object: s.Object,
					Level: level, State: StateSuppressed,
					Message: "被父告警抑制", At: now,
				}}
			}
			st.firing = true
			st.lastFiredAt = now
			st.consecutiveBelow = 0
			st.breachStart = time.Time{}
			return []AlarmEvent{e.firingEvent(activeKey, r, s, level, now)}
		}
		// 已 firing：抑制窗口内不重复通知（去重），超出窗口则刷新一次。
		if now.Sub(st.lastFiredAt) < e.suppressWindow {
			return []AlarmEvent{{
				ActiveKey: activeKey, RuleID: r.ID, Host: s.Host, Object: s.Object,
				Level: level, State: StateSuppressed,
				Message: "抑制窗口内重复触发", At: now,
			}}
		}
		st.lastFiredAt = now
		return []AlarmEvent{e.firingEvent(activeKey, r, s, level, now)}
	}

	// 条件为假：累计低于阈值的连续周期，达到 recoverN 判恢复（防抖动）。
	st.consecutiveBelow++
	st.breachStart = time.Time{}
	if st.firing {
		if st.consecutiveBelow >= e.recoverN {
			st.firing = false
			st.consecutiveBelow = 0
			return []AlarmEvent{{
				ActiveKey: activeKey, RuleID: r.ID, Host: s.Host, Object: s.Object,
				Level: level, State: StateRecovered,
				Message: "指标已恢复", At: now,
			}}
		}
	}
	return nil
}

func (e *AlarmEngine) firingEvent(activeKey string, r AlarmRule, s Sample, level string, now time.Time) AlarmEvent {
	state := StateFiring
	msg := "指标越限"
	if e.inMaintenance(now, s.Host) {
		state = StateMuted
		msg = "维护窗口内，仅记录"
	}
	return AlarmEvent{
		ActiveKey: activeKey, RuleID: r.ID, Host: s.Host, Object: s.Object,
		Level: level, State: state, Message: msg, At: now,
	}
}

func sortRules(rs []AlarmRule) {
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0 && rs[j-1].ID > rs[j].ID; j-- {
			rs[j-1], rs[j] = rs[j], rs[j-1]
		}
	}
}
