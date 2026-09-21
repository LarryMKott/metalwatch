package engine

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/LarryMKott/metalwatch/internal/task"
)

// TargetKind 区分采集目标类型，对应不同的默认采集周期。
type TargetKind int

const (
	// KindSensor 是周期性传感器采集（温度/电压/风扇等）。
	KindSensor TargetKind = iota
	// KindAsset 是资产/ inventory 采集（较低频）。
	KindAsset
)

// CollectionTask 描述一个需要周期性轮询的采集目标。
type CollectionTask struct {
	// ID 在调度器内唯一标识该目标。
	ID string
	// Kind 决定缺省周期（当 Interval 为 0 时使用）。
	Kind TargetKind
	// Host 为 BMC 主机标识，作为 task.Pool 的串行键；为空时退化为 ID。
	Host string
	// Interval 覆盖该目标的采集周期；为 0 时取 Kind 对应默认值。
	Interval time.Duration
	// Run 是单次采集动作，错误视为本次失败（触发 Pool 的退避语义）。
	Run func(ctx context.Context) error
}

// ScheduleConfig 是调度引擎配置。
type ScheduleConfig struct {
	// SensorInterval 为传感器采集默认周期。
	SensorInterval time.Duration
	// AssetInterval 为资产采集默认周期。
	AssetInterval time.Duration
	// Pool 是复用的 IPMI 轮询任务池（提供并发上限、单 BMC 串行、指数退避）。
	// 必须非空——调度引擎不另起一套并发控制。
	Pool *task.Pool
}

// targetState 是调度器内部维护的目标运行态。
type targetState struct {
	task     CollectionTask
	interval time.Duration
	nextDue  time.Time
	inFlight bool
}

// ScheduleEngine 在 task.Pool 之上做周期性采集调度，支持运行期动态增删目标。
type ScheduleEngine struct {
	cfg  ScheduleConfig
	pool *task.Pool

	mu      sync.Mutex
	targets map[string]*targetState
	stopCh  chan struct{}
	wg      sync.WaitGroup
	now     func() time.Time
	stopped bool
}

// NewScheduleEngine 构造调度引擎。pool 不能为空。
func NewScheduleEngine(cfg ScheduleConfig) *ScheduleEngine {
	if cfg.SensorInterval <= 0 {
		cfg.SensorInterval = 30 * time.Second
	}
	if cfg.AssetInterval <= 0 {
		cfg.AssetInterval = 5 * time.Minute
	}
	s := &ScheduleEngine{
		cfg:     cfg,
		pool:    cfg.Pool,
		targets: map[string]*targetState{},
		stopCh:  make(chan struct{}),
		now:     time.Now,
	}
	if s.pool == nil {
		// 防御性兜底：理论上调用方应传入共享的 IPMI 任务池。
		s.pool = task.NewPool(task.Options{})
	}
	return s
}

// SetClock 注入时钟，便于测试。
func (s *ScheduleEngine) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

func (s *ScheduleEngine) intervalOf(t CollectionTask) time.Duration {
	if t.Interval > 0 {
		return t.Interval
	}
	if t.Kind == KindAsset {
		return s.cfg.AssetInterval
	}
	return s.cfg.SensorInterval
}

// AddTarget 新增（或覆盖）一个采集目标。可在调度器运行期安全调用。
func (s *ScheduleEngine) AddTarget(t CollectionTask) {
	if t.ID == "" || t.Run == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.targets[t.ID]; ok {
		// 覆盖：保留原有 nextDue，避免重置调度相位导致抖动。
		st := s.targets[t.ID]
		st.task = t
		st.interval = s.intervalOf(t)
		return
	}
	s.targets[t.ID] = &targetState{
		task:     t,
		interval: s.intervalOf(t),
		nextDue:  s.now(),
	}
}

// RemoveTarget 删除目标。已在执行的采集不会被打断，仅不再调度下一次。
func (s *ScheduleEngine) RemoveTarget(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.targets, id)
}

// TargetIDs 返回当前所有目标的 ID（排序，便于稳定展示/测试）。
func (s *ScheduleEngine) TargetIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.targets))
	for id := range s.targets {
		ids = append(ids, id)
	}
	sortStrings(ids)
	return ids
}

