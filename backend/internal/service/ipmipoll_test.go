package service_test

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	_ "github.com/LarryMKott/metalwatch/internal/adapter/sqlite"
	"github.com/LarryMKott/metalwatch/internal/engine"
	"github.com/LarryMKott/metalwatch/internal/pipeline"
	"github.com/LarryMKott/metalwatch/internal/service"
	"github.com/LarryMKott/metalwatch/internal/task"
	"github.com/LarryMKott/metalwatch/migrations"
	"github.com/LarryMKott/metalwatch/pkg/config"
	"github.com/LarryMKott/metalwatch/pkg/crypto"
	"github.com/LarryMKott/metalwatch/pkg/ipmi"
)

// 假执行器返回固定 SDR 输出（parseSDR 可解析格式：名称 | 数值 | 单位 | 状态）。
type fakeExecutor struct {
	fail bool
	sdr  string
}

func (f *fakeExecutor) Run(_ context.Context, _, _, _ string, args ...string) ([]byte, error) {
	if f.fail {
		return nil, context.DeadlineExceeded
	}
	for _, a := range args {
		if a == "sdr" {
			return []byte(f.sdr), nil
		}
	}
	if len(args) >= 2 && args[0] == "chassis" {
		return []byte("Chassis Power is on"), nil
	}
	return []byte(""), nil
}

