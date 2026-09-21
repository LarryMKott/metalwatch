package engine

import (
	"testing"
	"time"
)

func mustEvent(evs []AlarmEvent, ruleID, state string) *AlarmEvent {
	for i := range evs {
		if evs[i].RuleID == ruleID && evs[i].State == state {
			return &evs[i]
		}
	}
	return nil
}

func TestAlarmImmediateFiring(t *testing.T) {
	e := NewAlarmEngine()
	rule := AlarmRule{ID: "r1", Metric: "temp", Comparator: CmpGT, Threshold: 80, Level: "critical", ForDuration: 0, Enabled: true}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	evs := e.Process([]Sample{{Metric: "temp", Value: 90, Host: "h1", At: now}}, []AlarmRule{rule}, now)
	if mustEvent(evs, "r1", StateFiring) == nil {
		t.Errorf("越限应立即触发 firing, 实际 %v", evs)
	}
}

func TestAlarmForDuration(t *testing.T) {
	e := NewAlarmEngine()
	rule := AlarmRule{ID: "r1", Metric: "temp", Comparator: CmpGT, Threshold: 80, Level: "warning", ForDuration: 10 * time.Second, Enabled: true}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := Sample{Metric: "temp", Value: 90, Host: "h1"}

	if evs := e.Process([]Sample{s}, []AlarmRule{rule}, base); mustEvent(evs, "r1", StateFiring) != nil {
		t.Errorf("持续不足不应触发")
	}
	if evs := e.Process([]Sample{s}, []AlarmRule{rule}, base.Add(5*time.Second)); mustEvent(evs, "r1", StateFiring) != nil {
		t.Errorf("5s 仍不足")
	}
	if evs := e.Process([]Sample{s}, []AlarmRule{rule}, base.Add(10*time.Second)); mustEvent(evs, "r1", StateFiring) == nil {
		t.Errorf("满 10s 应触发 firing")
	}
}

func TestAlarmRecovery(t *testing.T) {
	e := NewAlarmEngine()
	e.SetRecoveryThreshold(3)
	rule := AlarmRule{ID: "r1", Metric: "temp", Comparator: CmpGT, Threshold: 80, ForDuration: 0, Enabled: true}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	hi := Sample{Metric: "temp", Value: 90, Host: "h1"}
	lo := Sample{Metric: "temp", Value: 70, Host: "h1"}

	e.Process([]Sample{hi}, []AlarmRule{rule}, base)            // firing
	if evs := e.Process([]Sample{lo}, []AlarmRule{rule}, base.Add(time.Second)); mustEvent(evs, "r1", StateRecovered) != nil {
		t.Errorf("1 个低于周期不应恢复")
	}
	e.Process([]Sample{lo}, []AlarmRule{rule}, base.Add(2*time.Second)) // 2
	if evs := e.Process([]Sample{lo}, []AlarmRule{rule}, base.Add(3*time.Second)); mustEvent(evs, "r1", StateRecovered) == nil {
		t.Errorf("连续 3 个低于周期应恢复")
	}
}

func TestAlarmSuppressWindow(t *testing.T) {
	e := NewAlarmEngine()
	rule := AlarmRule{ID: "r1", Metric: "temp", Comparator: CmpGT, Threshold: 80, ForDuration: 0, Enabled: true}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	hi := Sample{Metric: "temp", Value: 90, Host: "h1"}
	e.Process([]Sample{hi}, []AlarmRule{rule}, base) // firing
	// 1 秒后再次越限，仍在抑制窗口(5min)内 -> 不应再次 firing，应为 suppressed
	evs := e.Process([]Sample{hi}, []AlarmRule{rule}, base.Add(time.Second))
	if mustEvent(evs, "r1", StateFiring) != nil {
		t.Errorf("抑制窗口内不应重复 firing")
	}
	if mustEvent(evs, "r1", StateSuppressed) == nil {
		t.Errorf("抑制窗口内应产生 suppressed 事件")
	}
}

func TestAlarmMaintenanceMuted(t *testing.T) {
	e := NewAlarmEngine()
	e.SetMaintenance([]MaintenanceWindow{{
		Start: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
		Host:  "",
	}})
	rule := AlarmRule{ID: "r1", Metric: "temp", Comparator: CmpGT, Threshold: 80, ForDuration: 0, Enabled: true}
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	evs := e.Process([]Sample{{Metric: "temp", Value: 95, Host: "h1", At: now}}, []AlarmRule{rule}, now)
	if mustEvent(evs, "r1", StateMuted) == nil {
		t.Errorf("维护窗口内应 muted, 实际 %v", evs)
	}
}

func TestAlarmParentSuppression(t *testing.T) {
	e := NewAlarmEngine()
	parent := AlarmRule{ID: "r1", Metric: "temp", Comparator: CmpGT, Threshold: 80, Level: "critical", ForDuration: 0, Enabled: true}
	child := AlarmRule{ID: "r2", Metric: "temp", Comparator: CmpGT, Threshold: 80, Level: "warning", ForDuration: 0, Enabled: true, Parent: "r1"}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := Sample{Metric: "temp", Value: 90, Host: "h1", At: now}

	evs := e.Process([]Sample{s}, []AlarmRule{parent, child}, now)
	if mustEvent(evs, "r1", StateFiring) == nil {
		t.Errorf("父告警应 firing")
	}
	if mustEvent(evs, "r2", StateSuppressed) == nil {
		t.Errorf("父告警触发时子告警应被抑制, 实际 %v", evs)
	}
}

func TestAlarmChildFiresAfterParentRecovers(t *testing.T) {
	e := NewAlarmEngine()
	e.SetRecoveryThreshold(1)
	parent := AlarmRule{ID: "r1", Metric: "temp", Comparator: CmpGT, Threshold: 80, ForDuration: 0, Enabled: true}
	child := AlarmRule{ID: "r2", Metric: "mem", Comparator: CmpGT, Threshold: 50, ForDuration: 0, Enabled: true, Parent: "r1"}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// 父告警先触发
	e.Process([]Sample{{Metric: "temp", Value: 90, Host: "h1", At: base}}, []AlarmRule{parent, child}, base)
	// 父恢复（1 个低于周期）
	e.Process([]Sample{{Metric: "temp", Value: 70, Host: "h1", At: base.Add(time.Second)}}, []AlarmRule{parent, child}, base.Add(time.Second))
	// 子指标越限，父已不 firing -> 子应 firing
	evs := e.Process([]Sample{{Metric: "mem", Value: 60, Host: "h1", At: base.Add(2 * time.Second)}}, []AlarmRule{parent, child}, base.Add(2*time.Second))
	if mustEvent(evs, "r2", StateFiring) == nil {
		t.Errorf("父恢复后子应可 firing, 实际 %v", evs)
	}
}
