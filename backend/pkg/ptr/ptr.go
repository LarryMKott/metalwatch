// Package ptr 收纳指针与值之间的最小转换。
//
// 为什么单独成一个叶包（而不是放进 pkg/utils）：需要它的地方分处
// internal/notify 与 api/web/handler，而 pkg/utils 依赖 internal/service，
// internal/service 又依赖 internal/notify —— 放进 utils 会形成
// notify → utils → service → notify 的环。
//
// 包的依赖必须只含标准库，否则迟早会被某个中间层拖进环里。
package ptr

import "strings"

// Deref 取指针指向的值，nil 返回空字符串。
//
// 用于把可空字段展平成对外 DTO 里的字符串：同一件事原先在
// internal/notify 与 api/web/handler 各写了一份，两处语义虽相同却无人保证
// 以后也相同（改一处忘另一处是本仓库已经踩过的坑）。
func Deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Of 返回 v 的地址，v 为空串也返回非 nil 指针。
//
// 用于「列可空，但空本身是要表达的信息」的字段：变更事件的旧值/新值、
// 告警的 detail，写成 NULL 会丢掉「确实比对过、结果是空」这层含义。
func Of(v string) *string { return &v }

// OfNonEmpty 空串（含纯空白）返回 nil，否则返回指针。
//
// 用于资产表的可空列（enroll 注册带入的 SMBIOS UUID / OS 版本）：此处空值与
// NULL 语义等同，统一落成 NULL 才能让下游「有没有值」的判断只有一个分支。
func OfNonEmpty(v string) *string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return &v
}

// Trim 去除首尾空白；入参为 nil 或去空后为空串则返回 nil。
//
// 用于 host 的可选文本字段（BMC 地址、SN、机位、备注）：既不把 "  " 存成有值，
// 也不把空串存成非 NULL——否则展示层与唯一索引上会同时存在两个「空」。
func Trim(v *string) *string {
	if v == nil {
		return nil
	}
	t := strings.TrimSpace(*v)
	if t == "" {
		return nil
	}
	return &t
}
