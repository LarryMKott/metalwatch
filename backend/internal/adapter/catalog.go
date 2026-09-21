package adapter

import (
	"fmt"
	"sync"

	"gitee.com/zhangyilin_233/metalwatch/pkg/config"
)

// 时序后端驱动名。
// 时序驱动常量以 config 为单一来源，避免两处维护。
const (
	TSDriverEmbedded        = config.DriverTSDBEmbedded
	TSDriverPrometheus      = config.DriverTSDBPrometheus
	TSDriverVictoriaMetrics = config.DriverTSDBVictoriaMetrics
	TSDriverInfluxDB2       = config.DriverTSDBInfluxDB2
)

var (
	tsMu       sync.RWMutex
	tsRegistry = map[string]TimeSeriesFactory{}
)

// RegisterTimeSeries 注册一个时序后端实现。
func RegisterTimeSeries(driver string, f TimeSeriesFactory) {
	tsMu.Lock()
	defer tsMu.Unlock()
	if _, dup := tsRegistry[driver]; dup {
		panic("adapter: 时序后端重复注册: " + driver)
	}
	tsRegistry[driver] = f
}

// OpenTimeSeries 按配置构造时序后端。默认 embedded。
func OpenTimeSeries(cfg TimeSeriesConfig) (TimeSeriesStore, error) {
	if cfg.Driver == "" {
		cfg.Driver = TSDriverEmbedded
	}
	tsMu.RLock()
	f, ok := tsRegistry[cfg.Driver]
	tsMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: 时序后端 %q 未注册（已实现: %v）",
			ErrNotImplemented, cfg.Driver, implementedTimeSeries())
	}
	return f(cfg)
}

func implementedTimeSeries() []string {
	tsMu.RLock()
	defer tsMu.RUnlock()
	out := make([]string, 0, len(tsRegistry))
	for k := range tsRegistry {
		out = append(out, k)
	}
	return out
}

// Catalog 返回所有存储后端的可用状态，供 WebUI「存储管理」页展示
// （默认 SQLite，其余支持独立部署，按开发计划分批接入）。
func Catalog() []Backend {
	implemented := map[string]bool{}
	for _, d := range Registered() {
		implemented[string(d)] = true
	}
	for _, d := range implementedTimeSeries() {
		implemented[d] = true
	}

	all := []Backend{
		{Name: string(DialectSQLite), Kind: "metadata", Deployment: "builtin",
			Note: "默认存储：单文件落 TRIM_PKGVAR，无需部署任何服务，重装不丢数据"},
		{Name: string(DialectPostgres), Kind: "metadata", Deployment: "external", WorkItem: "W1c",
			Note: "独立部署：通过 db.dsn 指向外部 PostgreSQL，适合多实例共用或已有 DBA 体系"},
		{Name: "mysql", Kind: "metadata", Deployment: "external", WorkItem: "W1c",
			Note: "独立部署：兼容 MySQL 5.7+/8.0，需注意其不支持部分唯一索引（本项目已规避）"},
		{Name: "gbase8s", Kind: "metadata", Deployment: "external", WorkItem: "W2",
			Note: "独立部署：国产库适配，SQL 方言差异需单独验证（分页语法、自增列、时间函数）"},
		{Name: TSDriverEmbedded, Kind: "timeseries", Deployment: "builtin", WorkItem: "W1b",
			Note: "默认时序存储：prometheus/tsdb 作为库嵌入进程，无端口、无外部依赖"},
		{Name: TSDriverPrometheus, Kind: "timeseries", Deployment: "external", WorkItem: "W1c",
			Note: "独立部署：写入走 remote_write，读取走 HTTP API；需在外部维护保留策略"},
		{Name: TSDriverVictoriaMetrics, Kind: "timeseries", Deployment: "external", WorkItem: "W1c",
			Note: "独立部署：单机版资源占用低，兼容 Prometheus 协议，适合替换外置 Prometheus"},
		{Name: TSDriverInfluxDB2, Kind: "timeseries", Deployment: "external", WorkItem: "W2",
			Note: "独立部署：行协议写入（line protocol），标签基数需按 docs/03 §3 约束"},
	}
	for i := range all {
		all[i].Implemented = implemented[all[i].Name]
	}
	return all
}
