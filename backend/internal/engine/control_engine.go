package engine

import (
	"sync"
	"time"

	mwpb "github.com/LarryMKott/metalwatch/proto/gen"
)

// FanCurvePoint 是分段调速曲线的一段：温度不超过 TempMax（含）时目标转速为 SpeedPercent。
// 曲线应按时 TempMax 升序排列。
type FanCurvePoint struct {
	TempMax      float64
	SpeedPercent int32
}

// FanPolicy 是风扇闭环控制策略。
type FanPolicy struct {
	// Curve 分段曲线（按 TempMax 升序）。
	Curve []FanCurvePoint
	// MinInterval 两次调速指令的最小间隔（滞回，防震荡）。
	MinInterval time.Duration
	// MinDelta 最小变化幅度（绝对百分比）；低于该幅度的变更被忽略（滞回）。
	MinDelta int32
	// MinSpeed/MaxSpeed 为转速夹紧范围（0~100）。
	MinSpeed int32
	MaxSpeed int32
}

// DefaultFanPolicy 返回默认分段曲线：随温度上升阶梯式提速，滞回间隔 10s、幅度 5%。
func DefaultFanPolicy() FanPolicy {
	return FanPolicy{
		Curve: []FanCurvePoint{
			{TempMax: 30, SpeedPercent: 20},
			{TempMax: 45, SpeedPercent: 35},
			{TempMax: 60, SpeedPercent: 55},
			{TempMax: 75, SpeedPercent: 80},
			{TempMax: 90, SpeedPercent: 100},
		},
		MinInterval: 10 * time.Second,
		MinDelta:    5,
		MinSpeed:    10,
		MaxSpeed:    100,
	}
}

// ChangeKind 描述本次 Step 的结果类型。
type ChangeKind int

const (
	// ChangeNone 表示未产生控制指令（受滞回/间隔抑制或已到位）。
	ChangeNone ChangeKind = iota
	// ChangeSetSpeed 表示下发风扇调速指令。
	ChangeSetSpeed
	// ChangeHandoff 表示交还 BMC 自动控制（auto_mode）。
	ChangeHandoff
)

// FanController 是风扇闭环控制器，纯逻辑、不发起任何网络请求，可直接单元测试。
type FanController struct {
	policy FanPolicy

	mu           sync.Mutex
	lastSpeed    int32
	lastChangeAt time.Time
	now          func() time.Time
}

// NewFanController 构造控制器，零值字段以默认值填充并校验曲线。
func NewFanController(p FanPolicy) *FanController {
	if len(p.Curve) == 0 {
		p = DefaultFanPolicy()
	}
	if p.MinInterval < 0 {
		p.MinInterval = 0
	}
	if p.MinDelta < 0 {
		p.MinDelta = 0
	}
	if p.MinSpeed < 0 {
		p.MinSpeed = 0
	}
	if p.MaxSpeed <= 0 || p.MaxSpeed > 100 {
		p.MaxSpeed = 100
	}
	return &FanController{policy: p, now: time.Now}
}

// SetClock 注入时钟，便于测试。
func (c *FanController) SetClock(now func() time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

// targetSpeed 由温度查曲线得出目标转速，并夹紧到 [MinSpeed, MaxSpeed]。
func (c *FanController) targetSpeed(temp float64) int32 {
	var t int32
	for _, seg := range c.policy.Curve {
		t = seg.SpeedPercent
		if temp <= seg.TempMax {
			break
		}
	}
	if t < c.policy.MinSpeed {
		t = c.policy.MinSpeed
	}
	if t > c.policy.MaxSpeed {
		t = c.policy.MaxSpeed
	}
	return t
}

// Step 依据当前温度与目标（可选 auto 交还）计算应下发的指令。
//   - temp：当前最高温度；
//   - currentSpeed：BMC 当前观测到的转速（用于“已到位”判断）；
//   - host：目标设备标识；
//   - now：当前时刻；
//   - auto：true 时交还 BMC 自动控制，返回 AutoMode=true 的指令。
//
// 返回指令（无变更时为 nil）与变更类型。
func (c *FanController) Step(temp float64, currentSpeed int32, host string, now time.Time, auto bool) (*mwpb.BmcCommand, ChangeKind) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if auto {
		// 交还自动调速：模式切换视为重要动作，直接下发。
		c.lastSpeed = 0
		c.lastChangeAt = now
		return &mwpb.BmcCommand{
			CmdType:      "fan_speed",
			Target:       host,
			AutoMode:     true,
			SpeedPercent: 0,
		}, ChangeHandoff
	}

	target := c.targetSpeed(temp)
	if target == currentSpeed {
		return nil, ChangeNone // 已到位
	}
	if absInt(target-c.lastSpeed) < c.policy.MinDelta {
		return nil, ChangeNone // 滞回：变化幅度过小
	}
	if now.Sub(c.lastChangeAt) < c.policy.MinInterval {
		return nil, ChangeNone // 滞回：间隔不足
	}

	c.lastSpeed = target
	c.lastChangeAt = now
	return &mwpb.BmcCommand{
		CmdType:      "fan_speed",
		Target:       host,
		SpeedPercent: target,
	}, ChangeSetSpeed
}

func absInt(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}
