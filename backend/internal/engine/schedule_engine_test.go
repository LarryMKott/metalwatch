package engine

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LarryMKott/metalwatch/internal/task"
)

func TestScheduleTickBasic(t *testing.T) {
	pool := task.NewPool(task.Options{})
	eng := NewScheduleEngine(ScheduleConfig{Pool: pool, SensorInterval: time.Second})
	clk := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	eng.SetClock(func() time.Time { return clk })

	var n int32
	eng.AddTarget(CollectionTask{
		ID: "t1", Kind: KindSensor, Interval: time.Second,
		Run: func(ctx context.Context) error { atomic.AddInt32(&n, 1); return nil },
	})

	if d := eng.Tick(context.Background(), clk); d != 1 {
		t.Errorf("首次 Tick 应分派 1, 实际 %d", d)
	}
	if atomic.LoadInt32(&n) != 1 {
		t.Errorf("Run 应执行 1 次")
	}
	// 同刻再次 Tick：尚未到下一周期
	if d := eng.Tick(context.Background(), clk); d != 0 {
		t.Errorf("同刻不应再次分派, 实际 %d", d)
	}
	// 越过下一周期
	if d := eng.Tick(context.Background(), clk.Add(2*time.Second)); d != 1 {
		t.Errorf("越过周期应分派 1, 实际 %d", d)
	}
	if atomic.LoadInt32(&n) != 2 {
		t.Errorf("Run 应执行 2 次, 实际 %d", n)
	}
}

func TestScheduleDynamicAddRemove(t *testing.T) {
	pool := task.NewPool(task.Options{Concurrency: 4})
	eng := NewScheduleEngine(ScheduleConfig{Pool: pool, SensorInterval: time.Second})
	clk := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	eng.SetClock(func() time.Time { return clk })

	var n int32
	eng.AddTarget(CollectionTask{ID: "a", Interval: time.Second, Run: func(context.Context) error {
		atomic.AddInt32(&n, 1)
		return nil
	}})
	eng.AddTarget(CollectionTask{ID: "b", Interval: time.Second, Run: func(context.Context) error {
		atomic.AddInt32(&n, 1)
		return nil
	}})
	if d := eng.Tick(context.Background(), clk); d != 2 {
		t.Errorf("两个目标应分派 2, 实际 %d", d)
	}
	eng.RemoveTarget("a")
	if ids := eng.TargetIDs(); len(ids) != 1 || ids[0] != "b" {
		t.Errorf("移除后应只剩 b, 实际 %v", ids)
	}
	if d := eng.Tick(context.Background(), clk.Add(2*time.Second)); d != 1 {
		t.Errorf("移除后仅 b 应分派 1, 实际 %d", d)
	}
}

func TestScheduleBackoffSkips(t *testing.T) {
	var poolNow time.Time
	pool := task.NewPool(task.Options{
		RetryThreshold: 1,
		BackoffBase:    time.Minute,
		Now:            func() time.Time { return poolNow },
	})
	eng := NewScheduleEngine(ScheduleConfig{Pool: pool, SensorInterval: time.Second})
	now0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	poolNow = now0
	eng.SetClock(func() time.Time { return now0 })

	var n int32
	eng.AddTarget(CollectionTask{ID: "x", Interval: time.Second, Run: func(context.Context) error {
		atomic.AddInt32(&n, 1)
		return errBoom{}
	}})
	// 第一次：执行（失败并进入退避），计入分派
	if d := eng.Tick(context.Background(), now0); d != 1 {
		t.Errorf("首次应分派 1, 实际 %d", d)
	}
	// 退避窗口内：不应再执行
	if d := eng.Tick(context.Background(), now0); d != 0 {
		t.Errorf("退避内不应分派, 实际 %d", d)
	}
	if atomic.LoadInt32(&n) != 1 {
		t.Errorf("退避内 Run 不应再次执行, 实际 %d", n)
	}
	// 退避结束后：再次执行
	poolNow = now0.Add(2 * time.Minute)
	if d := eng.Tick(context.Background(), poolNow); d != 1 {
		t.Errorf("退避结束后应分派 1, 实际 %d", d)
	}
	if atomic.LoadInt32(&n) != 2 {
		t.Errorf("退避后应再次执行, 实际 %d", n)
	}
}

func TestScheduleRunLoopSmoke(t *testing.T) {
	pool := task.NewPool(task.Options{})
	eng := NewScheduleEngine(ScheduleConfig{Pool: pool, SensorInterval: 5 * time.Millisecond})
	var n int32
	eng.AddTarget(CollectionTask{ID: "loop", Interval: 5 * time.Millisecond, Run: func(context.Context) error {
		atomic.AddInt32(&n, 1)
		return nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	eng.Run(ctx)
	time.Sleep(60 * time.Millisecond)
	cancel()
	eng.Stop()
	if atomic.LoadInt32(&n) < 1 {
		t.Errorf("Run 循环应至少执行 1 次, 实际 %d", n)
	}
}

type errBoom struct{}

func (errBoom) Error() string { return "boom" }
