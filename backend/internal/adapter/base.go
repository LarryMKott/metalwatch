package adapter

import (
	"context"
	"errors"
	"io/fs"
	"time"
)

// MetadataStore 是关系型元数据存储的统一接口。
// 目前由 SQLite 后端实现；后续 MySQL / PostgreSQL / GBase8s 后端只需实现本接口 +
// 在 init 中调用 Register，上层业务代码零改动。
type MetadataStore interface {
	Dialect() Dialect
	PingContext(ctx context.Context) error
	Close() error

	// Migrate 应用内嵌迁移，返回本次新应用的迁移数量。
	Migrate(ctx context.Context, fsys fs.FS, dir string) (int, error)
	// CurrentVersion 返回已应用的最大迁移版本。
	CurrentVersion(ctx context.Context) (int, error)

	Hosts() *HostRepo
	AgentTokens() *AgentTokenRepo
	EnrollCodes() *EnrollCodeRepo
	Thresholds() *ThresholdRepo
	Alerts() *AlertEventRepo
	BMC() *BMCRepo
	CollectRuns() *CollectRunRepo
	Components() *ComponentRepo
	Assets() *AssetRepo
	NotifyChannels() *NotifyChannelRepo
}

// 编译期断言：SQLite 后端满足接口契约。
var _ MetadataStore = (*Store)(nil)

// ---------- 时序存储（可插拔） ----------

// Sample 是一次采样。
type Sample struct {
	Metric string
	Labels map[string]string
	Value  float64
	TS     time.Time
}

// Point 是曲线上的一个点。
type Point struct {
	TS    time.Time
	Value float64
}

// Series 是一条时间线。
type Series struct {
	Labels map[string]string
	Points []Point
}

// Query 是时序查询条件（不引入 PromQL：只支持这四种参数，够用且可控）。
type Query struct {
	Metric string
	Labels map[string]string
	Start  time.Time
	End    time.Time
	Step   time.Duration
	// Aggregated 为 true 时读 5m 聚合档（range > 24h 由服务端自动选择）
	Aggregated bool
}

// TimeSeriesStore 是时序存储抽象。默认实现为进程内嵌 TSDB（零依赖），
// 也可切换到独立部署的 Prometheus / VictoriaMetrics / InfluxDB2。
type TimeSeriesStore interface {
	Name() string
	Write(ctx context.Context, samples []Sample) error
	Query(ctx context.Context, q Query) ([]Series, error)
	// Retention 返回原始档与聚合档的保留天数。
	Retention() (rawDays, aggDays int)
	Close() error
}

// IngestDeduper 是写入侧的幂等去重能力（docs/04 §2.2：batch_id 重放不重复入库）。
// 内嵌 TSDB 用独立表实现；接入外部时序服务时也可在其前放置同样的去重层。
type IngestDeduper interface {
	// SeenBatch 标记一个批次为已接收，返回该批次此前是否已被处理过。
	SeenBatch(ctx context.Context, batchID string, hostID int64, points int) (seen bool, err error)
}

// MaintenanceStore 是可选的周期维护能力（rollup 聚合、按保留期清理）。
// 由服务端维护循环定期调用；外部时序服务通常自行维护，可不实现。
type MaintenanceStore interface {
	// Maintenance 执行一轮聚合与过期清理，返回处理的序列数与清理的块数。
	Maintenance(ctx context.Context) (rolledUp int, pruned int64, err error)
}

// TimeSeriesFactory 构造时序后端实例。
type TimeSeriesFactory func(cfg TimeSeriesConfig) (TimeSeriesStore, error)

// TimeSeriesConfig 是时序后端的通用配置。
type TimeSeriesConfig struct {
	// Driver 取值见 constants.go 的 TSDriver* 常量。
	Driver string
	// RootDir 为内嵌实现的落盘目录（TRIM_PKGVAR/data/tsdb）。
	RootDir string
	// Endpoint 为外部时序服务的地址（如 http://127.0.0.1:8428）。
	Endpoint string
	// Username / Password 走配置或环境变量，不得落日志。
	Username string
	Password string
	// RawDays / AggDays 为保留策略。
	RawDays int
	AggDays int
}

// ErrNotImplemented 表示该后端已规划但尚未实现。
var ErrNotImplemented = errors.New("后端尚未实现")

// Backend 描述一个存储后端的可用状态，供 WebUI 的「存储管理」页展示。
type Backend struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"` // metadata | timeseries
	Implemented bool   `json:"implemented"`
	// Deployment 说明部署形态：builtin（随应用内嵌）/ external（独立部署）
	Deployment string `json:"deployment"`
	Note       string `json:"note"`
	// WorkItem 指向开发计划中的工作项（未实现时给出）
	WorkItem string `json:"work_item,omitempty"`
}
