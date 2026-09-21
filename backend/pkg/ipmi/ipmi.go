// Package ipmi 是 IPMI 协议封装（带外采集与控制）。
//
// 本机环境没有 ipmitool，也没有真机 BMC，因此这里把“执行一次 ipmitool 调用”抽象成
// Executor 接口：生产用 CommandExecutor（调用外部二进制），单测用 fakeExecutor（内存假数据）。
// 真机验证见 docs/04-部署。
//
// 安全约定：凭证（user/pass）不得写入日志——本实现任何日志路径都不打印 user/pass。
package ipmi

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	mwpb "gitee.com/zhangyilin_233/metalwatch/proto/gen"
)

// Executor 执行一次 ipmitool 调用。注入它以解耦逻辑与真机，便于测试。
type Executor interface {
	Run(ctx context.Context, host, user, pass string, args ...string) ([]byte, error)
}

// CommandExecutor 通过外部 ipmitool 二进制执行真实带外指令。
type CommandExecutor struct {
	// Bin 为 ipmitool 路径，默认 "ipmitool"。
	Bin string
	// Interface 为 -I 参数，默认 "lanplus"。
	Interface string
}

// Run 调起 ipmitool。凭证仅作为进程参数传入，绝不写入日志。
func (c CommandExecutor) Run(ctx context.Context, host, user, pass string, args ...string) ([]byte, error) {
	bin := c.Bin
	if bin == "" {
		bin = "ipmitool"
	}
	iface := c.Interface
	if iface == "" {
		iface = "lanplus"
	}
	full := append([]string{"-H", host, "-U", user, "-P", pass, "-I", iface}, args...)
	cmd := exec.CommandContext(ctx, bin, full...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return out, fmt.Errorf("ipmitool 执行失败: %s", string(ee.Stderr))
		}
		return out, fmt.Errorf("ipmitool 执行失败: %w", err)
	}
	return out, nil
}

// SensorReading 是一条 SDR 传感器读数（已解析）。
type SensorReading struct {
	Name   string
	Value  float64
	Unit   string
	Status string
	Raw    string
}

// Client 是面向单台 BMC 的带外客户端。
type Client struct {
	exec Executor
	Host string
	User string
	Pass string
}

// NewClient 构造客户端。exec 为空时回退到 CommandExecutor（真正机）。
func NewClient(exec Executor, host, user, pass string) *Client {
	if exec == nil {
		exec = CommandExecutor{}
	}
	return &Client{exec: exec, Host: host, User: user, Pass: pass}
}

// ReadSensors 读取全部传感器（SDR）。
func (c *Client) ReadSensors(ctx context.Context) ([]SensorReading, error) {
	out, err := c.exec.Run(ctx, c.Host, c.User, c.Pass, "sdr")
	if err != nil {
		return nil, err
	}
	return parseSDR(string(out)), nil
}

// FanRPM 返回名称含 "fan"（不区分大小写）且单位为 RPM 的传感器读数。
func (c *Client) FanRPM(ctx context.Context) (map[string]float64, error) {
	readings, err := c.ReadSensors(ctx)
	if err != nil {
		return nil, err
	}
	rpms := map[string]float64{}
	for _, r := range readings {
		if strings.Contains(strings.ToLower(r.Name), "fan") && strings.EqualFold(r.Unit, "RPM") {
			rpms[r.Name] = r.Value
		}
	}
	return rpms, nil
}

// PowerState 返回电源状态："on" / "off" / "unknown"。
func (c *Client) PowerState(ctx context.Context) (string, error) {
	out, err := c.exec.Run(ctx, c.Host, c.User, c.Pass, "chassis", "power", "status")
	if err != nil {
		return "unknown", err
	}
	lower := strings.ToLower(string(out))
	switch {
	case strings.Contains(lower, "is on"):
		return "on", nil
	case strings.Contains(lower, "is off"):
		return "off", nil
	default:
		return "unknown", nil
	}
}

