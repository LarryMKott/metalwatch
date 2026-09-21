// Package collect 是 Agent 的硬件采集核心。
//
// 设计要点：
//   - 采集项按平台实现（linux.go / windows.go，用 build tag 隔离）
//   - 单个采集项失败不影响其它项：错误逐项收集并返回，绝不整包失败
//   - 所有采集项都有超时，避免某个挂起的命令拖死整个上报周期
package collect

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/LarryMKott/metalwatch/agent/internal/model"
)

// Collector 负责把本机硬件状态采集为 model.Report。
type Collector struct {
	// 逐项采集函数，便于单独测试与替换
	steps []step
}

type step struct {
	name string
	fn   func(ctx context.Context, rep *model.Report) error
}

// New 构造采集器，注册当前平台的采集项。
func New() *Collector {
	c := &Collector{}
	c.steps = platformSteps()
	return c
}

// Collect 执行全部采集项。返回的 error 是所有失败项的汇总（可空）。
// 即使部分失败，返回的 Report 仍然可用（含成功采集到的数据）。
func (c *Collector) Collect(ctx context.Context) (model.Report, error) {
	rep := model.Report{Mode: "agent"}

	var failed []string
	for _, s := range c.steps {
		if err := s.fn(ctx, &rep); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", s.name, err))
		}
	}

	if len(failed) > 0 {
		sort.Strings(failed)
		return rep, fmt.Errorf("部分采集项失败: %s", strings.Join(failed, "; "))
	}
	return rep, nil
}

// HostFingerprint 返回注册所需的机器标识。
func HostFingerprint(ctx context.Context) model.HostIdentity {
	return hostIdentity(ctx)
}
