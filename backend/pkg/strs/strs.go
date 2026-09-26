// Package strs 收纳与业务无关的字符串小工具：默认值填充、截断、非空拼接。
//
// 为什么单独成一个叶包（而不是放进 pkg/utils）：pkg/utils 依赖 internal/service，
// 而 internal/service 自己就要用这些工具 —— 放进去会形成 service → utils → service 的环。
// 本包只依赖标准库，任何层都可以放心引用（与 pkg/ptr 同一取舍）。
package strs

import (
	"strings"
	"unicode/utf8"
)

// OrDefault 去除首尾空白后仍为空则返回 def。
//
// 用于可空输入与 proto 可选字段的兜底：部件的槽位（cpu0/dimm/nic/raid0）与主机的
// os_type 都靠它填默认值——空串进入身份键会让「同一槽位的空格差异」被算成两个部件。
func OrDefault(v, def string) string {
	if t := strings.TrimSpace(v); t != "" {
		return t
	}
	return def
}

// JoinNonEmpty 用 sep 连接非空片段；全空返回空串。
//
// 用于厂商 + 型号这类「两个字段拼一个展示名」：任一侧缺失时不应留下多余的分隔符。
func JoinNonEmpty(sep string, parts ...string) string {
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

// Truncate 把 s 截断到不超过 n 字节。
//
// 截断点回退到最后一个完整 UTF-8 序列：被截的多是错误信息与 HTTP 响应体，
// 里面常有中文；直接 s[:n] 可能切出半个字符，落库或进 JSON 后就是乱码。
// n <= 0 返回空串（旧实现用 s[:n]，n 为负会 panic）。
func Truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