func newIPMIEnv(t *testing.T, exec ipmi.Executor) (*service.IPMIPoller, *adapter.Store, adapter.TimeSeriesStore, *pipeline.Pipeline, []byte) {
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
	ts, err := adapter.OpenTimeSeries(adapter.TimeSeriesConfig{
		Driver: adapter.TSDriverEmbedded, RootDir: cfg.Server.DataDir, RawDays: 7, AggDays: 180,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ts.Close() })
	pipe := pipeline.New(ts, nil, pipeline.Options{FlushInterval: 20 * time.Millisecond})
	t.Cleanup(pipe.Stop)

	pool := task.NewPool(task.Options{Concurrency: 2, Timeout: 5 * time.Second})
	sched := engine.NewScheduleEngine(engine.ScheduleConfig{
		SensorInterval: time.Hour, AssetInterval: time.Hour, Pool: pool,
	})
	alerts := service.NewAlertService(s, s.Thresholds(), s.Alerts(), service.AlertDeps{}, nil)
	masterKey, err := crypto.GenerateMasterKey(filepath.Join(t.TempDir(), "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	factory := func() ipmi.Executor { return exec }
	poller := service.NewIPMIPoller(s, pool, sched, pipe, alerts, masterKey, time.Hour, factory, nil)
	return poller, s, ts, pipe, masterKey
}

func ipmiHost(t *testing.T, s *adapter.Store, key []byte, password string) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := s.Hosts().Create(ctx, &adapter.Host{
		Hostname: "bmc-node", PrimaryIP: "10.1.1.5", OSType: "linux", Status: "unknown",
	})
	if err != nil {
		t.Fatal(err)
	}
	nonce, cipher, err := crypto.Seal(key, []byte(password), crypto.AADForHost(id))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BMC().Upsert(ctx, &adapter.BMCCredential{
		HostID: id, Username: "admin", Protocol: "ipmi20",
		SecretCipher: cipher, SecretNonce: nonce, UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Hosts().SetBMC(ctx, id, "10.1.1.99", true, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestBMCCredentialEncryptedAtRest(t *testing.T) {
	s, err := adapter.Open(context.Background(), func() config.Config {
		cfg := config.Default()
		cfg.Server.DataDir = t.TempDir()
		cfg.DB.File = filepath.Join(cfg.Server.DataDir, "metalwatch.db")
		return cfg
	}())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	fsys, _ := migrations.DialectFS(string(s.Dialect()))
	if _, err := s.Migrate(context.Background(), fsys, string(s.Dialect())); err != nil {
		t.Fatal(err)
	}

	key, err := crypto.GenerateMasterKey(filepath.Join(t.TempDir(), "mk.key"))
	if err != nil {
		t.Fatal(err)
	}
	hostID := ipmiHost(t, s, key, "s3cret-P@ss")

	// M1 验收：密文字段搜不到明文
	row := s.DB.QueryRow(`SELECT secret_cipher, secret_nonce FROM bmc_credential WHERE host_id = ?`, hostID)
	var cipher, nonce []byte
	if err := row.Scan(&cipher, &nonce); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(cipher, []byte("s3cret-P@ss")) {
		t.Fatal("明文出现在密文字段")
	}

	// AAD 绑定 host_id：换主机解密必须失败
	cred, err := s.BMC().Get(context.Background(), hostID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := crypto.Open(key, cred.SecretNonce, cred.SecretCipher, crypto.AADForHost(hostID+999)); err == nil {
		t.Fatal("跨主机 AAD 解密应失败")
	}
	plain, err := crypto.Open(key, cred.SecretNonce, cred.SecretCipher, crypto.AADForHost(hostID))
	if err != nil || string(plain) != "s3cret-P@ss" {
		t.Fatalf("本主机解密应还原明文: %s err=%v", plain, err)
	}
}

func TestIPMIPollCollectsSensorsAndRecordsRun(t *testing.T) {
	exec := &fakeExecutor{sdr: "CPU Temp | 45 degrees C | ok\nFan1 | 1200 RPM | ok\nPSU Watt | 300 W | ok"}
	poller, s, ts, pipe, key := newIPMIEnv(t, exec)
	ipmiHost(t, s, key, "pw-secret")

	ctx := context.Background()
	if err := poller.PollNow(ctx, 1); err != nil {
		t.Fatalf("轮询失败: %v", err)
	}
	pipe.Stop()

	// 传感器按单位映射进时序
	for metric, want := range map[string]float64{
		"cpu_temp_celsius": 45, "fan_rpm": 1200, "power_watts": 300,
	} {
		series, err := ts.Query(ctx, adapter.Query{Metric: metric, Start: time.Now().Add(-time.Hour), End: time.Now().Add(time.Hour)})
		if err != nil || len(series) != 1 || len(series[0].Points) != 1 {
			t.Fatalf("%s 未落库: %+v err=%v", metric, series, err)
		}
		if series[0].Points[0].Value != want {
			t.Fatalf("%s 值 = %v, want %v", metric, series[0].Points[0].Value, want)
		}
	}

	// collect_run 有 ok 记录
	runs, total, err := s.CollectRuns().List(ctx, adapter.CollectRunFilter{Mode: "ipmi"})
	if err != nil || total != 1 || runs[0].State != "ok" {
		t.Fatalf("collect_run 应 1 条 ok: %+v err=%v", runs, err)
	}

	// 失败路径：执行器超时 → collect_run=timeout + 返回错误（触发池退避）
	exec2 := &fakeExecutor{fail: true}
	poller2, s2, _, _, key2 := newIPMIEnv(t, exec2)
	ipmiHost(t, s2, key2, "pw")
	if err := poller2.PollNow(ctx, 1); err == nil {
		t.Fatal("执行器失败应返回错误")
	}
	runs2, total2, _ := s2.CollectRuns().List(ctx, adapter.CollectRunFilter{State: "timeout"})
	if total2 != 1 || runs2[0].State != "timeout" {
		t.Fatalf("超时应记 timeout: %+v", runs2)
	}
}

func TestSyncTargetsFollowsCredentialChanges(t *testing.T) {
	exec := &fakeExecutor{sdr: "CPU Temp | 45 degrees C | ok"}
	poller, s, _, _, key := newIPMIEnv(t, exec)
	ctx := context.Background()

	if err := poller.SyncTargets(ctx); err != nil {
		t.Fatal(err)
	}
	if n := poller.TargetCount(); n != 0 {
		t.Fatalf("初始目标数 = %d, want 0", n)
	}

	ipmiHost(t, s, key, "pw")
	if err := poller.SyncTargets(ctx); err != nil {
		t.Fatal(err)
	}
	if n := poller.TargetCount(); n != 1 {
		t.Fatalf("录入凭据后目标数 = %d, want 1", n)
	}

	// 移除凭据 → 目标撤下
	if _, err := s.BMC().Delete(ctx, 1); err != nil {
		t.Fatal(err)
	}
	_ = s.Hosts().SetBMC(ctx, 1, "", false, time.Now().UTC())
	if err := poller.SyncTargets(ctx); err != nil {
		t.Fatal(err)
	}
	if n := poller.TargetCount(); n != 0 {
		t.Fatalf("移除后目标数 = %d, want 0", n)
	}
}
