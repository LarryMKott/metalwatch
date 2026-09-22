// metric.go 承载时序曲线与大盘聚合接口（docs/04 §3）：
//
//	GET /api/v1/hosts/{id}/metrics   传感器曲线（≤24h 走 raw，>24h 自动切 5m 聚合档）
//	GET /api/v1/overview             大盘：总数 / 在线 / 离线 / 活跃告警 + 趋势序列
//
// 路由域对象化约定见 HostHandler 头注（docs/01 D31 第 6 条）。
package handler

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	"github.com/LarryMKott/metalwatch/pkg/utils"
)

// 聚合档切换阈值与默认步长（docs/03 §2.3：≤24h raw、>24h 5m 聚合）。
const (
	aggSwitchRange = 24 * time.Hour
	aggStep        = 5 * time.Minute
)

// MetricHandler 承载指标曲线与大盘接口。依赖：时序存储 + 元数据（主机计数与告警趋势）。
type MetricHandler struct {
	tsdb  adapter.TimeSeriesStore
	store adapter.MetadataStore
}

// NewMetricHandler 构造指标处理器。tsdb 为 nil 时曲线接口返回 503（时序未启用态）。
func NewMetricHandler(tsdb adapter.TimeSeriesStore, store adapter.MetadataStore) *MetricHandler {
	return &MetricHandler{tsdb: tsdb, store: store}
}

// Register 把本域路由挂到 /api/v1 分组上。
func (h *MetricHandler) Register(v1 *gin.RouterGroup) {
	v1.Group("/hosts").GET("/:id/metrics", h.HostMetrics)
	v1.GET("/overview", h.Overview)
}

// HostMetrics 返回一台主机的指标曲线，合并该指标全部对象维度为一条曲线
// （前端 MetricSeries 契约：{metric, unit, points: [[ms, value]]}）。
func (h *MetricHandler) HostMetrics(c *gin.Context) {
	if h.tsdb == nil {
		c.JSON(http.StatusServiceUnavailable, utils.ErrorBody{
			Code: "tsdb_unavailable", Message: "时序存储不可用", RequestID: utils.RequestIDOf(c),
		})
		return
	}
	id, err := utils.IDParam(c)
	if err != nil {
		utils.BadJSON(c, err)
		return
	}
	metric := strings.TrimSpace(c.Query("metric"))
	if metric == "" {
		c.JSON(http.StatusUnprocessableEntity, utils.ErrorBody{
			Code: "invalid_input", Message: "metric 为必填", RequestID: utils.RequestIDOf(c),
		})
		return
	}

	end, ok := parseTimeParam(c, "to", "end")
	if !ok {
		return
	}
	if end.IsZero() {
		end = time.Now().UTC()
	}
	start, ok := parseTimeParam(c, "from", "start")
	if !ok {
		return
	}
	if start.IsZero() {
		start = end.Add(-time.Hour)
	}
	if !start.Before(end) {
		c.JSON(http.StatusUnprocessableEntity, utils.ErrorBody{
			Code: "invalid_input", Message: "from 必须早于 to", RequestID: utils.RequestIDOf(c),
		})
		return
	}

	step := time.Duration(0)
	if s := strings.TrimSpace(c.Query("step")); s != "" {
		v, err := time.ParseDuration(s)
		if err != nil || v < time.Second {
			c.JSON(http.StatusUnprocessableEntity, utils.ErrorBody{
				Code: "invalid_input", Message: "step 需为如 60s / 300s 的时长且不小于 1s",
				RequestID: utils.RequestIDOf(c),
			})
			return
		}
		step = v
	}

	labels := map[string]string{"host_id": strconv.FormatInt(id, 10)}
	for _, kv := range strings.Split(c.Query("labels"), ",") {
		k, v, found := strings.Cut(strings.TrimSpace(kv), ":")
		if found && k != "" && v != "" {
			labels[k] = v
		}
	}
	if mode := strings.TrimSpace(c.Query("mode")); mode != "" {
		labels["mode"] = mode
	}

	ctx, cancel := utils.Timeout(c, 10*time.Second)
	defer cancel()

	base := adapter.Query{Metric: metric, Labels: labels, Start: start, End: end, Step: step}
	downsampled := step >= aggStep

	// >24h 优先读 5m 聚合档；聚合档无数据（rollup 未跑完/新接入）回退 raw 降采样
	series, usedAgg := []adapter.Series{}, false
	if end.Sub(start) > aggSwitchRange {
		q := base
		q.Aggregated = true
		if series, err = h.tsdb.Query(ctx, q); err != nil {
			utils.Fail(c, err)
			return
		}
		usedAgg = len(series) > 0
		downsampled = true
	}
	if !usedAgg {
		q := base
		if step == 0 && end.Sub(start) > aggSwitchRange {
			q.Step = aggStep
			downsampled = true
		}
		if series, err = h.tsdb.Query(ctx, q); err != nil {
			utils.Fail(c, err)
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"metric":      metric,
		"unit":        unitOf(metric),
		"points":      mergeSeries(series, stepMilliseconds(step)),
		"source":      h.tsdb.Name(),
		"downsampled": downsampled,
	})
}

