// Package spool 是 Agent 断网续传的本地缓存队列（docs/03 §6）：
// bbolt 单文件（<spool>/spool.db），按采集时间 + 入队顺序 FIFO；
// 上限保护丢弃最旧条目；超期清理由 Purge 完成。
// 服务端按 batch_id 去重（ts_ingest_dedup），因此本队列是 at-least-once 语义。
package spool

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
	"google.golang.org/protobuf/proto"

	gen "github.com/LarryMKott/metalwatch/proto/gen"
)

var bucketName = []byte("reports")

// Options 是队列参数。
type Options struct {
	// MaxEntries 是队列容量上限；写满时丢弃最旧条目（docs/03 §6 容量保护）。
	// <=0 时取默认 20000（约等于 300 台主机 × 30s 周期 × 33 分钟的突发缓冲）。
	MaxEntries int
}

// Spool 是 bbolt 持久化队列。
type Spool struct {
	db         *bolt.DB
	maxEntries int
}

// Queued 是一条待补传的缓存项。
type Queued struct {
	Key    []byte
	Report *gen.AgentReport
}

// Open 打开（必要时创建）spool 数据库。
func Open(path string, opt Options) (*Spool, error) {
	if opt.MaxEntries <= 0 {
		opt.MaxEntries = 20000
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 3 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("打开 spool 失败: %w", err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(bucketName)
		return err
	})
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Spool{db: db, maxEntries: opt.MaxEntries}, nil
}

// Enqueue 入队一条上报。key = 采集时间(8B BE) + 递增序号(8B BE)：
// 时间序保证补传按采集时刻回放（时间轴不因断网错位），序号保证同刻稳定排序。
func (s *Spool) Enqueue(ctx context.Context, rep *gen.AgentReport) error {
	if rep == nil {
		return nil
	}
	raw, err := proto.Marshal(rep)
	if err != nil {
		return fmt.Errorf("序列化上报失败: %w", err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketName)
		seq, err := b.NextSequence()
		if err != nil {
			return err
		}
		ts := rep.GetTimestamp()
		if ts <= 0 {
			ts = time.Now().UnixMilli()
		}
		return b.Put(s.makeKey(uint64(ts), seq), raw)
	})
}

// Trim 把队列裁剪到容量上限内，返回丢弃的最旧条目数。
// 在入队路径外单独调用（Enqueue 保持 O(1)），由使用方在断网恢复后或定期调用。
// 注意不能用 Bucket.Stats().KeyN 判长度：未提交事务中的删除不计入，会误删全部。
func (s *Spool) Trim(ctx context.Context) (int, error) {
	dropped := 0
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketName)
		var keys [][]byte
		c := b.Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			keys = append(keys, append([]byte(nil), k...))
		}
		for overflow := len(keys) - s.maxEntries; overflow > 0; overflow-- {
			if err := b.Delete(keys[dropped]); err != nil {
				return err
			}
			dropped++
		}
		return nil
	})
	return dropped, err
}

// Peek 返回最旧的至多 n 条缓存项（不移除）。
// 损坏的条目直接删除：单条损坏不应卡死整条补传队列。
func (s *Spool) Peek(ctx context.Context, n int) ([]Queued, error) {
	if n <= 0 {
		return nil, nil
	}
	var corrupt [][]byte
	var out []Queued
	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucketName).Cursor()
		for k, v := c.First(); k != nil && len(out) < n; k, v = c.Next() {
			rep := &gen.AgentReport{}
			if err := proto.Unmarshal(v, rep); err != nil {
				key := make([]byte, len(k))
				copy(key, k)
				corrupt = append(corrupt, key)
				continue
			}
			key := make([]byte, len(k))
			copy(key, k)
			out = append(out, Queued{Key: key, Report: rep})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(corrupt) > 0 {
		if err := s.Ack(ctx, corrupt...); err != nil {
			return out, err
		}
		// 删除后可能腾出了配额，但本轮只返回已解码条目；下轮 Peek 会补上
	}
	return out, nil
}

// Ack 确认已成功送达的条目（删除对应 key）。未 Ack 的条目保留，下轮重发。
func (s *Spool) Ack(ctx context.Context, keys ...[]byte) error {
	if len(keys) == 0 {
		return nil
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketName)
		for _, k := range keys {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

// Purge 删除采集时间早于 cutoff 的条目（spool_max_hours 超期丢弃），返回删除数。
func (s *Spool) Purge(ctx context.Context, cutoff time.Time) (int, error) {
	cutKey := make([]byte, 8)
	binary.BigEndian.PutUint64(cutKey, uint64(cutoff.UnixMilli()))
	removed := 0
	err := s.db.Update(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucketName).Cursor()
		for k, _ := c.First(); k != nil && bytes.Compare(k, cutKey) < 0; k, _ = c.First() {
			if err := c.Delete(); err != nil {
				return err
			}
			removed++
		}
		return nil
	})
	return removed, err
}

// Len 返回当前缓存条数。
func (s *Spool) Len(ctx context.Context) (int, error) {
	var n int
	err := s.db.View(func(tx *bolt.Tx) error {
		n = tx.Bucket(bucketName).Stats().KeyN
		return nil
	})
	return n, err
}

// Close 关闭数据库。
func (s *Spool) Close() error { return s.db.Close() }

func (s *Spool) makeKey(ts, seq uint64) []byte {
	k := make([]byte, 16)
	binary.BigEndian.PutUint64(k, ts)
	binary.BigEndian.PutUint64(k[8:], seq)
	return k
}
