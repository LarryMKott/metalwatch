package engine

import (
	"testing"
	"time"
)

func TestFanCurveMapping(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		temp float64
		want int32
	}{
		{25, 20}, {40, 35}, {55, 55}, {70, 80}, {85, 100},
	}
	for _, tc := range cases {
		// 每个用例用独立 controller，避免上一次调速的 MinInterval 抑制本次断言
		c := NewFanController(DefaultFanPolicy())
		cmd, kind := c.Step(tc.temp, 0, "h1", now, false)
		if kind != ChangeSetSpeed || cmd == nil || cmd.SpeedPercent != tc.want {
			t.Errorf("temp=%v 期望转速 %d, 实际 kind=%v cmd=%v", tc.temp, tc.want, kind, cmd)
		}
	}
}

func TestFanAlreadyInPlace(t *testing.T) {
	c := NewFanController(DefaultFanPolicy())
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if cmd, kind := c.Step(25, 20, "h1", now, false); kind != ChangeNone || cmd != nil {
		t.Errorf("已到位应无指令, kind=%v cmd=%v", kind, cmd)
	}
}

func TestFanHysteresisDelta(t *testing.T) {
	pol := DefaultFanPolicy()
	pol.MinDelta = 50 // 变化幅度需 >= 50 才动作
	c := NewFanController(pol)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// 置位：temp=85 段转速 100，与初值 0 相差 100 >= 50 -> 动作，lastSpeed=100
	if _, k := c.Step(85, 0, "h1", base, false); k != ChangeSetSpeed {
		t.Fatalf("初始化置位失败")
	}
	// 目标 55（temp 55 段），与 100 相差 45 < 50 -> 忽略
	if cmd, k := c.Step(55, 100, "h1", base.Add(20*time.Second), false); k != ChangeNone || cmd != nil {
		t.Errorf("幅度不足应忽略, kind=%v cmd=%v", k, cmd)
	}
	// 目标 35（temp 35 落在 ≤45 段 -> 35%），与 100 相差 65 >= 50 -> 动作
	if cmd, k := c.Step(35, 100, "h1", base.Add(40*time.Second), false); k != ChangeSetSpeed || cmd == nil || cmd.SpeedPercent != 35 {
		t.Errorf("大幅变化应动作, kind=%v cmd=%v", k, cmd)
	}
}

func TestFanMinInterval(t *testing.T) {
	pol := DefaultFanPolicy()
	pol.MinInterval = 10 * time.Second
	c := NewFanController(pol)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, k := c.Step(40, 0, "h1", base, false); k != ChangeSetSpeed {
		t.Fatalf("初始化置位失败")
	}
	// 1s 后即请求大幅变化，间隔不足 -> 忽略
	if cmd, k := c.Step(85, 35, "h1", base.Add(1*time.Second), false); k != ChangeNone || cmd != nil {
		t.Errorf("间隔不足应忽略, kind=%v cmd=%v", k, cmd)
	}
	// 11s 后 -> 动作
	if cmd, k := c.Step(85, 35, "h1", base.Add(11*time.Second), false); k != ChangeSetSpeed || cmd == nil {
		t.Errorf("间隔满足应动作, kind=%v cmd=%v", k, cmd)
	}
}

func TestFanClamp(t *testing.T) {
	pol := DefaultFanPolicy()
	pol.MaxSpeed = 90
	c := NewFanController(pol)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cmd, k := c.Step(95, 0, "h1", now, false)
	if k != ChangeSetSpeed || cmd == nil || cmd.SpeedPercent != 90 {
		t.Errorf("超上限应夹紧到 90, kind=%v cmd=%v", k, cmd)
	}
}

func TestFanAutoHandoff(t *testing.T) {
	c := NewFanController(DefaultFanPolicy())
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cmd, k := c.Step(80, 50, "h1", now, true)
	if k != ChangeHandoff || cmd == nil || !cmd.AutoMode || cmd.SpeedPercent != 0 {
		t.Errorf("auto 应下发交还控制, kind=%v cmd=%v", k, cmd)
	}
}
