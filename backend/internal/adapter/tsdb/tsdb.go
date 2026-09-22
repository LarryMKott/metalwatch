package tsdb

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	"github.com/LarryMKott/metalwatch/migrations"

	_ "modernc.org/sqlite" // 纯 Go 驱动，与元数据库共用（免 CGO，docs/01 D14）
)

// 分块窗口：原始档 1 小时一块、聚合档 1 天一块。
// 写入按「时间线 × 窗口」归并到同一块，查询退化为少量 BLOB 的顺序读取（docs/03 §2）。
const (
	rawChunkWindow = time.Hour
	aggChunkWindow = 24 * time.Hour
	// rollup 的聚合粒度，与 DDL tier 注释一致：0=原始档 1=5m 聚合档。
	aggBucket = 5 * time.Minute
	tierRaw   = 0
	tierAgg   = 1

	encXOR = "xor" // 原始档：每点 1 个值
	encAgg = "agg" // 聚合档：每点 2 个值（avg、max）
)

// Store 是内嵌时序存储：独立 SQLite 文件（<data_dir>/tsdb.db，见 migrations/tsdb/0001_init.sql 头注）。
// 与元数据生命周期解耦（docs/01 D30）；写路径由 pipeline 串行化（单写者）。
type Store struct {
	db      *sql.DB
	rawDays int
	aggDays int
}

func init() {
	adapter.RegisterTimeSeries(adapter.TSDriverEmbedded, open)
}

func open(cfg adapter.TimeSeriesConfig) (adapter.TimeSeriesStore, error) {
	if cfg.RootDir == "" {
		return nil, fmt.Errorf("tsdb: 未指定落盘目录（TimeSeriesConfig.RootDir）")
	}
	rawDays := cfg.RawDays
	if rawDays < 1 {
		rawDays = 7
	}
	aggDays := cfg.AggDays
	if aggDays < rawDays {
		aggDays = rawDays
	}

	if err := os.MkdirAll(cfg.RootDir, 0o750); err != nil {
		return nil, fmt.Errorf("tsdb: 创建数据目录 %s 失败: %w", cfg.RootDir, err)
	}

	// foreign_keys 必须开：ts_chunk → ts_series 的级联删除依赖它。
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "temp_store(MEMORY)")
	q.Add("_pragma", "cache_size(-32000)") // 32MB：时序以顺序 BLOB 读写为主，页缓存需求低于元数据库

	dsn := "file:" + filepath.ToSlash(filepath.Join(cfg.RootDir, "tsdb.db")) + "?" + q.Encode()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("tsdb: 打开失败: %w", err)
	}
	// WAL 允许读写并发；写侧已由 pipeline 串行化，放开连接数让查询不吃写延迟。
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("tsdb: 连接失败: %w", err)
	}

	// 时序表有独立方言目录（migrations/tsdb/），复用元数据同一套迁移器。
	fsys, err := migrations.TSDBFS()
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("tsdb: 内嵌迁移缺失: %w", err)
	}
	if _, err := adapter.NewStore(db, adapter.DialectSQLite).Migrate(ctx, fsys, "tsdb"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("tsdb: 迁移失败: %w", err)
	}

	return &Store{db: db, rawDays: rawDays, aggDays: aggDays}, nil
}

// Name 返回驱动名（config.Timeseries.Driver 的 embedded）。
func (s *Store) Name() string { return adapter.TSDriverEmbedded }

// Retention 返回两档保留天数。
func (s *Store) Retention() (rawDays, aggDays int) { return s.rawDays, s.aggDays }

// Close 关闭底层库。
func (s *Store) Close() error { return s.db.Close() }

// ---------- 写入 ----------

