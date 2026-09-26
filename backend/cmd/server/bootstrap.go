package main

import (
	"context"
	"fmt"
	"log/slog"
	"os/signal"
	"syscall"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	_ "github.com/LarryMKott/metalwatch/internal/adapter/sqlite" // 默认元数据后端
	_ "github.com/LarryMKott/metalwatch/internal/adapter/tsdb"   // 默认时序后端（W1c）
	"github.com/LarryMKott/metalwatch/migrations"
	"github.com/LarryMKott/metalwatch/pkg/logger"
)

// bootstrap 是三个子命令启动前的公共准备：日志、退出信号、元数据库与结构迁移。
//
// 抽成类型而不是继续摆在 run() 的局部作用域里：这三步有严格的顺序与回滚要求
// （日志要先于开库就位、库要先于迁移就位、任一步失败都要把已占用的资源还回去），
// 收进「构造 + close」之后，配对关系一眼可见，也不再有三个子命令各自
// 重新拼装一遍 db / logger / ctx 的余地。
type bootstrap struct {
	// ctx 在收到 SIGINT/SIGTERM 时取消，子命令据此优雅退出。
	ctx context.Context
	// stop 停止信号转发，必须由 close 调用，否则信号 goroutine 泄漏。
	stop     context.CancelFunc
	settings settings
	db       *adapter.Store
	applied  int
	log      *slog.Logger
	closeLog func() error
}

// newBootstrap 就绪日志、退出信号与元数据库，并应用内嵌迁移。
//
// alsoStdout 表示日志除落文件外同时打到终端：非 serve 子命令（一次性命令，
// 用户就等着看输出）与开发环境都需要，生产 serve 则只落文件。
func newBootstrap(s settings, alsoStdout bool) (*bootstrap, error) {
	log, closeLog, err := logger.New(logger.Options{
		Level:      s.config.Server.LogLevel,
		LogDir:     s.config.Server.LogDir,
		AlsoStdout: alsoStdout,
	})
	if err != nil {
		return nil, fmt.Errorf("初始化日志失败: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)

	db, err := adapter.Open(ctx, s.config)
	if err != nil {
		stop()
		_ = closeLog()
		return nil, err
	}

	applied, err := migrate(ctx, db)
	if err != nil {
		_ = db.Close()
		stop()
		_ = closeLog()
		return nil, err
	}

	return &bootstrap{
		ctx: ctx, stop: stop, settings: s, db: db,
		applied: applied, log: log, closeLog: closeLog,
	}, nil
}

// close 逆序释放 bootstrap 占用的资源。必须 defer 调用。
func (b *bootstrap) close() {
	_ = b.db.Close()
	b.stop()
	_ = b.closeLog()
}

// migrate 应用内嵌迁移，返回本次应用的数量。
func migrate(ctx context.Context, db *adapter.Store) (int, error) {
	fsys, err := migrations.DialectFS(string(db.Dialect()))
	if err != nil {
		return 0, fmt.Errorf("未找到 %s 方言的迁移目录: %w", db.Dialect(), err)
	}
	return db.Migrate(ctx, fsys, string(db.Dialect()))
}
