package service

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	_ "github.com/LarryMKott/metalwatch/internal/adapter/sqlite"
	"github.com/LarryMKott/metalwatch/migrations"
	"github.com/LarryMKott/metalwatch/pkg/config"
	"github.com/LarryMKott/metalwatch/pkg/crypto"
	"github.com/LarryMKott/metalwatch/pkg/ipmi"
)

// bmcCtlExecutor 记录收到的 ipmitool 参数，按需返回预置输出或错误。
type bmcCtlExecutor struct {
	failCmd string // 命中该子命令时返回错误（如 "mc"）
	out     string // 常规输出
	calls   [][]string
}

func (f *bmcCtlExecutor) Run(_ context.Context, _, _, _ string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	for _, a := range args {
		if a == f.failCmd {
			return nil, errors.New("bmc unreachable")
		}
	}
	if len(args) >= 1 && args[0] == "mc" {
		return []byte("Device ID                 : 32\nProduct Name              : TestBMC-X11\nFirmware Revision         : 2.71"), nil
	}
	return []byte(f.out), nil
}

func newBMCControlEnv(t *testing.T, exec ipmi.Executor) (*BMCControlService, *adapter.Store, int64, []byte) {
	t.Helper()
	cfg := config.Default()
	cfg.Server.DataDir = t.TempDir()
	cfg.DB.File = filepath.Join(cfg.Server.DataDir, "metalwatch.db")

	ctx := context.Background()
	s, err := adapter.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	fsys, err := migrations.DialectFS(string(s.Dialect()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Migrate(ctx, fsys, string(s.Dialect())); err != nil {
		t.Fatal(err)
	}

	masterKey := []byte("0123456789abcdef0123456789abcdef")
	host := &adapter.Host{Hostname: "ctl-node", PrimaryIP: "10.77.0.9"}
	id, err := s.Hosts().Create(ctx, host)
	if err != nil {
		t.Fatal(err)
	}
	nonce, cipher, err := crypto.Seal(masterKey, []byte("bmc-pass"), crypto.AADForHost(id))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BMC().Upsert(ctx, &adapter.BMCCredential{
		HostID: id, Username: "admin", Protocol: "ipmi20",
		SecretCipher: cipher, SecretNonce: nonce, UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	_ = s.Hosts().SetBMC(ctx, id, "10.77.0.9", false, time.Now())

	svc := NewBMCControlService(s, masterKey, func() ipmi.Executor { return exec }, nil)
	return svc, s, id, masterKey
}

func TestBMCControlValidate(t *testing.T) {
	cases := []BMCCommandInput{
		{CmdType: "fan"}, // 无 speed 无 auto
		{CmdType: "fan", SpeedPercent: ptrOf(int32(101))},      // 越界
		{CmdType: "power", PowerAction: "boom"},                // 非法动作
		{CmdType: "identify", DurationSec: ptrOf(int32(4000))}, // 越界
		{CmdType: "policy", Target: "magic"},                   // v1 仅 thermal
		{CmdType: "reboot-everything"},                         // 未知类型
	}
	for i, in := range cases {
		if err := in.Validate(); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("case %d: 期望 ErrInvalidInput, got %v", i, err)
		}
	}
	ok := []BMCCommandInput{
		{CmdType: "fan", SpeedPercent: ptrOf(int32(60))},
		{CmdType: "fan", AutoMode: ptrOf(true)},
		{CmdType: "power", PowerAction: "cycle"},
		{CmdType: "identify", DurationSec: ptrOf(int32(15))},
		{CmdType: "identify"}, // 缺省 30s
		{CmdType: "policy", Target: "thermal"},
	}
	for i, in := range ok {
		if err := in.Validate(); err != nil {
			t.Fatalf("case %d: 不该报错, got %v", i, err)
		}
	}
}

func TestBMCControlExecuteFanAndAudit(t *testing.T) {
	exec := &bmcCtlExecutor{out: "ok"}
	svc, store, hostID, _ := newBMCControlEnv(t, exec)

	res, err := svc.Execute(context.Background(), hostID, "admin",
		BMCCommandInput{CmdType: "fan", SpeedPercent: ptrOf(int32(66))})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || !strings.HasPrefix(res.RequestID, "bmc-") {
		t.Fatalf("res = %+v", res)
	}
	// 手动调速 = 先切手动模式（raw 0x30 0x30 0x01 0x01），再下转速
	if len(exec.calls) != 2 || exec.calls[1][len(exec.calls[1])-1] != "0x42" {
		t.Fatalf("ipmitool 调用不符: %v", exec.calls)
	}

	rows, err := store.BMCCommands().List(context.Background(), 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("audit rows = %v, err = %v", rows, err)
	}
	row := rows[0]
	if row.Status != "success" || row.Operator != "admin" || row.CmdType != "fan" ||
		row.Hostname != "ctl-node" || !strings.Contains(row.Params, `"speed_percent":66`) {
		t.Fatalf("audit row = %+v params=%s", row.BMCCommand, row.Params)
	}
}

func TestBMCControlExecuteAutoHandoffAndPolicy(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   BMCCommandInput
	}{{"fan auto", BMCCommandInput{CmdType: "fan", AutoMode: ptrOf(true)}},
		{"policy", BMCCommandInput{CmdType: "policy", Target: "thermal"}}} {
		exec := &bmcCtlExecutor{out: "ok"}
		svc, _, hostID, _ := newBMCControlEnv(t, exec)
		res, err := svc.Execute(context.Background(), hostID, "admin", tc.in)
		if err != nil || !res.OK {
			t.Fatalf("%s: res=%+v err=%v", tc.name, res, err)
		}
		// 交还自动 = 单条 raw 0x30 0x30 0x01 0x00，不带转速
		if len(exec.calls) != 1 {
			t.Fatalf("%s: calls = %v", tc.name, exec.calls)
		}
	}
}

func TestBMCControlExecuteFailureRecorded(t *testing.T) {
	exec := &bmcCtlExecutor{failCmd: "chassis"}
	svc, store, hostID, _ := newBMCControlEnv(t, exec)

	res, err := svc.Execute(context.Background(), hostID, "admin",
		BMCCommandInput{CmdType: "power", PowerAction: "off"})
	if err != nil {
		t.Fatalf("执行失败应是业务结果而非 API 错误: %v", err)
	}
	if res.OK {
		t.Fatal("res.OK 应为 false")
	}
	rows, _ := store.BMCCommands().List(context.Background(), 10)
	if len(rows) != 1 || rows[0].Status != "failed" {
		t.Fatalf("failed 审计缺失: %+v", rows)
	}
}

func TestBMCControlHostAndBMCNotExist(t *testing.T) {
	exec := &bmcCtlExecutor{}
	svc, _, hostID, _ := newBMCControlEnv(t, exec)
	in := BMCCommandInput{CmdType: "power", PowerAction: "on"}

	if _, err := svc.Execute(context.Background(), hostID+999, "admin", in); !errors.Is(err, ErrNotFoundInService) {
		t.Fatalf("主机不存在: %v", err)
	}
	// 有主机但没配 BMC
	ctx := context.Background()
	other := &adapter.Host{Hostname: "no-bmc", PrimaryIP: "10.77.0.10"}
	oid, err := svc.store.Hosts().Create(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Execute(ctx, oid, "admin", in); !errors.Is(err, ErrNotFoundInService) {
		t.Fatalf("未配置 BMC: %v", err)
	}
	if _, err := svc.Capability(ctx, oid); !errors.Is(err, ErrNotFoundInService) {
		t.Fatalf("capability 未配置 BMC: %v", err)
	}
}

func TestBMCControlCapability(t *testing.T) {
	exec := &bmcCtlExecutor{}
	svc, _, hostID, _ := newBMCControlEnv(t, exec)

	cap, err := svc.Capability(context.Background(), hostID)
	if err != nil {
		t.Fatal(err)
	}
	if !cap.FanControl || !cap.PowerControl || !cap.Identify ||
		cap.BMCModel != "TestBMC-X11" || cap.FirmwareVersion != "2.71" {
		t.Fatalf("cap = %+v", cap)
	}

	// 探测失败：能力全 false + Detail 说明，不作为 API 错误
	svc2, _, hostID2, _ := newBMCControlEnv(t, &bmcCtlExecutor{failCmd: "mc"})
	cap2, err := svc2.Capability(context.Background(), hostID2)
	if err != nil {
		t.Fatal(err)
	}
	if cap2.FanControl || cap2.PowerControl || cap2.Identify || cap2.Detail == "" {
		t.Fatalf("cap2 = %+v", cap2)
	}
}

func ptrOf[T any](v T) *T { return &v }
