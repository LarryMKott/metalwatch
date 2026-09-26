package agentapp

import (
	"flag"
	"strings"
	"time"

	"github.com/LarryMKott/metalwatch/agent/internal/agentcfg"
)

// Flags 持有 Agent 的命令行参数：注册在构造时完成，解析与归一在 Parse 完成。
//
// 独立成类型而不是在入口里散落 flag.String：入口只剩「注册 → 解析 → Run」三步，
// 参数列表与用法说明也只有一个出处（两个平台入口本来要各写一遍）。
type Flags struct {
	fs        *flag.FlagSet
	code      *string
	server    *string
	transport *string
	tlsSkip   *bool
	interval  *time.Duration
	spool     *string
}

// NewFlags 在 fs 上注册全部参数。
//
// fs 建议传 flag.CommandLine：其默认 ErrorHandling 为 ExitOnError，
// 解析失败与 --help 会按惯例退出（与改动前一致）。
func NewFlags(fs *flag.FlagSet) *Flags {
	f := &Flags{fs: fs}
	f.code = fs.String("code", "", "一次性注册码（首次接入使用）")
	f.server = fs.String("server", "", "服务端地址（默认取本地凭据记录的地址，否则 "+
		agentcfg.DefaultServerURL+"）")
	f.transport = fs.String("transport", TransportGRPC,
		"传输协议："+TransportGRPC+"（默认，双向流+断网续传）或 "+
			TransportJSON+"（兼容模式）")
	f.tlsSkip = fs.Bool("tls-skip-verify", false, "https 自签证书时跳过校验（内网部署选项）")
	f.interval = fs.Duration("interval", 30*time.Second, "指标上报周期")
	f.spool = fs.String("spool", "", "断网缓存目录（开发环境默认 bin-local/spool，"+
		"生产默认 "+agentcfg.DefaultSpoolDir()+"）")
	return f
}

// Parse 解析 args 并归一取值（去首尾空白，避免从文件/脚本注入时带上换行）。
func (f *Flags) Parse(args []string) (Options, error) {
	if err := f.fs.Parse(args); err != nil {
		return Options{}, err
	}
	return Options{
		Code:      strings.TrimSpace(*f.code),
		Server:    strings.TrimSpace(*f.server),
		Transport: strings.TrimSpace(*f.transport),
		TLSSkip:   *f.tlsSkip,
		Interval:  *f.interval,
		SpoolDir:  strings.TrimSpace(*f.spool),
	}, nil
}
