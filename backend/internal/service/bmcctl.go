package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	mwpb "github.com/LarryMKott/metalwatch/proto/gen"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	"github.com/LarryMKott/metalwatch/pkg/crypto"
	"github.com/LarryMKott/metalwatch/pkg/ipmi"
	"github.com/LarryMKott/metalwatch/pkg/strs"
)

// 执行超时：ipmitool 单条指令在 BMC 响应正常时是亚秒级，跨网段+重试留到 15s。
const bmcCommandTimeout = 15 * time.Second

// capabilityTimeout 是能力探测（mc info）的超时，交互式接口要快失败。
const capabilityTimeout = 8 * time.Second

// BMCControlService 承载 BMC 管控链路（W15）：指令校验 → 凭据解密 → ipmitool
// 执行（服务端直连，与 W4 采集同路径）→ 记录回写 → 操作审计。
//
// v1 采用**服务端直连**而非 proto 注释里的「Agent 代执行」：BMC 凭据只存在
// 服务端（AES-256-GCM），下发凭据到 Agent 的机制尚未设计，且部署拓扑里服务端
// 与 BMC 同网段（W4 已验证直连可行）。决策见 docs/01 D43；Agent 代执行留待
// Mesh 阶段（W14）与凭据分发方案一起做，届时复用 StreamServer.SendCommand。
type BMCControlService struct {
	store     adapter.MetadataStore
	masterKey []byte
	newExec   IPMIExecutorFactory // nil 时用真实 ipmitool；测试注入假执行器
	now       func() time.Time
	log       *slog.Logger
}

// NewBMCControlService 构造 BMC 管控服务。masterKey 为凭据解密主密钥（docs/01 D9）。
func NewBMCControlService(store adapter.MetadataStore, masterKey []byte,
	newExec IPMIExecutorFactory, log *slog.Logger) *BMCControlService {
	if log == nil {
		log = slog.Default()
	}
	return &BMCControlService{
		store: store, masterKey: masterKey, newExec: newExec,
		now: time.Now, log: log,
	}
}

// BMCCommandInput 是指令下发请求体（与前端 BmcCommandInput 同形，snake_case）。
type BMCCommandInput struct {
	CmdType      string `json:"cmd_type"`      // fan | power | identify | policy
	Target       string `json:"target"`        // 整机留空；fan 可选风扇名
	SpeedPercent *int32 `json:"speed_percent"` // fan 手动调速必填（1~100）
	AutoMode     *bool  `json:"auto_mode"`     // fan：true = 交还 BMC 自动调速
	PowerAction  string `json:"power_action"`  // power：on | off | reset | cycle | soft
	DurationSec  *int32 `json:"duration_sec"`  // identify：指示灯点亮时长（秒）
}

// BMCCommandResult 是指令执行结果（与前端 BmcCommandResult 同形）。
type BMCCommandResult struct {
	OK        bool   `json:"ok"`
	Message   string `json:"message,omitempty"`
	RequestID string `json:"request_id,omitempty"` // 即 command_id，可用于审计追溯
}

// BMCCapability 是能力探测结果（与前端 BmcCapability 同形）。
type BMCCapability struct {
	HostID          int64  `json:"host_id"`
	FanControl      bool   `json:"fan_control"`
	PowerControl    bool   `json:"power_control"`
	Identify        bool   `json:"identify"`
	BMCModel        string `json:"bmc_model,omitempty"`
	FirmwareVersion string `json:"firmware_version,omitempty"`
	Detail          string `json:"detail,omitempty"` // 探测失败原因（能力全 false 时前端提示用）
}

// 允许的指令动作白名单。
var bmcPowerActions = map[string]bool{"on": true, "off": true, "reset": true, "cycle": true, "soft": true}

// Validate 校验指令参数；不合法返回 ErrInvalidInput（HTTP 422）。
func (in *BMCCommandInput) Validate() error {
	switch in.CmdType {
	case "fan":
		if in.AutoMode != nil && *in.AutoMode {
			return nil // 交还自动调速，speed 忽略
		}
		if in.SpeedPercent == nil {
			return fmt.Errorf("%w: fan 指令需要 speed_percent（1~100）或 auto_mode=true", ErrInvalidInput)
		}
		if *in.SpeedPercent < 1 || *in.SpeedPercent > 100 {
			return fmt.Errorf("%w: speed_percent 超出范围（1~100）", ErrInvalidInput)
		}
	case "power":
		if !bmcPowerActions[in.PowerAction] {
			return fmt.Errorf("%w: power_action 必须是 on/off/reset/cycle/soft", ErrInvalidInput)
		}
	case "identify":
		d := int32(30)
		if in.DurationSec != nil {
			d = *in.DurationSec
		}
		if d < 0 || d > 3600 {
			return fmt.Errorf("%w: duration_sec 超出范围（0~3600）", ErrInvalidInput)
		}
	case "policy":
		// v1 唯一语义：交还 BMC 自动调速（W16 闭环接入前，策略下发无执行方）。
		if in.Target != "" && in.Target != "thermal" {
			return fmt.Errorf("%w: policy.target 当前仅支持 thermal", ErrInvalidInput)
		}
	default:
		return fmt.Errorf("%w: cmd_type 必须是 fan/power/identify/policy", ErrInvalidInput)
	}
	return nil
}