// Run 启动后台调度循环，直到 ctx 取消或 Stop 被调用。
func (s *ScheduleEngine) Run(ctx context.Context) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			// 计算下一次到期前需要等待的时间
			wait := s.sleepUntil(ctx)
			if wait < 0 {
				return
			}
			if wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-s.stopCh:
					timer.Stop()
					return
				case <-timer.C:
				}
			}
			s.dispatchDue(ctx)
		}
	}()
}

// sleepUntil 返回距最近到期目标还需等待的时长；若应停止返回负数。
func (s *ScheduleEngine) sleepUntil(ctx context.Context) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-ctx.Done():
		return -1
	case <-s.stopCh:
		return -1
	default:
	}
	now := s.now()
	minWait := time.Duration(math.MaxInt64)
	for _, st := range s.targets {
		d := st.nextDue.Sub(now)
		if d < minWait {
			minWait = d
		}
	}
	if minWait == time.Duration(math.MaxInt64) {
		return time.Hour // 无目标：长睡，新增目标时由 AddTarget 不会唤醒循环，
		// 但 Run 的 1s ticker 仍会周期性唤醒，开销可忽略。
	}
	if minWait < 0 {
		return 0
	}
	return minWait
}

// dispatchDue 把当前到期的目标交给任务池执行（并发上限由 Pool 控制）。
func (s *ScheduleEngine) dispatchDue(ctx context.Context) {
	s.mu.Lock()
	now := s.now()
	var due []*targetState
	for _, st := range s.targets {
		if st.inFlight {
			continue
		}
		if !st.nextDue.After(now) {
			due = append(due, st)
		}
	}
	s.mu.Unlock()

	for _, st := range due {
		st := st
		s.mu.Lock()
		if st.inFlight {
			s.mu.Unlock()
			continue
		}
		st.inFlight = true
		s.mu.Unlock()

		go func() {
			key := st.task.Host
			if key == "" {
				key = st.task.ID
			}
			err := s.pool.Do(ctx, key, st.task.Run)

			s.mu.Lock()
			st.inFlight = false
			// 退避中：保持调度相位，不推进、不计入分派。
			if errors.Is(err, task.ErrBackoff) {
				s.mu.Unlock()
				return
			}
			// 推进到下一周期：从“现在”起算，避免累积漂移。
			st.nextDue = s.now().Add(st.interval)
			s.mu.Unlock()
		}()
	}
}

// Tick 是供测试使用的一次性调度：在给定 now 时刻分派所有到期目标并推进其相位，
// 返回本次分派（实际进入 Pool）的数量。运行中的 Run 循环与手动 Tick 不应混用。
func (s *ScheduleEngine) Tick(ctx context.Context, now time.Time) int {
	s.mu.Lock()
	prev := s.now
	s.now = func() time.Time { return now }
	var due []*targetState
	for _, st := range s.targets {
		if st.inFlight {
			continue
		}
		if !st.nextDue.After(now) {
			due = append(due, st)
		}
	}
	s.mu.Unlock()

	dispatched := 0
	for _, st := range due {
		st := st
		s.mu.Lock()
		if st.inFlight {
			s.mu.Unlock()
			continue
		}
		st.inFlight = true
		s.mu.Unlock()

		key := st.task.Host
		if key == "" {
			key = st.task.ID
		}
		err := s.pool.Do(ctx, key, st.task.Run)

		s.mu.Lock()
		st.inFlight = false
		// 退避中：保持调度相位，不推进、不计入分派。
		if errors.Is(err, task.ErrBackoff) {
			s.mu.Unlock()
			continue
		}
		st.nextDue = now.Add(st.interval)
		s.mu.Unlock()
		dispatched++
	}
	s.mu.Lock()
	s.now = prev
	s.mu.Unlock()
	return dispatched
}

// Stop 停止调度循环并等待在途分派 goroutine 退出。
func (s *ScheduleEngine) Stop() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	close(s.stopCh)
	s.mu.Unlock()
	s.wg.Wait()
}

func sortStrings(xs []string) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j-1] > xs[j]; j-- {
			xs[j-1], xs[j] = xs[j], xs[j-1]
		}
	}
}