// Overview 汇总大盘数据：当前计数取自元数据库，趋势序列由 `up` 指标
// （每次上报服务端写入 1）与告警事件按 5m 桶聚合。
func (h *MetricHandler) Overview(c *gin.Context) {
	ctx, cancel := utils.Timeout(c, 10*time.Second)
	defer cancel()

	// 当前计数：total / online / offline（unknown 计入 total 但不入两态）
	_, total, err := h.store.Hosts().List(ctx, adapter.ListFilter{Limit: 1})
	if err != nil {
		utils.Fail(c, err)
		return
	}
	_, online, err := h.store.Hosts().List(ctx, adapter.ListFilter{Limit: 1, Status: "online"})
	if err != nil {
		utils.Fail(c, err)
		return
	}
	_, offline, err := h.store.Hosts().List(ctx, adapter.ListFilter{Limit: 1, Status: "offline"})
	if err != nil {
		utils.Fail(c, err)
		return
	}
	activeAlerts, err := h.store.Alerts().CountFiring(ctx)
	if err != nil {
		utils.Fail(c, err)
		return
	}

	// 趋势：最近 24h、5m 桶。online(t) = up 指标在该桶 ≥0.5 的时间线数；
	// offline(t) 以当前 total 兜底（历史精确离线数需保留状态快照，属 W1c 后续优化）。
	now := time.Now().UTC().Truncate(aggStep)
	start := now.Add(-24 * time.Hour)
	var series []gin.H
	if h.tsdb != nil {
		upSeries, err := h.tsdb.Query(ctx, adapter.Query{
			Metric: "up", Start: start, End: now.Add(aggStep), Step: aggStep,
		})
		if err != nil {
			utils.Fail(c, err)
			return
		}
		onlineByBucket := map[int64]int{}
		for _, s := range upSeries {
			seen := map[int64]bool{}
			for _, p := range s.Points {
				b := p.TS.UnixMilli()
				if seen[b] || p.Value < 0.5 {
					continue
				}
				seen[b] = true
				onlineByBucket[b]++
			}
		}
		events, err := h.store.Alerts().FirstSeenBetween(ctx, start, now.Add(aggStep))
		if err != nil {
			utils.Fail(c, err)
			return
		}
		alertByBucket := map[int64]int{}
		for _, t := range events {
			b := t.Truncate(aggStep).UnixMilli()
			alertByBucket[b]++
		}
		for i := 0; i <= 288; i++ {
			b := start.Add(time.Duration(i) * aggStep)
			bms := b.UnixMilli()
			on := onlineByBucket[bms]
			off := total - on
			if off < 0 {
				off = 0
			}
			series = append(series, gin.H{
				"ts":      b.Format(time.RFC3339),
				"online":  on,
				"offline": off,
				"alert":   alertByBucket[bms],
			})
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"host_total":   total,
		"host_online":  online,
		"host_offline": offline,
		"alert_active": activeAlerts,
		"series":       series,
	})
}

// ---------- 与实例无关的纯工具（D31 第 1 条允许的包级函数） ----------

func parseTimeParam(c *gin.Context, names ...string) (time.Time, bool) {
	for _, n := range names {
		v := strings.TrimSpace(c.Query(n))
		if v == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			c.JSON(http.StatusUnprocessableEntity, utils.ErrorBody{
				Code: "invalid_input", Message: n + " 需为 RFC3339 时间（如 2026-09-22T00:00:00Z）",
				RequestID: utils.RequestIDOf(c),
			})
			return time.Time{}, false
		}
		return t, true
	}
	return time.Time{}, true
}

func stepMilliseconds(step time.Duration) int64 {
	if step <= 0 {
		return 0
	}
	return step.Milliseconds()
}

// mergeSeries 把多条对象维度的时间线合并为一条曲线：按 step 桶取平均；
// 无 step 时按时间排序直接拼接（单时间线场景）。
func mergeSeries(series []adapter.Series, stepMs int64) [][2]float64 {
	type bucket struct {
		sum float64
		n   int
	}
	merged := map[int64]*bucket{}
	var keys []int64
	add := func(ts int64, v float64) {
		b := ts
		if stepMs > 0 {
			b = ts - ts%stepMs
		}
		bk, ok := merged[b]
		if !ok {
			bk = &bucket{}
			merged[b] = bk
			keys = append(keys, b)
		}
		bk.sum += v
		bk.n++
	}
	for _, s := range series {
		for _, p := range s.Points {
			add(p.TS.UnixMilli(), p.Value)
		}
	}
	if len(keys) == 0 {
		return [][2]float64{}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	out := make([][2]float64, 0, len(keys))
	for _, k := range keys {
		b := merged[k]
		out = append(out, [2]float64{float64(k), b.sum / float64(b.n)})
	}
	return out
}

// unitOf 按指标名后缀给出单位（docs/03 §3 命名规范：单位进名字）。
func unitOf(metric string) string {
	switch {
	case strings.HasSuffix(metric, "_celsius"):
		return "celsius"
	case strings.HasSuffix(metric, "_rpm"):
		return "rpm"
	case strings.HasSuffix(metric, "_volts"):
		return "volts"
	case strings.HasSuffix(metric, "_watts"):
		return "watts"
	case strings.HasSuffix(metric, "_percent"):
		return "percent"
	case strings.HasSuffix(metric, "_bytes"):
		return "bytes"
	case strings.HasSuffix(metric, "_hours"):
		return "hours"
	case strings.HasSuffix(metric, "_seconds"):
		return "seconds"
	case strings.HasSuffix(metric, "_total"):
		return "count"
	default:
		return ""
	}
}