// Execute 校验、落审计、执行、回写。hostID 不存在或未配置 BMC 时返回
// ErrNotFoundInService（HTTP 404）；执行失败不作为接口错误——返回 ok=false 的
// 正常响应，审计里留下 failed 记录（IPMI 故障是业务结果，不是 API 故障）。
func (s *BMCControlService) Execute(ctx context.Context, hostID int64,
	operator string, in BMCCommandInput) (*BMCCommandResult, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}

	host, err := s.store.Hosts().GetByID(ctx, hostID)
	if err != nil {
		return nil, fmt.Errorf("%w: 主机不存在", ErrNotFoundInService)
	}
	if host.BMCIP == nil || *host.BMCIP == "" {
		return nil, fmt.Errorf("%w: 该主机未配置 BMC 地址", ErrNotFoundInService)
	}
	cred, err := s.store.BMC().Get(ctx, hostID)
	if err != nil {
		if errors.Is(err, adapter.ErrNotFound) {
			return nil, fmt.Errorf("%w: 该主机未配置 BMC 凭据", ErrNotFoundInService)
		}
		return nil, err
	}
	pass, err := crypto.Open(s.masterKey, cred.SecretNonce, cred.SecretCipher, crypto.AADForHost(hostID))
	if err != nil {
		return nil, fmt.Errorf("解密 BMC 凭据失败: %w", err)
	}

	cmd, err := buildProtoCommand(in)
	if err != nil {
		return nil, err
	}

	// 审计先行：即使进程在执行中崩溃，也留得下「谁在何时对哪台机器下过什么指令」
	now := s.now().UTC()
	record := &adapter.BMCCommand{
		HostID: hostID, CommandID: cmd.GetCommandId(),
		CmdType: in.CmdType, Target: in.Target,
		Params: marshalParams(in), Operator: operator, CreatedAt: now,
	}
	if _, err := s.store.BMCCommands().Create(ctx, record); err != nil {
		return nil, fmt.Errorf("写入指令记录失败: %w", err)
	}

	var exec ipmi.Executor
	if s.newExec != nil {
		exec = s.newExec()
	}
	client := ipmi.NewClient(exec, *host.BMCIP, cred.Username, string(pass))

	execCtx, cancel := context.WithTimeout(ctx, bmcCommandTimeout)
	defer cancel()
	res, execErr := client.ApplyCommand(execCtx, cmd)

	status, msg := "failed", truncateOneLine(res.GetMessage(), 512)
	if execErr == nil && res.GetOk() {
		status = "success"
	}
	var observed *float64
	if res.GetOk() {
		v := res.GetObservedValue()
		observed = &v
	}
	if err := s.store.BMCCommands().Finish(ctx, cmd.GetCommandId(), status, msg, observed, s.now().UTC()); err != nil {
		// 审计回写失败不影响结果返回，但要留日志——审计缺失比响应缺失更难补
		s.log.Warn("BMC 指令审计回写失败", "command_id", cmd.GetCommandId(), "err", err)
	}
	if execErr != nil {
		s.log.Warn("BMC 指令执行失败", "host_id", hostID, "cmd_type", in.CmdType,
			"command_id", cmd.GetCommandId(), "err", execErr)
	}

	return &BMCCommandResult{OK: res.GetOk(), Message: msg, RequestID: cmd.GetCommandId()}, nil
}

