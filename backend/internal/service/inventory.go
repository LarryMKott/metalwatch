package service

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	gen "github.com/LarryMKott/metalwatch/proto/gen"
)

// InventoryService 承载资产快照与硬件变更检测（W2/W18）：
//
//	AssetSnapshot（gRPC asset 上行）→ 规范化部件清单 → 指纹比对 →
//	  ├─ 首次采集：只建基线，不产生变更（D11 ①）
//	  ├─ 指纹未变：仅刷新部件 last_seen
//	  └─ 指纹变化：diff → change_event（added/removed 需连续两轮确认，D11 ②；
//	       modified 只对白名单字段敏感，D11 ③）→ asset_change 告警 + change.detected 事件
type InventoryService struct {
	store  adapter.MetadataStore
	alerts *AlertService
	log    *slog.Logger
}

// NewInventoryService 构造资产检测服务。
func NewInventoryService(store adapter.MetadataStore, alerts *AlertService, log *slog.Logger) *InventoryService {
	if log == nil {
		log = slog.Default()
	}
	return &InventoryService{store: store, alerts: alerts, log: log}
}

// 修改敏感字段白名单（D11 ③）：序列号、容量、固件——避免热插拔噪声。
var modifiedWhitelist = map[string]bool{
	"serial": true, "capacity_bytes": true, "firmware": true,
}

// IngestSnapshot 处理一次资产快照。mode 为采集来源（agent/ipmi），进前端「来源」列。
func (s *InventoryService) IngestSnapshot(ctx context.Context, hostID int64,
	hostname string, snap *gen.AssetSnapshot, mode string, at time.Time) error {

	comps := componentsFromSnapshot(snap)
	fingerprint := fingerprintOf(comps)

	prevFP, hasPrev, err := s.store.Assets().LatestFingerprint(ctx, hostID, mode)
	if err != nil {
		return fmt.Errorf("读取上次快照失败: %w", err)
	}

	// 既有部件先取出：modified 比对与回归判定都要用旧值（必须在 Upsert 之前读）。
	// 用 ListAll 而非 ListActive：被 MarkRemoved 的部件也要入表，
	// 否则「拔了又插」会被误判成全新 added。
	existing, err := s.store.Components().ListAll(ctx, hostID, "")
	if err != nil {
		return fmt.Errorf("读取部件失败: %w", err)
	}
	existingByKey := map[string]adapter.Component{}
	for _, c := range existing {
		existingByKey[componentKey(c.Category, c.Slot)] = c
	}

	if err := s.store.Components().UpsertBatch(ctx, hostID, comps, at); err != nil {
		return fmt.Errorf("部件写入失败: %w", err)
	}

	var snapshotID *int64
	recordSnapshot := func(withPayload bool) error {
		var payload *string
		if withPayload {
			raw, _ := json.Marshal(comps)
			str := string(raw)
			payload = &str
		}
		id, err := s.store.Assets().CreateSnapshot(ctx, &adapter.HardwareSnapshot{
			HostID: hostID, CollectMode: mode, CapturedAt: at,
			Fingerprint: fingerprint, Payload: payload,
		})
		if err != nil {
			return err
		}
		snapshotID = &id
		return nil
	}

	// D11 ①：首次采集只建基线
	if !hasPrev {
		if err := recordSnapshot(true); err != nil {
			return err
		}
		s.log.Info("资产基线已建立", "host_id", hostID, "components", len(comps))
		return nil
	}

	changed := prevFP != fingerprint
	if err := recordSnapshot(changed); err != nil {
		return err
	}

	if !changed {
		// 指纹未变也要做移除确认：上一轮「第一次缺席」的部件本轮继续缺席，
		// 正是 D11 ② 的第二次确认时机，不能被指纹短路跳过
		n, err := s.confirmRemovals(ctx, hostID, hostname, mode, at)
		if err != nil {
			return err
		}
		if n > 0 {
			s.log.Warn("检测到硬件变更", "host_id", hostID, "changes", n)
		}
		return nil
	}

	count, err := s.diff(ctx, hostID, hostname, existingByKey, comps, mode, snapshotID, at)
	if err != nil {
		return err
	}
	if count > 0 {
		s.log.Warn("检测到硬件变更", "host_id", hostID, "changes", count)
	}
	return nil
}

