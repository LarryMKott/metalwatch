// Package config 负责加载与校验 MetalWatch 服务端配置。
//
// 配置来源优先级（后者覆盖前者）：
//  1. 代码内置默认值
//  2. YAML 配置文件（由 FPK 的 cmd/install_callback 与 cmd/config_callback 渲染到 TRIM_PKGETC）
//  3. 环境变量（用于密钥等敏感项，避免落盘）
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	// 内置 IANA 时区数据库（约 450KB）。
	//
	// 原因：Validate 用 time.LoadLocation 校验 server.timezone。该函数默认只查宿主机的
	// /usr/share/zoneinfo 与 $GOROOT/lib/time/zoneinfo.zip——FPK 跑在飞牛精简系统上，
	// 前者不一定存在；而我们以 `-trimpath` 构建，后者也不可用。结果就是
	// "Asia/Shanghai" 解析失败并被判为致命配置错误，应用启动即退出。
	// 空导入 tzdata 后时区数据与二进制一起分发，不再依赖宿主机环境。
	_ "time/tzdata"

	"gopkg.in/yaml.v3"
)

// 数据库驱动名。默认 sqlite（零部署）；postgres 用于「数据库独立部署」场景。
const (
	DriverSQLite   = "sqlite"
	DriverPostgres = "postgres"
)

// 时序存储驱动名。embedded 为默认（进程内嵌，零依赖）。
const (
	DriverTSDBEmbedded        = "embedded"
	DriverTSDBPrometheus      = "prometheus"
	DriverTSDBVictoriaMetrics = "victoriametrics"
	DriverTSDBInfluxDB2       = "influxdb2"
)

type Config struct {
	Server     Server     `yaml:"server"`
	DB         DB         `yaml:"db"`
	Timeseries Timeseries `yaml:"timeseries"`
	Collect    Collect    `yaml:"collect"`
	Retention  Retention  `yaml:"retention"`
	GeoIP      GeoIP      `yaml:"geoip"`
	Alert      Alert      `yaml:"alert"`
}

