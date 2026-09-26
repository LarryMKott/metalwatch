// Package env 判定 MetalWatch 的运行环境（开发 / 生产）。
//
// 服务端（backend）与 Agent（agent）是两个独立 module，但**共用同一个全局环境变量**，
// 保证两侧对环境语义的理解一致——否则会出现「服务端按开发模式监听 127.0.0.1、
// Agent 按生产模式连 TRIM_PKGVAR 里的地址」这类只在联调时才暴露的错配。
//
// 解析优先级（高 → 低）：
//
//  1. 显式环境变量 METALWATCH_ENV（取值 dev / prod，大小写不敏感、两侧空白忽略）
//  2. 未设置时按部署形态判定：存在 TRIM_PKGVAR（飞牛 FPK 注入）即视为生产
//  3. 其余（在源码目录里直接跑）→ 开发
//
// 之所以把 TRIM_PKGVAR 作为兜底判据：真机部署由 FPK 的 cmd/main 拉起进程，
// 该变量必然存在；而它一旦存在就说明数据必须落到 TRIM_PKGVAR 指向的持久卷，
// 绝不能因为忘了写 METALWATCH_ENV 就落到开发用的相对路径上。
package env

import (
	"fmt"
	"os"
	"strings"
)

// Name 是全局环境变量名，服务端与 Agent 共用同一个变量。
const Name = "METALWATCH_ENV"

// 环境取值。只有两个：本机开发，或真实部署。
const (
	Dev  = "dev"
	Prod = "prod"
)

// trimPkgVar 是飞牛 FPK 注入的持久化目录变量，作为生产环境的兜底判据。
const trimPkgVar = "TRIM_PKGVAR"

// Getenv 是读取环境变量的函数签名。
//
// 注入而不是直接调 os.Getenv，是为了让调用方（尤其测试）能构造确定的环境，
// 不必污染进程级环境变量。
type Getenv func(string) string

// Environment 是不可变的运行环境值对象：只回答「是哪个环境」与「凭什么这么判定」。
type Environment struct {
	name   string
	source string
}

// Resolve 解析当前运行环境，getenv 传 nil 时使用进程环境。
//
// 取值非法时返回错误而非静默回退：环境决定数据写在哪里，猜错的代价是
// 「数据写到不该写的地方」，必须让用户显式改正。
func Resolve(getenv Getenv) (Environment, error) {
	if getenv == nil {
		getenv = os.Getenv
	}

	// 纯空白按「未设置」处理，而不是「非法值」：环境里出现 `METALWATCH_ENV= `
	// 多半是脚本变量展开了个空（`METALWATCH_ENV=$X` 而 X 未定义），
	// 此时报「取值非法」会把「忘了传变量」伪装成「变量写错」，反而更难排查。
	// 代价是「空白」与「未设置」不再可区分；要区分得改用 os.LookupEnv 语义，
	// 但那会让这一种情况多出一套分支，收益不抵复杂度 —— 此处刻意合并。
	raw := strings.TrimSpace(getenv(Name))
	if raw != "" {
		switch strings.ToLower(raw) {
		case Dev:
			return Environment{name: Dev, source: Name + "=" + raw}, nil
		case Prod:
			return Environment{name: Prod, source: Name + "=" + raw}, nil
		default:
			return Environment{}, fmt.Errorf(
				"%s 取值非法: %q（只能是 %s 或 %s）", Name, raw, Dev, Prod)
		}
	}

	if strings.TrimSpace(getenv(trimPkgVar)) != "" {
		return Environment{
			name:   Prod,
			source: "检测到 " + trimPkgVar + "（飞牛 FPK 环境）",
		}, nil
	}

	return Environment{
		name:   Dev,
		source: "未设置 " + Name + " 且非 FPK 环境",
	}, nil
}

// Name 返回环境名（dev / prod）。
func (e Environment) Name() string { return e.name }

// Source 返回判定依据，用于启动日志排查「为什么判定成这个环境」。
func (e Environment) Source() string { return e.source }

// IsDev 报告是否为开发环境。
func (e Environment) IsDev() bool { return e.name == Dev }

// IsProd 报告是否为生产环境。
func (e Environment) IsProd() bool { return e.name == Prod }

// String 实现 fmt.Stringer，日志里可直接打环境名。
func (e Environment) String() string { return e.name }
