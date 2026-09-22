package grpcstream

import (
	"fmt"
	"strings"
	"time"

	gen "github.com/LarryMKott/metalwatch/proto/gen"

	"github.com/LarryMKott/metalwatch/agent/internal/model"
)

// safeUTF8 把非法 UTF-8 字节替换为 U+FFFD。Protobuf 序列化强制 UTF-8，
// 而 Windows 侧个别采集字段（机型/系统版本等）可能带入历史编码残留；
// JSON 通道容忍、gRPC 通道不容忍，这里统一兜底。
func safeUTF8(s string) string { return strings.ToValidUTF8(s, "�") }

// unitOf 按指标名后缀推导单位（docs/03 §3 命名规范：单位进名字）。
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
	case strings.HasSuffix(metric, "_hours"):
		return "hours"
	case strings.HasSuffix(metric, "_total"):
		return "count"
	default:
		return ""
	}
}

// objectLabel 从采集标签里提取 proto 的单一 device 字段。
// 采集侧标签随传感器类型而异（device/slot/chip/rail），proto 契约只保留 device。
func objectLabel(labels map[string]string) string {
	for _, k := range []string{"device", "slot", "array", "chip", "rail"} {
		if v := strings.TrimSpace(labels[k]); v != "" {
			return v
		}
	}
	return ""
}

// toProtoReport 把采集模型转换为上行消息（kind=metrics）。
// RAID 与硬件清单在 proto 中位于 AssetSnapshot（W2 变更检测），此处暂不携带。
func toProtoReport(rep model.Report, batchID string, at time.Time) *gen.AgentReport {
	msg := &gen.AgentReport{
		Kind:      "metrics",
		Timestamp: at.UnixMilli(),
		BatchId:   batchID,
	}
	for _, m := range rep.Metrics {
		msg.Sensors = append(msg.Sensors, &gen.SensorMetric{
			Name:   safeUTF8(m.Name),
			Device: safeUTF8(objectLabel(m.Labels)),
			Value:  m.Value,
			Unit:   unitOf(m.Name),
		})
	}
	for _, d := range rep.Disks {
		msg.Disks = append(msg.Disks, &gen.DiskSmart{
			Device:            safeUTF8(strings.TrimPrefix(d.Device, "/dev/")),
			Model:             safeUTF8(d.Model),
			Sn:                safeUTF8(d.Serial),
			TempCelsius:       float64(d.TempCelsius),
			ReallocatedSector: int32(d.ReallocatedSector),
			WearLevel:         d.WearPercent,
			Health:            d.SmartStatus == "OK",
			PowerOnHours:      int32(d.PowerOnHours),
		})
	}
	return msg
}

// heartbeatReport 构造流上保活用的心跳消息。
func heartbeatReport(at time.Time) *gen.AgentReport {
	return &gen.AgentReport{Kind: "heartbeat", Timestamp: at.UnixMilli()}
}

// newBatchID 生成幂等键：纳秒时间戳（与 JSON 通道同格式，便于服务端统一去重）。
func newBatchID() string {
	return fmt.Sprintf("%d-%08x", time.Now().UTC().UnixNano(), uint32(time.Now().UTC().UnixMicro()))
}
