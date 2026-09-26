package main

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"strconv"

	"github.com/LarryMKott/metalwatch/pkg/config"
	"github.com/LarryMKott/metalwatch/pkg/env"
)

// 开发环境的相对路径约定（相对 backend 工作目录）。
// 两个都已被 .gitignore 覆盖，不会污染仓库。
const (
	// devConfigPath 是开发环境未指定 --config 时自动拾取的样例配置。
	devConfigPath = "configs/app.yaml"
	// devDataDir 是开发环境未指定 --data 时的数据目录（与 start-backend 脚本一致）。
	devDataDir = "tmp-data"
	// devHost 是开发环境的默认监听主机。只听本机：开发机常接在不受控网络里，
	// 端口外露会让「本地跑的实例」变成事实上的内网服务。
	devHost = "127.0.0.1"
)

// options 是命令行参数。
//
// 用类型而不是在 run() 里散落 flag.String 的返回值：参数定义、解析与
// 「是否被显式传入」的判定绑在一处，新增参数不会漏掉 given 记录，
// 「哪个参数属于哪个子命令」也只有一个出处。
type options struct {
	config      string
	dataDir     string
	logDir      string
	listen      string
	pidFile     string
	codeDays    int
	codeUses    int
	migrateOnly bool

	// given 记录被显式设置过的 flag 名（flag.Visit 只遍历这些）。
	given map[string]bool
}

// newOptions 在 fs 上注册全部参数。
func newOptions(fs *flag.FlagSet) *options {
	o := &options{given: map[string]bool{}}
	fs.StringVar(&o.config, "config", "", "配置文件路径（FPK: $TRIM_PKGETC/metalwatch.yaml）")
	fs.StringVar(&o.dataDir, "data", "", "运行数据目录（FPK: $TRIM_PKGVAR/data）")
	fs.StringVar(&o.logDir, "log-dir", "", "日志目录（FPK: $TRIM_PKGVAR/logs）")
	fs.StringVar(&o.listen, "listen", "", "监听地址，如 0.0.0.0:18080（默认取配置 server.port）")
	fs.StringVar(&o.pidFile, "pid-file", "", "写入进程 PID 的文件（FPK 生命周期脚本使用）")
	fs.IntVar(&o.codeDays, "days", 7, "agent-code 子命令：注册码有效期（天）")
	fs.IntVar(&o.codeUses, "uses", 1, "agent-code 子命令：注册码可用次数")
	fs.BoolVar(&o.migrateOnly, "migrate", false, "等价于 migrate 子命令")
	return o
}

// parse 解析 args，并记录哪些参数被显式传入。
//
// 必须用 flag.Visit 而不是「与默认值比较」：用户显式传 `--log-dir ""`
// （想把配置里的日志目录清空）与「根本没传」是两种意图，比较取值区分不出来。
func (o *options) parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return err
	}
	fs.Visit(func(f *flag.Flag) { o.given[f.Name] = true })
	return nil
}

// wasGiven 报告参数是否被显式传入。
func (o *options) wasGiven(name string) bool { return o.given[name] }

// listenAddr 是监听地址。主机为空表示监听所有网卡。
type listenAddr struct {
	host string
	port int
}

// String 返回 "host:port"；host 为空时形如 ":18080"。
func (a listenAddr) String() string { return net.JoinHostPort(a.host, strconv.Itoa(a.port)) }

// parseListen 解析 "0.0.0.0:18080" / ":18080" / "127.0.0.1:18080"。
//
// 主机部分原样保留（空字符串代表监听所有网卡），由调用方与运行环境默认值合并——
// 若在这里替用户补 0.0.0.0，开发环境「只听本机」的默认值就会被静默绕过。
func parseListen(s string) (listenAddr, error) {
	host, portStr, err := net.SplitHostPort(s)
	if err != nil {
		return listenAddr{}, fmt.Errorf("--listen 格式应为 host:port（收到 %q）: %w", s, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return listenAddr{}, fmt.Errorf("--listen 端口非法: %q", portStr)
	}
	return listenAddr{host: host, port: port}, nil
}

// settings 是一次运行最终生效的配置：运行环境、配置文件与命令行合并后的结果。
//
// 它是值对象——构造完成后不再变化，下游只读。
// 与 options 的区别：options 是用户敲进来的（允许为空），settings 是补齐默认值后的成品。
// 两者分开的意义在于「补齐」这一步成为唯一可审计的地方：
// 生产环境拒绝隐式默认这条规则只需在一个函数里成立。
type settings struct {
	// env 是运行环境判定（docs/01 D35）。
	env env.Environment
	// config 是生效的服务端配置（含补齐后的 data_dir）。
	config config.Config
	// configPath 是实际使用的配置文件路径，为空表示用的是内置默认值。
	configPath string
	// addr 是最终监听地址，已按运行环境补好主机默认值。
	addr listenAddr
	// pidFile 是 PID 文件路径，FPK 生命周期脚本据此管理进程；开发环境为空。
	pidFile string
}

// dataDir 是生效的数据目录。
//
// 单独给出一个访问器，是因为数据写错位置的代价极高（写失败 / 被卸载清理），
// 日志与断言里出现它的地方应该一眼可读。
func (s settings) dataDir() string { return s.config.Server.DataDir }

// resolveSettings 合并「命令行 > 配置文件 > 运行环境默认值」。
//
// 生产环境没有最后一档：数据目录缺失即失败返回，不做隐式兜底——
// 原先那个看起来无害的 "./data" 兜底正是数据被写到错误位置的源头。
func resolveSettings(o *options, environment env.Environment) (settings, error) {
	// 开发环境：未指定配置时拾取样例配置；文件不存在则退回内置默认值。
	configPath := o.config
	if !o.wasGiven("config") && environment.IsDev() {
		if _, err := os.Stat(devConfigPath); err == nil {
			configPath = devConfigPath
		}
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return settings{}, err
	}
	if o.dataDir != "" {
		cfg.Server.DataDir = o.dataDir
	}
	if o.logDir != "" {
		cfg.Server.LogDir = o.logDir
	}
	if cfg.Server.DataDir == "" {
		if !environment.IsDev() {
			return settings{}, errors.New("生产环境未指定数据目录：请用 --data 指定，" +
				"或设置 METALWATCH_DATA_DIR / 配置文件 server.data_dir（FPK 下对应 TRIM_PKGVAR/data）")
		}
		cfg.Server.DataDir = devDataDir
	}

	// 监听地址：生产听所有网卡（由飞牛网关转发）；开发只听本机，避免端口外露。
	addr := listenAddr{port: cfg.Server.Port}
	if environment.IsDev() {
		addr.host = devHost
	}
	if o.listen != "" {
		parsed, err := parseListen(o.listen)
		if err != nil {
			return settings{}, err
		}
		addr = parsed
		// 端口同时回写配置：app.Deps 会把 Config 交给 handler，
		// 显式 --listen 改了端口却不回写，会出现「实际听 18099、对外自称 18080」。
		cfg.Server.Port = parsed.port
	}

	return settings{
		env:        environment,
		config:     cfg,
		configPath: configPath,
		addr:       addr,
		pidFile:    o.pidFile,
	}, nil
}
