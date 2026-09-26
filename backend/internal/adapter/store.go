// Package store 是存储适配器层：对上层暴露统一的 SQL 访问入口，
// 底层按 dialect 可插拔地接不同数据库后端。
//
// 设计要点：
//   - 默认 sqlite（零部署，单文件落 TRIM_PKGVAR）
//   - 支持独立部署的数据库（postgres）：通过 Register 注册新的 Opener 即可扩展，
//     上层业务代码无需改动
//   - 占位符差异由 Rebind 统一处理（sqlite 用 ?，postgres 用 $N）
package adapter

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/LarryMKott/metalwatch/pkg/config"
)

// Dialect 标识底层数据库方言。
type Dialect string

const (
	DialectSQLite   Dialect = "sqlite"
	DialectPostgres Dialect = "postgres"
)

// ErrUnsupportedDialect 表示配置了尚未注册驱动的方言。
var ErrUnsupportedDialect = errors.New("不支持的数据库方言")

// Store 包装 *sql.DB 并附带方言信息，是上层唯一的数据访问入口。
type Store struct {
	*sql.DB
	dialect Dialect
}

// Dialect 返回底层方言。
func (s *Store) Dialect() Dialect { return s.dialect }

// NewStore 用已建立的 *sql.DB 构造 Store。
// 供内嵌时序库（adapter/tsdb）复用迁移器等能力：同一驱动、同一迁移机制、
// 但独立的库文件与 schema 版本表（见 docs/01 D30）。
func NewStore(db *sql.DB, d Dialect) *Store { return &Store{DB: db, dialect: d} }

// Rebind 把 SQL 中的 ? 占位符转换为当前方言的写法。
//
// 限制：不解析引号内的 ? （本项目的 SQL 均由代码内联，不含字符串字面量中的问号）。
func (s *Store) Rebind(query string) string { return Rebind(query, s.dialect) }

// Rebind 按方言转换占位符。
func Rebind(query string, d Dialect) string {
	if d != DialectPostgres {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 8)
	n := 0
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteByte(query[i])
	}
	return b.String()
}

// Opener 由各后端实现：负责建立连接并完成方言相关的初始化。
type Opener func(cfg config.Config) (*sql.DB, error)

var (
	registryMu sync.RWMutex
	registry   = map[Dialect]Opener{}
)

// Register 注册一个后端实现。重复注册同名方言会 panic（属于编码错误）。
func Register(d Dialect, o Opener) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[d]; dup {
		panic(fmt.Sprintf("store: 方言 %s 重复注册", d))
	}
	registry[d] = o
}

// Registered 返回已注册的方言列表（用于启动日志与错误提示）。
func Registered() []Dialect {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]Dialect, 0, len(registry))
	for d := range registry {
		out = append(out, d)
	}
	return out
}

// Open 按配置打开存储。返回的 Store 已可直接使用。
func Open(ctx context.Context, cfg config.Config) (*Store, error) {
	d := Dialect(cfg.DB.Driver)

	registryMu.RLock()
	opener, ok := registry[d]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q（已注册: %v）", ErrUnsupportedDialect, d, Registered())
	}

	db, err := opener(cfg)
	if err != nil {
		return nil, fmt.Errorf("打开 %s 存储失败: %w", d, err)
	}

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("连接 %s 存储失败: %w", d, err)
	}

	return &Store{DB: db, dialect: d}, nil
}

// IsUniqueViolation 判断错误是否为唯一约束冲突（跨方言）。
// 注意不能用裸的 "constraint failed" 匹配：sqlite 的外键冲突
// （FOREIGN KEY constraint failed）同样含该字样，误判会把外键错误当幂等冲突吞掉。
func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") || // sqlite
		strings.Contains(msg, "duplicate key") || // postgres
		strings.Contains(msg, "duplicate entry") // mysql
}
