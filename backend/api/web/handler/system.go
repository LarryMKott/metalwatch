package handler

import (
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	"github.com/LarryMKott/metalwatch/internal/app"
	"github.com/LarryMKott/metalwatch/pkg/utils"
)

// Health 是存活与就绪探针：任一存储不可用即 503，便于外部监控直接告警。
// 不暴露路径、凭据与主机清单。
func Health(d *app.Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := utils.Timeout(c, 3*time.Second)
		defer cancel()

		dbState := "ok"
		if err := d.Store.PingContext(ctx); err != nil {
			dbState = "down"
		}
		tsState := "ok"
		if d.TSDB == nil {
			tsState = "disabled"
		}

		body := gin.H{
			"status":     "ok",
			"version":    d.Version,
			"uptime_sec": int(time.Since(d.Started).Seconds()),
			"storage": gin.H{
				"metadata": gin.H{"driver": string(d.Store.Dialect()), "state": dbState},
				"tsdb":     gin.H{"driver": tsDriverName(d), "state": tsState},
			},
		}
		if dbState != "ok" {
			body["status"] = "degraded"
			c.JSON(http.StatusServiceUnavailable, body)
			return
		}
		c.JSON(http.StatusOK, body)
	}
}

// SystemStatus 供前端「系统状态」页展示应用自身健康状况。
func SystemStatus(d *app.Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := utils.Timeout(c, 5*time.Second)
		defer cancel()

		var m runtime.MemStats
		runtime.ReadMemStats(&m)

		schemaVersion, err := d.Store.CurrentVersion(ctx)
		if err != nil {
			utils.Fail(c, err)
			return
		}

		_, hostCount, err := d.Store.Hosts().List(ctx, adapter.ListFilter{Limit: 1})
		if err != nil {
			utils.Fail(c, err)
			return
		}

		storage := gin.H{
			"metadata_driver": string(d.Store.Dialect()),
			"tsdb_driver":     tsDriverName(d),
			"schema_version":  schemaVersion,
		}
		if d.Store.Dialect() == adapter.DialectSQLite {
			path := d.Config.DBFilePath()
			storage["metadata_file"] = path
			if fi, err := os.Stat(path); err == nil {
				storage["size_bytes"] = fi.Size()
			}
		}

		collect := gin.H{
			"sensor_interval_sec": d.Config.Collect.SensorInterval,
			"ipmi_concurrency":    d.Config.Collect.IPMIConcurrency,
		}
		if d.Pool != nil {
			failing, blocked := d.Pool.Stats()
			collect["failing_targets"] = failing
			collect["blocked_targets"] = blocked
		}

		c.JSON(http.StatusOK, gin.H{
			"version":     d.Version,
			"uptime_sec":  int(time.Since(d.Started).Seconds()),
			"goroutines":  runtime.NumGoroutine(),
			"heap_bytes":  m.HeapAlloc,
			"sys_bytes":   m.Sys,
			"storage":     storage,
			"host_count":  hostCount,
			"collect":     collect,
			"server_time": time.Now().UTC().Format(time.RFC3339),
		})
	}
}

// StorageBackends 返回全部存储后端的可用状态，供前端「存储管理」页渲染动态表单，
// 并明确区分「已实现」与「规划中」。
func StorageBackends(d *app.Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"current": gin.H{
				"metadata_driver": string(d.Store.Dialect()),
				"tsdb_driver":     tsDriverName(d),
			},
			"backends": adapter.Catalog(),
		})
	}
}

func tsDriverName(d *app.Deps) string {
	if d.TSDB == nil {
		return ""
	}
	return d.TSDB.Name()
}