// Capability 探测一台主机的 BMC 控制能力（mc info）。
// 未配置 BMC 返回 ErrNotFoundInService；探测失败返回能力全 false 的正常响应
// （前端据此禁用操作按钮并提示原因，而不是整页报错）。
func (s *BMCControlService) Capability(ctx context.Context, hostID int64) (*BMCCapability, error) {
	host, err := s.store.Hosts().GetByID(ctx, hostID)
	if err != nil {
		return nil, fmt.Errorf("%w: 主机不存在", ErrNotFoundInService)
	}
	out := &BMCCapability{HostID: hostID}
	if host.BMCIP == nil || *host.BMCIP == "" {
		return nil, fmt.Errorf("%w: 该主机未配置 BMC 地址", ErrNotFoundInService)
	}
	cred, err := s.store.BMC().Get(ctx, hostID)
	if err != nil {
		if errors.Is(err, adapter.ErrNotFound) {
			return nil, fmt.Errorf("%w: 该主机未配置 BMC 凭据", ErrNotFoundInService)
		}
		return nil, err
	}
	pass, err := crypto.Open(s.masterKey, cred.SecretNonce, cred.SecretCipher, crypto.AADForHost(hostID))
	if err != nil {
		return nil, fmt.Errorf("解密 BMC 凭据失败: %w", err)
	}

	var exec ipmi.Executor
	if s.newExec != nil {
		exec = s.newExec()
	}
	capCtx, cancel := context.WithTimeout(ctx, capabilityTimeout)
	defer cancel()
	info, err := exec.Run(capCtx, *host.BMCIP, cred.Username, string(pass), "mc", "info")
	if err != nil {
		out.Detail = truncateOneLine(err.Error(), 200)
		return out, nil
	}
	text := string(info)
	if !ipmiLooksReal(text) {
		out.Detail = "BMC 响应不是有效的 mc info 输出"
		return out, nil
	}
	// 全部控制能力置 true：mc info 可达即认为支持 chassis/raw 指令集
	//（个别机型缺某条 raw 指令以执行结果为准，真机验证归 W17）
	out.FanControl, out.PowerControl, out.Identify = true, true, true
	out.BMCModel = mcInfoField(text, "Product Name")
	out.FirmwareVersion = mcInfoField(text, "Firmware Revision")
	return out, nil
}

// ListAudit 取最近 limit 条操作审计。
func (s *BMCControlService) ListAudit(ctx context.Context, limit int) ([]adapter.BMCCommandWithHost, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return s.store.BMCCommands().List(ctx, limit)
}

// buildProtoCommand 把请求体映射为 proto 指令。policy → fan_speed+auto
// （v1 唯一语义）；fan 的 auto/speed=0 都映射为交还自动（与 proto 注释一致）。
func buildProtoCommand(in BMCCommandInput) (*mwpb.BmcCommand, error) {
	id, err := newCommandID()
	if err != nil {
		return nil, fmt.Errorf("生成指令 ID 失败: %w", err)
	}
	cmd := &mwpb.BmcCommand{CommandId: id, Target: in.Target}
	switch in.CmdType {
	case "fan":
		cmd.CmdType = "fan_speed"
		if in.AutoMode != nil && *in.AutoMode {
			cmd.AutoMode = true
		} else {
			pct := *in.SpeedPercent
			if pct == 0 {
				cmd.AutoMode = true // proto 约定：0 表示交还自动调速
			} else {
				cmd.SpeedPercent = pct
			}
		}
	case "power":
		cmd.CmdType = "power"
		cmd.PowerAction = in.PowerAction
	case "identify":
		cmd.CmdType = "identify"
		d := int32(30)
		if in.DurationSec != nil {
			d = *in.DurationSec
		}
		cmd.DurationSec = d
	case "policy":
		cmd.CmdType = "fan_speed"
		cmd.AutoMode = true
	default:
		return nil, fmt.Errorf("%w: cmd_type 必须是 fan/power/identify/policy", ErrInvalidInput)
	}
	return cmd, nil
}

// marshalParams 落审计用的参数快照：只保留请求里出现过的字段。
func marshalParams(in BMCCommandInput) string {
	m := map[string]any{"cmd_type": in.CmdType}
	if in.Target != "" {
		m["target"] = in.Target
	}
	if in.SpeedPercent != nil {
		m["speed_percent"] = *in.SpeedPercent
	}
	if in.AutoMode != nil {
		m["auto_mode"] = *in.AutoMode
	}
	if in.PowerAction != "" {
		m["power_action"] = in.PowerAction
	}
	if in.DurationSec != nil {
		m["duration_sec"] = *in.DurationSec
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func newCommandID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "bmc-" + hex.EncodeToString(buf), nil
}

// truncateOneLine 把执行输出压成单行并截断（ipmitool 输出可能多行长文本；
// 审计表与前端列表都只需要摘要）。
func truncateOneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	return strs.Truncate(s, max)
}

// ipmiLooksReal 粗判 mc info 输出：真输出必有 "Device ID" 行。
func ipmiLooksReal(text string) bool {
	return strings.Contains(text, "Device ID")
}

// mcInfoField 从 mc info 输出里取字段值。ipmitool 的字段名与冒号之间用空格
// 填充对齐（如 "Product Name              : X"），所以按「冒号左侧 trim 后
// 等于字段名」匹配，而不是找 "字段名:" 子串——那样永远匹配不上。
func mcInfoField(text, field string) string {
	for _, line := range strings.Split(text, "\n") {
		name, value, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(name) == field {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