// ApplyCommand 把服务端下发的 BmcCommand 翻译为具体 ipmitool 调用并执行，
// 返回 BmcResult（含 ObservedValue，如转速百分比或电源 on=1）。
//
// 风扇调速采用 Dell iDRAC 风格的 raw 命令（0x30 0x30），各厂商不同——真机需按机型调整。
func (c *Client) ApplyCommand(ctx context.Context, cmd *mwpb.BmcCommand) (*mwpb.BmcResult, error) {
	res := &mwpb.BmcResult{CommandId: cmd.GetCommandId(), ExecutedAt: time.Now().UnixMilli()}
	if cmd == nil {
		res.Ok = false
		res.Message = "空指令"
		return res, fmt.Errorf("空指令")
	}

	var out []byte
	var err error

	switch cmd.GetCmdType() {
	case "fan_speed":
		if cmd.GetAutoMode() {
			// 交还 BMC 自动调速。
			out, err = c.exec.Run(ctx, c.Host, c.User, c.Pass, "raw", "0x30", "0x30", "0x01", "0x00")
		} else {
			pct := clampSpeed(cmd.GetSpeedPercent())
			// 手动调速：先切手动模式，再下发转速。
			if _, e := c.exec.Run(ctx, c.Host, c.User, c.Pass, "raw", "0x30", "0x30", "0x01", "0x01"); e != nil {
				res.Ok, res.Message = false, e.Error()
				return res, e
			}
			out, err = c.exec.Run(ctx, c.Host, c.User, c.Pass, "raw", "0x30", "0x30", "0x02", "0xff", fmt.Sprintf("0x%02x", pct))
		}
		res.ObservedValue = float64(clampSpeed(cmd.GetSpeedPercent()))

	case "power":
		act := cmd.GetPowerAction()
		if act == "" {
			act = "status"
		}
		out, err = c.exec.Run(ctx, c.Host, c.User, c.Pass, "chassis", "power", act)
		if act == "status" {
			if strings.Contains(strings.ToLower(string(out)), "on") {
				res.ObservedValue = 1
			}
		}

	case "identify":
		dur := strconv.Itoa(int(cmd.GetDurationSec()))
		out, err = c.exec.Run(ctx, c.Host, c.User, c.Pass, "chassis", "identify", dur)

	default:
		res.Ok = false
		res.Message = "未知指令类型: " + cmd.GetCmdType()
		return res, fmt.Errorf("%s", res.Message)
	}

	if err != nil {
		res.Ok = false
		res.Message = err.Error()
		return res, err
	}
	res.Ok = true
	res.Message = strings.TrimSpace(string(out))
	return res, nil
}

// parseSDR 解析 `ipmitool sdr` 的典型管道输出：
//
//	名称 | 数值 单位 | 状态
//
// 关键：`ipmitool sdr` 的**单位与数值同在第 2 段**（如 "42 degrees C"），
// 第 3 段是状态（ok / ns / nc）。早期实现误把第 3 段当作单位，
// 导致 Unit 变成 "ok"、风扇 RPM 永远匹配不到。这里按「末段为状态」处理。
//
// 离散型传感器无数值时 Value 记 0，Unit 取该段原始文本。
func parseSDR(text string) []SensorReading {
	var out []SensorReading
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		n := len(parts)
		r := SensorReading{Raw: line, Name: strings.TrimSpace(parts[0])}

		if n >= 2 {
			mid := strings.Fields(parts[1])
			if len(mid) > 0 {
				if f, err := strconv.ParseFloat(mid[0], 64); err == nil {
					r.Value = f
					r.Unit = strings.Join(mid[1:], " ")
				} else {
					// 离散型：整段作为文本（如 "0x00" 状态位）
					r.Unit = strings.TrimSpace(parts[1])
				}
			}
		}
		// 末段为状态；n==2 时无状态列。
		if n >= 3 {
			r.Status = strings.TrimSpace(parts[n-1])
		}
		out = append(out, r)
	}
	return out
}

func clampSpeed(pct int32) int32 {
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return pct
}
