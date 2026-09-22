// Package service — 指标命名规范（docs/03 §3）的单一事实来源：
// JSON 通道（api/agentpb）与 gRPC 通道（api/agent）共用，避免两处白名单漂移。
package service

import "strings"

// allowedMetrics 指标白名单：只接受已知指标，避免脏数据污染时序库。
var allowedMetrics = map[string]bool{
	"up": true, "collect_duration_seconds": true,
	"cpu_temp_celsius": true, "memory_temp_celsius": true,
	"fan_rpm": true, "voltage_volts": true, "power_watts": true,
	"psu_status": true, "disk_temp_celsius": true, "disk_smart_status": true,
	"disk_wear_percent": true, "disk_reallocated_sectors": true,
	"disk_power_on_hours": true, "raid_state": true, "raid_rebuild_percent": true,
	"nic_error_total": true, "host_info": true,
}

// forbiddenLabelKeys 是高基数标签黑名单（会打爆时序索引，见 docs/03 §3）。
var forbiddenLabelKeys = map[string]bool{
	"sn": true, "serial": true, "timestamp": true, "ts": true,
	"message": true, "title": true, "alert": true,
}

// ValidMetricName 判断指标名是否在白名单内。
func ValidMetricName(name string) bool { return allowedMetrics[name] }

// LabelAllowed 判断自定义标签键是否允许（拒绝高基数键，键名比较不区分大小写）。
func LabelAllowed(key string) bool { return !forbiddenLabelKeys[strings.ToLower(key)] }
