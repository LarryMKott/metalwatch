//go:build linux

package collect

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/LarryMKott/metalwatch/agent/internal/model"
)

// platformSteps 注册 Linux 采集项。
//
// 直接读 /sys/class/hwmon 与 /proc，不依赖 lm-sensors；避免解析 `sensors` 的本地化输出。
func platformSteps() []step {
	return []step{
		{"hwmon", collectHwmon},
		{"net", collectNetErrors},
		{"smart", collectSMART},
		{"raid", collectRAID},
		{"inventory", collectInventory},
	}
}

// collectHwmon 从 hwmon 读取温度、风扇、电压、功耗。
// 命名约定：temp*_input（毫摄氏度）、fan*_input（RPM）、in*_input（毫伏）、power*_input（微瓦）。
func collectHwmon(ctx context.Context, rep *model.Report) error {
	base := "/sys/class/hwmon"
	entries, err := os.ReadDir(base)
	if err != nil {
		return err
	}

	deviceName := func(dir string) string {
		raw, err := os.ReadFile(filepath.Join(dir, "name"))
		if err != nil {
			return filepath.Base(dir)
		}
		return strings.TrimSpace(string(raw))
	}

	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		dir := filepath.Join(base, e.Name())
		chip := deviceName(dir)
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, f := range files {
			name := f.Name()
			val, ok := readMilli(dir, name)
			if !ok {
				continue
			}
			label := labelOf(dir, name)

			switch {
			case strings.HasPrefix(name, "temp") && strings.HasSuffix(name, "_input"):
				add(rep, "cpu_temp_celsius", map[string]string{"chip": chip, "sensor": label}, val/1000)
			case strings.HasPrefix(name, "fan") && strings.HasSuffix(name, "_input"):
				add(rep, "fan_rpm", map[string]string{"slot": label}, val)
			case strings.HasPrefix(name, "in") && strings.HasSuffix(name, "_input"):
				add(rep, "voltage_volts", map[string]string{"rail": label}, val/1000)
			case strings.HasPrefix(name, "power") && strings.HasSuffix(name, "_input"):
				add(rep, "power_watts", map[string]string{"slot": label}, val/1_000_000)
			}
		}
	}
	return nil
}

// collectNetErrors 只统计网卡硬件错误（rx_errors/tx_errors），不做流量分析（docs/07 范围边界）。
func collectNetErrors(ctx context.Context, rep *model.Report) error {
	base := "/sys/class/net"
	entries, err := os.ReadDir(base)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if e.Name() == "lo" {
			continue
		}
		for _, kind := range []string{"rx_errors", "tx_errors"} {
			raw, err := os.ReadFile(filepath.Join(base, e.Name(), "statistics", kind))
			if err != nil {
				continue
			}
			v, err := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
			if err != nil {
				continue
			}
			add(rep, "nic_error_total", map[string]string{"device": e.Name(), "type": kind}, v)
		}
	}
	return nil
}

// collectSMART 调用随包内置的 smartctl。
// 实现在 W5 完善（需内置静态二进制并按盘型号解析字段），此处保留调用骨架与失败隔离。
func collectSMART(ctx context.Context, rep *model.Report) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	// TODO(W5): 执行内置 smartctl --scan-open -j，解析 JSON 后填充 rep.Disks
	_ = ctx
	return nil
}

// collectRAID 解析 storcli / megacli 输出。
func collectRAID(ctx context.Context, rep *model.Report) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	// TODO(W5): storcli /cALL show all J 或 megacli -LDInfo -Lall -aALL
	_ = ctx
	return nil
}

// collectInventory 采集静态硬件清单（DMI + lshw），计算指纹后按需上报。
func collectInventory(ctx context.Context, rep *model.Report) error {
	// TODO(W2): dmidecode + lshw 解析，生成 fingerprint；指纹不变时返回 nil 不带 payload
	_ = ctx
	return nil
}

// readMilli 读取形如 temp1_input 的整数值。
func readMilli(dir, name string) (float64, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// labelOf 读取配套的 *_label 文件；缺失时退回传感器序号（如 temp1）。
func labelOf(dir, inputName string) string {
	prefix := strings.TrimSuffix(inputName, "_input")
	if raw, err := os.ReadFile(filepath.Join(dir, prefix+"_label")); err == nil {
		if s := strings.TrimSpace(string(raw)); s != "" {
			return s
		}
	}
	return prefix
}

// hostIdentity 采集机器标识用于注册。
func hostIdentity(ctx context.Context) model.HostIdentity {
	id := model.HostIdentity{OSType: "linux", AgentVersion: versionOrDefault(), Collect: []string{"sensors", "smart", "raid", "net_err"}}

	if raw, err := os.ReadFile("/etc/hostname"); err == nil {
		id.Hostname = strings.TrimSpace(string(raw))
	}
	if raw, err := os.ReadFile("/sys/class/dmi/id/product_uuid"); err == nil {
		id.SMBIOSUUID = strings.TrimSpace(string(raw))
	}
	if raw, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "PRETTY_NAME=") {
				id.OSVersion = strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"`)
			}
		}
	}
	id.PrimaryIP = firstNonLoopbackIP()
	_ = ctx
	return id
}