// diff 比对既有部件与新快照，落变更事件并触发 asset_change 告警。
func (s *InventoryService) diff(ctx context.Context, hostID int64, hostname string,
	existingByKey map[string]adapter.Component, comps []adapter.Component,
	mode string, snapshotID *int64, at time.Time) (int, error) {

	n := 0
	// added：新快照有、而部件历史里没有。
	// 注意 —— 历史里存在但 removed_at 非空的是「拔了又插」的回归，
	// 不算新增（change_type 只有 added/removed/modified 三值，回归不落事件）。
	for _, c := range comps {
		prev, ok := existingByKey[componentKey(c.Category, c.Slot)]
		if !ok {
			if err := s.recordChange(ctx, hostID, hostname, snapshotID, mode, at,
				c.Category, c.Slot, "added", "", nil, ptrString(componentSummary(c))); err != nil {
				return n, err
			}
			n++
			continue
		}
		if prev.RemovedAt != nil {
			// UpsertBatch 已把 removed_at 清空并刷新 last_seen（恢复在役），此处无需额外动作。
			s.log.Debug("部件回归已恢复在役", "host_id", hostID, "category", c.Category, "slot", c.Slot)
		}
	}
	// removed：本轮缺席 → 第一次只标记（D11 ②），连续第二轮缺席才落事件
	removedCount, err := s.confirmRemovals(ctx, hostID, hostname, mode, at)
	if err != nil {
		return n, err
	}
	n += removedCount
	// modified：只对白名单字段敏感（D11 ③）
	for _, c := range comps {
		prev, ok := existingByKey[componentKey(c.Category, c.Slot)]
		if !ok || prev.RemovedAt != nil {
			continue
		}
		for _, field := range []string{"serial", "capacity_bytes", "firmware"} {
			oldV, newV := whitelistValue(&prev, field), whitelistValue(&c, field)
			if oldV == newV {
				continue
			}
			oldCopy, newCopy := oldV, newV
			if err := s.recordChange(ctx, hostID, hostname, snapshotID, mode, at,
				c.Category, c.Slot, "modified", field, &oldCopy, &newCopy); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// confirmRemovals 扫描本轮缺席的部件：第一次缺席标记待确认，连续第二次缺席落 removed 事件。
// 独立于指纹：指纹未变的轮次也必须执行（否则去抖确认被短路）。
func (s *InventoryService) confirmRemovals(ctx context.Context, hostID int64,
	hostname, mode string, at time.Time) (int, error) {

	missing, err := s.store.Components().ListMissingSince(ctx, hostID, at)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, c := range missing {
		if c.RemovedAt == nil {
			if err := s.store.Components().MarkRemoved(ctx, c.ID, at); err != nil {
				return n, err
			}
			continue // 第一次缺席：标记待确认，不产生事件
		}
		reported, err := s.store.Assets().HasChangeSince(ctx, hostID, c.Category, c.Slot, "removed", *c.RemovedAt)
		if err != nil {
			return n, err
		}
		if reported {
			continue // 移除事件已报过，不重复
		}
		if err := s.recordChange(ctx, hostID, hostname, nil, mode, at,
			c.Category, c.Slot, "removed", "", ptrString(componentSummary(c)), nil); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// recordChange 落一条变更事件并触发 asset_change 告警（W18：变更事件入库并告警）。
func (s *InventoryService) recordChange(ctx context.Context, hostID int64, hostname string,
	snapshotID *int64, mode string, at time.Time,
	category, slot, changeType, field string, oldValue, newValue *string) error {

	title := fmt.Sprintf("硬件变更：%s %s %s", hostname, category, slot)
	if changeType == "modified" {
		title = fmt.Sprintf("硬件变更：%s %s %s 字段 %s 变化", hostname, category, slot, field)
	}
	e := &adapter.AlertEvent{
		HostID:   &hostID,
		Severity: "info",
		Category: "asset_change",
		Title:    title,
		Detail:   ptrString(fmt.Sprintf(`{"change_type":%q,"category":%q,"slot":%q,"field":%q}`, changeType, category, slot, field)),
	}
	created, alertID, err := s.store.Alerts().UpsertFiring(ctx, e,
		fmt.Sprintf("asset-change:%d:%s:%s:%s", hostID, category, slot, changeType), formatUTC(at))
	if err != nil {
		return fmt.Errorf("asset_change 告警写入失败: %w", err)
	}
	_ = created // 同类部件反复变化只刷新既有告警行

	ev := &adapter.ChangeEvent{
		HostID: hostID, SnapshotID: snapshotID, Category: category, Slot: slot,
		ChangeType: changeType, Field: field, OldValue: oldValue, NewValue: newValue,
		DetectedAt: at, ConfirmCount: 2,
	}
	if changeType != "removed" {
		// added/modified 当轮即确认（快照可见即为事实）；removed 由两轮缺席去抖
		ev.ConfirmCount = 1
	}
	if alertID > 0 {
		ev.AlertEventID = &alertID
	}
	if _, err := s.store.Assets().CreateChange(ctx, ev); err != nil {
		return fmt.Errorf("变更事件写入失败: %w", err)
	}
	if created {
		// 事件分发（change.detected，docs/04 §5）；新告警才推送，刷新不推
		s.alerts.dispatch(ctx, e, "change.detected", at)
	}
	return nil
}

// compKeySep 是部件映射键的分隔符。历史教训：分隔符必须单点定义——
// 此前两处源码一处是真实 0x1f 字节、一处是字面反斜杠转义，键永不相等。
const compKeySep = ""

// componentKey 构造部件在 diff 映射中的键。
func componentKey(category, slot string) string { return category + compKeySep + slot }

// componentsFromSnapshot 把 proto 资产快照规范化为部件清单。
// slot 为部件身份键：CPU 用 socket、内存/NIC 用槽位、磁盘用 device。
func componentsFromSnapshot(snap *gen.AssetSnapshot) []adapter.Component {
	comps := []adapter.Component{}
	add := func(c adapter.Component) { comps = append(comps, c) }

	if cpu := snap.GetCpu(); cpu != nil {
		add(adapter.Component{
			Category: "cpu", Slot: orDefault(cpu.GetSocket(), "cpu0"),
			Name: cpu.GetModel(),
			Extra: marshalExtra(map[string]any{
				"cores": cpu.GetCores(), "threads": cpu.GetThreads(), "base_mhz": cpu.GetBaseMhz(),
			}),
		})
	}
	for _, m := range snap.GetMemory() {
		capacity := int64(m.GetSizeBytes())
		add(adapter.Component{
			Category: "memory", Slot: orDefault(m.GetSlot(), "dimm"),
			Name:          strings_joinNonEmpty(" ", m.GetManufacturer(), m.GetPartNumber()),
			Serial:        m.GetPartNumber(),
			CapacityBytes: &capacity,
			Extra:         marshalExtra(map[string]any{"speed": m.GetSpeed(), "type": m.GetType()}),
		})
	}
	for _, n := range snap.GetNic() {
		add(adapter.Component{
			Category: "nic", Slot: orDefault(n.GetName(), "nic"),
			Name: n.GetDriver(), Serial: n.GetMac(),
			Extra: marshalExtra(map[string]any{"speed_mbps": n.GetSpeedMbps(), "link_state": n.GetLinkState()}),
		})
	}
	if r := snap.GetRaid(); r != nil {
		add(adapter.Component{
			Category: "raid_controller", Slot: orDefault(r.GetController(), "raid0"),
			Extra: marshalExtra(map[string]any{
				"level": r.GetLevel(), "state": r.GetState(),
				"rebuild_percent": r.GetRebuildPercent(), "disk_count": r.GetDiskCount(),
			}),
		})
	}
	add(adapter.Component{
		Category: "bios", Slot: "bios",
		Name: snap.GetBoardModel(), Firmware: snap.GetBiosVersion(),
	})
	return comps
}

// fingerprintOf 计算部件清单的规范化指纹：字段排序后 SHA-1（docs/03：规范化 JSON 的哈希）。
func fingerprintOf(comps []adapter.Component) string {
	type minimal struct {
		Category string `json:"category"`
		Slot     string `json:"slot"`
		Serial   string `json:"serial,omitempty"`
		Firmware string `json:"firmware,omitempty"`
		Cap      *int64 `json:"capacity_bytes,omitempty"`
	}
	rows := make([]minimal, 0, len(comps))
	for _, c := range comps {
		rows = append(rows, minimal{c.Category, c.Slot, c.Serial, c.Firmware, c.CapacityBytes})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Category != rows[j].Category {
			return rows[i].Category < rows[j].Category
		}
		return rows[i].Slot < rows[j].Slot
	})
	raw, _ := json.Marshal(rows)
	sum := sha1.Sum(raw)
	return "sha1:" + hex.EncodeToString(sum[:])
}

// whitelistValue 取白名单字段的字符串化值（modified 比对用）。
func whitelistValue(c *adapter.Component, field string) string {
	switch field {
	case "serial":
		return c.Serial
	case "firmware":
		return c.Firmware
	case "capacity_bytes":
		if c.CapacityBytes == nil {
			return ""
		}
		return fmt.Sprintf("%d", *c.CapacityBytes)
	}
	return ""
}

func componentSummary(c adapter.Component) string {
	out := c.Name
	if c.Serial != "" {
		out += " (SN:" + c.Serial + ")"
	}
	return out
}

func strings_joinNonEmpty(sep string, parts ...string) string {
	out := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if out != "" {
			out += sep
		}
		out += p
	}
	return out
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func marshalExtra(m map[string]any) string {
	raw, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(raw)
}
