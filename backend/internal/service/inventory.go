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
	"github.com/LarryMKott/metalwatch/pkg/ptr"
	"github.com/LarryMKott/metalwatch/pkg/strs"
	"github.com/LarryMKott/metalwatch/pkg/timex"
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

// modifiedFields 是修改敏感字段白名单（D11 ③）：序列号、容量、固件——避免热插拔噪声。
// 顺序即比对顺序，与 component.whitelistValue 的分支一一对应。
var modifiedFields = []string{"serial", "capacity_bytes", "firmware"}

// IngestSnapshot 处理一次资产快照。mode 为采集来源（agent/ipmi），进前端「来源」列。
func (s *InventoryService) IngestSnapshot(ctx context.Context, hostID int64,
	hostname string, snap *gen.AssetSnapshot, mode string, at time.Time) error {

	comps := componentsOf(snap)
	fingerprint := comps.fingerprint()

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
	prev := newComponentSet(existing)

	if err := s.store.Components().UpsertBatch(ctx, hostID, comps.rows(), at); err != nil {
		return fmt.Errorf("部件写入失败: %w", err)
	}

	var snapshotID *int64
	recordSnapshot := func(withPayload bool) error {
		var payload *string
		if withPayload {
			payload = comps.payload()
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
		s.log.Info("资产基线已建立", "host_id", hostID, "components", comps.count())
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

	count, err := s.diff(ctx, hostID, hostname, prev, comps, mode, snapshotID, at)
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
	prev, comps componentSet, mode string, snapshotID *int64, at time.Time) (int, error) {

	n := 0
	// added：新快照有、而部件历史里没有。
	// 注意 —— 历史里存在但 removed_at 非空的是「拔了又插」的回归，
	// 不算新增（change_type 只有 added/removed/modified 三值，回归不落事件）。
	for _, c := range comps.items {
		previous, ok := prev.find(c)
		if !ok {
			if err := s.recordChange(ctx, hostID, hostname, snapshotID, mode, at,
				c.Category, c.Slot, "added", "", nil, ptr.Of(c.summary())); err != nil {
				return n, err
			}
			n++
			continue
		}
		if previous.RemovedAt != nil {
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
	for _, c := range comps.items {
		previous, ok := prev.find(c)
		if !ok || previous.RemovedAt != nil {
			continue
		}
		for _, field := range modifiedFields {
			oldV, newV := previous.whitelistValue(field), c.whitelistValue(field)
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
	for _, row := range missing {
		c := component{row}
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
			c.Category, c.Slot, "removed", "", ptr.Of(c.summary()), nil); err != nil {
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
		Detail:   ptr.Of(fmt.Sprintf(`{"change_type":%q,"category":%q,"slot":%q,"field":%q}`, changeType, category, slot, field)),
	}
	created, alertID, err := s.store.Alerts().UpsertFiring(ctx, e,
		fmt.Sprintf("asset-change:%d:%s:%s:%s", hostID, category, slot, changeType), timex.RFC3339(at))
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

// ── 部件领域对象 ─────────────────────────────────────────────────────────────
//
// 一次快照规范化后的部件清单不是裸切片，而是一个有身份的集合：身份键（类别 + 槽位）
// 决定 diff 的归类，指纹决定「这轮算不算变化」，白名单取值决定 modified 的敏感度（D11）。
// 这些都是「部件清单」自己的规则，因此收在 component / componentSet 上，
// 而不是写成一堆处理别人家数据的散装函数。

// component 是一个硬件部件的领域视图，包住存储行 adapter.Component。
//
// 值接收者：部件在 diff 全程只读，没有任何就地修改语义。
type component struct{ adapter.Component }

// key 部件身份键：类别 + 槽位。
// 槽位是部件的身份而非位置（D11）：CPU 用 socket、内存/NIC 用槽位名、RAID 用控制器。
func (c component) key() string { return c.Category + keySep + c.Slot }

// summary 人类可读摘要，进变更事件的新旧值与告警详情。
func (c component) summary() string {
	if c.Serial == "" {
		return c.Name
	}
	return c.Name + " (SN:" + c.Serial + ")"
}

// whitelistValue 取白名单字段的字符串化值（modified 比对用，D11 ③）。
// 白名单外的字段返回空串——调用方只枚举 modifiedFields，不做兜底。
func (c component) whitelistValue(field string) string {
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

// componentSet 是一次采集规范化后的部件清单，同时按身份键建索引。
// 既用于「本轮采集了什么」，也用于「库里原来有什么」——两侧都是部件清单，
// 共用同一套键与比对规则，避免两侧各算一套键而永远配不上。
type componentSet struct {
	items []component
	byKey map[string]component
}

// newComponentSet 把一批存储行按身份键索引（nil 得到空集合）。
func newComponentSet(rows []adapter.Component) componentSet {
	cs := componentSet{
		items: make([]component, 0, len(rows)),
		byKey: make(map[string]component, len(rows)),
	}
	for _, r := range rows {
		cs.add(r, nil)
	}
	return cs
}

// add 追加一个部件并返回它（便于按需继续判断）。
// extra 为附加属性（可变、不参与指纹），序列化后进 extra 列；
// 序列化失败留空而不报错——附加属性是可选信息，不该让一次采集整体失败。
func (cs *componentSet) add(row adapter.Component, extra map[string]any) component {
	if extra != nil {
		if raw, err := json.Marshal(extra); err == nil {
			row.Extra = string(raw)
		}
	}
	c := component{row}
	cs.items = append(cs.items, c)
	cs.byKey[c.key()] = c
	return c
}

// count 部件数。
func (cs componentSet) count() int { return len(cs.items) }

// rows 返回底层存储行，供写库使用。返回副本，调用方改不动集合内部状态。
func (cs componentSet) rows() []adapter.Component {
	out := make([]adapter.Component, 0, len(cs.items))
	for _, c := range cs.items {
		out = append(out, c.Component)
	}
	return out
}

// find 按身份键取既有部件。
func (cs componentSet) find(c component) (component, bool) {
	prev, ok := cs.byKey[c.key()]
	return prev, ok
}

// payload 规范化 JSON，作为快照留档（指纹只存哈希，payload 才留全量）。
func (cs componentSet) payload() *string {
	raw, err := json.Marshal(cs.rows())
	if err != nil {
		return nil
	}
	s := string(raw)
	return &s
}

// fingerprint 计算规范化指纹：字段排序后 SHA-1（docs/03：规范化 JSON 的哈希）。
// 只纳入身份与白名单字段——附加属性（频率、链路状态）变化不该算硬件变更。
func (cs componentSet) fingerprint() string {
	type minimal struct {
		Category string `json:"category"`
		Slot     string `json:"slot"`
		Serial   string `json:"serial,omitempty"`
		Firmware string `json:"firmware,omitempty"`
		Cap      *int64 `json:"capacity_bytes,omitempty"`
	}
	rows := make([]minimal, 0, len(cs.items))
	for _, c := range cs.items {
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

// componentsOf 把 proto 资产快照规范化为部件清单（W2）。
// 只覆盖 Agent 上行的这几类：CPU/内存/NIC/RAID 控制器/BIOS；
// 磁盘与主板由 SMART 与 IPMI 通道补充，不在此处。
func componentsOf(snap *gen.AssetSnapshot) componentSet {
	cs := newComponentSet(nil)

	if cpu := snap.GetCpu(); cpu != nil {
		cs.add(adapter.Component{
			Category: "cpu", Slot: strs.OrDefault(cpu.GetSocket(), "cpu0"),
			Name: cpu.GetModel(),
		}, map[string]any{
			"cores": cpu.GetCores(), "threads": cpu.GetThreads(), "base_mhz": cpu.GetBaseMhz(),
		})
	}
	for _, m := range snap.GetMemory() {
		capacity := int64(m.GetSizeBytes())
		cs.add(adapter.Component{
			Category: "memory", Slot: strs.OrDefault(m.GetSlot(), "dimm"),
			Name:          strs.JoinNonEmpty(" ", m.GetManufacturer(), m.GetPartNumber()),
			Serial:        m.GetPartNumber(),
			CapacityBytes: &capacity,
		}, map[string]any{"speed": m.GetSpeed(), "type": m.GetType()})
	}
	for _, n := range snap.GetNic() {
		cs.add(adapter.Component{
			Category: "nic", Slot: strs.OrDefault(n.GetName(), "nic"),
			Name: n.GetDriver(), Serial: n.GetMac(),
		}, map[string]any{"speed_mbps": n.GetSpeedMbps(), "link_state": n.GetLinkState()})
	}
	if r := snap.GetRaid(); r != nil {
		cs.add(adapter.Component{
			Category: "raid_controller", Slot: strs.OrDefault(r.GetController(), "raid0"),
		}, map[string]any{
			"level": r.GetLevel(), "state": r.GetState(),
			"rebuild_percent": r.GetRebuildPercent(), "disk_count": r.GetDiskCount(),
		})
	}
	cs.add(adapter.Component{
		Category: "bios", Slot: "bios",
		Name: snap.GetBoardModel(), Firmware: snap.GetBiosVersion(),
	}, nil)

	return cs
}
