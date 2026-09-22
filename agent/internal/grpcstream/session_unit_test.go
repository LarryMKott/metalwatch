package grpcstream

import (
	"testing"
	"time"

	"github.com/LarryMKott/metalwatch/agent/internal/model"
)

func TestBackoffGrowthAndCap(t *testing.T) {
	lo, hi := 0.75, 1.75 // [-25%, +75%) 抖动区间
	for attempt, base := range map[int]time.Duration{
		0: time.Second, 1: 2 * time.Second, 3: 8 * time.Second,
		6: 60 * time.Second, 10: 60 * time.Second, // 封顶
	} {
		for i := 0; i < 20; i++ {
			got := backoff(attempt)
			if got < time.Duration(float64(base)*lo) || got >= time.Duration(float64(base)*hi) {
				t.Fatalf("attempt %d backoff = %v, 期望在 [%v, %v) 内", attempt, got,
					time.Duration(float64(base)*lo), time.Duration(float64(base)*hi))
			}
		}
	}
	if got := backoff(-1); got < 100*time.Millisecond {
		t.Fatalf("负 attempt 应钳到下限, got %v", got)
	}
}

func TestToProtoReport(t *testing.T) {
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	rep := model.Report{
		Metrics: []model.Sample{
			{Name: "cpu_temp_celsius", Labels: map[string]string{"chip": "Core0"}, Value: 47.5},
			{Name: "fan_rpm", Labels: map[string]string{"slot": "Fan1"}, Value: 4200},
			{Name: "voltage_volts", Labels: map[string]string{"rail": "12V"}, Value: 12.1},
		},
		Disks: []model.DiskInfo{{
			Device: "/dev/sda", Model: "ST8000NM", Serial: "SN-1",
			TempCelsius: 38, SmartStatus: "OK", PowerOnHours: 1000,
		}},
	}
	msg := toProtoReport(rep, "batch-9", at)

	if msg.Kind != "metrics" || msg.BatchId != "batch-9" || msg.Timestamp != at.UnixMilli() {
		t.Fatalf("消息头不符: %+v", msg)
	}
	if len(msg.Sensors) != 3 {
		t.Fatalf("sensors = %d, want 3", len(msg.Sensors))
	}
	c0 := msg.Sensors[0]
	if c0.Device != "Core0" || c0.Unit != "celsius" || c0.Value != 47.5 {
		t.Fatalf("cpu sensor 转换错误: %+v", c0)
	}
	if msg.Sensors[1].Device != "Fan1" || msg.Sensors[1].Unit != "rpm" {
		t.Fatalf("fan sensor 转换错误: %+v", msg.Sensors[1])
	}
	if msg.Sensors[2].Device != "12V" || msg.Sensors[2].Unit != "volts" {
		t.Fatalf("voltage sensor 转换错误: %+v", msg.Sensors[2])
	}
	if len(msg.Disks) != 1 || msg.Disks[0].Device != "sda" || !msg.Disks[0].Health {
		t.Fatalf("disk 转换错误: %+v", msg.Disks)
	}
}
