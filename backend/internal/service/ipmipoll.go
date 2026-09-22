package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	"github.com/LarryMKott/metalwatch/internal/engine"
	"github.com/LarryMKott/metalwatch/internal/pipeline"
	"github.com/LarryMKott/metalwatch/internal/task"
	"github.com/LarryMKott/metalwatch/pkg/crypto"
	"github.com/LarryMKott/metalwatch/pkg/ipmi"
)

// IPMIExecutorFactory 构造 ipmi.Executor（生产用 CommandExecutor 跑真实 ipmitool；
// 测试注入假执行器）。返回 nil 时由 ipmi.NewClient 回退到 CommandExecutor。
type IPMIExecutorFactory func() ipmi.Executor

// IPMIPoller 把 IPMI 带外采集接入调度：带外主机 → ScheduleEngine 目标 → task.Pool
// （并发上限 + 同 BMC 串行 + 指数退避，docs/01 D10）→ 传感器样本 → 告警/时序。
// OS 宕机时带外链路照常工作，监控不中断（W4 核心价值）。
type IPMIPoller struct {
	store     adapter.MetadataStore
	pipe      *pipeline.Pipeline
	alerts    *AlertService
	pool      *task.Pool
	sched     *engine.ScheduleEngine
	interval  time.Duration
	masterKey []byte
	newExec   IPMIExecutorFactory
	log       *slog.Logger
}

// NewIPMIPoller 构造带外采集轮询器。masterKey 为 AES-256-GCM 主密钥（docs/01 D9）。
// pool 为任务池（连通性测试走池）；sched 为调度引擎（目标登记）。
func NewIPMIPoller(store adapter.MetadataStore, pool *task.Pool, sched *engine.ScheduleEngine,
	pipe *pipeline.Pipeline, alerts *AlertService, masterKey []byte, interval time.Duration,
	newExec IPMIExecutorFactory, log *slog.Logger) *IPMIPoller {
	if log == nil {
		log = slog.Default()
	}
	return &IPMIPoller{
		store: store, pool: pool, sched: sched, pipe: pipe, alerts: alerts,
		interval: interval, masterKey: masterKey, newExec: newExec, log: log,
	}
}

// SyncTargets 把调度目标与「启用带外采集且有 BMC 地址」的主机集合对齐：
// 新增的加目标，已移除的撤目标。凭据管理接口变更后调用即可生效。
func (p *IPMIPoller) SyncTargets(ctx context.Context) error {
	hosts, _, err := p.store.Hosts().List(ctx, adapter.ListFilter{IPMIOnly: true, Limit: 500})
	if err != nil {
		return fmt.Errorf("读取带外主机失败: %w", err)
	}

	want := map[int64]bool{}
	for _, h := range hosts {
		want[h.ID] = true
		if !p.hasTarget(h.ID) {
			id := h.ID
			hostname, bmcIP := h.Hostname, derefString(h.BMCIP)
			p.sched.AddTarget(engine.CollectionTask{
				ID:   targetID(h.ID),
				Kind: engine.KindSensor,
				Host: strconv.FormatInt(h.ID, 10),
				Run: func(ctx context.Context) error {
					return p.pollHost(ctx, id, hostname, bmcIP)
				},
			})
			p.log.Info("带外采集目标已登记", "host_id", h.ID, "hostname", hostname, "bmc", bmcIP)
		}
	}
	for _, id := range p.sched.TargetIDs() {
		if hostID, ok := parseTargetID(id); ok && !want[hostID] {
			p.sched.RemoveTarget(id)
			p.log.Info("带外采集目标已移除", "host_id", hostID)
		}
	}
	return nil
}

func (p *IPMIPoller) hasTarget(hostID int64) bool {
	want := targetID(hostID)
	for _, id := range p.sched.TargetIDs() {
		if id == want {
			return true
		}
	}
	return false
}

func targetID(hostID int64) string { return "ipmi-" + strconv.FormatInt(hostID, 10) }

func parseTargetID(id string) (int64, bool) {
	suffix, ok := strings.CutPrefix(id, "ipmi-")
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseInt(suffix, 10, 64)
	return v, err == nil
}

