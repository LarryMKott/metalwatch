//go:build windows

package collect

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"

	"gitee.com/zhangyilin_233/metalwatch/agent/internal/model"
)

// platformSteps 注册 Windows 采集项。
//
// 说明（docs/01 R3）：优先走 WMI/CIM；Server Core 或 WMI 异常时回退 PowerShell，
// 因此这里直接调用 powershell.exe 的 CIM 查询，兼容性最广且不引入 cgo 依赖。
func platformSteps() []step {
	return []step{
		{"thermal", collectThermal},
		{"disks", collectDisks},
		{"inventory", collectInventory},
	}
}

// collectThermal 通过 MSAcpi_ThermalZoneTemperature 读取温度（单位 0.1 开尔文）。
// 部分服务器需厂商工具（OMSA/SSA）才有完整传感器，缺失时仅上报可获取项。
func collectThermal(ctx context.Context, rep *model.Report) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	out, err := runPS(ctx, `Get-CimInstance -Namespace root/WMI -ClassName MSAcpi_ThermalZoneTemperature |
		Select-Object InstanceName,CurrentTemperature | ConvertTo-Json -Compress`)
	if err != nil {
		return err
	}
	var zones []struct {
		InstanceName       string `json:"InstanceName"`
		CurrentTemperature int    `json:"CurrentTemperature"`
	}
	if err := json.Unmarshal(out, &zones); err != nil {
		var single struct {
			InstanceName       string `json:"InstanceName"`
			CurrentTemperature int    `json:"CurrentTemperature"`
		}
		if err2 := json.Unmarshal(out, &single); err2 != nil {
			return err
		}
		zones = append(zones, single)
	}

	for _, z := range zones {
		if z.CurrentTemperature <= 0 {
			continue
		}
		// 0.1 开尔文 → 摄氏度
		celsius := float64(z.CurrentTemperature)/10 - 273.15
		add(rep, "cpu_temp_celsius", map[string]string{"chip": z.InstanceName}, round1(celsius))
	}
	return nil
}

// collectDisks 通过 Win32_DiskDrive 采集磁盘基础信息；SMART 细节由厂商工具补齐（W6）。
func collectDisks(ctx context.Context, rep *model.Report) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	out, err := runPS(ctx, `Get-CimInstance Win32_DiskDrive |
		Select-Object DeviceID,Model,SerialNumber,Size,MediaType | ConvertTo-Json -Compress`)
	if err != nil {
		return err
	}
	var disks []struct {
		DeviceID     string `json:"DeviceID"`
		Model        string `json:"Model"`
		SerialNumber string `json:"SerialNumber"`
		Size         uint64 `json:"Size"`
		MediaType    string `json:"MediaType"`
	}
	if err := json.Unmarshal(out, &disks); err != nil {
		var single struct {
			DeviceID     string `json:"DeviceID"`
			Model        string `json:"Model"`
			SerialNumber string `json:"SerialNumber"`
			Size         uint64 `json:"Size"`
			MediaType    string `json:"MediaType"`
		}
		if err2 := json.Unmarshal(out, &single); err2 != nil {
			return err
		}
		disks = append(disks, single)
	}

	for _, d := range disks {
		media := "HDD"
		switch {
		case strings.Contains(strings.ToUpper(d.Model), "SSD"), strings.Contains(strings.ToUpper(d.MediaType), "SSD"):
			media = "SSD"
		case strings.Contains(strings.ToUpper(d.Model), "NVME"):
			media = "NVMe"
		}
		rep.Disks = append(rep.Disks, model.DiskInfo{
			Device: d.DeviceID, Model: strings.TrimSpace(d.Model),
			Serial: strings.TrimSpace(d.SerialNumber), MediaType: media,
			CapacityBytes: d.Size,
		})
	}
	return nil
}

// collectInventory 采集静态清单（主板/BIOS/CPU/内存条）。
func collectInventory(ctx context.Context, rep *model.Report) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// TODO(W6): Win32_BaseBoard / Win32_BIOS / Win32_Processor / Win32_PhysicalMemory
	_ = ctx
	return nil
}

func hostIdentity(ctx context.Context) model.HostIdentity {
	id := model.HostIdentity{
		OSType: "windows", AgentVersion: versionOrDefault(),
		Collect: []string{"thermal", "smart", "raid"},
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if out, err := runPS(ctx, `(Get-CimInstance Win32_ComputerSystemProduct).UUID`); err == nil {
		id.SMBIOSUUID = mustUUID(string(out))
	}
	if out, err := runPS(ctx, `$env:COMPUTERNAME`); err == nil {
		id.Hostname = strings.TrimSpace(string(out))
	}
	if out, err := runPS(ctx, `(Get-CimInstance Win32_OperatingSystem).Caption`); err == nil {
		id.OSVersion = strings.TrimSpace(string(out))
	}
	id.PrimaryIP = firstNonLoopbackIP()
	return id
}

// runPS 执行 PowerShell 并把 JSON 输出转成字节流。
func runPS(ctx context.Context, script string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "powershell.exe",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return []byte(strings.TrimSpace(string(out))), nil
}

func round1(v float64) float64 {
	return float64(int64(v*10+0.5)) / 10
}
