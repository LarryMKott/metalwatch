package task

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock 让退避逻辑可被确定性地测试。
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestPoolEnforcesGlobalConcurrency(t *testing.T) {
	const (
		workers = 12
		limit   = 3
	)
	p := NewPool(Options{Concurrency: limit, Timeout: time.Second})

	var current, maxSeen int32
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = p.Do(context.Background(), "bmc-"+string(rune('a'+i%5)), func(context.Context) error {
				n := atomic.AddInt32(&current, 1)
				for {
					old := atomic.LoadInt32(&maxSeen)
					if n <= old || atomic.CompareAndSwapInt32(&maxSeen, old, n) {
						break
					}
				}
				time.Sleep(20 * time.Millisecond)
				atomic.AddInt32(&current, -1)
				return nil
			})
		}(i)
	}
	wg.Wait()

	if maxSeen > limit {
		t.Fatalf("并发上限被突破：max=%d limit=%d", maxSeen, limit)
	}
	if maxSeen < 2 {
		t.Fatalf("并发度异常偏低，可能被串行化了：max=%d", maxSeen)
	}
}

func TestPoolSerializesSameTarget(t *testing.T) {
	p := NewPool(Options{Concurrency: 8, Timeout: time.Second})

	var inflight int32
	var overlapped bool
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = p.Do(context.Background(), "bmc-same", func(context.Context) error {
				if atomic.AddInt32(&inflight, 1) > 1 {
					overlapped = true
				}
				time.Sleep(10 * time.Millisecond)
				atomic.AddInt32(&inflight, -1)
				return nil
			})
		}()
	}
	wg.Wait()

	if overlapped {
		t.Fatal("同一 BMC 出现并发访问（docs/01 D10 要求严格串行）")
	}
}

func TestPoolEntersBackoffAfterThreshold(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)}
	p := NewPool(Options{
		Concurrency:    1,
		Timeout:        time.Second,
		RetryThreshold: 3,
		BackoffBase:    30 * time.Second,
		BackoffMax:     5 * time.Minute,
		Now:            clock.Now,
	})

	failing := func(context.Context) error { return errors.New("bmc unreachable") }

	// 前两次失败只累加计数，不进入退避
	for i := 0; i < 2; i++ {
		if err := p.Do(context.Background(), "bmc-1", failing); err == nil {
			t.Fatal("应返回失败")
		} else if errors.Is(err, ErrBackoff) {
			t.Fatalf("第 %d 次不应退避", i+1)
		}
	}

	// 第 3 次失败后进入退避
	if err := p.Do(context.Background(), "bmc-1", failing); err == nil {
		t.Fatal("应返回失败")
	} else if errors.Is(err, ErrBackoff) {
		t.Fatal("触发退避的那次本身不应返回 ErrBackoff")
	}

	// 退避窗口内：任务不得被执行
	var executed int32
	err := p.Do(context.Background(), "bmc-1", func(context.Context) error {
		atomic.AddInt32(&executed, 1)
		return nil
	})
	if !errors.Is(err, ErrBackoff) {
		t.Fatalf("退避窗口内应返回 ErrBackoff，实际: %v", err)
	}
	if executed != 0 {
		t.Fatal("退避窗口内不应发起连接")
	}

	failing_, blocked := p.Stats()
	if blocked != 1 || failing_ != 1 {
		t.Fatalf("Stats 异常: failing=%d blocked=%d", failing_, blocked)
	}

	// 时间推进过退避窗口后应恢复
	clock.Advance(31 * time.Second)
	if err := p.Do(context.Background(), "bmc-1", func(context.Context) error { return nil }); err != nil {
		t.Fatalf("退出退避后应可执行: %v", err)
	}
	if _, blocked := p.Stats(); blocked != 0 {
		t.Fatalf("成功后应清除退避状态，blocked=%d", blocked)
	}
}

func TestPoolRespectsContextCancel(t *testing.T) {
	p := NewPool(Options{Concurrency: 1, Timeout: time.Second})

	// 占满唯一的并发槽位
	block := make(chan struct{})
	go func() {
		_ = p.Do(context.Background(), "hold", func(context.Context) error {
			<-block
			return nil
		})
	}()
	time.Sleep(30 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := p.Do(ctx, "other", func(context.Context) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("应返回 context.Canceled，实际: %v", err)
	}
	close(block)
}

func TestPoolTaskTimeoutCountsAsFailure(t *testing.T) {
	clock := &fakeClock{now: time.Now().UTC()}
	p := NewPool(Options{
		Concurrency: 1, Timeout: 20 * time.Millisecond,
		RetryThreshold: 1, BackoffBase: time.Minute, BackoffMax: time.Minute,
		Now: clock.Now,
	})

	err := p.Do(context.Background(), "slow-bmc", func(ctx context.Context) error {
		select {
		case <-time.After(time.Second):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("超时应返回 DeadlineExceeded，实际: %v", err)
	}

	// 阈值=1，超时后应立即进入退避
	if err := p.Do(context.Background(), "slow-bmc", func(context.Context) error { return nil }); !errors.Is(err, ErrBackoff) {
		t.Fatalf("超时应触发退避，实际: %v", err)
	}
}
