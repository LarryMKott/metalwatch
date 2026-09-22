package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	"github.com/LarryMKott/metalwatch/pkg/crypto"
)

// ErrEnrollCodeUnavailable 表示注册码无效、过期或已用尽。
// 两个传输层（JSON / gRPC）据此映射各自的未认证错误。
var ErrEnrollCodeUnavailable = errors.New("注册码无效、已过期或已被使用")

// EnrollInput 是 Agent 注册的领域入参（与传输协议无关）。
type EnrollInput struct {
	Code         string // 一次性注册码
	Hostname     string
	PrimaryIP    string
	SMBIOSUUID   string // 硬件指纹；相同视为同一台机器
	OSType       string
	OSVersion    string
	AgentVersion string
}

// EnrollResult 是注册结果。令牌明文只在此出现一次，由调用方负责送达。
type EnrollResult struct {
	Host           *adapter.Host
	AgentToken     string
	ReportInterval time.Duration
	AssetInterval  time.Duration
}

// EnrollService 承载 Agent 注册流程：校验一次性注册码 → 复用或新建资产 → 签发令牌。
// JSON 通道（api/agentpb）与 gRPC 通道（api/agent）共用，保证两条协议行为一致（docs/01 D31）。
type EnrollService struct {
	store          adapter.MetadataStore
	hosts          *HostService
	reportInterval time.Duration
	assetInterval  time.Duration
	log            *slog.Logger
}

// NewEnrollService 构造注册服务。周期来自服务端配置，作为下发口径随结果返回。
func NewEnrollService(store adapter.MetadataStore, hosts *HostService,
	reportInterval, assetInterval time.Duration, log *slog.Logger) *EnrollService {
	if log == nil {
		log = slog.Default()
	}
	return &EnrollService{
		store: store, hosts: hosts,
		reportInterval: reportInterval, assetInterval: assetInterval, log: log,
	}
}

// Enroll 执行注册。错误语义：
//   - ErrInvalidInput：必填项或 IP 格式非法；
//   - ErrEnrollCodeUnavailable：注册码无效/过期/用尽；
//   - 其余错误为存储层故障，原样上抛。
func (s *EnrollService) Enroll(ctx context.Context, in EnrollInput) (*EnrollResult, error) {
	code := strings.TrimSpace(in.Code)
	hostname := strings.TrimSpace(in.Hostname)
	ip := strings.TrimSpace(in.PrimaryIP)
	uuid := strings.TrimSpace(in.SMBIOSUUID)

	if code == "" || hostname == "" || net.ParseIP(ip) == nil {
		return nil, fmt.Errorf("%w: enroll_code、hostname 与合法的 primary_ip 均为必填", ErrInvalidInput)
	}

	codeHash := crypto.HashToken(code)
	// 消费注册码是一次性语义的原子闸门：必须先消费再建资产，防并发重复注册。
	if err := s.store.EnrollCodes().Consume(ctx, codeHash); err != nil {
		if errors.Is(err, adapter.ErrCodeExhausted) {
			return nil, ErrEnrollCodeUnavailable
		}
		return nil, err
	}

	// 硬件指纹相同视为同一台机器：复用 host_id，重装 Agent 不产生重复资产
	host, err := s.store.Hosts().GetBySMBIOSUUID(ctx, uuid)
	if errors.Is(err, adapter.ErrNotFound) || uuid == "" {
		host, err = s.hosts.Create(ctx, CreateHostInput{
			Hostname:   hostname,
			PrimaryIP:  ip,
			SMBIOSUUID: optionalString(uuid),
			OSType:     in.OSType,
			OSVersion:  optionalString(in.OSVersion),
		})
		if err != nil {
			// 资产创建失败时归还次数：注册码不被白白烧掉（消费/归还严格配对）
			if rerr := s.store.EnrollCodes().Refund(ctx, codeHash); rerr != nil {
				s.log.Warn("注册码归还失败", "err", rerr)
			}
			return nil, err
		}
	}
	if err != nil {
		return nil, err
	}

	token, err := crypto.RandomToken("mwa_", 32)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if _, err := s.store.AgentTokens().Create(ctx, &adapter.AgentToken{
		HostID: &host.ID, TokenHash: crypto.HashToken(token),
		EnrolledAt: &now, State: "active",
	}); err != nil {
		return nil, err
	}

	s.log.Info("Agent 注册成功", "host_id", host.ID, "hostname", host.Hostname)
	return &EnrollResult{
		Host:           host,
		AgentToken:     token,
		ReportInterval: s.reportInterval,
		AssetInterval:  s.assetInterval,
	}, nil
}

// optionalString 空串转 nil 指针（资产表可空列约定）。
func optionalString(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}
