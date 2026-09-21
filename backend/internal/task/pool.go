// Package collect 是采集任务层：带并发上限、单 BMC 串行与指数退避的任务池。
//
// 设计约束来自 docs/01 D10：
//   - 同一台 BMC 必须串行访问，避免把 BMC 打挂
//   - 全局并发上限可配（向导 wizard_ipmi_concurrency）
//   - 连续失败达到阈值后进入指数退避，退避期间不再发起连接
//   - 单次任务有超时，超时按失败计
package task

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// ErrBackoff 表示目标任务处于退避窗口，本次调度被跳过（不视为失败）。
var ErrBackoff = errors.New("目标处于退避窗口")

// Task 是一次采集动作，返回错误表示本次采集失败。
type Task func(ctx context.Context) error

// Options 是任务池配置。
type Options struct {
	// Concurrency 为全局并发上限（跨目标）。
	Concurrency int
	// Timeout 为单次任务超时。
	Timeout time.Duration
	// RetryThreshold 为连续失败多少次后进入退避。
	RetryThreshold int
	// BackoffBase 为退避基数，实际退避时长按 2^n 递增。
	BackoffBase time.Duration
	// BackoffMax 为退避上限。
	BackoffMax time.Duration
	// Logger 可为空。
	Logger *slog.Logger
	// Now 便于测试注入时钟；为空时使用 time.Now。
	Now func() time.Time
}

// Pool 是采集任务池。
type Pool struct {
	opt Options

	sem chan struct{}

	mu      sync.Mutex
	locks   map[string]*sync.Mutex
	fails   map[string]int
	blocked map[string]time.Time
}

// NewPool 构造任务池。
func NewPool(opt Options) *Pool {
	if opt.Concurrency <= 0 {
		opt.Concurrency = 1
	}
	if opt.Timeout <= 0 {
		opt.Timeout = 30 * time.Second
	}
	if opt.RetryThreshold <= 0 {
		opt.RetryThreshold = 3
	}
	if opt.BackoffBase <= 0 {
		opt.BackoffBase = 30 * time.Second
	}
	if opt.BackoffMax <= 0 {
		opt.BackoffMax = 30 * time.Minute
	}
	if opt.Logger == nil {
		opt.Logger = slog.Default()
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	return &Pool{
		opt:     opt,
		sem:     make(chan struct{}, opt.Concurrency),
		locks:   make(map[string]*sync.Mutex),
		fails:   make(map[string]int),
		blocked: make(map[string]time.Time),
	}
}

// Do 在池中执行一次采集任务。target 为 BMC 地址或主机标识，用于串行与退避键。
func (p *Pool) Do(ctx context.Context, target string, task Task) error {
	if err := p.checkBackoff(target); err != nil {
		return err
	}

	// 全局并发
	select {
	case p.sem <- struct{}{}:
		defer func() { <-p.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}

	// 同一目标串行
	lock := p.lockFor(target)
	lock.Lock()
	defer lock.Unlock()

	// 拿到锁后复查：等待期间该目标可能已进入退避
	if err := p.checkBackoff(target); err != nil {
		return err
	}

	taskCtx, cancel := context.WithTimeout(ctx, p.opt.Timeout)
	defer cancel()

	err := task(taskCtx)
	p.recordResult(target, err)
	return err
}

// checkBackoff 判断目标是否处于退避窗口。
func (p *Pool) checkBackoff(target string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	until, ok := p.blocked[target]
	if !ok {
		return nil
	}
	now := p.opt.Now()
	if now.After(until) {
		delete(p.blocked, target)
		return nil
	}
	return fmt.Errorf("%w: %s 还有 %s", ErrBackoff, target, until.Sub(now).Round(time.Second))
}

// recordResult 根据结果更新失败计数与退避窗口。
func (p *Pool) recordResult(target string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if err == nil {
		if _, had := p.fails[target]; had {
			p.opt.Logger.Info("采集恢复正常", "target", target)
		}
		delete(p.fails, target)
		delete(p.blocked, target)
		return
	}

	p.fails[target]++
	n := p.fails[target]
	if n < p.opt.RetryThreshold {
		p.opt.Logger.Warn("采集失败", "target", target, "failures", n, "err", err)
		return
	}

	// 指数退避：base * 2^(n-threshold)，上限 BackoffMax
	shift := n - p.opt.RetryThreshold
	delay := p.opt.BackoffBase
	for i := 0; i < shift && delay < p.opt.BackoffMax; i++ {
		delay *= 2
	}
	if delay > p.opt.BackoffMax {
		delay = p.opt.BackoffMax
	}
	p.blocked[target] = p.opt.Now().Add(delay)
	p.opt.Logger.Warn("目标进入退避", "target", target,
		"failures", n, "backoff", delay.String(), "err", err)
}

func (p *Pool) lockFor(target string) *sync.Mutex {
	p.mu.Lock()
	defer p.mu.Unlock()
	if l, ok := p.locks[target]; ok {
		return l
	}
	l := &sync.Mutex{}
	p.locks[target] = l
	return l
}

// Stats 返回当前失败计数与退避中的目标数，供系统状态页展示。
func (p *Pool) Stats() (failing, blocked int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, n := range p.fails {
		if n > 0 {
			failing++
		}
	}
	return failing, len(p.blocked)
}
