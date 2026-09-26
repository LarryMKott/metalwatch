// Package timex 收纳时间格式的单一约定。
//
// 存在的理由：「时间戳一律 UTC RFC3339」是存储层跨方言四条硬约束的第 ① 条，
// 而它此前有两份独立实现——存储层的 formatTime 与服务层的 formatUTC。
// 两份实现里任何一份被改成本地时区，字符串比较（告警去重键、区间筛选、
// 快照前后顺序判定）都会静默错位，且没有任何测试会因此变红。
//
// 与 pkg/ptr、pkg/strs 同为叶包：只依赖标准库，任何层都可以引用。
package timex

import "time"

// RFC3339 按存储约定格式化：先转 UTC，再输出 RFC3339。
//
// 写入存储的时间戳一律走这里——调用方不需要（也不应该）自己决定时区，
// 时区只影响展示，不影响落库。
func RFC3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }
