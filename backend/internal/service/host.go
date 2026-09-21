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

	"github.com/LarryMKott/metalwatch/internal/adapter"
)

// 领域错误：API 层据此映射 HTTP 状态码。
var (
	ErrInvalidInput = errors.New("输入不合法")
	ErrConflict     = errors.New("资源冲突")
)

var (
	hostnameRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9\-\.]{0,62}[A-Za-z0-9])?$`)
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
}

// NewHostService 构造资产管理服务。
func NewHostService(repo *adapter.HostRepo, log *slog.Logger) *HostService {
	if log == nil {
		log = slog.Default()
	}
	return &HostService{repo: repo, log: log}
}

// Create 校验并写入一台资产。
func (s *HostService) Create(ctx context.Context, in CreateHostInput) (*adapter.Host, error) {
	if err := validateCreateHost(in); err != nil {
		return nil, err
	}

	h := &adapter.Host{
		Hostname:    strings.TrimSpace(in.Hostname),
		PrimaryIP:   strings.TrimSpace(in.PrimaryIP),
		BMCIP:       trimPtr(in.BMCIP),
		SN:          trimPtr(in.SN),
		SMBIOSUUID:  trimPtr(in.SMBIOSUUID),
		Site:        trimPtr(in.Site),
		Rack:        trimPtr(in.Rack),
		RackUnit:    in.RackUnit,
		OSType:      defaultString(in.OSType, "unknown"),
		OSVersion:   trimPtr(in.OSVersion),
		Remark:      trimPtr(in.Remark),
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

// Delete 删除资产。
func (s *HostService) Delete(ctx context.Context, id int64) error {
	if id <= 0 {
		return fmt.Errorf("%w: 非法的主机 ID", ErrInvalidInput)
	}
	err := s.repo.Delete(ctx, id)
	if errors.Is(err, adapter.ErrNotFound) {
		return fmt.Errorf("%w: 主机 %d 不存在", ErrNotFoundInService, id)
	}
	if err != nil {
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
	if osType := defaultString(in.OSType, "unknown"); !validOSType[osType] {
		return fmt.Errorf("%w: os_type 只能是 linux/windows/unknown", ErrInvalidInput)
	}
	if in.RackUnit != nil && (*in.RackUnit < 1 || *in.RackUnit > 60) {
		return fmt.Errorf("%w: rack_unit 需在 1~60 之间", ErrInvalidInput)
	}
	return nil
}

func trimPtr(v *string) *string {
	if v == nil {
		return nil
	}
	t := strings.TrimSpace(*v)
	if t == "" {
		return nil
	}
	return &t
}

func defaultString(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return strings.TrimSpace(v)
}
