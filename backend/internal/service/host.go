// Package service 是业务服务层：承载校验规则、业务约束与跨仓储编排。
// 这一层不感知 HTTP，也不感知具体数据库方言。
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	"github.com/LarryMKott/metalwatch/pkg/ptr"
	"github.com/LarryMKott/metalwatch/pkg/strs"
	"github.com/LarryMKott/metalwatch/pkg/timex"
)

// 领域错误：API 层据此映射 HTTP 状态码。
var (
	ErrInvalidInput = errors.New("输入不合法")
	ErrConflict     = errors.New("资源冲突")
)

var (
	hostnameRe  = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9\-\.]{0,62}[A-Za-z0-9])?$`)
	validOSType = map[string]bool{"linux": true, "windows": true, "unknown": true}
)

// CreateHostInput 是创建资产的入参。
type CreateHostInput struct {
	Hostname    string  `json:"hostname"`
	PrimaryIP   string  `json:"primary_ip"`
	BMCIP       *string `json:"bmc_ip,omitempty"`
	SN          *string `json:"sn,omitempty"`
	SMBIOSUUID  *string `json:"smbios_uuid,omitempty"`
	Site        *string `json:"site,omitempty"`
	Rack        *string `json:"rack,omitempty"`
	RackUnit    *int    `json:"rack_unit,omitempty"`
	OSType      string  `json:"os_type,omitempty"`
	OSVersion   *string `json:"os_version,omitempty"`
	CollectIPMI bool    `json:"collect_ipmi"`
	Remark      *string `json:"remark,omitempty"`
}

// HostService 提供资产管理能力。
type HostService struct {
	repo *adapter.HostRepo
	log  *slog.Logger
	// alerts 用于删除主机时收尾其遗留的 firing 告警；未注入时为 nil（单测与
	// 纯资产场景无需该依赖，删除退化为只删主机行）。
	alerts alertReconciler
}

// alertReconciler 是 HostService 对告警仓储的最小依赖面。
// 用接口而不是具体类型：HostService 不需要知道告警怎么存，只需要「按主机收尾」这一个动作。
type alertReconciler interface {
	ResolveByHost(ctx context.Context, hostID int64, at string) (int, error)
}

// HostServiceOption 是 HostService 的可选依赖。
type HostServiceOption func(*HostService)

// WithAlertReconciler 注入告警收尾能力（删除主机时解除其 firing 告警）。
func WithAlertReconciler(r alertReconciler) HostServiceOption {
	return func(s *HostService) { s.alerts = r }
}

// NewHostService 构造资产管理服务。可选依赖用选项注入，既有调用点无需改动。
func NewHostService(repo *adapter.HostRepo, log *slog.Logger, opts ...HostServiceOption) *HostService {
	if log == nil {
		log = slog.Default()
	}
	s := &HostService{repo: repo, log: log}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Create 校验并写入一台资产。
func (s *HostService) Create(ctx context.Context, in CreateHostInput) (*adapter.Host, error) {
	if err := validateCreateHost(in); err != nil {
		return nil, err
	}

	h := &adapter.Host{
		Hostname:     strings.TrimSpace(in.Hostname),
		PrimaryIP:    strings.TrimSpace(in.PrimaryIP),
		BMCIP:        ptr.Trim(in.BMCIP),
		SN:           ptr.Trim(in.SN),
		SMBIOSUUID:   ptr.Trim(in.SMBIOSUUID),
		Site:         ptr.Trim(in.Site),
		Rack:         ptr.Trim(in.Rack),
		RackUnit:     in.RackUnit,
		OSType:       strs.OrDefault(in.OSType, "unknown"),
		OSVersion:    ptr.Trim(in.OSVersion),
		Remark:       ptr.Trim(in.Remark),
		CollectAgent: true,
		CollectIPMI:  in.CollectIPMI,
		Status:       "unknown",
	}

	id, err := s.repo.Create(ctx, h)
	if err != nil {
		if adapter.IsUniqueViolation(err) {
			return nil, fmt.Errorf("%w: 主机 %s 的 SMBIOS UUID 或 BMC 地址已存在", ErrConflict, h.Hostname)
		}
		return nil, err
	}
	h.ID = id
	s.log.Info("资产已录入", "id", id, "hostname", h.Hostname, "primary_ip", h.PrimaryIP)
	return h, nil
}

// Get 读取单台资产。
func (s *HostService) Get(ctx context.Context, id int64) (*adapter.Host, error) {
	if id <= 0 {
		return nil, fmt.Errorf("%w: 非法的主机 ID", ErrInvalidInput)
	}
	h, err := s.repo.GetByID(ctx, id)
	if errors.Is(err, adapter.ErrNotFound) {
		return nil, fmt.Errorf("%w: 主机 %d 不存在", ErrNotFoundInService, id)
	}
	return h, err
}

// ErrNotFoundInService 表示业务层未找到资源。
var ErrNotFoundInService = errors.New("资源不存在")

// List 分页查询资产。
func (s *HostService) List(ctx context.Context, f adapter.ListFilter) ([]*adapter.Host, int, error) {
	return s.repo.List(ctx, f)
}

// Delete 删除资产，并收尾该主机遗留的 firing 告警。
//
// 告警收尾必须在 DELETE 之前完成：alert_event.host_id 是 ON DELETE SET NULL，
// 主机行一旦消失，就没有任何字段能把遗留的 firing 行关联回来 —— 它们会永久停在
// firing（占住 active_key 唯一键、CountFiring 长期虚高），且同名主机重建时
// 新告警会 UPDATE 到这些无主行上，按主机关联查询不到。
func (s *HostService) Delete(ctx context.Context, id int64) error {
	if id <= 0 {
		return fmt.Errorf("%w: 非法的主机 ID", ErrInvalidInput)
	}
	// 先确认存在再收尾：否则会把告警收尾作用在一台并未被删除的主机上。
	// 顺带保持 ErrNotFound 的对外语义与改动前一致，不依赖 Delete 的受影响行数。
	if _, err := s.repo.GetByID(ctx, id); err != nil {
		if errors.Is(err, adapter.ErrNotFound) {
			return fmt.Errorf("%w: 主机 %d 不存在", ErrNotFoundInService, id)
		}
		return err
	}

	if s.alerts != nil {
		n, err := s.alerts.ResolveByHost(ctx, id, timex.RFC3339(time.Now()))
		if err != nil {
			// 收尾失败就不删：宁可让用户重试，也不留下再也关联不回来的 firing 行
			return fmt.Errorf("解除主机 %d 的告警失败，已取消删除: %w", id, err)
		}
		if n > 0 {
			s.log.Info("主机告警已随删除解除", "id", id, "resolved", n)
		}
	}

	if err := s.repo.Delete(ctx, id); err != nil {
		if errors.Is(err, adapter.ErrNotFound) {
			return fmt.Errorf("%w: 主机 %d 不存在", ErrNotFoundInService, id)
		}
		return err
	}
	s.log.Info("资产已删除", "id", id)
	return nil
}

func validateCreateHost(in CreateHostInput) error {
	if strings.TrimSpace(in.Hostname) == "" {
		return fmt.Errorf("%w: hostname 不能为空", ErrInvalidInput)
	}
	if !hostnameRe.MatchString(strings.TrimSpace(in.Hostname)) {
		return fmt.Errorf("%w: hostname 只能包含字母、数字、连字符与点，且不能以符号开头/结尾", ErrInvalidInput)
	}
	ip := strings.TrimSpace(in.PrimaryIP)
	if ip == "" {
		return fmt.Errorf("%w: primary_ip 不能为空", ErrInvalidInput)
	}
	if net.ParseIP(ip) == nil {
		return fmt.Errorf("%w: primary_ip %q 不是合法的 IP 地址", ErrInvalidInput, ip)
	}
	if in.BMCIP != nil && strings.TrimSpace(*in.BMCIP) != "" {
		if net.ParseIP(strings.TrimSpace(*in.BMCIP)) == nil {
			return fmt.Errorf("%w: bmc_ip %q 不是合法的 IP 地址", ErrInvalidInput, *in.BMCIP)
		}
	}
	if osType := strs.OrDefault(in.OSType, "unknown"); !validOSType[osType] {
		return fmt.Errorf("%w: os_type 只能是 linux/windows/unknown", ErrInvalidInput)
	}
	if in.RackUnit != nil && (*in.RackUnit < 1 || *in.RackUnit > 60) {
		return fmt.Errorf("%w: rack_unit 需在 1~60 之间", ErrInvalidInput)
	}
	return nil
}
