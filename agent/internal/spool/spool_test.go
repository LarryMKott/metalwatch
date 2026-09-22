package spool

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	gen "github.com/LarryMKott/metalwatch/proto/gen"
)

func newSpool(t *testing.T, maxEntries int) *Spool {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "spool.db"), Options{MaxEntries: maxEntries})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func report(batch string, at time.Time) *gen.AgentReport {
	return &gen.AgentReport{Kind: "metrics", BatchId: batch, Timestamp: at.UnixMilli()}
}

func TestSpoolFIFOAndAck(t *testing.T) {
	s := newSpool(t, 100)
	ctx := context.Background()
	base := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)

	for i, id := range []string{"b1", "b2", "b3"} {
		if err := s.Enqueue(ctx, report(id, base.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := s.Len(ctx); n != 3 {
		t.Fatalf("Len = %d, want 3", n)
	}

	items, err := s.Peek(ctx, 2)
	if err != nil || len(items) != 2 {
		t.Fatalf("Peek(2) = %d items err=%v, want 2", len(items), err)
	}
	// 按采集时间序（时间轴不因断网错位）
	if items[0].Report.BatchId != "b1" || items[1].Report.BatchId != "b2" {
		t.Fatalf("FIFO 顺序错误: %s, %s", items[0].Report.BatchId, items[1].Report.BatchId)
	}

	// Ack 已送达的 b1；未 Ack 的 b2 留队
	if err := s.Ack(ctx, items[0].Key); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Len(ctx); n != 2 {
		t.Fatalf("Ack 后 Len = %d, want 2", n)
	}
	items, _ = s.Peek(ctx, 10)
	if items[0].Report.BatchId != "b2" {
		t.Fatalf("Ack 后队首应为 b2, got %s", items[0].Report.BatchId)
	}
}

func TestSpoolPurge(t *testing.T) {
	s := newSpool(t, 100)
	ctx := context.Background()
	now := time.Now()

	_ = s.Enqueue(ctx, report("old", now.Add(-26*time.Hour)))
	_ = s.Enqueue(ctx, report("fresh", now.Add(-time.Hour)))

	removed, err := s.Purge(ctx, now.Add(-25*time.Hour))
	if err != nil || removed != 1 {
		t.Fatalf("Purge removed=%d err=%v, want 1", removed, err)
	}
	items, _ := s.Peek(ctx, 10)
	if len(items) != 1 || items[0].Report.BatchId != "fresh" {
		t.Fatalf("超期清理后应只剩 fresh: %d", len(items))
	}
}

func TestSpoolTrimDropsOldest(t *testing.T) {
	s := newSpool(t, 3)
	ctx := context.Background()
	base := time.Now()

	for i := 0; i < 5; i++ {
		_ = s.Enqueue(ctx, report(fmt.Sprintf("b%d", i), base.Add(time.Duration(i)*time.Second)))
	}
	dropped, err := s.Trim(ctx)
	if err != nil || dropped != 2 {
		t.Fatalf("Trim dropped=%d err=%v, want 2", dropped, err)
	}
	items, _ := s.Peek(ctx, 10)
	if len(items) != 3 || items[0].Report.BatchId != "b2" {
		t.Fatalf("容量保护应丢弃最旧两条: first=%v", items[0].Report.BatchId)
	}
}
