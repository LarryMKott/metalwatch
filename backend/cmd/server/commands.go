package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/LarryMKott/metalwatch/pkg/crypto"
)

// 子命令名。
const (
	cmdServe     = "serve"
	cmdMigrate   = "migrate"
	cmdAgentCode = "agent-code"
)

// command 是一个子命令。
//
// 用接口而不是 switch + 一串自由函数：每个子命令的差异（注册码几天有效期、
// 用不用 PID 文件）收在自己的类型里，公共准备统一由 bootstrap 提供，
// 新增子命令不必再往 run() 的参数列表里追加东西，也不必让每个分支
// 各自把 db / logger / ctx 重新拼一遍。
type command interface {
	// run 执行子命令。ctx 在收到退出信号时取消；返回 nil 表示正常结束。
	run(ctx context.Context) error
}

// newCommand 按名字构造子命令。
func (b *bootstrap) newCommand(name string, o *options) (command, error) {
	switch name {
	case cmdServe:
		return &serveCommand{boot: b}, nil
	case cmdMigrate:
		return migrateCommand{boot: b}, nil
	case cmdAgentCode:
		return &agentCodeCommand{boot: b, days: o.codeDays, uses: o.codeUses}, nil
	default:
		return nil, fmt.Errorf("未知子命令 %q（可用: %s / %s / %s）",
			name, cmdServe, cmdMigrate, cmdAgentCode)
	}
}

// migrateCommand 只应用数据库结构迁移后退出。
// FPK 的安装阶段可能单独调用它，把「建表」与「起服务」分开。
type migrateCommand struct{ boot *bootstrap }

func (c migrateCommand) run(context.Context) error {
	s := c.boot.settings
	c.boot.log.Info("迁移完成",
		"applied", c.boot.applied,
		"dialect", string(c.boot.db.Dialect()),
		"env", s.env.Name(),
		"data_dir", s.dataDir())
	return nil
}

// agentCodeCommand 签发一次性 Agent 注册码。
//
// 明文只出现这一次：库里存的是摘要（crypto.HashToken），签发后无法再取回，
// 因此「签发」与「打印」必须紧挨着，中间不能有可失败的分支。
type agentCodeCommand struct {
	boot *bootstrap
	days int
	uses int
}

func (c *agentCodeCommand) run(ctx context.Context) error {
	s := c.boot.settings
	c.boot.log.Info("运行环境", "env", s.env.Name(), "data_dir", s.dataDir())

	code, err := c.issue(ctx)
	if err != nil {
		return err
	}
	c.boot.log.Info("注册码已签发",
		"expire_at", code.expireAt.Format(time.RFC3339), "max_uses", c.uses)
	code.printTo(os.Stdout)
	return nil
}

// issue 生成注册码并把摘要写入库。
func (c *agentCodeCommand) issue(ctx context.Context) (enrollCode, error) {
	raw, err := crypto.RandomToken("MW-", 16)
	if err != nil {
		return enrollCode{}, err
	}
	expireAt := time.Now().UTC().Add(time.Duration(c.days) * 24 * time.Hour)
	if err := c.boot.db.EnrollCodes().Issue(ctx, crypto.HashToken(raw), expireAt, c.uses, "cli"); err != nil {
		return enrollCode{}, err
	}
	return enrollCode{raw: raw, expireAt: expireAt, uses: c.uses}, nil
}

// enrollCode 是刚签发的一次性注册码。
type enrollCode struct {
	raw      string
	expireAt time.Time
	uses     int
}

// printTo 打印注册码与 Agent 侧的接入命令。
func (e enrollCode) printTo(w io.Writer) {
	fmt.Fprintf(w, "\n注册码: %s\n有效期至: %s\n可用次数: %d\n\n",
		e.raw, e.expireAt.Format(time.RFC3339), e.uses)
	fmt.Fprintln(w, "请在 Agent 端用该注册码完成注册（详见 docs/04 §2.1）：")
	fmt.Fprintln(w, "  metalwatch-agent --server http://<server>:18080 --code <注册码> --spool <缓存目录>")
}