// Timeseries 是时序存储配置：默认进程内嵌（零依赖），也可指向独立部署的服务。
type Timeseries struct {
	// Driver 取值：embedded（默认）/ prometheus / victoriametrics / influxdb2
	Driver string `yaml:"driver"`
	// Endpoint 为外部时序服务地址，driver != embedded 时必填。
	Endpoint string `yaml:"endpoint"`
	// Username / Password 建议走环境变量，不要写进配置文件。
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type Server struct {
	Port     int    `yaml:"port"`
	Timezone string `yaml:"timezone"`
	LogLevel string `yaml:"log_level"`
	// DataDir 为运行数据目录，来源优先级：--data > METALWATCH_DATA_DIR > 本配置 > 环境默认值。
	// 生产环境对应 FPK 的 TRIM_PKGVAR/data，必须显式给出（缺失即启动失败）；
	// 开发环境由 cmd/server 落到仓库内的 tmp-data（见 docs/01 D35）。
	DataDir string `yaml:"data_dir"`
	// LogDir 同上，生产环境对应 TRIM_PKGVAR/logs。
	LogDir string `yaml:"log_dir"`
}

type DB struct {
	Driver string `yaml:"driver"`
	// DSN 仅在 driver != sqlite 时必填。为空时回退到环境变量 METALWATCH_DB_DSN，
	// 便于把连接串（含口令）放在应用外部而不落盘。
	DSN string `yaml:"dsn"`
	// File 为 sqlite 模式下的库文件路径；为空则由 DataDir/metalwatch.db 推导。
	File         string `yaml:"file"`
	MaxOpenConns int    `yaml:"max_open_conns"`
	MaxIdleConns int    `yaml:"max_idle_conns"`
	// BusyTimeout 为 sqlite 的写锁等待时长。
	BusyTimeout time.Duration `yaml:"busy_timeout"`
}

type Collect struct {
	IPMIConcurrency   int  `yaml:"ipmi_concurrency"`
	SensorInterval    int  `yaml:"sensor_interval"`
	AssetInterval     int  `yaml:"asset_interval"`
	SmartInterval     int  `yaml:"smart_interval"`
	BMCSerial         bool `yaml:"bmc_serial"`
	BMCRetryMax       int  `yaml:"bmc_retry_max"`
	BMCBackoffMaxSec  int  `yaml:"bmc_backoff_max_sec"`
	CollectTimeoutSec int  `yaml:"collect_timeout_sec"`
}

type Retention struct {
	RawDays int `yaml:"raw_days"`
	AggDays int `yaml:"agg_days"`
}

type GeoIP struct {
	Enabled bool   `yaml:"enabled"`
	MMDBDir string `yaml:"mmdb_dir"`
}

type Alert struct {
	WebhookURL string `yaml:"webhook_url"`
}

// Default 返回一套可直接运行的默认配置。
//
// DataDir / LogDir 刻意留空，不猜数据该落在哪：由 cmd/server 按运行环境补齐
// （开发 → 仓库内 tmp-data；生产 → 必须显式指定，否则启动失败）。
// 给一个看似无害的 "./data" 默认值反而危险——在只读的安装目录下会直接写失败，
// 在 FPK 里则可能把数据写到会被卸载清理的路径。
func Default() Config {
	return Config{
		Server: Server{Port: 18080, Timezone: "Asia/Shanghai", LogLevel: "info"},
		DB: DB{
			Driver:       DriverSQLite,
			MaxOpenConns: 1,
			MaxIdleConns: 1,
			BusyTimeout:  5 * time.Second,
		},
		Collect: Collect{
			IPMIConcurrency:   16,
			SensorInterval:    30,
			AssetInterval:     86400,
			SmartInterval:     900,
			BMCSerial:         true,
			BMCRetryMax:       3,
			BMCBackoffMaxSec:  1800,
			CollectTimeoutSec: 30,
		},
		Retention: Retention{RawDays: 7, AggDays: 180},
		GeoIP:     GeoIP{Enabled: true},
		Timeseries: Timeseries{
			Driver: DriverTSDBEmbedded,
		},
	}
}

// Load 读取配置文件并应用环境变量覆盖，最后做整体校验。
// path 为空或文件不存在时不报错，直接使用默认值（便于本地开发与测试）。
func Load(path string) (Config, error) {
	cfg := Default()

	if path != "" {
		raw, err := os.ReadFile(path)
		switch {
		case err == nil:
			// 严格模式：未知字段直接报错，避免配置写错却静默生效。
			dec := yaml.NewDecoder(strings.NewReader(string(raw)))
			dec.KnownFields(true)
			if err := dec.Decode(&cfg); err != nil && !errors.Is(err, os.ErrClosed) {
				return cfg, fmt.Errorf("解析配置文件 %s 失败: %w", path, err)
			}
		case os.IsNotExist(err):
			// 允许缺失，使用默认值
		default:
			return cfg, fmt.Errorf("读取配置文件 %s 失败: %w", path, err)
		}
	}

	applyEnv(&cfg)

	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func applyEnv(cfg *Config) {
	if v := os.Getenv("METALWATCH_DB_DSN"); v != "" {
		cfg.DB.DSN = v
	}
	if v := os.Getenv("METALWATCH_DB_DRIVER"); v != "" {
		cfg.DB.Driver = v
	}
	if v := os.Getenv("METALWATCH_TSDB_DRIVER"); v != "" {
		cfg.Timeseries.Driver = v
	}
	if v := os.Getenv("METALWATCH_TSDB_ENDPOINT"); v != "" {
		cfg.Timeseries.Endpoint = v
	}
	if v := os.Getenv("METALWATCH_ALERT_WEBHOOK"); v != "" {
		cfg.Alert.WebhookURL = v
	}
	if v := os.Getenv("METALWATCH_LOG_LEVEL"); v != "" {
		cfg.Server.LogLevel = v
	}
	if v := os.Getenv("METALWATCH_DATA_DIR"); v != "" {
		cfg.Server.DataDir = v
	}
	if v := os.Getenv("METALWATCH_SENSOR_INTERVAL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Collect.SensorInterval = n
		}
	}
	if v := os.Getenv("METALWATCH_IPMI_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Collect.IPMIConcurrency = n
		}
	}
}

