package ipmi

import (
	"context"
	"strings"
	"testing"

	mwpb "gitee.com/zhangyilin_233/metalwatch/proto/gen"
)

// fakeExecutor 是内存假执行器：记录调用参数并返回预设输出，便于断言指令翻译。
type fakeExecutor struct {
	calls    int
	lastArgs []string
	lastHost string
	lastUser string
	lastPass string
	output   string
	err      error
}

func (f *fakeExecutor) Run(ctx context.Context, host, user, pass string, args ...string) ([]byte, error) {
	f.calls++
	f.lastHost, f.lastUser, f.lastPass = host, user, pass
	f.lastArgs = append([]string{}, args...)
	return []byte(f.output), f.err
}

func contains(args []string, sub ...string) bool {
	for _, s := range sub {
		found := false
		for _, a := range args {
			if a == s {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func TestParseSDR(t *testing.T) {
	text := "Temp1          | 42 degrees C | ok\n" +
		"Fan1            | 2400 RPM    | ok\n" +
		"PS1 Status      | 0x00        | ok\n"
	rs := parseSDR(text)
	if len(rs) != 3 {
		t.Fatalf("应解析 3 条, 实际 %d", len(rs))
	}
	if rs[0].Name != "Temp1" || rs[0].Value != 42 || !strings.Contains(rs[0].Unit, "degrees C") {
		t.Errorf("Temp1 解析错误: %+v", rs[0])
	}
	if rs[1].Name != "Fan1" || rs[1].Value != 2400 {
		t.Errorf("Fan1 解析错误: %+v", rs[1])
	}
}

func TestClientReadSensorsAndFanRPM(t *testing.T) {
	f := &fakeExecutor{output: "Fan1 | 2400 RPM | ok\nFan2 | 3000 RPM | ok\nTemp | 50 degrees C | ok\n"}
	c := NewClient(f, "10.0.0.1", "admin", "secret")
	rpms, err := c.FanRPM(context.Background())
	if err != nil {
		t.Fatalf("FanRPM 出错: %v", err)
	}
	if rpms["Fan1"] != 2400 || rpms["Fan2"] != 3000 {
		t.Errorf("风扇转速解析错误: %v", rpms)
	}
	if _, ok := rpms["Temp"]; ok {
		t.Errorf("Temp 不应被当作风扇")
	}
}

func TestClientPowerState(t *testing.T) {
	f := &fakeExecutor{output: "Chassis Power is on\n"}
	c := NewClient(f, "h", "u", "p")
	if s, _ := c.PowerState(context.Background()); s != "on" {
		t.Errorf("电源状态应为 on, 实际 %q", s)
	}
}

func TestApplyFanSpeedManual(t *testing.T) {
	f := &fakeExecutor{output: ""}
	c := NewClient(f, "h", "u", "p")
	cmd := &mwpb.BmcCommand{CmdType: "fan_speed", Target: "h", SpeedPercent: 100}
	res, err := c.ApplyCommand(context.Background(), cmd)
	if err != nil {
		t.Fatalf("ApplyCommand 出错: %v", err)
	}
	if !res.Ok || res.ObservedValue != 100 {
		t.Errorf("结果异常: %+v", res)
	}
	if f.calls != 2 {
		t.Errorf("手动调速应先切手动再设转速, 应调用 2 次, 实际 %d", f.calls)
	}
	if !contains(f.lastArgs, "0x30", "0x30", "0x02", "0xff", "0x64") {
		t.Errorf("设转速指令参数错误: %v", f.lastArgs)
	}
}

func TestApplyFanSpeedAuto(t *testing.T) {
	f := &fakeExecutor{output: ""}
	c := NewClient(f, "h", "u", "p")
	cmd := &mwpb.BmcCommand{CmdType: "fan_speed", Target: "h", AutoMode: true}
	res, err := c.ApplyCommand(context.Background(), cmd)
	if err != nil {
		t.Fatalf("ApplyCommand 出错: %v", err)
	}
	if !res.Ok || res.ObservedValue != 0 {
		t.Errorf("auto 结果异常: %+v", res)
	}
	if f.calls != 1 || !contains(f.lastArgs, "0x30", "0x30", "0x01", "0x00") {
		t.Errorf("交还自动应单次调用且参数含 0x01 0x00: calls=%d args=%v", f.calls, f.lastArgs)
	}
}

func TestApplyPowerActions(t *testing.T) {
	for _, act := range []string{"on", "off", "cycle", "reset"} {
		f := &fakeExecutor{output: "ok"}
		c := NewClient(f, "h", "u", "p")
		cmd := &mwpb.BmcCommand{CmdType: "power", PowerAction: act}
		if _, err := c.ApplyCommand(context.Background(), cmd); err != nil {
			t.Fatalf("power %s 出错: %v", act, err)
		}
		if !contains(f.lastArgs, "chassis", "power", act) {
			t.Errorf("power %s 参数错误: %v", act, f.lastArgs)
		}
	}
}

func TestApplyIdentify(t *testing.T) {
	f := &fakeExecutor{output: ""}
	c := NewClient(f, "h", "u", "p")
	cmd := &mwpb.BmcCommand{CmdType: "identify", DurationSec: 30}
	if _, err := c.ApplyCommand(context.Background(), cmd); err != nil {
		t.Fatalf("identify 出错: %v", err)
	}
	if !contains(f.lastArgs, "chassis", "identify", "30") {
		t.Errorf("identify 参数错误: %v", f.lastArgs)
	}
}

func TestApplyCommandFailure(t *testing.T) {
	f := &fakeExecutor{err: errStub{}}
	c := NewClient(f, "h", "u", "p")
	cmd := &mwpb.BmcCommand{CmdType: "fan_speed", SpeedPercent: 50}
	res, err := c.ApplyCommand(context.Background(), cmd)
	if err == nil || res.Ok {
		t.Errorf("失败应返回 err 且 Ok=false")
	}
}

// TestCredentialsPassedNotLogged 验证凭证确实传入执行器（以便 BMC 鉴权），
// 同时确认本封装不在指令翻译层打印凭证——真实落盘由调用方日志策略保证。
func TestCredentialsPassedNotLogged(t *testing.T) {
	f := &fakeExecutor{output: ""}
	c := NewClient(f, "10.0.0.5", "root", "S3cret")
	_, _ = c.ApplyCommand(context.Background(), &mwpb.BmcCommand{CmdType: "power", PowerAction: "status"})
	if f.lastUser != "root" || f.lastPass != "S3cret" {
		t.Errorf("凭证应传入执行器用于鉴权")
	}
}

type errStub struct{}

func (errStub) Error() string { return "stub error" }
