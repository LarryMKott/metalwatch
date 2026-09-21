package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultIsValid(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("默认配置应通过校验: %v", err)
	}
	if cfg.DB.Driver != DriverSQLite {
		t.Fatalf("默认存储应为 sqlite，实际 %s", cfg.DB.Driver)
	}
	if cfg.Collect.BMCSerial != true {
		t.Fatal("默认应开启同一 BMC 串行（docs/01 D10）")
	}
}

func TestValidateRejectsBadValues(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantSub string
	}{
		{"端口越界", func(c *Config) { c.Server.Port = 70000 }, "server.port"},
		{"日志级别非法", func(c *Config) { c.Server.LogLevel = "verbose" }, "server.log_level"},
		{"未知驱动", func(c *Config) { c.DB.Driver = "oracle" }, "db.driver"},
		{"postgres 缺 DSN", func(c *Config) { c.DB.Driver = DriverPostgres }, "db.dsn"},
		{"采集周期过小", func(c *Config) { c.Collect.SensorInterval = 1 }, "sensor_interval"},
		{"采集周期过大", func(c *Config) { c.Collect.SensorInterval = 3600 }, "sensor_interval"},
		{"并发为 0", func(c *Config) { c.Collect.IPMIConcurrency = 0 }, "ipmi_concurrency"},
		{"并发过大", func(c *Config) { c.Collect.IPMIConcurrency = 9999 }, "ipmi_concurrency"},
		{"退避上限过小", func(c *Config) { c.Collect.BMCBackoffMaxSec = 10 }, "bmc_backoff_max_sec"},
		{"保留期为 0", func(c *Config) { c.Retention.RawDays = 0 }, "raw_days"},
		{"聚合期短于原始期", func(c *Config) {
			c.Retention.RawDays = 30
			c.Retention.AggDays = 7
		}, "agg_days"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			tc.mutate(&cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatal("应校验失败但通过了")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("错误信息应包含 %q，实际: %v", tc.wantSub, err)
			}
		})
	}
}

func TestPostgresWithDSNIsValid(t *testing.T) {
	cfg := Default()
	cfg.DB.Driver = DriverPostgres
	cfg.DB.DSN = "postgres://metalwatch:pw@10.0.0.5:5432/metalwatch?sslmode=require"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("带 DSN 的 postgres 配置应通过: %v", err)
	}
}

func TestEnvOverride(t *testing.T) {
	t.Setenv("METALWATCH_DB_DRIVER", DriverPostgres)
	t.Setenv("METALWATCH_DB_DSN", "postgres://u:p@h:5432/db")
	t.Setenv("METALWATCH_SENSOR_INTERVAL", "60")
	t.Setenv("METALWATCH_IPMI_CONCURRENCY", "8")
	t.Setenv("METALWATCH_ALERT_WEBHOOK", "https://example.com/hook")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if cfg.DB.Driver != DriverPostgres || cfg.DB.DSN == "" {
		t.Fatalf("环境变量未覆盖存储配置: %+v", cfg.DB)
	}
	if cfg.Collect.SensorInterval != 60 || cfg.Collect.IPMIConcurrency != 8 {
		t.Fatalf("环境变量未覆盖采集配置: %+v", cfg.Collect)
	}
	if cfg.Alert.WebhookURL != "https://example.com/hook" {
		t.Fatalf("webhook 未生效: %s", cfg.Alert.WebhookURL)
	}
}

func TestLoadMissingFileUsesDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "not-exists.yaml"))
	if err != nil {
		t.Fatalf("配置文件缺失不应报错: %v", err)
	}
	if cfg.Server.Port != 18080 {
		t.Fatalf("应回落到默认端口，实际 %d", cfg.Server.Port)
	}
}

func TestLoadParsesYAMLAndRejectsUnknownField(t *testing.T) {
	dir := t.TempDir()

	good := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(good, []byte(`
server:
  port: 19001
  log_level: debug
db:
  driver: sqlite
collect:
  sensor_interval: 45
  ipmi_concurrency: 24
retention:
  raw_days: 7
  agg_days: 365
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(good)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if cfg.Server.Port != 19001 || cfg.Collect.SensorInterval != 45 || cfg.Retention.AggDays != 365 {
		t.Fatalf("YAML 未正确解析: %+v", cfg)
	}

	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("server:\n  prot: 18080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(bad); err == nil {
		t.Fatal("未知字段应报错（KnownFields 严格模式）")
	}
}

func TestDBFilePath(t *testing.T) {
	cfg := Default()
	cfg.Server.DataDir = "/vol1/@appdata/metalwatch/data"
	if got := cfg.DBFilePath(); got != filepath.Join(cfg.Server.DataDir, "metalwatch.db") {
		t.Fatalf("默认库路径推导错误: %s", got)
	}

	cfg.DB.File = filepath.Join("custom", "mw.db")
	if got := cfg.DBFilePath(); got != cfg.DB.File {
		t.Fatalf("显式 db.file 应优先: %s", got)
	}
	// 断言与 filepath.Dir 一致，避免平台差异（Windows 下盘符与分隔符不同）
	if got, want := cfg.StorageDir(), filepath.Dir(cfg.DB.File); got != want {
		t.Fatalf("StorageDir 推导错误: got=%s want=%s", got, want)
	}
}
