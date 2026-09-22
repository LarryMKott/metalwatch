// Package pipeline 是采集数据的内存缓冲与批量提交通道（docs/03 §2.2）：
//
//	上报校验通过 ─▶ Ingest（入缓冲）─▶ Run 循环（每 5s 或满批量）─▶ TimeSeriesStore.Write
//
// 单一 goroutine 消费，天然满足「TSDB 单写者」约束；告警判定不走本管道
// （在 HTTP 处理路径上用原始值同步评估，见 service.AlertService）。
package pipeline

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/LarryMKott/metalwatch/internal/adapter"
)

// Options 是管道参数。
type Options struct {
	// BufferSize 是缓冲容量；写满时 Ingest 阻塞形成背压（而不是丢数据）。
	BufferSize int
	// FlushInterval 是最长攒批等待；到点即把已有样本刷出。
	FlushInterval time.Duration
	// MaxBatch 是单次提交的最大样本数。
	MaxBatch int
}

// Pipeline 缓冲采样并批量写入时序存储。
type Pipeline struct {
	store adapter.TimeSeriesStore
	log   *slog.Logger
	ch    chan adapter.Sample
	opt   Options

	wg     sync.WaitGroup
	cancel context.CancelFunc
}

// New 构造管道并启动消费循环。
func New(store adapter.TimeSeriesStore, log *slog.Logger, opt Options) *Pipeline {
	if opt.BufferSize <= 0 {
		opt.BufferSize = 8192
	}
	if opt.FlushInterval <= 0 {
		opt.FlushInterval = 5 * time.Second
	}
	if opt.MaxBatch <= 0 {
		opt.MaxBatch = 5000
	}
	if log == nil {
		log = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &Pipeline{
		store: store, log: log, ch: make(chan adapter.Sample, opt.BufferSize),
		opt: opt, cancel: cancel,
	}
	p.wg.Add(1)
	go p.run(ctx)
	return p
}

// Ingest 把一批样本送入缓冲。缓冲写满时阻塞（背压上报方，保证不丢点）。
func (p *Pipeline) Ingest(samples []adapter.Sample) {
	for _, s := range samples {
		p.ch <- s
	}
}

// run 是唯一的写者：攒批提交，退出前刷空缓冲。
func (p *Pipeline) run(ctx context.Context) {
	defer p.wg.Done()
	ticker := time.NewTicker(p.opt.FlushInterval)
	defer ticker.Stop()

	batch := make([]adapter.Sample, 0, p.opt.MaxBatch)
	// 写入用独立 context：停机后的最后一次 flush 不能因 ctx 已取消而丢数据。
	writeCtx := context.Background()
	flush := func() {
		if len(batch) == 0 {
			return
		}
		// 单次写入失败不拖垮管道：丢弃并记日志（下一个周期的数据不受影响）。
		if err := p.store.Write(writeCtx, batch); err != nil {
			p.log.Error("时序批量写入失败", "points", len(batch), "err", err)
		}
		batch = batch[:0]
	}

	for {
		select {
		case s := <-p.ch:
			batch = append(batch, s)
			if len(batch) >= p.opt.MaxBatch {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-ctx.Done():
			// 刷空剩余缓冲后退出（Close 已停住生产方）。
			for {
				select {
				case s := <-p.ch:
					batch = append(batch, s)
					if len(batch) >= p.opt.MaxBatch {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

// Stop 停止消费循环并刷空缓冲；调用后再 Ingest 会 panic（服务端停机路径上先停 HTTP）。
func (p *Pipeline) Stop() {
	p.cancel()
	p.wg.Wait()
}
