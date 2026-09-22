package adapter

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrNotFound 表示目标记录不存在。
var ErrNotFound = errors.New("记录不存在")

// Host 是资产主表的领域表示。可空字段用指针表达。
type Host struct {
	ID           int64
	Hostname     string
	PrimaryIP    string
	BMCIP        *string
	SN           *string
	SMBIOSUUID   *string
	Site         *string
	Rack         *string
	RackUnit     *int
	OSType       string
	OSVersion    *string
	CollectAgent bool
	CollectIPMI  bool
	AgentVersion *string
	Status       string
	LastSeenAt   *time.Time
	GeoCountry   *string
	GeoASN       *int
	Remark       *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// HostRepo 提供资产数据的读写。
type HostRepo struct{ s *Store }

// Hosts 返回资产仓储。
func (s *Store) Hosts() *HostRepo { return &HostRepo{s: s} }

const hostColumns = `id, hostname, primary_ip, bmc_ip, sn, smbios_uuid, site, rack, rack_unit,
	os_type, os_version, collect_agent, collect_ipmi, agent_version, status, last_seen_at,
	geo_country, geo_asn, remark, created_at, updated_at`

// Create 写入一台主机，返回自增 ID。
// 时间戳由应用层生成（UTC RFC3339），保证跨方言行为一致（见 docs/03 可移植性约定）。
func (r *HostRepo) Create(ctx context.Context, h *Host) (int64, error) {
	if strings.TrimSpace(h.Hostname) == "" {
		return 0, fmt.Errorf("hostname 不能为空")
	}
	if strings.TrimSpace(h.PrimaryIP) == "" {
		return 0, fmt.Errorf("primary_ip 不能为空")
	}
	now := time.Now().UTC()
	if h.CreatedAt.IsZero() {
		h.CreatedAt = now
	}
	h.UpdatedAt = now

	q := r.s.Rebind(`INSERT INTO host
		(hostname, primary_ip, bmc_ip, sn, smbios_uuid, site, rack, rack_unit,
		 os_type, os_version, collect_agent, collect_ipmi, agent_version, status,
		 last_seen_at, geo_country, geo_asn, remark, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)

	res, err := r.s.ExecContext(ctx, q,
		h.Hostname, h.PrimaryIP, h.BMCIP, h.SN, h.SMBIOSUUID, h.Site, h.Rack, h.RackUnit,
		defaultStr(h.OSType, "unknown"), h.OSVersion, boolToInt(h.CollectAgent), boolToInt(h.CollectIPMI),
		h.AgentVersion, defaultStr(h.Status, "unknown"), tsOrNil(h.LastSeenAt),
		h.GeoCountry, h.GeoASN, h.Remark,
		formatTime(h.CreatedAt), formatTime(h.UpdatedAt))
	if err != nil {
		return 0, fmt.Errorf("写入主机 %s 失败: %w", h.Hostname, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	h.ID = id
	return id, nil
}

// GetByID 按主键读取主机。
func (r *HostRepo) GetByID(ctx context.Context, id int64) (*Host, error) {
	q := r.s.Rebind(`SELECT ` + hostColumns + ` FROM host WHERE id = ?`)
	row := r.s.QueryRowContext(ctx, q, id)
	h, err := scanHost(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return h, nil
}

// ListFilter 是列表查询条件。
type ListFilter struct {
	// Keyword 模糊匹配 hostname / primary_ip / sn
	Keyword string
	Status  string
	// IPMIOnly 为 true 时只返回启用了带外采集且有 BMC 地址的主机
	IPMIOnly bool
	Limit    int
	Offset   int
}

// List 返回主机列表与总数。
func (r *HostRepo) List(ctx context.Context, f ListFilter) ([]*Host, int, error) {
	where, args := buildHostWhere(r.s, f)

	var total int
	if err := r.s.QueryRowContext(ctx,
		r.s.Rebind(`SELECT COUNT(*) FROM host`+where), args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("统计数据总失败: %w", err)
	}

	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}

	q := r.s.Rebind(`SELECT ` + hostColumns + ` FROM host` + where +
		` ORDER BY id DESC LIMIT ? OFFSET ?`)
	rows, err := r.s.QueryContext(ctx, q, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("查询主机列表失败: %w", err)
	}
	defer rows.Close()

	var out []*Host
	for rows.Next() {
		h, err := scanHost(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// UpdateStatus 更新在线状态与最后可见时间（Agent 心跳/上报路径使用）。
func (r *HostRepo) UpdateStatus(ctx context.Context, id int64, status string, seen time.Time) error {
	q := r.s.Rebind(`UPDATE host SET status = ?, last_seen_at = ?, updated_at = ? WHERE id = ?`)
	res, err := r.s.ExecContext(ctx, q, status, formatTime(seen), formatTime(time.Now().UTC()), id)
	if err != nil {
		return fmt.Errorf("更新主机状态失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete 删除主机（部件、快照、部件变更按外键级联；告警外键为 SET NULL，保留事件史）。
func (r *HostRepo) Delete(ctx context.Context, id int64) error {
	q := r.s.Rebind(`DELETE FROM host WHERE id = ?`)
	res, err := r.s.ExecContext(ctx, q, id)
	if err != nil {
		return fmt.Errorf("删除主机失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func buildHostWhere(s *Store, f ListFilter) (string, []any) {
	var conds []string
	var args []any

	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		like := "%" + kw + "%"
		conds = append(conds,
			"(hostname LIKE ? OR primary_ip LIKE ? OR COALESCE(sn,'') LIKE ?)")
		args = append(args, like, like, like)
	}
	if st := strings.TrimSpace(f.Status); st != "" {
		conds = append(conds, "status = ?")
		args = append(args, st)
	}
	if f.IPMIOnly {
		conds = append(conds, "collect_ipmi = 1 AND bmc_ip IS NOT NULL")
	}
	if len(conds) == 0 {
		return "", nil
	}
	_ = s // 保留参数以便将来注入方言特化条件
	return " WHERE " + strings.Join(conds, " AND "), args
}

// rowScanner 抽象 *sql.Row 与 *sql.Rows 的公共部分。
type rowScanner interface {
	Scan(dest ...any) error
}

func scanHost(sc rowScanner) (*Host, error) {
	var (
		h          Host
		rackUnit   sql.NullInt64
		lastSeen   sql.NullString
		geoASN     sql.NullInt64
		agentOn    int
		ipmiOn     int
		createdAt  string
		updatedAt  string
		bmcIP      sql.NullString
		sn         sql.NullString
		uuid       sql.NullString
		site       sql.NullString
		rack       sql.NullString
		osVersion  sql.NullString
		agentVer   sql.NullString
		geoCountry sql.NullString
		remark     sql.NullString
	)
	err := sc.Scan(&h.ID, &h.Hostname, &h.PrimaryIP, &bmcIP, &sn, &uuid, &site, &rack, &rackUnit,
		&h.OSType, &osVersion, &agentOn, &ipmiOn, &agentVer, &h.Status, &lastSeen,
		&geoCountry, &geoASN, &remark, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}

	h.BMCIP = strPtr(bmcIP)
	h.SN = strPtr(sn)
	h.SMBIOSUUID = strPtr(uuid)
	h.Site = strPtr(site)
	h.Rack = strPtr(rack)
	h.OSVersion = strPtr(osVersion)
	h.AgentVersion = strPtr(agentVer)
	h.GeoCountry = strPtr(geoCountry)
	h.Remark = strPtr(remark)
	if rackUnit.Valid {
		v := int(rackUnit.Int64)
		h.RackUnit = &v
	}
	if geoASN.Valid {
		v := int(geoASN.Int64)
		h.GeoASN = &v
	}
	h.CollectAgent = agentOn != 0
	h.CollectIPMI = ipmiOn != 0
	h.LastSeenAt = parseTimePtr(lastSeen)
	h.CreatedAt = parseTime(createdAt)
	h.UpdatedAt = parseTime(updatedAt)
	return &h, nil
}

func strPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func defaultStr(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func tsOrNil(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return formatTime(*t)
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func parseTimePtr(v sql.NullString) *time.Time {
	if !v.Valid || v.String == "" {
		return nil
	}
	t := parseTime(v.String)
	if t.IsZero() {
		return nil
	}
	return &t
}

// SetBMC 更新主机的带外信息：bmc_ip 与是否启用带外采集。
// 供 BMC 凭据管理接口调用；启用前必须已有合法 bmc_ip。
func (r *HostRepo) SetBMC(ctx context.Context, id int64, bmcIP string, collectIPMI bool, at time.Time) error {
	ip := func() *string {
		if bmcIP == "" {
			return nil
		}
		return &bmcIP
	}()
	flag := 0
	if collectIPMI {
		flag = 1
	}
	res, err := r.s.ExecContext(ctx, r.s.Rebind(
		`UPDATE host SET bmc_ip = ?, collect_ipmi = ?, updated_at = ? WHERE id = ?`),
		ip, flag, formatTime(at), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