// pollHost 执行一台 BMC 的一次轮询：解密凭据 → 读 SDR → 样本入告警/时序 → 写 collect_run。
// 返回错误会触发 task.Pool 的退避语义（连续失败进指数退避，docs/01 D10）。
func (p *IPMIPoller) pollHost(ctx context.Context, hostID int64, hostname, bmcIP string) error {
	started := time.Now().UTC()
	run := &adapter.CollectRun{
		HostID: hostID, Mode: "ipmi", StartedAt: started,
	}
	fail := func(state, code string, err error) error {
		msg := truncateStr(err.Error(), 500)
		codeCopy, msgCopy := code, msg
		run.State, run.ErrorCode, run.ErrorMsg = state, &codeCopy, &msgCopy
		run.FinishedAt = time.Now().UTC()
		run.DurationMS = time.Since(started).Milliseconds()
		if _, cerr := p.store.CollectRuns().Create(ctx, run); cerr != nil {
			p.log.Warn("采集记录写入失败", "host_id", hostID, "err", cerr)
		}
		return err
	}

	cred, err := p.store.BMC().Get(ctx, hostID)
	if err != nil {
		return fail("fail", "credential_missing", fmt.Errorf("读取 BMC 凭据失败: %w", err))
	}
	pass, err := crypto.Open(p.masterKey, cred.SecretNonce, cred.SecretCipher, crypto.AADForHost(hostID))
	if err != nil {
		return fail("fail", "credential_decrypt", fmt.Errorf("解密 BMC 凭据失败: %w", err))
	}

	var exec ipmi.Executor
	if p.newExec != nil {
		exec = p.newExec()
	}
	client := ipmi.NewClient(exec, bmcIP, cred.Username, string(pass))
	readings, err := client.ReadSensors(ctx)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil || errors.Is(err, context.DeadlineExceeded) {
			return fail("timeout", "ipmi_timeout", err)
		}
		return fail("fail", "ipmi_error", err)
	}

	samples := make([]adapter.Sample, 0, len(readings))
	skipped := 0
	for _, r := range readings {
		s, ok := sensorToSample(r, hostID, hostname)
		if !ok {
			skipped++
			continue
		}
		samples = append(samples, s)
	}

	// 带外样本走同一告警/时序链路：OS 宕机时告警照常触发（W4 核心价值）
	if p.alerts != nil {
		p.alerts.Evaluate(ctx, hostID, hostname, samples, time.Now().UTC())
	}
	if p.pipe != nil {
		p.pipe.Ingest(samples)
	}

	state := "ok"
	if len(samples) == 0 {
		state = "degraded" // 连通但无可解析数值传感器
	}
	msg := fmt.Sprintf("%d sensors", len(samples))
	if skipped > 0 {
		msg += fmt.Sprintf(", %d skipped", skipped)
	}
	run.State = state
	run.ErrorMsg = &msg
	run.FinishedAt = time.Now().UTC()
	run.DurationMS = time.Since(started).Milliseconds()
	if _, cerr := p.store.CollectRuns().Create(ctx, run); cerr != nil {
		p.log.Warn("采集记录写入失败", "host_id", hostID, "err", cerr)
	}
	p.log.Debug("带外轮询完成", "host_id", hostID, "sensors", len(samples), "skipped", skipped)
	return nil
}

// sensorToSample 把 SDR 读数映射为白名单指标（按单位分类，docs/03 §3）：
//   - RPM → fan_rpm（slot=传感器名）
//   - W（瓦） → power_watts（slot）
//   - V（伏） → voltage_volts（rail）
//   - C/摄氏 → cpu_temp_celsius（chip）——SDR 温度统一挂该指标名，厂商名进标签
//
// 非数值/离散量（状态类文本）返回 false 跳过。
func sensorToSample(r ipmi.SensorReading, hostID int64, hostname string) (adapter.Sample, bool) {
	if r.Value == 0 && r.Unit == "" {
		return adapter.Sample{}, false
	}
	unit := strings.ToUpper(strings.TrimSpace(r.Unit))
	var metric, objKey string
	switch {
	case unit == "RPM":
		metric, objKey = "fan_rpm", "slot"
	case strings.HasPrefix(unit, "W"):
		metric, objKey = "power_watts", "slot"
	case strings.HasPrefix(unit, "V"):
		metric, objKey = "voltage_volts", "rail"
	case unit == "C" || strings.HasPrefix(unit, "°") || strings.Contains(unit, "DEG") ||
		strings.Contains(unit, "CELSIUS"):
		metric, objKey = "cpu_temp_celsius", "chip"
	default:
		return adapter.Sample{}, false
	}
	return adapter.Sample{
		Metric: metric,
		Labels: map[string]string{
			"host_id": strconv.FormatInt(hostID, 10), "host": hostname, "mode": "ipmi",
			objKey: r.Name,
		},
		Value: r.Value,
		TS:    time.Now().UTC(),
	}, true
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// TestConnection 对一台主机做最轻量的带外连通性检查（读电源状态）。
// 经任务池执行以尊重同 BMC 串行与退避约束；凭据解密失败/带外不可达返回错误。
func (p *IPMIPoller) TestConnection(ctx context.Context, hostID int64) (map[string]any, error) {
	host, err := p.store.Hosts().GetByID(ctx, hostID)
	if err != nil {
		return nil, err
	}
	cred, err := p.store.BMC().Get(ctx, hostID)
	if err != nil {
		return nil, fmt.Errorf("该主机尚未录入 BMC 凭据: %w", err)
	}
	pass, err := crypto.Open(p.masterKey, cred.SecretNonce, cred.SecretCipher, crypto.AADForHost(hostID))
	if err != nil {
		return nil, fmt.Errorf("解密 BMC 凭据失败: %w", err)
	}

	var power string
	work := func(ctx context.Context) error {
		var exec ipmi.Executor
		if p.newExec != nil {
			exec = p.newExec()
		}
		client := ipmi.NewClient(exec, derefString(host.BMCIP), cred.Username, string(pass))
		state, err := client.PowerState(ctx)
		if err != nil {
			return err
		}
		power = state
		return nil
	}
	if p.pool != nil {
		if err := p.pool.Do(ctx, strconv.FormatInt(hostID, 10), work); err != nil {
			return nil, err
		}
	} else if err = work(ctx); err != nil {
		return nil, err
	}
	return map[string]any{
		"ok": true, "host_id": hostID, "bmc_ip": derefString(host.BMCIP), "power": power,
	}, nil
}

// PollNow 立即对一台带外主机执行一次轮询（调度之外的手动触发/测试入口）。
func (p *IPMIPoller) PollNow(ctx context.Context, hostID int64) error {
	host, err := p.store.Hosts().GetByID(ctx, hostID)
	if err != nil {
		return err
	}
	return p.pollHost(ctx, hostID, host.Hostname, derefString(host.BMCIP))
}

// TargetCount 返回当前登记的带外采集目标数。
func (p *IPMIPoller) TargetCount() int { return len(p.sched.TargetIDs()) }
