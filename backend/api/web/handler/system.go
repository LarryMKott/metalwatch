package handler

import (
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	"github.com/LarryMKott/metalwatch/internal/task"
	"github.com/LarryMKott/metalwatch/pkg/config"
	"github.com/LarryMKott/metalwatch/pkg/utils"
)

// SystemHandler 承载系统健康与存储目录接口（D31 第 6 条：路由域对象化）。
// 依赖在构造时注入，方法只做参数解析与响应封装。
type SystemHandler struct {
	store   adapter.MetadataStore
	tsdb    adapter.TimeSeriesStore
	pool    *task.Pool
	config  config.Config
	version string
	started time.Time
}

// NewSystemHandler 组装系统状态处理器。store 必填；tsdb / pool 允许为 nil
// （对应「未启用时序」「任务池未就绪」的展示态）。
func NewSystemHandler(store adapter.MetadataStore, tsdb adapter.TimeSeriesStore,
	pool *task.Pool, cfg config.Config, version string, started time.Time) *SystemHandler {
	return &SystemHandler{store: store, tsdb: tsdb, pool: pool, config: cfg,
		version: version, started: started}
}

// Register 把本域路由挂到给定的根与 /api/v1 分组上。
func (h *SystemHandler) Register(root *gin.Engine, v1 *gin.RouterGroup) {
	root.GET("/healthz", h.Health)
	v1.GET("/system/status", h.SystemStatus)
	v1.GET("/system/storage/backends", h.StorageBackends)
}

// Health 是存活与就绪探针：任一存储不可用即 503，便于外部监控直接告警。
// 不暴露路径、凭据与主机清单。
func (h *SystemHandler) Health(c *gin.Context) {
	ctx, cancel := utils.Timeout(c, 3*time.Second)
	defer cancel()

	dbState := "ok"
	if err := h.store.PingContext(ctx); err != nil {
		dbState = "down"
	}
	tsState := "ok"
	if h.tsdb == nil {
		tsState = "disabled"
	}

	body := gin.H{
		"status":     "ok",
		"version":    h.version,
		"uptime_sec": int(time.Since(h.started).Seconds()),
		"storage": gin.H{
			"metadata": gin.H{"driver": string(h.store.Dialect()), "state": dbState},
			"tsdb":     gin.H{"driver": h.tsDriverName(), "state": tsState},
		},
	}
	if dbState != "ok" {
		body["status"] = "degraded"
		c.JSON(http.StatusServiceUnavailable, body)
		return
	}
	c.JSON(http.StatusOK, body)
}

// SystemStatus 供前端「系统状态」页展示应用自身健康状况。
func (h *SystemHandler) SystemStatus(c *gin.Context) {
	ctx, cancel := utils.Timeout(c, 5*time.Second)
	defer cancel()

	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	schemaVersion, err := h.store.CurrentVersion(ctx)
	if err != nil {
		utils.Fail(c, err)
		return
	}

	_, hostCount, err := h.store.Hosts().List(ctx, adapter.ListFilter{Limit: 1})
	if err != nil {
		utils.Fail(c, err)
		return
	}

	storage := gin.H{
		"metadata_driver": string(h.store.Dialect()),
		"tsdb_driver":     h.tsDriverName(),
		"schema_version":  schemaVersion,
	}
	if h.store.Dialect() == adapter.DialectSQLite {
		path := h.config.DBFilePath()
		storage["metadata_file"] = path
		if fi, err := os.Stat(path); err == nil {
			storage["size_bytes"] = fi.Size()
		}
	}

	collect := gin.H{
		"sensor_interval_sec": h.config.Collect.SensorInterval,
		"ipmi_concurrency":    h.config.Collect.IPMIConcurrency,
	}
	if h.pool != nil {
		failing, blocked := h.pool.Stats()
		collect["failing_targets"] = failing
		collect["blocked_targets"] = blocked
	}

	c.JSON(http.StatusOK, gin.H{
		"version":     h.version,
		"uptime_sec":  int(time.Since(h.started).Seconds()),
		"goroutines":  runtime.NumGoroutine(),
		"heap_bytes":  m.HeapAlloc,
		"sys_bytes":   m.Sys,
		"storage":     storage,
		"host_count":  hostCount,
		"collect":     collect,
		"server_time": time.Now().UTC().Format(time.RFC3339),
	})
}

// StorageBackends 返回全部存储后端的可用状态，供前端「存储管理」页渲染动态表单，
// 并明确区分「已实现」与「规划中」。
func (h *SystemHandler) StorageBackends(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"current": gin.H{
			"metadata_driver": string(h.store.Dialect()),
			"tsdb_driver":     h.tsDriverName(),
		},
		"backends": adapter.Catalog(),
	})
}

func (h *SystemHandler) tsDriverName() string {
	if h.tsdb == nil {
		return ""
	}
	return h.tsdb.Name()
}
