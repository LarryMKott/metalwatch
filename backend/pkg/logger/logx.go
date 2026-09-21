// Package logx 提供结构化日志：JSON 行写入文件（TRIM_PKGVAR/logs）同时输出到 stdout，
// 便于飞牛侧查看进程输出与本地检索。
package logger

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// Options 是日志初始化参数。
type Options struct {
	Level  string
	LogDir string
	// FileName 为日志文件名，默认 metalwatch.log。
	FileName string
	// AlsoStdout 是否同时输出到标准输出（前台运行时建议 true）。
	AlsoStdout bool
}

// New 创建 logger 与其持有的文件句柄（调用方负责 Close）。
func New(opt Options) (*slog.Logger, func() error, error) {
	var sink io.Writer = os.Stdout
	var closer = func() error { return nil }

	if opt.LogDir != "" {
		if err := os.MkdirAll(opt.LogDir, 0o750); err != nil {
			return nil, nil, err
		}
		name := opt.FileName
		if name == "" {
			name = "metalwatch.log"
		}
		f, err := os.OpenFile(filepath.Join(opt.LogDir, name),
			os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
		if err != nil {
			return nil, nil, err
		}
		if opt.AlsoStdout {
			sink = io.MultiWriter(os.Stdout, f)
		} else {
			sink = f
		}
		closer = f.Close
	}

	handler := slog.NewJSONHandler(sink, &slog.HandlerOptions{
		Level: parseLevel(opt.Level),
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			// 统一时间格式，便于与 Agent 上报时间戳对照
			if a.Key == slog.TimeKey {
				a.Value = slog.StringValue(a.Value.Time().UTC().Format("2006-01-02T15:04:05.000Z"))
			}
			return a
		},
	})
	return slog.New(handler), closer, nil
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