// Validate 校验配置取值，返回面向用户的、可执行的错误信息。
func (c Config) Validate() error {
	var errs []string

	if c.Server.Port < 1 || c.Server.Port > 65535 {
		errs = append(errs, fmt.Sprintf("server.port 必须在 1~65535 之间（当前 %d）", c.Server.Port))
	}
	switch c.Server.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, "server.log_level 只能是 debug/info/warn/error（当前 "+c.Server.LogLevel+"）")
	}
	if c.Server.Timezone != "" {
		// 二进制已内置 tzdata（cmd/server 空导入 time/tzdata），不依赖宿主机的
		// /usr/share/zoneinfo，因此这里解析失败只可能是时区名写错——属于配置错误，阻断启动。
		if _, err := time.LoadLocation(c.Server.Timezone); err != nil {
			errs = append(errs, "server.timezone 无效: "+c.Server.Timezone+"（应为 IANA 时区名，如 Asia/Shanghai）")
		}
	}

	switch c.DB.Driver {
	case DriverSQLite:
	case DriverPostgres:
		if strings.TrimSpace(c.DB.DSN) == "" {
			errs = append(errs, "db.dsn 为空：使用 postgres 时必须提供外部数据库连接串（也可用环境变量 METALWATCH_DB_DSN）")
		}
	default:
		errs = append(errs, "db.driver 只能是 sqlite 或 postgres（当前 "+c.DB.Driver+"）")
	}

	switch c.Timeseries.Driver {
	case DriverTSDBEmbedded:
	case DriverTSDBPrometheus, DriverTSDBVictoriaMetrics, DriverTSDBInfluxDB2:
		if strings.TrimSpace(c.Timeseries.Endpoint) == "" {
			errs = append(errs, "timeseries.endpoint 为空：使用独立部署的时序存储时必须提供地址（也可用环境变量 METALWATCH_TSDB_ENDPOINT）")
		}
	default:
		errs = append(errs, "timeseries.driver 只能是 embedded/prometheus/victoriametrics/influxdb2（当前 "+c.Timeseries.Driver+"）")
	}

	if c.Collect.SensorInterval < 10 || c.Collect.SensorInterval > 600 {
		errs = append(errs, fmt.Sprintf("collect.sensor_interval 需在 10~600 秒之间（当前 %d）", c.Collect.SensorInterval))
	}
	if c.Collect.IPMIConcurrency < 1 || c.Collect.IPMIConcurrency > 256 {
		errs = append(errs, fmt.Sprintf("collect.ipmi_concurrency 需在 1~256 之间（当前 %d）", c.Collect.IPMIConcurrency))
	}
	if c.Collect.AssetInterval < 300 {
		errs = append(errs, "collect.asset_interval 不应小于 300 秒（静态资产无需高频采集）")
	}
	if c.Collect.SmartInterval < 300 {
		errs = append(errs, "collect.smart_interval 不应小于 300 秒（SMART 读取对磁盘有开销）")
	}
	if c.Collect.BMCRetryMax < 1 {
		errs = append(errs, "collect.bmc_retry_max 至少为 1")
	}
	if c.Collect.BMCBackoffMaxSec < 60 {
		errs = append(errs, "collect.bmc_backoff_max_sec 至少为 60 秒，否则 BMC 过载时来不及退避")
	}
	if c.Retention.RawDays < 1 {
		errs = append(errs, "retention.raw_days 至少为 1")
	}
	if c.Retention.AggDays < c.Retention.RawDays {
		errs = append(errs, "retention.agg_days 不应小于 raw_days")
	}

	if len(errs) > 0 {
		return fmt.Errorf("配置校验未通过:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

// DBFilePath 返回 sqlite 库文件的最终路径。
func (c Config) DBFilePath() string {
	if c.DB.File != "" {
		return c.DB.File
	}
	return filepath.Join(c.Server.DataDir, "metalwatch.db")
}

// StorageDir 返回需要保证存在的运行数据目录。
func (c Config) StorageDir() string {
	if c.DB.File != "" {
		return filepath.Dir(c.DB.File)
	}
	return c.Server.DataDir
}
