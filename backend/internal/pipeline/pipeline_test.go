package pipeline

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LarryMKott/metalwatch/internal/adapter"
)

// fakeStore 收集 Write 收到的样本，供断言。
type fakeStore struct {
	mu      sync.Mutex
	written []adapter.Sample
	fail    bool          // 置 true 时 Write 返回错误，用于验证管道不被单次失败拖垮
	block   chan struct{} // 非空时 Write 阻塞直到关闭，用于测退出游空
}

func (f *fakeStore) Name() string { return "fake" }
func (f *fakeStore) Close() error { return nil }
func (f *fakeStore) Retention() (int, int) {
	return 7, 180
}
func (f *fakeStore) Query(_ context.Context, _ adapter.Query) ([]adapter.Series, error) {
	return nil, nil
}
func (f *fakeStore) Write(_ context.Context, samples []adapter.Sample) error {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	fail := f.fail
	if !fail {
		f.written = append(f.written, samples...)
	}
	f.mu.Unlock()
	if fail {
		return context.DeadlineExceeded
	}
	return nil
}

func (f *fakeStore) snapshot() []adapter.Sample {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]adapter.Sample, len(f.written))
	copy(out, f.written)
	return out
}

func TestPipelineFlushesByInterval(t *testing.T) {
	fs := &fakeStore{}
	p := New(fs, nil, Options{FlushInterval: 30 * time.Millisecond, MaxBatch: 100})
	base := time.Now().UTC()
	p.Ingest([]adapter.Sample{
		{Metric: "up", Value: 1, TS: base},
		{Metric: "up", Value: 1, TS: base},
	})
	p.Stop() // Stop 触发退出游空，无需等 ticker

	if got := len(fs.snapshot()); got != 2 {
		t.Fatalf("写入点数 = %d, want 2", got)
	}
}

func TestPipelineFlushesByBatchSize(t *testing.T) {
	fs := &fakeStore{}
	p := New(fs, nil, Options{FlushInterval: time.Hour, MaxBatch: 4})
	base := time.Now().UTC()
	for i := 0; i < 9; i++ {
		p.Ingest([]adapter.Sample{{Metric: "m", Value: float64(i), TS: base}})
	}
	// MaxBatch=4：中间应已触发两轮满批提交；Stop 后补齐余量
	deadline := time.Now().Add(2 * time.Second)
	for len(fs.snapshot()) < 8 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if got := len(fs.snapshot()); got != 8 {
		t.Fatalf("满批提交点数 = %d, want 8", got)
	}
	p.Stop()
	if got := len(fs.snapshot()); got != 9 {
		t.Fatalf("Stop 后总点数 = %d, want 9", got)
	}
}

func TestPipelineWriteErrorKeepsRunning(t *testing.T) {
	fs := &fakeStore{fail: true}
	p := New(fs, nil, Options{FlushInterval: 10 * time.Millisecond, MaxBatch: 100})
	base := time.Now().UTC()
	p.Ingest([]adapter.Sample{{Metric: "a", Value: 1, TS: base}})
	p.Stop() // 失败被记日志并丢弃，不 panic 不卡死

	// 写失败后管道仍可继续服务新样本
	fs.mu.Lock()
	fs.fail = false
	fs.mu.Unlock()
	p2 := New(fs, nil, Options{FlushInterval: 10 * time.Millisecond, MaxBatch: 100})
	p2.Ingest([]adapter.Sample{{Metric: "b", Value: 2, TS: base}})
	p2.Stop()
	if got := len(fs.snapshot()); got != 1 {
		t.Fatalf("失败恢复后应写入 1 点, got %d", got)
	}
}