// Write 落一批采样。批次幂等由 SeenBatch 在管道入口保证，本方法只负责归并与压缩。
// 时间戳乱序（24h 补传窗口内的 spool 补传）是常态输入，直接按窗口归并。
func (s *Store) Write(ctx context.Context, samples []adapter.Sample) error {
	if len(samples) == 0 {
		return nil
	}

	type acc struct {
		metric string
		labels string // 规范化 JSON（encoding/json 对 map 按键排序输出）
		pts    []point
	}
	series := map[string]*acc{}
	var order []string
	for _, sm := range samples {
		labels := sm.Labels
		if labels == nil {
			labels = map[string]string{}
		}
		lj, err := json.Marshal(labels)
		if err != nil {
			return fmt.Errorf("tsdb: 序列化标签失败: %w", err)
		}
		key := sm.Metric + "\x1f" + string(lj)
		a, ok := series[key]
		if !ok {
			a = &acc{metric: sm.Metric, labels: string(lj)}
			series[key] = a
			order = append(order, key)
		}
		a.pts = append(a.pts, point{ts: sm.TS.UTC().UnixMilli(), v: sm.Value})
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, key := range order {
		a := series[key]
		k := seriesKey{metric: a.metric, labelsJSON: a.labels, tier: tierRaw}
		if err := upsertSeries(ctx, tx, k, a.pts); err != nil {
			return err
		}
		for _, w := range groupWindows(a.pts, rawChunkWindow) {
			if err := mergeRawChunk(ctx, tx, k.id(), tierRaw, w); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// SeenBatch 标记一个上报批次；返回 true 表示此前已处理过（调用方直接幂等返回）。
// 实现 docs/03 §2.2 的 batch_id 去重：Agent 断网续传会重放同一批次。
func (s *Store) SeenBatch(ctx context.Context, batchID string, hostID int64, points int) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO ts_ingest_dedup (batch_id, host_id, points, seen_at)
		 VALUES (?, ?, ?, ?) ON CONFLICT(batch_id) DO NOTHING`,
		batchID, hostID, points, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 0, nil
}

// ---------- 查询 ----------

// Query 按 metric + 标签子集 + 时间区间 + step 查询（不引入 PromQL，docs/03 §2.3）。
func (s *Store) Query(ctx context.Context, q adapter.Query) ([]adapter.Series, error) {
	tier := tierRaw
	if q.Aggregated {
		tier = tierAgg
	}
	end := q.End
	if end.IsZero() {
		end = time.Now()
	}
	start := q.Start
	if start.IsZero() {
		start = end.Add(-24 * time.Hour)
	}
	startMs, endMs := start.UTC().UnixMilli(), end.UTC().UnixMilli()

	rows, err := s.db.QueryContext(ctx,
		`SELECT series_id, labels_json FROM ts_series WHERE metric = ? AND tier = ?`, q.Metric, tier)
	if err != nil {
		return nil, err
	}
	type cand struct {
		id     int64
		labels map[string]string
	}
	var cands []cand
	for rows.Next() {
		var id int64
		var lj string
		if err := rows.Scan(&id, &lj); err != nil {
			_ = rows.Close()
			return nil, err
		}
		labels := map[string]string{}
		_ = json.Unmarshal([]byte(lj), &labels)
		if !labelsMatch(labels, q.Labels) {
			continue
		}
		cands = append(cands, cand{id, labels})
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var stepMs int64
	if q.Step > 0 {
		stepMs = q.Step.Milliseconds()
	}

	out := make([]adapter.Series, 0, len(cands))
	for _, cd := range cands {
		pts, err := s.readPoints(ctx, cd.id, tier, startMs, endMs)
		if err != nil {
			return nil, err
		}
		if stepMs > 0 {
			pts = downsample(pts, stepMs)
		}
		if len(pts) == 0 {
			continue
		}
		sr := adapter.Series{Labels: cd.labels}
		for _, p := range pts {
			sr.Points = append(sr.Points, adapter.Point{TS: time.UnixMilli(p.ts).UTC(), Value: p.v})
		}
		out = append(out, sr)
	}
	return out, nil
}

// readPoints 读一条时间线在区间内的点（跨块解码、按时间排序）。
func (s *Store) readPoints(ctx context.Context, seriesID int64, tier int, startMs, endMs int64) ([]point, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT points, data FROM ts_chunk
		 WHERE series_id = ? AND tier = ? AND start_ts <= ? AND end_ts >= ?
		 ORDER BY start_ts`, seriesID, tier, endMs, startMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []point
	for rows.Next() {
		var n int
		var data []byte
		if err := rows.Scan(&n, &data); err != nil {
			return nil, err
		}
		d, err := decodeChunk(data, n, tier == tierAgg)
		if err != nil {
			continue // 单块损坏不影响其余曲线，留待后续维护覆盖修复
		}
		for i, ts := range d.TSs {
			if ts < startMs || ts > endMs {
				continue
			}
			out = append(out, point{ts: ts, v: d.Avg[i]})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ts < out[j].ts })
	return out, rows.Err()
}

type point struct {
	ts int64
	v  float64
}

// downsample 按绝对对齐的 step 网格分桶取平均（docs/04 §3 曲线示例按 step 对齐）。
func downsample(pts []point, stepMs int64) []point {
	if stepMs <= 0 || len(pts) == 0 {
		return pts
	}
	type bucket struct {
		sum float64
		n   int
	}
	buckets := map[int64]*bucket{}
	var keys []int64
	for _, p := range pts {
		b := p.ts - p.ts%stepMs
		bk, ok := buckets[b]
		if !ok {
			bk = &bucket{}
			buckets[b] = bk
			keys = append(keys, b)
		}
		bk.sum += p.v
		bk.n++
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	out := make([]point, 0, len(keys))
	for _, k := range keys {
		b := buckets[k]
		out = append(out, point{ts: k, v: b.sum / float64(b.n)})
	}
	return out
}

func labelsMatch(have, want map[string]string) bool {
	for k, v := range want {
		if have == nil || have[k] != v {
			return false
		}
	}
	return true
}

// ---------- 维护：rollup 聚合与保留期清理 ----------

// Maintenance 执行一轮 5m 聚合与过期清理，由服务端维护循环周期调用。
func (s *Store) Maintenance(ctx context.Context) (rolledUp int, pruned int64, err error) {
	rolledUp, err = s.rollup(ctx, time.Now().UTC())
	if err != nil {
		return rolledUp, 0, fmt.Errorf("tsdb: rollup 失败: %w", err)
	}
	pruned, err = s.prune(ctx, time.Now().UTC())
	if err != nil {
		return rolledUp, pruned, fmt.Errorf("tsdb: 清理失败: %w", err)
	}
	return rolledUp, pruned, nil
}

// rollup 把原始档聚合为 5m 档（每桶 avg + max，docs/03 §2.4）。
// 只聚合完整桶：窗口终点截到上一个完整 5m 边界，避免半桶数据反复重算。
func (s *Store) rollup(ctx context.Context, now time.Time) (int, error) {
	horizonEnd := now.Truncate(aggBucket).Add(-aggBucket).UnixMilli()

	rows, err := s.db.QueryContext(ctx,
		`SELECT series_id, metric, labels_json FROM ts_series WHERE tier = ? ORDER BY series_id`, tierRaw)
	if err != nil {
		return 0, err
	}
	var srcs []seriesSrc
	for rows.Next() {
		var r seriesSrc
		if err := rows.Scan(&r.id, &r.metric, &r.labels); err != nil {
			_ = rows.Close()
			return 0, err
		}
		srcs = append(srcs, r)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	rolled := 0
	for _, r := range srcs {
		aggKey := seriesKey{metric: r.metric, labelsJSON: r.labels, tier: tierAgg}
		aggLast, err := s.ensureAggSeries(ctx, aggKey, r)
		if err != nil {
			return rolled, err
		}
		from := aggLast
		if from <= 0 {
			from = now.Add(-time.Duration(s.rawDays) * 24 * time.Hour).UnixMilli()
		}
		if from >= horizonEnd {
			continue
		}
		pts, err := s.readPoints(ctx, r.id, tierRaw, from, horizonEnd)
		if err != nil {
			return rolled, err
		}
		if len(pts) == 0 {
			continue
		}
		bucketed := aggregateBuckets(pts, aggBucket)
		if err := s.writeAggChunks(ctx, aggKey.id(), bucketed); err != nil {
			return rolled, err
		}
		last := bucketed[len(bucketed)-1]
		if _, err := s.db.ExecContext(ctx,
			`UPDATE ts_series SET last_ts = MAX(last_ts, ?), last_value = ? WHERE series_id = ?`,
			last.ts, last.avg, aggKey.id()); err != nil {
			return rolled, err
		}
		rolled++
	}
	return rolled, nil
}

type aggPoint struct {
	ts  int64
	avg float64
	max float64
}

// aggregateBuckets 按 5m 网格分桶，产出 (桶起点, avg, max) 序列。
func aggregateBuckets(pts []point, bucket time.Duration) []aggPoint {
	bms := bucket.Milliseconds()
	type accB struct {
		sum float64
		max float64
		n   int
	}
	m := map[int64]*accB{}
	var keys []int64
	for _, p := range pts {
		b := p.ts - p.ts%bms
		a, ok := m[b]
		if !ok {
			a = &accB{max: p.v}
			m[b] = a
			keys = append(keys, b)
		}
		a.sum += p.v
		a.n++
		if p.v > a.max {
			a.max = p.v
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	out := make([]aggPoint, 0, len(keys))
	for _, k := range keys {
		a := m[k]
		out = append(out, aggPoint{ts: k, avg: a.sum / float64(a.n), max: a.max})
	}
	return out
}

// ensureAggSeries 保证聚合档时间线目录行存在，返回其 last_ts（无数据为 0）。
func (s *Store) ensureAggSeries(ctx context.Context, k seriesKey, src seriesSrc) (int64, error) {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO ts_series (series_id, metric, labels_json, labels_hash, tier, last_ts, created_at)
		 VALUES (?, ?, ?, ?, ?, 0, ?) ON CONFLICT(series_id) DO NOTHING`,
		k.id(), src.metric, src.labels, k.hash(), k.tier,
		time.Now().UTC().Format(time.RFC3339)); err != nil {
		return 0, err
	}
	var last int64
	err := s.db.QueryRowContext(ctx,
		`SELECT last_ts FROM ts_series WHERE series_id = ?`, k.id()).Scan(&last)
	return last, err
}

// writeAggChunks 把聚合点按天归并进 agg 编码块。
// 已落盘完整桶内的点会被本轮重算覆盖（半桶补齐），区间外的历史点原样保留。
func (s *Store) writeAggChunks(ctx context.Context, aggID int64, pts []aggPoint) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	groups := map[int64][]aggPoint{}
	var keys []int64
	for _, p := range pts {
		b := p.ts - p.ts%aggChunkWindow.Milliseconds()
		if _, ok := groups[b]; !ok {
			keys = append(keys, b)
		}
		groups[b] = append(groups[b], p)
	}
	for _, b := range keys {
		g := groups[b]
		winStart, winEnd := b, b+aggChunkWindow.Milliseconds()-1
		existing, err := loadChunks(ctx, tx, aggID, tierAgg, winStart, winEnd)
		if err != nil {
			return err
		}
		for _, ch := range existing {
			for i, ts := range ch.tss {
				if ts < g[0].ts || ts > g[len(g)-1].ts {
					g = append(g, aggPoint{ts: ts, avg: ch.avg[i], max: ch.max[i]})
				}
			}
		}
		sort.Slice(g, func(i, j int) bool { return g[i].ts < g[j].ts })
		tss := make([]int64, len(g))
		avg := make([]float64, len(g))
		max := make([]float64, len(g))
		for i, p := range g {
			tss[i], avg[i], max[i] = p.ts, p.avg, p.max
		}
		var ids []int64
		for _, ch := range existing {
			ids = append(ids, ch.id)
		}
		if err := storeChunk(ctx, tx, chunkData{
			seriesID: aggID, tier: tierAgg,
			start: tss[0], end: tss[len(tss)-1],
			points: len(tss), encoding: encAgg,
			data: encodeChunk(tss, avg, max, true),
		}, ids); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// prune 按 tier 各自的保留期删除过期块与时间线，并清理过期去重记录。
func (s *Store) prune(ctx context.Context, now time.Time) (int64, error) {
	var total int64
	cuts := []struct {
		tier  int
		cutMs int64
	}{
		{tierRaw, now.Add(-time.Duration(s.rawDays) * 24 * time.Hour).UnixMilli()},
		{tierAgg, now.Add(-time.Duration(s.aggDays) * 24 * time.Hour).UnixMilli()},
	}
	for _, c := range cuts {
		res, err := s.db.ExecContext(ctx,
			`DELETE FROM ts_chunk WHERE tier = ? AND end_ts < ?`, c.tier, c.cutMs)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
		// 最新数据也超期的时间线整条删除（级联清块），避免目录无限膨胀
		res, err = s.db.ExecContext(ctx,
			`DELETE FROM ts_series WHERE tier = ? AND last_ts > 0 AND last_ts < ?`, c.tier, c.cutMs)
		if err != nil {
			return total, err
		}
		n, _ = res.RowsAffected()
		total += n
	}

	res, err := s.db.ExecContext(ctx,
		`DELETE FROM ts_ingest_dedup WHERE seen_at < ?`,
		now.Add(-25*time.Hour).Format(time.RFC3339))
	if err != nil {
		return total, err
	}
	n, _ := res.RowsAffected()
	total += n

	if total > 0 {
		// 删除不缩文件：依赖 auto_vacuum=INCREMENTAL 做增量回收（docs/03 §1.5）
		if _, err := s.db.ExecContext(ctx, `PRAGMA incremental_vacuum(256)`); err != nil {
			return total, err
		}
	}
	return total, nil
}

// ---------- 块归并共用逻辑 ----------

type chunkData struct {
	seriesID int64
	tier     int
	start    int64
	end      int64
	points   int
	encoding string
	data     []byte
}

type loadedChunk struct {
	id    int64
	start int64
	end   int64
	tss   []int64
	avg   []float64
	max   []float64
}

type windowPts struct {
	start, end int64 // 窗口边界（ms，含头不含尾）
	pts        []point
}

// groupWindows 把点按对齐的时间窗口分组。
func groupWindows(pts []point, win time.Duration) []windowPts {
	wms := win.Milliseconds()
	m := map[int64]*windowPts{}
	var keys []int64
	for _, p := range pts {
		b := p.ts - p.ts%wms
		w, ok := m[b]
		if !ok {
			w = &windowPts{start: b, end: b + wms}
			m[b] = w
			keys = append(keys, b)
		}
		w.pts = append(w.pts, p)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	out := make([]windowPts, 0, len(keys))
	for _, k := range keys {
		out = append(out, *m[k])
	}
	return out
}

// loadChunks 取与 [winStart, winEnd] 重叠的既有块。同窗口内通常只有一块；
// 出现多块（历史乱序合并的产物）时由调用方合并重写。
func loadChunks(ctx context.Context, tx *sql.Tx, seriesID int64, tier int, winStart, winEnd int64) ([]*loadedChunk, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, start_ts, end_ts, points, data FROM ts_chunk
		 WHERE series_id = ? AND tier = ? AND start_ts <= ? AND end_ts >= ?
		 ORDER BY start_ts`, seriesID, tier, winEnd, winStart)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*loadedChunk
	for rows.Next() {
		var c loadedChunk
		var n int
		var data []byte
		if err := rows.Scan(&c.id, &c.start, &c.end, &n, &data); err != nil {
			return nil, err
		}
		d, err := decodeChunk(data, n, tier == tierAgg)
		if err != nil {
			continue // 损坏块丢弃：新数据重写同窗口块后自然修复
		}
		c.tss, c.avg, c.max = d.TSs, d.Avg, d.Max
		out = append(out, &c)
	}
	return out, rows.Err()
}

// storeChunk 写回合并后的块：有既有块则更新首块并删除其余重叠块，否则插入。
func storeChunk(ctx context.Context, tx *sql.Tx, c chunkData, replacedIDs []int64) error {
	if len(replacedIDs) > 0 {
		if _, err := tx.ExecContext(ctx,
			`UPDATE ts_chunk SET start_ts = ?, end_ts = ?, points = ?, encoding = ?, bytes = ?, data = ?
			 WHERE id = ?`,
			c.start, c.end, c.points, c.encoding, len(c.data), c.data, replacedIDs[0]); err != nil {
			return err
		}
		for _, id := range replacedIDs[1:] {
			if _, err := tx.ExecContext(ctx, `DELETE FROM ts_chunk WHERE id = ?`, id); err != nil {
				return err
			}
		}
		return nil
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO ts_chunk (series_id, tier, start_ts, end_ts, points, encoding, bytes, data)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		c.seriesID, c.tier, c.start, c.end, c.points, c.encoding, len(c.data), c.data)
	return err
}

// mergeRawChunk 把窗口内的新点与既有块合并重写（Write 路径，xor 编码）。
func mergeRawChunk(ctx context.Context, tx *sql.Tx, seriesID int64, tier int, w windowPts) error {
	existing, err := loadChunks(ctx, tx, seriesID, tier, w.start, w.end-1)
	if err != nil {
		return err
	}
	merged := map[int64]float64{}
	var ids []int64
	for _, ch := range existing {
		ids = append(ids, ch.id)
		for i, ts := range ch.tss {
			merged[ts] = ch.avg[i]
		}
	}
	for _, p := range w.pts {
		merged[p.ts] = p.v // 同刻点：新值覆盖旧值
	}
	if len(merged) == 0 {
		return nil
	}
	tss := make([]int64, 0, len(merged))
	for ts := range merged {
		tss = append(tss, ts)
	}
	sort.Slice(tss, func(i, j int) bool { return tss[i] < tss[j] })
	vals := make([]float64, len(tss))
	for i, ts := range tss {
		vals[i] = merged[ts]
	}
	return storeChunk(ctx, tx, chunkData{
		seriesID: seriesID, tier: tier,
		start: tss[0], end: tss[len(tss)-1],
		points: len(tss), encoding: encXOR,
		data: encodeChunk(tss, vals, vals, false),
	}, ids)
}

// upsertSeries 写时间线目录行并推进 last_ts / last_value。
func upsertSeries(ctx context.Context, tx *sql.Tx, k seriesKey, pts []point) error {
	lastTs := int64(0)
	var lastVal float64
	for _, p := range pts {
		if p.ts >= lastTs {
			lastTs, lastVal = p.ts, p.v
		}
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO ts_series (series_id, metric, labels_json, labels_hash, tier, last_ts, last_value, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(series_id) DO UPDATE SET
		   last_ts = MAX(last_ts, excluded.last_ts),
		   last_value = CASE WHEN excluded.last_ts >= last_ts THEN excluded.last_value ELSE last_value END`,
		k.id(), k.metric, k.labelsJSON, k.hash(), k.tier, lastTs, lastVal,
		time.Now().UTC().Format(time.RFC3339))
	return err
}

// seriesSrc 是 rollup 扫描到的一条原始档时间线（目录行投影）。
type seriesSrc struct {
	id     int64
	metric string
	labels string
}

// seriesKey 是一条时间线的不可变值对象（D31 第 7 条）：metric + 规范化标签 + 档位。
// 构造后只读、可比较；id 与 hash 是它在 ts_series 表里的两种映射。
type seriesKey struct {
	metric     string
	labelsJSON string // 规范化 JSON（encoding/json 对 map 按键排序输出）
	tier       int
}

// id 计算 ts_series.series_id：FNV-1a 64（DDL 头注约定的应用层编号方式）。
// 含 tier——不同档独立编号；掩掉符号位以适配 SQLite 有符号 INTEGER。
func (k seriesKey) id() int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(k.metric))
	_, _ = h.Write([]byte{0x1f})
	_, _ = h.Write([]byte(k.labelsJSON))
	_, _ = h.Write([]byte{0x1f})
	_, _ = h.Write([]byte{byte(k.tier)})
	return int64(h.Sum64() & 0x7fffffffffffffff)
}

// hash 计算 labels_hash：不含 tier——(labels_hash, tier) 唯一索引依赖
// 「同一标签集同一档只有一条时间线」。
func (k seriesKey) hash() string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(k.metric))
	_, _ = h.Write([]byte{0x1f})
	_, _ = h.Write([]byte(k.labelsJSON))
	return hex.EncodeToString(h.Sum(nil))
}
